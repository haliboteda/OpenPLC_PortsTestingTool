package ptecho

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePort is a two-way pipe standing in for a serial adapter or a socket.
type fakePort struct {
	feed chan []byte
	// What one Read could not fit, handed over next time as a real port does.
	rest []byte

	mu      sync.Mutex
	written bytes.Buffer
	count   int
	closed  bool
}

func newFakePort() *fakePort { return &fakePort{feed: make(chan []byte, 64)} }

// Read is only ever called from the one pump goroutine, so rest needs no lock.
func (f *fakePort) Read(p []byte) (int, error) {
	if len(f.rest) == 0 {
		b, ok := <-f.feed
		if !ok {
			return 0, io.EOF
		}
		f.rest = b
	}
	n := copy(p, f.rest)
	f.rest = f.rest[n:]
	return n, nil
}

func (f *fakePort) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, io.ErrClosedPipe
	}
	// Bounded: a sink pushes for as long as the test lets it.
	if f.written.Len() < 1<<20 {
		f.written.Write(p)
	}
	f.count += len(p)
	return len(p), nil
}

func (f *fakePort) Close() error {
	f.mu.Lock()
	if !f.closed {
		f.closed = true
		close(f.feed)
	}
	f.mu.Unlock()
	return nil
}

func (f *fakePort) bytesWritten() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.count
}

func (f *fakePort) sawBack() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.written.String()
}

// Source and sink modes put a stream with no newline on the wire; a line
// reader would stop at 64 KiB with "token too long" on a working link.
func TestPeerSurvivesAStreamThatHasNoNewlineInIt(t *testing.T) {
	fp := newFakePort()
	p := New("usb@fake", fp, nil)

	block := bytes.Repeat([]byte("0123456789"), 512) // 5 KiB, not one newline
	const blocks = 16                                // 80 KiB, past any line buffer
	for i := 0; i < blocks; i++ {
		fp.feed <- block
	}
	waitFor(t, func() bool { return fp.bytesWritten() >= len(block)*blocks })
	p.Stop()

	if got, want := fp.bytesWritten(), len(block)*blocks; got != want {
		t.Fatalf("echoed %d bytes of %d - the far end stopped reading partway", got, want)
	}
	if err := p.Stats().Err; err != "" {
		t.Fatalf("a healthy stream left an error on the link: %s", err)
	}
}

// Echo mode is judged by the board comparing what came back against what it
// sent, so the payload has to return byte for byte - line ending included.
func TestPeerEchoesVerbatim(t *testing.T) {
	fp := newFakePort()
	p := New("rs485@fake", fp, nil)

	fp.feed <- []byte("42\r\n")
	waitFor(t, func() bool { return fp.bytesWritten() >= 4 })
	p.Stop()

	if got := fp.sawBack(); got != "42\r\n" {
		t.Fatalf("sent back %q, not the bytes that arrived", got)
	}
}

// Short printable text is printed so a person can see a live echo; a stream is
// counted, or one log line per block would bury the board's own frames.
func TestLogPrintsLinesAndCountsStreams(t *testing.T) {
	var got []string
	lg := &Logger{Say: func(key string, kv ...any) { got = append(got, fmt.Sprint(key, kv)) }, last: time.Now()}

	lg.Saw(In, []byte("42\r\n"))
	if len(got) != 1 || !strings.HasPrefix(got[0], "echo.in") || !strings.Contains(got[0], "42") {
		t.Fatalf("a short line was not printed as itself: %v", got)
	}

	lg.Saw(In, bytes.Repeat([]byte("x"), 4096))
	if len(got) != 1 {
		t.Fatalf("a stream block was printed instead of counted: %v", got)
	}
	lg.Flush()
	if len(got) != 2 || !strings.HasPrefix(got[1], "echo.second") || !strings.Contains(got[1], "4096") {
		t.Fatalf("the flush did not report the bytes it counted: %v", got)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the link to do its half")
}

// mode=source: the PC only reads; echoing the board's stream back would take
// the other direction's bandwidth (PORTTOOL-FLOW.md B.9 「上下行分开测」).
func TestDrainCountsWithoutEchoing(t *testing.T) {
	fp := newFakePort()
	p := New("usb@fake", fp, nil)
	p.SetDrain(true)

	fp.feed <- bytes.Repeat([]byte("x"), 4096)
	waitFor(t, func() bool { return p.Stats().RxBytes >= 4096 })
	p.Stop()

	if n := fp.bytesWritten(); n != 0 {
		t.Fatalf("drained link sent %d bytes back", n)
	}
}

// mode=sink: the PC pushes until stopped, and nothing more after.
func TestPushWritesUntilStopped(t *testing.T) {
	fp := newFakePort()
	p := New("usb@fake", fp, nil)

	stop := p.Push(nil, nil)
	waitFor(t, func() bool { return fp.bytesWritten() >= 64*1024 })
	stop()
	after := fp.bytesWritten()
	time.Sleep(20 * time.Millisecond)
	p.Stop()

	if fp.bytesWritten() != after {
		t.Fatalf("push kept writing after stop: %d then %d", after, fp.bytesWritten())
	}
}
