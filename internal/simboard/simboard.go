// Package simboard runs the simulated board and presents it as a port.
//
// The simulated board is the real firmware compiled for the PC (see
// TestCase/host/porttool_caps/harness/sim_main.c). It speaks the port tool
// protocol on stdin/stdout, so from here it is just another stream - which is
// the whole reason the rest of the program can stay unaware of it: ptboard
// takes an io.ReadWriteCloser and does not care what is on the other end.
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

	"PortTool/internal/serialx"
)

// PortName is the reserved port name that selects the simulated board instead
// of a serial port. It is a name no operating system gives a real port, so a
// plan or a panel asking for it cannot be ambiguous.
const PortName = "sim"

// LabelKey names, in the panel's dictionary, what the panel shows for it: said
// plainly enough that nobody files a simulated run as a bench result.
const LabelKey = "go.sim.label"

// ErrNotBuilt is Find's answer when there is no simulator to run. The panel
// words it in its own language; this text is for the command line.
var ErrNotBuilt = errors.New("the simulated board is not built: " +
	"cd TestCase/host/porttool_caps && python build.py --sim, " +
	"or point PORTTOOL_SIM at one that is")

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
		return Open()
	}
	return serialx.Open(name, baud)
}

func exeName() string {
	if runtime.GOOS == "windows" {
		return "porttool_simboard.exe"
	}
	return "porttool_simboard"
}

// searchUp walks up from `start` looking for the harness build and returns
// the first one it finds.
func searchUp(start string) (string, bool) {
	rel := filepath.Join("TestCase", "host", "porttool_caps", "harness", exeName())

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

// Find locates the simulated board binary.
//
// PORTTOOL_SIM wins when it is set, so a build somewhere else can be used
// without moving anything.
//
// *** Otherwise BOTH the executable's own directory and the working directory
// *** are searched, in that order. *** Only the working directory was searched
// until 2026-09-14, and that made the simulated board appear and disappear
// depending on how the panel had been started: double-clicking the exe in its
// own folder found it, while a Start-menu shortcut or a taskbar icon - which
// set the working directory somewhere else entirely - did not, and the port
// list then quietly had one fewer entry with nothing said about why. Where a
// program was launched from is not something the person launching it should
// have to think about.
//
// The executable comes first because it is the stable one: the working
// directory is wherever the shell happened to be.
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
// still running, reports false. The panel needs it because a simulator that
// died looks exactly like a board that is not answering, and the advice for
// the two is different.
func Exited(port io.ReadWriteCloser) (code int, exited bool) {
	c, ok := port.(*conn)
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
	if c, ok := port.(*conn); ok {
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
