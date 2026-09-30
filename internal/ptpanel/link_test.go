package ptpanel

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// *** These tests need no board and no simulated board. ***
//
// That is the point of them. The far end of a link is the one piece the
// simulated board cannot exercise: it plays every peer itself, inside the
// firmware, so the code in link.go never runs during a simulated pass. A
// session mode that broke this end would sail through H5 and fail the moment
// somebody with a real board pressed the button - which is what happened on
// 2026-09-14.

// fakePort is a two-way pipe standing in for a serial adapter or a socket.
type fakePort struct {
	feed chan []byte
	// What one Read could not fit. A real port hands over whatever is left
	// next time; dropping it here would make the fake lose bytes the code
	// under test delivered correctly.
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
	// Bounded: a sink pushes for as long as the test lets it, and keeping
	// every block would be the test running the machine out of memory rather
	// than proving anything.
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

// A stream with no newline in it is what source and sink modes put on the
// wire. bufio.Scanner answers 64 KiB of that with "token too long" and stops
// reading - on a link that is working perfectly. The board then fills its
// endpoint and reports busy climbing while its byte count stands still, and
// every symptom points at the hardware.
func TestPumpSurvivesAStreamThatHasNoNewlineInIt(t *testing.T) {
	fp := newFakePort()
	l := &linkPort{Board: "usb", COM: "fake", port: fp}

	block := bytes.Repeat([]byte("0123456789"), 512) // 5 KiB, not one newline
	const blocks = 16                                // 80 KiB, past any line buffer

	done := make(chan struct{})
	go func() {
		defer close(done)
		l.pump(fp, func(string) {})
	}()

	for i := 0; i < blocks; i++ {
		fp.feed <- block
	}
	waitFor(t, func() bool { return fp.bytesWritten() >= len(block)*blocks })
	_ = fp.Close()
	<-done

	if got, want := fp.bytesWritten(), len(block)*blocks; got != want {
		t.Fatalf("echoed %d bytes of %d - the far end stopped reading partway", got, want)
	}
	l.mu.Lock()
	err := l.lastErr
	l.mu.Unlock()
	if err != "" {
		t.Fatalf("a healthy stream left an error on the link: %s", err)
	}
}

// Echo mode is judged by the board comparing what came back against what it
// sent, so the payload has to return byte for byte.
func TestPumpEchoesVerbatim(t *testing.T) {
	fp := newFakePort()
	l := &linkPort{Board: "rs485", COM: "fake", port: fp}

	done := make(chan struct{})
	go func() {
		defer close(done)
		l.pump(fp, func(string) {})
	}()

	fp.feed <- []byte("42\r\n")
	waitFor(t, func() bool { return fp.bytesWritten() >= 4 })
	_ = fp.Close()
	<-done

	if got := fp.sawBack(); got != "42\r\n" {
		t.Fatalf("sent back %q, not the bytes that arrived", got)
	}
}

// Sink mode means the board sits there counting bytes somebody else pushes. If
// nobody pushes, rx_bytes and kbps stay zero however healthy the hardware is -
// which is what the panel did until 2026-09-14.
func TestSinkModePushesAtTheBoard(t *testing.T) {
	fp := newFakePort()
	l := &linkPort{Board: "usb", COM: "fake", port: fp}

	l.setMode(modeSink, func(string) {})
	waitFor(t, func() bool { return fp.bytesWritten() > 0 })
	l.setMode(modeEcho, nil)

	if fp.bytesWritten() == 0 {
		t.Fatal("sink mode pushed nothing - the board would report zero bytes forever")
	}

	// And it stops when the mode leaves sink, or a board that has stopped
	// listening gets a stream nobody is reading.
	settled := fp.bytesWritten()
	time.Sleep(50 * time.Millisecond)
	if grew := fp.bytesWritten() - settled; grew > 1<<16 {
		t.Fatalf("still pushing %d bytes after the mode changed", grew)
	}
	_ = fp.Close()
}

// Echo mode must originate nothing at all: a byte this end invented would come
// back to the board as a number it never sent.
func TestEchoModeOriginatesNothing(t *testing.T) {
	fp := newFakePort()
	l := &linkPort{Board: "rs485", COM: "fake", port: fp}

	l.setMode(modeEcho, func(string) {})
	time.Sleep(50 * time.Millisecond)

	if n := fp.bytesWritten(); n != 0 {
		t.Fatalf("echo mode sent %d bytes nobody asked for", n)
	}
	_ = fp.Close()
}

func TestLinkModeFromCmd(t *testing.T) {
	cases := []struct {
		cmd        string
		port, mode string
	}{
		{"pt.start usb mode=sink period=500", "usb", modeSink},
		{"pt.start usb mode=source period=500", "usb", modeSource},
		{"pt.set usb mode=echo", "usb", modeEcho},
		{"pt.stop usb", "usb", modeEcho},
		{"pt.stop all", "all", modeEcho},
		// Says nothing about a mode, so it must not disturb one that is running.
		{"pt.start usb period=500", "", ""},
		{"pt.caps", "", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		port, mode := linkModeFromCmd(c.cmd)
		if port != c.port || mode != c.mode {
			t.Errorf("%q -> (%q, %q), want (%q, %q)", c.cmd, port, mode, c.port, c.mode)
		}
	}
}

// Short printable text is printed so a person can see a live echo; a stream is
// counted, or one log line per block would bury the board's own frames.
func TestLogPrintsLinesAndCountsStreams(t *testing.T) {
	var got []string
	lg := &linkLogger{log: func(s string) { got = append(got, s) }, last: time.Now()}

	lg.saw("收到", []byte("42\r\n"))
	if len(got) != 1 || !strings.Contains(got[0], "42") {
		t.Fatalf("a short line was not printed as itself: %v", got)
	}

	lg.saw("收到", bytes.Repeat([]byte("x"), 4096))
	if len(got) != 1 {
		t.Fatalf("a stream block was printed instead of counted: %v", got)
	}
	lg.flush()
	if len(got) != 2 || !strings.Contains(got[1], "4096") {
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
