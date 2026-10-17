package slcan

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCANable answers the SLCAN commands the way the adapter does and records
// every frame it is asked to transmit. bus() puts a frame on its receive side.
type fakeCANable struct {
	pr *io.PipeReader
	pw *io.PipeWriter

	mu     sync.Mutex
	buf    bytes.Buffer
	cmds   []string
	sent   []string
	refuse map[string]bool
	open   bool
}

func newFake() *fakeCANable {
	pr, pw := io.Pipe()
	return &fakeCANable{pr: pr, pw: pw, refuse: map[string]bool{}}
}

func (f *fakeCANable) Read(p []byte) (int, error) { return f.pr.Read(p) }

func (f *fakeCANable) Write(p []byte) (int, error) {
	f.mu.Lock()
	f.buf.Write(p)
	var replies []string
	for {
		s := f.buf.String()
		i := strings.IndexByte(s, '\r')
		if i < 0 {
			break
		}
		cmd := s[:i]
		f.buf.Next(i + 1)
		f.cmds = append(f.cmds, cmd)
		switch {
		case f.refuse[cmd]:
			replies = append(replies, "\a")
		case cmd == "V":
			replies = append(replies, "V1013\r")
		case cmd == "O":
			f.open = true
			replies = append(replies, "\r")
		case cmd == "C":
			f.open = false
			replies = append(replies, "\r")
		case strings.HasPrefix(cmd, "S"):
			replies = append(replies, "\r")
		case strings.HasPrefix(cmd, "t") || strings.HasPrefix(cmd, "T"):
			f.sent = append(f.sent, cmd)
			replies = append(replies, "z\r")
		default:
			replies = append(replies, "\a")
		}
	}
	f.mu.Unlock()
	go func() {
		for _, r := range replies {
			_, _ = io.WriteString(f.pw, r)
		}
	}()
	return len(p), nil
}

func (f *fakeCANable) bus(line string) { go func() { _, _ = io.WriteString(f.pw, line+"\r") }() }

func (f *fakeCANable) Close() error { return f.pw.Close() }

func (f *fakeCANable) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cmds...)
}

func (f *fakeCANable) transmitted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	for _, f := range []Frame{
		{ID: 0x123, Data: []byte{0, 0, 0, 5}},
		{ID: 0x7FF},
		{ID: 0x1ABCDEF0, Ext: true, Data: []byte{1, 2, 3, 4, 5, 6, 7, 8}},
	} {
		text, err := Encode(f)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := Decode(text)
		if !ok || got.ID != f.ID || got.Ext != f.Ext || !bytes.Equal(got.Data, f.Data) {
			t.Errorf("%+v -> %q -> %+v ok=%v", f, text, got, ok)
		}
	}
	if s, _ := Encode(Frame{ID: 0x123, Data: []byte{0xAB}}); s != "t1231AB" {
		t.Errorf("standard frame is %q, want t1231AB (can_send.py's spelling)", s)
	}
	for _, bad := range []Frame{{ID: 0x800}, {ID: 1 << 29, Ext: true}, {Data: make([]byte, 9)}} {
		if _, err := Encode(bad); err == nil {
			t.Errorf("%+v encoded without complaint", bad)
		}
	}
	for _, junk := range []string{"", "z", "t12", "t123Z", "t1239", "t1232AB", "x123100"} {
		if _, ok := Decode(junk); ok {
			t.Errorf("%q decoded as a frame", junk)
		}
	}
}

func TestOpenSendsTheAnalysersSequence(t *testing.T) {
	f := newFake()
	a, err := Open(f, 500000)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	got := strings.Join(f.commands(), ",")
	if got != "C,S6,V,O" {
		t.Errorf("commands %s, want C,S6,V,O", got)
	}
}

func TestOpenRefusesAnUnknownBitrateAndARefusedOpen(t *testing.T) {
	if _, err := Open(newFake(), 123456); err == nil {
		t.Error("123456 bit/s has no SLCAN code but Open accepted it")
	}
	f := newFake()
	f.refuse["O"] = true
	if _, err := Open(f, 500000); err == nil {
		t.Error("the adapter refused O but Open reported success")
	}
}

func TestEchoAnswersLikeABoardInModeEcho(t *testing.T) {
	f := newFake()
	a, err := Open(f, 250000)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	stop := a.Echo()

	// One write, so the three arrive in this order: a board in mode=normal
	// sending seq 5; a value that carries past 32 bits (the rest stays); and
	// a frame too short to answer.
	f.bus("t123400000005t1236FFFFFFFF0102t12320001")

	deadline := time.Now().Add(2 * time.Second)
	for len(f.transmitted()) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	st := stop()

	sent := f.transmitted()
	if len(sent) != 2 || sent[0] != "t123400000006" {
		t.Fatalf("transmitted %v, want t123400000006 first and two in all", sent)
	}
	if sent[1] != "t1236000000000102" {
		t.Errorf("second reply %q, want t1236000000000102", sent[1])
	}
	if st.Received != 3 || st.Replied != 2 || st.Other != 1 {
		t.Errorf("stats %+v, want 3 received, 2 replied, 1 other", st)
	}
}
