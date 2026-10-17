// Package simboard runs the simulated board and presents it as a port.
//
// The simulated board is the real firmware compiled for the PC (see
// c/sim_main.c; tools/build.sh sim builds it). It speaks the port tool
// protocol on stdin/stdout, so to ptboard it is just another
// io.ReadWriteCloser.
//
// *** Nothing this produces is evidence about hardware. Every reading comes
// *** from a stub modelling an ideal unit with every peer cable plugged in. It
// *** exists so the PC side can be wired up and driven without a board.
package simboard

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"PortTool/src/serialx"
)

// PortName is the reserved port name that selects the simulated board instead
// of a serial port; no operating system gives a real port this name.
const PortName = "sim"

// LabelKey names, in the panel's dictionary, what the panel shows for it: said
// plainly enough that nobody files a simulated run as a bench result.
const LabelKey = "go.sim.label"

// ErrNotBuilt is Find's answer when there is no simulator to run. The panel
// words it in its own language; this text is for the command line.
var ErrNotBuilt = errors.New("the simulated board is not built: " +
	"tools/build.sh sim, or point PORTTOOL_SIM at one that is")

// StartError is a simulator that was found but would not start.
type StartError struct {
	Path string
	Err  error
}

func (e *StartError) Error() string {
	return fmt.Sprintf("cannot start the simulated board %s: %v", e.Path, e.Err)
}

func (e *StartError) Unwrap() error { return e.Err }

// IsSim reports whether a port name asks for the simulated board.
func IsSim(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), PortName)
}

// OpenPort opens a board connection by name: the reserved name reaches the
// simulated board, anything else is a serial port. The one place that decides
// it, so the panel and a plan run cannot disagree about what "sim" means.
func OpenPort(name string, baud int) (io.ReadWriteCloser, error) {
	if IsSim(name) {
		c, err := Open()
		if err != nil {
			return nil, err
		}
		return &wired{inner: c}, nil // Link can wire it to a second one later
	}
	return serialx.Open(name, baud)
}

func exeName() string {
	if runtime.GOOS == "windows" {
		return "porttool_simboard.exe"
	}
	return "porttool_simboard"
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// binDir is this platform's folder under Output/bin, where tools/build.sh
// puts PortTool and the simulated board side by side.
func binDir() string {
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		return "darwin-arm64"
	}
	return runtime.GOOS
}

// searchUp walks up from `start` looking for the build in Output/bin and
// returns the first one it finds.
func searchUp(start string) (string, bool) {
	rel := filepath.Join("Output", "bin", binDir(), exeName())

	dir := start
	for i := 0; i < 8; i++ {
		cand := filepath.Join(dir, rel)
		if _, err := os.Stat(cand); err == nil {
			return cand, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", false
}

// Find locates the simulated board binary: PORTTOOL_SIM when set, otherwise
// searched from the executable's own directory, then the working directory.
// Both, because a shortcut sets the working directory somewhere else; the
// executable first because it is the stable one.
func Find() (string, error) {
	if p := os.Getenv("PORTTOOL_SIM"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("PORTTOOL_SIM points at %s, which is not there", p)
	}

	if exe, err := os.Executable(); err == nil {
		// Resolved, so an exe reached through a symlink searches from where
		// the binary really lives rather than from the link.
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		// The delivery package ships it beside PortTool.exe, so a copy of that
		// folder anywhere still offers "sim" (decision 112, PORTTOOL-FLOW.md C.4).
		if cand := filepath.Join(filepath.Dir(exe), exeName()); fileExists(cand) {
			return cand, nil
		}
		if cand, ok := searchUp(filepath.Dir(exe)); ok {
			return cand, nil
		}
	}

	if dir, err := os.Getwd(); err == nil {
		if cand, ok := searchUp(dir); ok {
			return cand, nil
		}
	}

	return "", ErrNotBuilt
}

// conn is the child process presented as one stream.
type conn struct {
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  io.ReadCloser
	done chan struct{} // closed once the process has exited
	code int           // its exit code, valid after done is closed
}

func (c *conn) Read(p []byte) (int, error)  { return c.out.Read(p) }
func (c *conn) Write(p []byte) (int, error) { return c.in.Write(p) }

// Close shuts the board down by closing its input, which is what its loop
// watches for. Killing it outright would leave the last frames unread.
func (c *conn) Close() error {
	err := c.in.Close()
	<-c.done
	return err
}

// Exited reports whether the simulated board behind a port opened here has
// exited, and with what code. A port that is not the simulated board, or one
// still running, reports false. A dead simulator looks like a board that is
// not answering, and the panel's advice for the two differs.
func Exited(port io.ReadWriteCloser) (code int, exited bool) {
	c, ok := Unwrap(port).(*conn)
	if !ok {
		return 0, false
	}
	select {
	case <-c.done:
		return c.code, true
	default:
		return 0, false
	}
}

// Path is the simulated board executable behind a port, or "" for a serial port.
func Path(port io.ReadWriteCloser) string {
	if c, ok := Unwrap(port).(*conn); ok {
		return c.cmd.Path
	}
	return ""
}

// Open starts the simulated board.
func Open() (io.ReadWriteCloser, error) {
	path, err := Find()
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(path)
	// Its own directory, so anything it ever reads relative to itself resolves
	// the same way whether the panel was started from the repo or elsewhere.
	cmd.Dir = filepath.Dir(path)
	cmd.Stderr = os.Stderr

	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		_ = in.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, &StartError{Path: path, Err: err}
	}

	c := &conn{cmd: cmd, in: in, out: out, done: make(chan struct{})}
	// Waited on here rather than in Close, so an exit is known the moment it
	// happens and not only when somebody disconnects.
	go func() {
		err := cmd.Wait()
		c.code = 0
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			c.code = exitErr.ExitCode()
		} else if err != nil {
			c.code = -1
		}
		close(c.done)
	}()
	return c, nil
}
