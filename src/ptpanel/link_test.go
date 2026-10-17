package ptpanel

import (
	"bytes"
	"io"
	"sync"
	"testing"
	"time"
)

// No board and no simulated board: the simulated board plays every peer itself,
// so a simulated pass gets no further than bindLink; sending, echoing and the
// sink / source switch are covered only here. See
// $PROD/docs/engineering/HOW-TO-RUN-PORTTOOL-TESTS.md "T4-02 的判据".

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

// Sink mode: the board counts bytes this end pushes; nobody pushing reads as
// zero rx_bytes and kbps however healthy the hardware is.
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

// mirrorFake is a board in mode=mirror: every write comes straight back.
type mirrorFake struct{ *fakePort }

// The lock keeps a write racing stop from sending on the closed feed.
func (m mirrorFake) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return 0, io.ErrClosedPipe
	}
	select {
	case m.feed <- append([]byte(nil), p...):
	case <-time.After(time.Second): // the reader has gone; Close is waiting on the lock
	}
	return len(p), nil
}

// In mirror mode this end floods and measures instead of echoing, and the
// numbers are there for judging (PORTTOOL-FLOW.md B.9).
func TestLinkMirrorFloods(t *testing.T) {
	port := mirrorFake{newFakePort()}
	l := newLinkPort("usb", "COM9", 0, "serial", port, nil)
	defer l.stop()

	l.setMode(modeMirror)
	time.Sleep(200 * time.Millisecond)
	fl, ok := l.peer.Flooding()
	if !ok {
		t.Fatal("mirror mode started no flood")
	}
	got := map[string]string{}
	for _, kv := range fl.Fields() {
		got[kv[0]] = kv[1]
	}
	if got["peer_sent"] == "" || got["peer_sent"] == "0" || got["peer_lost"] != "0" {
		t.Fatalf("flood fields %v", got)
	}
	if l.peer.Stats().Echoes != 0 {
		t.Fatal("this end echoed while the board was mirroring")
	}
	l.setMode(modeEcho)
	if _, ok := l.peer.Flooding(); ok {
		t.Fatal("leaving mirror mode left the flood running")
	}
}
