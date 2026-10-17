package ptpanel

// A 持续 run writes the board's lines to a file as it goes: the page's own log
// is capped at minutes. Echo acknowledgements are left out (the frame's miss=
// already says it) and the header says so. PROD-CONFIG-ITEMS.md 「滚动日志」.

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"PortTool/src/ptboard"
	"PortTool/src/ptproto"
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

// startRunLog opens the file and starts copying the board into it. A failure
// to open is reported and the run goes on without a file (nil).
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
	// Flushed on a timer as well as on close, so the file is current mid-run.
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

// isEchoNoise is the board's acknowledgement of a pt.echo, matched on the
// reply text: only replies come back over this subscription.
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
