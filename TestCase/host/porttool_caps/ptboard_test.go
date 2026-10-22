package testcase

// The other half of T4-01: the recorded transcript replayed through a fake serial
// port, covering what only a real link can get wrong: split replies, frames during
// a command, refusals in the board's own words, multi-line replies with no count.

import (
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"go.bug.st/serial"

	"PortTool/src/ptboard"
	"PortTool/src/ptproto"
)

// fakeBoard answers commands out of the recorded transcript, and can push
// unsolicited frames at any moment, which is what a running session does.
type fakeBoard struct {
	pr  *io.PipeReader
	pw  *io.PipeWriter
	buf strings.Builder

	mu      sync.Mutex
	replies map[string][][]string // command -> its replies, in order of use
	used    map[string]int
	seen    []string
}

func newFakeBoard(t *testing.T) *fakeBoard {
	t.Helper()
	pr, pw := io.Pipe()
	f := &fakeBoard{pr: pr, pw: pw, replies: map[string][][]string{}, used: map[string]int{}}
	for cmd, runs := range transcript(t) {
		f.replies[cmd] = runs
	}
	return f
}

func (f *fakeBoard) Read(p []byte) (int, error) { return f.pr.Read(p) }

func (f *fakeBoard) Write(p []byte) (int, error) {
	f.buf.Write(p)
	s := f.buf.String()
	for {
		i := strings.IndexAny(s, "\r\n")
		if i < 0 {
			break
		}
		cmd := strings.TrimSpace(s[:i])
		s = strings.TrimLeft(s[i+1:], "\r\n")
		if cmd != "" {
			f.answer(cmd)
		}
	}
	f.buf.Reset()
	f.buf.WriteString(s)
	return len(p), nil
}

func (f *fakeBoard) answer(cmd string) {
	f.mu.Lock()
	f.seen = append(f.seen, cmd)
	runs := f.replies[cmd]
	n := f.used[cmd]
	if n >= len(runs) {
		n = len(runs) - 1 // reuse the last recorded reply for repeats
	}
	var lines []string
	if n >= 0 && len(runs) > 0 {
		lines = runs[n]
		f.used[cmd]++
	}
	f.mu.Unlock()

	// From a goroutine: the pipe is synchronous, and the board's reader is on
	// the other side of it.
	go func() {
		for _, l := range lines {
			f.emit(l)
		}
	}()
}

// emit writes one line exactly as the firmware would, "\r\n" included.
func (f *fakeBoard) emit(line string) { _, _ = io.WriteString(f.pw, line+"\r\n") }

func (f *fakeBoard) Close() error { return f.pw.Close() }

func (f *fakeBoard) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

func TestBoardReadsCapsOverAFakeLink(t *testing.T) {
	f := newFakeBoard(t)
	b := ptboard.New(f, 0)
	defer b.Close()

	caps, err := b.Caps()
	if err != nil {
		t.Fatalf("Caps: %v", err)
	}
	if want := goldenVersion(t); caps.Version != want {
		t.Fatalf("caps version = %s, want %s", caps.Version, want)
	}
	if _, ok := caps.Port("din"); !ok {
		t.Fatalf("caps came back without din: %d ports", len(caps.Ports))
	}
	if got := f.commands(); len(got) != 1 || got[0] != "pt.caps" {
		t.Errorf("board saw %v, want exactly [pt.caps]", got)
	}
}

func TestFramesDoNotDisturbAReplyInFlight(t *testing.T) {
	f := newFakeBoard(t)
	b := ptboard.New(f, 0)
	defer b.Close()

	events, stop := b.Subscribe(64)
	defer stop()

	// A session pushing samples while a command is being answered is the
	// normal case, not an edge one: sessions do not pause for commands.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			f.emit("!din t=4821" + string(rune('0'+i%10)) + " v=0x16 ch1=0")
			time.Sleep(time.Millisecond)
		}
	}()

	caps, err := b.Caps()
	if err != nil {
		t.Fatalf("Caps with frames interleaved: %v", err)
	}
	// ParseCaps refuses a reply whose declared line count and rows disagree,
	// so getting here at all is the assertion: not one caps line was lost to
	// the frames arriving between them.
	if _, ok := caps.Port("relay"); !ok {
		t.Errorf("caps lost lines to the frames: %d ports", len(caps.Ports))
	}
	<-done

	deadline := time.After(2 * time.Second)
	var frames int
	for frames == 0 {
		select {
		case ev := <-events:
			if ev.Kind == ptproto.LineFrame {
				frames++
				if ev.Frame.Port != "din" {
					t.Errorf("frame port = %q, want din", ev.Frame.Port)
				}
			}
		case <-deadline:
			t.Fatal("no frame reached a subscriber while a command was in flight")
		}
	}
}

func TestRefusalKeepsTheBoardsOwnWords(t *testing.T) {
	f := newFakeBoard(t)
	b := ptboard.New(f, 0)
	defer b.Close()

	_, err := b.Send("pt.start din ch=1,9", ptboard.ExpectOne, time.Second)
	if err == nil {
		t.Fatal("a refused command returned no error")
	}
	var refused *ptboard.RefusedError
	if !asRefused(err, &refused) {
		t.Fatalf("error was %T, want *ptboard.RefusedError", err)
	}
	// The reason names the parameter and the range; the panel must not summarise it.
	if !strings.Contains(refused.Reason, "must be channels") {
		t.Errorf("reason = %q, want the board's own explanation", refused.Reason)
	}
}

func TestMultiLineReplyWithNoDeclaredCount(t *testing.T) {
	f := newFakeBoard(t)
	b := ptboard.New(f, 0)
	defer b.Close()

	// pt.run with no target answers with one line per target and never says
	// how many, so the reader has to stop on the quiet rather than on a count.
	lines, err := b.Send("pt.run", ptboard.ExpectMany, 2*time.Second)
	if err != nil {
		t.Fatalf("pt.run: %v", err)
	}
	if len(lines) == 0 {
		t.Error("collected no lines from a catalogue reply")
	}
}

func TestBacklogKeepsEverythingIncludingLogText(t *testing.T) {
	f := newFakeBoard(t)
	b := ptboard.New(f, 0)
	defer b.Close()

	f.emit("=== port tool 0.2.0 ===")
	f.emit("!relay t=1 mode=hold level=0 ch1=0")
	if _, err := b.Caps(); err != nil {
		t.Fatal(err)
	}
	// Give the reader a moment for the two unsolicited lines.
	time.Sleep(50 * time.Millisecond)

	var kinds = map[ptproto.LineKind]int{}
	for _, ev := range b.Backlog() {
		kinds[ev.Kind]++
	}
	if kinds[ptproto.LineLog] == 0 {
		t.Error("the banner did not reach the backlog; a person needs to see it")
	}
	if kinds[ptproto.LineFrame] == 0 {
		t.Error("the sample frame did not reach the backlog")
	}
	if kinds[ptproto.LineOK] < 11 {
		t.Errorf("backlog has %d OK lines, want the whole caps reply", kinds[ptproto.LineOK])
	}

	// Sequence numbers must be gap-free here, which is how a view that fell
	// behind can tell it missed something.
	prev := uint64(0)
	for _, ev := range b.Backlog() {
		if ev.Seq != prev+1 {
			t.Fatalf("backlog seq jumped %d -> %d", prev, ev.Seq)
		}
		prev = ev.Seq
	}
}

func asRefused(err error, target **ptboard.RefusedError) bool {
	if r, ok := err.(*ptboard.RefusedError); ok {
		*target = r
		return true
	}
	return false
}

// rateBoard is the fake with a rate: it records every switch and every empty
// line, the keepalive a switched board needs (PORTTOOL-FLOW.md B.7).
type rateBoard struct {
	*fakeBoard
	mu     sync.Mutex
	modes  []int
	blanks int
}

func (r *rateBoard) SetMode(m *serial.Mode) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.modes = append(r.modes, m.BaudRate)
	return nil
}

func (r *rateBoard) Write(p []byte) (int, error) {
	if strings.TrimSpace(string(p)) == "" {
		r.mu.Lock()
		r.blanks++
		r.mu.Unlock()
	}
	return r.fakeBoard.Write(p)
}

func (r *rateBoard) state() ([]int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.modes...), r.blanks
}

// Decision 104: the PC follows the rate the board's reply announces, keeps the
// switched line alive, and goes back with the board.
func TestBoardFollowsTheControlLineRate(t *testing.T) {
	r := &rateBoard{fakeBoard: newFakeBoard(t)}
	b := ptboard.New(r, 0)
	defer b.Close()

	if _, err := b.Send("pt.start rs232 baud=19200 period=200", ptboard.ExpectOne, 0); err != nil {
		t.Fatalf("start: %v", err)
	}
	modes, blanks := r.state()
	if len(modes) != 1 || modes[0] != 19200 || blanks < 1 {
		t.Fatalf("after the start: modes %v, %d empty lines; want [19200] and one at once", modes, blanks)
	}
	time.Sleep(1200 * time.Millisecond)
	if _, blanks = r.state(); blanks < 3 {
		t.Errorf("%d empty lines in 1.2 s on the switched line; the board gives up after 3 s of silence", blanks)
	}

	if _, err := b.Send("pt.stop rs232", ptboard.ExpectOne, 0); err != nil {
		t.Fatalf("stop: %v", err)
	}
	modes, before := r.state()
	if len(modes) != 2 || modes[1] != 115200 {
		t.Fatalf("after the stop: modes %v, want [19200 115200]", modes)
	}
	time.Sleep(800 * time.Millisecond)
	if _, after := r.state(); after != before {
		t.Errorf("keepalive went on at the default rate: %d -> %d empty lines", before, after)
	}
}
