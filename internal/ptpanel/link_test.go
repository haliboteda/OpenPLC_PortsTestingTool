package ptpanel

import (
	"bytes"
	"io"
	"sync"
	"testing"
	"time"
)

// *** These tests need no board and no simulated board. ***
//
// That is the point of them. The far end of a link is the one piece the
// simulated board cannot exercise: it plays every peer itself, inside the
// firmware, so the code in link.go never runs during a simulated pass. A
// session mode that broke this end would sail through T4-02 and fail the moment
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

// Sink mode means the board sits there counting bytes somebody else pushes. If
// nobody pushes, rx_bytes and kbps stay zero however healthy the hardware is -
// which is what the panel did until 2026-09-14.
func TestSinkModePushesAtTheBoard(t *testing.T) {
	fp := newFakePort()
	l := newLinkPort("usb", "fake", 0, "serial", fp, nil)

	l.setMode(modeSink)
	waitFor(t, func() bool { return fp.bytesWritten() > 0 })
	l.setMode(modeEcho)

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
	l := newLinkPort("rs485", "fake", 0, "serial", fp, nil)

	l.setMode(modeEcho)
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
