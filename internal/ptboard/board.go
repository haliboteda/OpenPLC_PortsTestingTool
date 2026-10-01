// Package ptboard owns the conversation with one board: it reads the serial
// line forever, sorts what comes back into replies, samples and log text, and
// sends commands one at a time.
//
// Reading never stops. Not while a command is in flight, not while the person
// has paused the log view, not while nothing is subscribed. The OS receive
// buffer is small and a session pushing frames will overrun it within seconds
// of nobody draining it, and a frame lost that way is gone - there is no
// retransmission on this link. Pausing is something the *view* does.
package ptboard

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"PortTool/internal/ptproto"

	"PortTool/internal/serialx"
)

// DefaultTimeout is how long to wait for a reply to a command the board
// answers immediately - everything except pt.run.
const DefaultTimeout = 3 * time.Second

// RunTimeout is for pt.run, where the board goes away and does the work before
// it answers.
//
// *** Not a guess and not the same as DefaultTimeout. *** Measured on the
// board 2026-09-08: sdram.sweep 6.5 s (the whole 64 MiB, four patterns),
// sd.integrity with passes=64 takes 9.1 s (write/read/verify rounds), and
// waits 5 s by design). Against the old 3 s every one of those timed out while
// the board was still working, and the panel then reported a healthy chip as a
// failure - which is exactly what happened on the bench before this existed.
//
// Generous on purpose: the reply is ended by the target's own OK line, so a
// long limit costs nothing when the board answers sooner. What it buys is that
// a slower card or a bigger sweep does not turn into a false failure.
const RunTimeout = 180 * time.Second

// TimeoutFor is how long a given command is allowed. Derived from the command
// rather than passed in by every caller, so a new slow command cannot be
// forgotten at one call site and time out only there.
func TimeoutFor(cmd string) time.Duration {
	if fields := strings.Fields(cmd); len(fields) > 0 && fields[0] == "pt.run" {
		return RunTimeout
	}
	return DefaultTimeout
}

// idleGap ends a reply whose length nothing declares - pt.list and a bare
// pt.run both answer with "as many OK lines as apply". The board writes
// them back to back at 115200, so a gap this long between them means it has
// moved on.
const idleGap = 250 * time.Millisecond

// Event is one line from the board, already sorted.
type Event struct {
	Kind  ptproto.LineKind
	Line  string        // the raw line, exactly as it arrived
	Frame ptproto.Frame // filled in when Kind is LineFrame
	At    time.Time
	Seq   uint64 // monotonic per board, so a view can tell what it missed
}

// Board is a live connection. Safe for concurrent use.
type Board struct {
	rw io.ReadWriteCloser

	mu       sync.Mutex
	pending  *request
	subs     map[int]chan Event
	nextSub  int
	ring     []Event
	ringCap  int
	seq      uint64
	closed   bool
	readErr  error
	lost     chan struct{} // closed when the reader stops, for whatever reason
	sendLock sync.Mutex    // one command at a time: replies are matched by order
}

type request struct {
	lines []string
	done  func(lines []string) bool
	ch    chan []string
	timer *time.Timer
}

// New starts reading from rw immediately. ringCap is how many events to keep
// for a view that connects late or falls behind.
func New(rw io.ReadWriteCloser, ringCap int) *Board {
	if ringCap <= 0 {
		ringCap = 20000
	}
	b := &Board{
		rw:      rw,
		subs:    map[int]chan Event{},
		ringCap: ringCap,
		lost:    make(chan struct{}),
	}
	go b.readLoop()
	return b
}

func (b *Board) readLoop() {
	// SteadyReader, not the port directly: an idle port answers with zero
	// bytes and no error, and a hundred of those make bufio give up on a
	// board that is simply between replies - which is what "the board stopped
	// answering" turned out to be.
	sc := bufio.NewScanner(serialx.SteadyReader{R: b.rw})
	sc.Buffer(make([]byte, 0, 4096), 64*1024)
	// The board terminates lines with \r\n; Scanner's line splitter handles
	// \n and leaves the \r, so it is trimmed below.
	for sc.Scan() {
		b.handleLine(strings.TrimRight(sc.Text(), "\r"))
	}
	err := sc.Err()
	if err == nil {
		err = io.EOF
	}
	b.mu.Lock()
	b.readErr = err
	req := b.pending
	b.pending = nil
	b.mu.Unlock()
	close(b.lost)
	if req != nil {
		req.stop()
		req.ch <- nil
	}
}

// Lost is closed when the reader stops: the port was closed, or it failed -
// an unplugged adapter, a board that lost power. Err says which.
func (b *Board) Lost() <-chan struct{} { return b.lost }

// Err is why the reader stopped, or nil while it is running.
func (b *Board) Err() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.readErr
}

func (b *Board) handleLine(line string) {
	if line == "" {
		return
	}
	kind, body := ptproto.Classify(line)

	ev := Event{Kind: kind, Line: line, At: time.Now()}
	if kind == ptproto.LineFrame {
		if f, ok := ptproto.ParseFrame(body); ok {
			ev.Frame = f
		}
	}

	b.mu.Lock()
	b.seq++
	ev.Seq = b.seq
	b.ring = append(b.ring, ev)
	if len(b.ring) > b.ringCap {
		b.ring = b.ring[len(b.ring)-b.ringCap:]
	}

	// A reply is only ever an OK or ERR line. Frames and bare log text keep
	// flowing past a command in flight, which is the normal case: sessions do
	// not pause because somebody typed something.
	var finished *request
	if req := b.pending; req != nil && (kind == ptproto.LineOK || kind == ptproto.LineErr) {
		req.lines = append(req.lines, line)
		if kind == ptproto.LineErr || req.done(req.lines) {
			finished = req
			b.pending = nil
		} else {
			req.resetIdle(b)
		}
	}

	// Delivered while still holding the lock. A subscriber channel is closed
	// only after being removed from this map under the same lock, so this is
	// what keeps a browser tab closing mid-frame from sending on a closed
	// channel. The sends cannot block - they are all select/default - so the
	// reader is not held up by a slow view.
	for _, c := range b.subs {
		// A view that cannot keep up loses events rather than stalling the
		// reader; its Seq gap is what tells it that happened.
		select {
		case c <- ev:
		default:
		}
	}
	b.mu.Unlock()

	if finished != nil {
		finished.stop()
		finished.ch <- finished.lines
	}
}

func (r *request) stop() {
	if r.timer != nil {
		r.timer.Stop()
	}
}

// resetIdle restarts the "no more lines are coming" clock, for replies whose
// length nothing declares.
func (r *request) resetIdle(b *Board) {
	if r.timer == nil {
		return
	}
	r.timer.Reset(idleGap)
}

// Expect decides when a reply is complete.
type Expect func(lines []string) bool

// ExpectOne ends at the first OK line. Most commands answer with exactly one.
func ExpectOne(lines []string) bool { return len(lines) >= 1 }

// ExpectCaps reads the count out of the header pt.caps sends, so the reply
// ends exactly where the firmware says it does rather than on a guess.
func ExpectCaps(lines []string) bool {
	if len(lines) == 0 {
		return false
	}
	kind, body := ptproto.Classify(lines[0])
	if kind != ptproto.LineOK {
		return true // an ERR or something odd: do not wait for more
	}
	n, ok := ptproto.GetU32(ptproto.Fields(body), "lines")
	if !ok {
		return true
	}
	return len(lines) >= int(n)+1
}

// ExpectMany never completes on its own, so the reply ends on the idle gap.
// For pt.list and a bare pt.run, which answer with as many lines as apply and
// give no count.
func ExpectMany(lines []string) bool { return false }

// ExpectFor picks the right rule for a command line, so a caller passing a
// command through from a person does not have to know the protocol.
//
// Only three shapes exist: pt.caps declares its own length, the catalogue
// replies give no count at all, and everything else answers in one line.
func ExpectFor(cmd string) Expect {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return ExpectOne
	}
	switch fields[0] {
	case "pt.caps":
		return ExpectCaps
	case "pt.list":
		return ExpectMany
	case "pt.run":
		if len(fields) == 1 {
			return ExpectMany // the catalogue: one line per target, no count
		}
		// Performing one: the checks it runs print prose first, then one OK
		// line. Log lines do not end a reply, so this is still one line.
		return ExpectOne
	default:
		return ExpectOne
	}
}

// Send writes one command and collects its reply.
//
// Commands are serialised because the protocol has no request ids: a reply is
// matched to a command by being the next OK or ERR to arrive. Two in flight
// would be indistinguishable.
func (b *Board) Send(cmd string, expect Expect, timeout time.Duration) ([]string, error) {
	if expect == nil {
		expect = ExpectOne
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	b.sendLock.Lock()
	defer b.sendLock.Unlock()

	req := &request{done: expect, ch: make(chan []string, 1)}

	b.mu.Lock()
	if b.closed || b.readErr != nil {
		err := b.readErr
		b.mu.Unlock()
		if err == nil {
			err = errors.New("board is closed")
		}
		return nil, err
	}
	b.pending = req
	// One timer serves both roles: it fires at idleGap once lines have started
	// arriving, and stands in as the overall timeout until then.
	req.timer = time.AfterFunc(timeout, func() {
		b.mu.Lock()
		if b.pending != req {
			b.mu.Unlock()
			return
		}
		b.pending = nil
		b.mu.Unlock()
		req.ch <- req.lines
	})
	b.mu.Unlock()

	if _, err := io.WriteString(b.rw, cmd+"\r\n"); err != nil {
		req.stop()
		b.mu.Lock()
		if b.pending == req {
			b.pending = nil
		}
		b.mu.Unlock()
		return nil, fmt.Errorf("writing %q: %w", cmd, err)
	}

	lines := <-req.ch
	if lines == nil {
		return nil, fmt.Errorf("board stopped answering during %q", cmd)
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("no reply to %q within %s", cmd, timeout)
	}
	if kind, body := ptproto.Classify(lines[0]); kind == ptproto.LineErr {
		// The board's own words, not a summary of them. A refusal says which
		// parameter and why, and rewording it throws that away - see
		// PORTTOOL-FLOW.md A.6.
		return lines, &RefusedError{Command: cmd, Reason: body}
	}
	return lines, nil
}

// RefusedError is an ERR reply. Reason is the board's text, verbatim.
type RefusedError struct {
	Command string
	Reason  string
}

func (e *RefusedError) Error() string { return e.Reason }

// Caps asks the board what it exposes.
func (b *Board) Caps() (ptproto.Caps, error) {
	lines, err := b.Send("pt.caps", ExpectCaps, DefaultTimeout)
	if err != nil {
		return ptproto.Caps{}, err
	}
	return ptproto.ParseCaps(lines)
}

// Subscribe returns a channel of every event from now on, and a function to
// stop. Late or slow subscribers drop events rather than blocking the reader;
// use Backlog for what came before.
func (b *Board) Subscribe(buffer int) (<-chan Event, func()) {
	if buffer <= 0 {
		buffer = 256
	}
	ch := make(chan Event, buffer)
	b.mu.Lock()
	id := b.nextSub
	b.nextSub++
	b.subs[id] = ch
	b.mu.Unlock()

	return ch, func() {
		b.mu.Lock()
		if c, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(c)
		}
		b.mu.Unlock()
	}
}

// Backlog returns the events kept since the connection opened, oldest first.
// This is what a view shows when it opens, and what it fills a pause from.
func (b *Board) Backlog() []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Event, len(b.ring))
	copy(out, b.ring)
	return out
}

// Close stops the reader and releases the port.
func (b *Board) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	for id, c := range b.subs {
		delete(b.subs, id)
		close(c)
	}
	b.mu.Unlock()
	return b.rw.Close()
}
