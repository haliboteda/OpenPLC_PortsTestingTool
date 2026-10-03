package ptpanel

// Writing a long run down as it happens.
//
// The page keeps its log in the browser, capped at a few tens of thousands of
// lines. Five ctrl sessions at 200 ms produce roughly 75 lines a second, so
// that cap is about four and a half minutes - which is fine for pressing a
// button and reading the answer, and useless for a burn-in. Leave one running
// for four hours and the first three and a half of them are simply gone, the
// export button included.
//
// So a 持续 run writes to a file as it goes. Not a copy of the page's buffer
// taken at the end - by then the interesting part has already scrolled out of
// it (DECISIONS.md 37).
//
// ⚠️ Echo acknowledgements are deliberately left out. They are two lines in
// every three, they say nothing a reader can use - the frame's own miss= field
// already reports whether the loop is closing - and keeping them roughly
// triples the file for no evidence. The header says so, so nobody reads their
// absence as lines lost.

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"PortTool/internal/ptboard"
	"PortTool/internal/ptproto"
)

// RunLogDir overrides where run logs are written. Tests set it; a bench never
// does. Same shape as PlanDir and portmap.File.
var RunLogDir string

func runLogDir() string {
	if RunLogDir != "" {
		return RunLogDir
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "runlogs")
	}
	return "runlogs"
}

// runLog is one file, open for the length of one 持续 run.
type runLog struct {
	mu    sync.Mutex
	f     *os.File
	w     *bufio.Writer
	lines uint64
	path  string
	stop  func() // unsubscribes from the board
	done  chan struct{}
	flush *time.Ticker
}

// startRunLog opens the file and starts copying the board into it.
//
// A failure to open is reported and then dropped: a bench whose panel refused
// to start a run because a directory is read-only would be a worse trade than
// a run with no file behind it. The caller gets nil and carries on.
func (s *Server) startRunLog(b *ptboard.Board, ports []string, hours int) *runLog {
	dir := runLogDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.say(m("go.runlog.no_dir", "dir", dir, "detail", err))
		return nil
	}

	name := fmt.Sprintf("run-%s.log", time.Now().Format("20060102-150405"))
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		s.say(m("go.runlog.no_file", "path", path, "detail", err))
		return nil
	}

	rl := &runLog{f: f, w: bufio.NewWriterSize(f, 32*1024), path: path,
		done: make(chan struct{})}

	// The file is English whatever the page shows (decision 11).
	span := "until stopped"
	if hours > 0 {
		span = fmt.Sprintf("%d h", hours)
	}
	rl.write("# port tool run log")
	rl.write("# started  " + time.Now().Format("2006-01-02 15:04:05"))
	rl.write("# ports    " + strings.Join(ports, ", "))
	rl.write("# duration " + span)
	rl.write("# NOTE: echo acknowledgements are not recorded - see runlog.go.")
	rl.write("#       Every sample frame and every other reply is.")

	events, stop := b.Subscribe(1024)
	rl.stop = stop
	// Flushed on a timer as well as on close: a run that is still going has
	// already written the interesting part, and a reader looking at the file
	// mid-burn-in should not be shown a buffer that is 30 KiB behind.
	rl.flush = time.NewTicker(2 * time.Second)

	go func() {
		defer close(rl.done)
		defer rl.flush.Stop()
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					return
				}
				if isEchoNoise(ev) {
					continue
				}
				rl.write(ev.At.Format("15:04:05.000") + "  " + ev.Line)
			case <-rl.flush.C:
				rl.mu.Lock()
				_ = rl.w.Flush()
				rl.mu.Unlock()
			}
		}
	}()

	s.say(m("go.runlog.writing", "path", path))
	return rl
}

// isEchoNoise is the two lines in three this file leaves out: the board's
// acknowledgement of a pt.echo. Matched on the reply text rather than on the
// command, because only replies come back over this subscription.
func isEchoNoise(ev ptboard.Event) bool {
	return ev.Kind == ptproto.LineOK && strings.HasPrefix(ev.Line, "OK echo ")
}

func (rl *runLog) write(line string) {
	if rl == nil {
		return
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if rl.w == nil {
		return
	}
	_, _ = rl.w.WriteString(line + "\r\n")
	rl.lines++
}

// close stops copying, flushes, and says where the file is and how big it got.
func (rl *runLog) close(s *Server, why msg) {
	if rl == nil {
		return
	}
	if rl.stop != nil {
		rl.stop()
	}
	<-rl.done

	rl.mu.Lock()
	rl.write0("# stopped  " + time.Now().Format("2006-01-02 15:04:05") + "  (" + why.English() + ")")
	_ = rl.w.Flush()
	_ = rl.f.Close()
	n := rl.lines
	rl.w = nil
	rl.mu.Unlock()

	s.say(m("go.runlog.done", "path", rl.path, "lines", n))
}

// write0 is write() for a caller that already holds the lock.
func (rl *runLog) write0(line string) {
	if rl.w == nil {
		return
	}
	_, _ = rl.w.WriteString(line + "\r\n")
	rl.lines++
}
