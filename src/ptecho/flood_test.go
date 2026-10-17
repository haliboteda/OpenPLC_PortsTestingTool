package ptecho

import (
	"io"
	"sync"
	"testing"
	"time"
)

// mirrorPort sends every write back to the reader, like a board in
// mode=mirror; alter may drop or change a write on its way back.
type mirrorPort struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	closed bool
	writes int
	alter  func(n int, b []byte) []byte
}

func newMirrorPort(alter func(n int, b []byte) []byte) *mirrorPort {
	m := &mirrorPort{alter: alter}
	m.cond = sync.NewCond(&m.mu)
	return m
}

func (m *mirrorPort) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return 0, io.ErrClosedPipe
	}
	b := append([]byte(nil), p...)
	if m.alter != nil {
		b = m.alter(m.writes, b)
	}
	m.writes++
	m.buf = append(m.buf, b...)
	m.cond.Broadcast()
	return len(p), nil
}

func (m *mirrorPort) Read(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for len(m.buf) == 0 && !m.closed {
		m.cond.Wait()
	}
	if len(m.buf) == 0 {
		return 0, io.EOF
	}
	n := copy(p, m.buf)
	m.buf = m.buf[n:]
	return n, nil
}

func (m *mirrorPort) Close() error {
	m.mu.Lock()
	m.closed = true
	m.cond.Broadcast()
	m.mu.Unlock()
	return nil
}

func runFlood(t *testing.T, alter func(int, []byte) []byte) FloodStats {
	t.Helper()
	p := New("mirror", newMirrorPort(alter), nil)
	defer p.Stop()
	p.StartFlood(FloodOptions{PacketSize: 63, InFlight: 630})
	time.Sleep(200 * time.Millisecond)
	st, ok := p.StopFlood(2 * time.Second)
	if !ok {
		t.Fatal("StopFlood said no flood was running")
	}
	return st
}

func TestFloodPerfectMirror(t *testing.T) {
	st := runFlood(t, nil)
	if st.Sent == 0 || st.Echoed != st.Sent {
		t.Fatalf("sent %d, echoed %d: a perfect mirror returns every packet", st.Sent, st.Echoed)
	}
	if st.Lost != 0 || st.Bad != 0 {
		t.Fatalf("lost %d bad %d on a perfect mirror", st.Lost, st.Bad)
	}
	if st.Kbps == 0 || st.RTTMaxUS < st.RTTMinUS || st.RTTAvgUS < st.RTTMinUS {
		t.Fatalf("rate and latency not measured: %+v", st)
	}
}

func TestFloodCountsDroppedPacket(t *testing.T) {
	st := runFlood(t, func(n int, b []byte) []byte {
		if n == 3 {
			return nil
		}
		return b
	})
	if st.Lost != 1 || st.Bad != 0 {
		t.Fatalf("one packet dropped: want lost 1 bad 0, got %+v", st)
	}
	if st.Echoed+st.Lost != st.Sent {
		t.Fatalf("echoed %d + lost %d != sent %d", st.Echoed, st.Lost, st.Sent)
	}
}

func TestFloodCountsAlteredPacket(t *testing.T) {
	st := runFlood(t, func(n int, b []byte) []byte {
		if n == 5 {
			b[20] = 'x'
		}
		return b
	})
	if st.Bad != 1 || st.Lost != 0 {
		t.Fatalf("one packet altered: want bad 1 lost 0, got %+v", st)
	}
}

func TestFloodDoesNotEcho(t *testing.T) {
	m := newMirrorPort(nil)
	p := New("mirror", m, nil)
	defer p.Stop()
	p.StartFlood(FloodOptions{PacketSize: 63, InFlight: 630})
	time.Sleep(100 * time.Millisecond)
	st, _ := p.StopFlood(time.Second)
	if got := p.Stats().Echoes; got != 0 {
		t.Fatalf("the peer echoed %d blocks during a flood; the board is the mirror", got)
	}
	m.mu.Lock()
	writes := m.writes
	m.mu.Unlock()
	if uint64(writes) != st.Sent {
		t.Fatalf("%d writes for %d packets: something besides the flood wrote", writes, st.Sent)
	}
}
