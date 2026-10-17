// Package ptecho is the far end of a loop=link session - the one
// implementation of it, used by the panel and by plan steps alike (decision 77).
//
// eth, usb and rs485 are judged by whether what the board put on the link
// comes back, so without a peer they look like broken wiring.
//
// ⚠️ Bytes, verbatim: a peer that reformats, re-terminates or re-chunks reads
// as a dead link (DECISIONS.md 18), and sink / source streams have no newline.
package ptecho

import (
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Peer is one channel answering the board.
type Peer struct {
	// Name is what shows up in the log, e.g. "rs485@COM16" or "tcp 10.0.0.5:7".
	Name string

	rw io.ReadWriteCloser
	// Echoing and a sink pushing can both be writing at the same moment, and
	// neither a serial port nor a socket is safe for two writers.
	writeMu sync.Mutex

	reads   atomic.Uint64
	echoes  atomic.Uint64
	rxBytes atomic.Uint64
	txBytes atomic.Uint64
	lastErr atomic.Value // string

	// Set while a mirror-mode flood runs: arriving bytes are checked, not
	// echoed, since the board is the one sending them back. See flood.go.
	flood atomic.Pointer[flood]
	// Set while the board runs mode=source: arriving bytes are counted, not
	// echoed (PORTTOOL-FLOW.md B.9 「上下行分开测」).
	drain atomic.Bool

	once sync.Once
	done chan struct{}
}

// Stats is what a caller reports or judges.
type Stats struct {
	Name    string
	Reads   uint64 // reads that returned data
	Echoes  uint64 // of those, sent back
	RxBytes uint64
	TxBytes uint64 // echoed plus anything pushed with Write
	Err     string
}

// Say reports one line by message key and name/value arguments, so the
// caller words it in its own language (keys echo.* in the panel dictionary).
type Say func(key string, kv ...any)

// Dir is which way a block crossed the link.
type Dir int

const (
	In      Dir = iota // from the board
	Out                // echoed back to it
	ToBoard            // pushed to it by a sink session
)

var dirKey = [...]string{In: "echo.in", Out: "echo.out", ToBoard: "echo.to_board"}

// New starts echoing on rw and returns at once; it runs until rw closes or
// Stop is called. say may be nil; given, it shows the bytes crossing - short
// printable text as it arrives, a stream summed once a second.
func New(name string, rw io.ReadWriteCloser, say Say) *Peer {
	p := &Peer{Name: name, rw: rw, done: make(chan struct{})}
	p.lastErr.Store("")
	go func() {
		defer close(p.done)
		p.pump(say)
	}()
	return p
}

func (p *Peer) pump(say Say) {
	lg := &Logger{Say: say, last: time.Now()}
	defer lg.Flush()
	buf := make([]byte, 4096)
	for {
		n, err := p.rw.Read(buf)
		if n > 0 {
			p.reads.Add(1)
			p.rxBytes.Add(uint64(n))
			if f := p.flood.Load(); f != nil {
				// Summed, not shown: a flood's packets are printable and would
				// otherwise each print as a line.
				lg.Sum(In, n)
				f.take(buf[:n])
			} else if p.drain.Load() {
				lg.Sum(In, n)
			} else {
				lg.Saw(In, buf[:n])
				if _, werr := p.Write(buf[:n]); werr != nil {
					lg.Flush()
					lg.say("echo.write_failed", "detail", werr.Error())
					return
				}
				p.echoes.Add(1)
				lg.Saw(Out, buf[:n])
			}
		}
		if err != nil {
			// Close() from Stop lands here, which is the ordinary way this ends.
			if err != io.EOF {
				p.lastErr.Store(err.Error())
				lg.Flush()
				lg.say("echo.read_failed", "detail", err.Error())
			}
			return
		}
		if n == 0 {
			// An idle serial port answers (0, nil); that is silence, not the
			// end of the stream, and must not become a busy loop.
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// Write sends bytes the board did not send first - a sink pushing at it.
func (p *Peer) Write(b []byte) (int, error) {
	p.writeMu.Lock()
	n, err := p.rw.Write(b)
	p.writeMu.Unlock()
	p.txBytes.Add(uint64(n))
	if err != nil {
		p.lastErr.Store(err.Error())
	} else {
		p.lastErr.Store("")
	}
	return n, err
}

// SetDrain switches echoing off (on) or back on: the PC half of mode=source.
func (p *Peer) SetDrain(on bool) { p.drain.Store(on) }

// Push writes a pattern at the board until the returned stop is called: the
// PC half of mode=sink. A write blocks while the board cannot take more, so
// the pacing is the link's own. onErr, when given, hears the error that ended
// it; say, when given, sees the blocks summed once a second.
func (p *Peer) Push(say Say, onErr func(error)) (stop func()) {
	block := make([]byte, 4096)
	for i := range block {
		block[i] = byte('0' + i%10)
	}
	done := make(chan struct{})
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		lg := &Logger{Say: say, last: time.Now()}
		defer lg.Flush()
		for {
			select {
			case <-done:
				return
			default:
			}
			n, err := p.Write(block)
			if n > 0 {
				lg.Sum(ToBoard, n)
			}
			if err != nil {
				if onErr != nil {
					onErr(err)
				}
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(done) })
		select {
		case <-ended:
		case <-time.After(time.Second):
			// A write stuck on a full link ends when the channel closes.
		}
	}
}

// Stats reports what has crossed so far.
func (p *Peer) Stats() Stats {
	s := Stats{Name: p.Name, Reads: p.reads.Load(), Echoes: p.echoes.Load(),
		RxBytes: p.rxBytes.Load(), TxBytes: p.txBytes.Load()}
	if e, ok := p.lastErr.Load().(string); ok {
		s.Err = e
	}
	return s
}

// Done says the channel has closed: the far end went away or Stop was called.
func (p *Peer) Done() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// Stop closes the channel and waits for the echoing to finish.
func (p *Peer) Stop() {
	if f := p.flood.Load(); f != nil {
		f.halt()
	}
	p.once.Do(func() {
		_ = p.rw.Close()
		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
			// A serial read that will not unblock must not hang a production
			// run. The port is closed either way.
		}
	})
}

// Logger keeps a link's log readable at both speeds: an echo session's short
// lines as they arrive, a stream summed once a second so it does not bury the
// board's own frames.
type Logger struct {
	Say  Say
	last time.Time
	rx   uint64
	tx   uint64
}

func (g *Logger) say(key string, kv ...any) {
	if g.Say != nil {
		g.Say(key, kv...)
	}
}

// Saw records one block crossing in direction d.
func (g *Logger) Saw(d Dir, b []byte) {
	if g.Say == nil {
		return
	}
	if s, ok := readableLine(b); ok {
		g.Say(dirKey[d], "text", s)
		return
	}
	g.Sum(d, len(b))
}

// Sum adds n bytes in direction d to the once-a-second summary.
func (g *Logger) Sum(d Dir, n int) {
	if g.Say == nil {
		return
	}
	if d == In {
		g.rx += uint64(n)
	} else {
		g.tx += uint64(n)
	}
	if time.Since(g.last) >= time.Second {
		g.Flush()
	}
}

// Flush reports what was summed since the last report.
func (g *Logger) Flush() {
	if g.Say == nil || (g.rx == 0 && g.tx == 0) {
		return
	}
	g.Say("echo.second", "rx", g.rx, "tx", g.tx)
	g.rx, g.tx = 0, 0
	g.last = time.Now()
}

// readableLine says whether a block is the short printable text an echo
// session sends.
func readableLine(b []byte) (string, bool) {
	if len(b) == 0 || len(b) > 200 {
		return "", false
	}
	for _, c := range b {
		if c == '\r' || c == '\n' || c == '\t' {
			continue
		}
		if c < 0x20 || c == 0x7f {
			return "", false
		}
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", false
	}
	return s, true
}
