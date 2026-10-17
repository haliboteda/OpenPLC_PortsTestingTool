package ptecho

// The PC half of mode=mirror: numbered packets sent as fast as the window
// allows, checked as the board sends them back, giving rate, loss and latency
// in one run (DECISIONS.md 98, PORTTOOL-FLOW.md B.9 in $PROD).

import (
	"fmt"
	"strconv"
	"sync"
	"time"
)

// FloodOptions sizes one flood.
type FloodOptions struct {
	// PacketSize is bytes per packet, newline included.
	PacketSize int
	// InFlight caps the bytes sent but not yet back. It must stay below the
	// board's mirror buffer, or usb drops what it cannot hold.
	InFlight int
}

// Flood sizes for each link: usb's ring is 2048 bytes and its packets 63, eth
// is flow-controlled by TCP. See porttool_usb.c / porttool_eth.c in $BOOT.
var (
	FloodUSB = FloodOptions{PacketSize: 63, InFlight: 1024}
	FloodTCP = FloodOptions{PacketSize: 1024, InFlight: 16384}
)

// floodHeader is "#<seq> <t_us>"; the rest of a packet is padding.
const floodHeader = 1 + 10 + 1 + 12

// floodStall is how long a full window may wait for any byte back before the
// outstanding bytes are written off, so a dropped packet cannot stop the flood.
const floodStall = time.Second

// FloodStats is what one flood measured. Lost counts packets that never came
// back; Bad those that came back altered.
type FloodStats struct {
	Sent, Echoed, Lost, Bad uint64
	Kbps                    uint64
	RTTMinUS, RTTAvgUS      uint64
	RTTMaxUS                uint64
}

// Fields names the stats the way a plan's checks read them (PRODUCTION-FRAMEWORK.md
// "判据" in $PROD).
func (s FloodStats) Fields() [][2]string {
	u := func(v uint64) string { return strconv.FormatUint(v, 10) }
	return [][2]string{
		{"peer_sent", u(s.Sent)}, {"peer_echoed", u(s.Echoed)},
		{"peer_lost", u(s.Lost)}, {"peer_bad", u(s.Bad)},
		{"peer_kbps", u(s.Kbps)},
		{"peer_rtt_min_us", u(s.RTTMinUS)}, {"peer_rtt_avg_us", u(s.RTTAvgUS)},
		{"peer_rtt_max_us", u(s.RTTMaxUS)},
	}
}

type flood struct {
	opt   FloodOptions
	start time.Time

	mu       sync.Mutex
	cond     *sync.Cond
	stopping bool
	sent     uint64 // packets
	txBytes  uint64
	rxBytes  uint64
	written  uint64 // bytes written off by a stall
	lastRx   time.Time
	line     []byte
	expected uint64 // next sequence number due back
	badSince uint64 // bad lines since the last good one
	echoed   uint64
	lost     uint64
	bad      uint64
	echoB    uint64 // bytes in good packets
	lastGood time.Time
	rttMin   uint64
	rttMax   uint64
	rttSum   uint64

	done chan struct{}
}

// StartFlood starts sending numbered packets and stops echoing: from here
// until StopFlood every byte that arrives is checked, not sent back, because
// the board is the one mirroring.
func (p *Peer) StartFlood(opt FloodOptions) {
	if opt.PacketSize < floodHeader+2 {
		opt.PacketSize = floodHeader + 2
	}
	if opt.InFlight < opt.PacketSize {
		opt.InFlight = opt.PacketSize
	}
	f := &flood{opt: opt, start: time.Now(), done: make(chan struct{})}
	f.cond = sync.NewCond(&f.mu)
	f.lastRx = f.start
	if old := p.flood.Swap(f); old != nil {
		old.halt()
	}
	go f.send(p)
}

// StopFlood stops sending, waits up to drain for what is still in flight,
// and returns the result. ok is false when no flood was running.
func (p *Peer) StopFlood(drain time.Duration) (FloodStats, bool) {
	f := p.flood.Load()
	if f == nil {
		return FloodStats{}, false
	}
	f.halt()
	deadline := time.Now().Add(drain)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		back := f.expected >= f.sent
		f.mu.Unlock()
		if back {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	p.flood.CompareAndSwap(f, nil)
	return f.stats(true), true
}

// Flooding reports the running flood's numbers so far.
func (p *Peer) Flooding() (FloodStats, bool) {
	f := p.flood.Load()
	if f == nil {
		return FloodStats{}, false
	}
	return f.stats(false), true
}

func (f *flood) halt() {
	f.mu.Lock()
	if !f.stopping {
		f.stopping = true
		f.cond.Broadcast()
	}
	f.mu.Unlock()
	<-f.done
}

func (f *flood) send(p *Peer) {
	defer close(f.done)
	pkt := make([]byte, f.opt.PacketSize)
	for {
		f.mu.Lock()
		for !f.stopping && f.txBytes-f.rxBytes-f.written+uint64(len(pkt)) > uint64(f.opt.InFlight) {
			if time.Since(f.lastRx) > floodStall {
				f.written = f.txBytes - f.rxBytes
				break
			}
			f.mu.Unlock()
			time.Sleep(time.Millisecond)
			f.mu.Lock()
		}
		if f.stopping {
			f.mu.Unlock()
			return
		}
		seq := f.sent
		f.sent++
		f.txBytes += uint64(len(pkt))
		f.mu.Unlock()

		f.fill(pkt, seq, uint64(time.Since(f.start)/time.Microsecond))
		if _, err := p.Write(pkt); err != nil {
			return
		}
	}
}

func (f *flood) fill(pkt []byte, seq, tUS uint64) {
	h := fmt.Sprintf("#%010d %012d", seq%1e10, tUS%1e12)
	n := copy(pkt, h)
	for i := n; i < len(pkt)-1; i++ {
		pkt[i] = '.'
	}
	pkt[len(pkt)-1] = '\n'
}

// take checks bytes the board mirrored back.
func (f *flood) take(b []byte) {
	now := time.Now()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rxBytes += uint64(len(b))
	f.lastRx = now
	for _, c := range b {
		if c != '\n' {
			if len(f.line) <= f.opt.PacketSize {
				f.line = append(f.line, c)
			}
			continue
		}
		f.check(f.line, now)
		f.line = f.line[:0]
	}
}

func (f *flood) check(line []byte, now time.Time) {
	seq, tUS, ok := f.parse(line)
	if !ok || seq < f.expected || seq >= f.sent {
		f.bad++
		f.badSince++
		return
	}
	if gap := seq - f.expected; gap > f.badSince {
		f.lost += gap - f.badSince
	}
	f.badSince = 0
	f.expected = seq + 1
	f.echoed++
	f.echoB += uint64(f.opt.PacketSize)
	f.lastGood = now
	rtt := uint64(now.Sub(f.start)/time.Microsecond) - tUS
	if f.echoed == 1 || rtt < f.rttMin {
		f.rttMin = rtt
	}
	if rtt > f.rttMax {
		f.rttMax = rtt
	}
	f.rttSum += rtt
}

func (f *flood) parse(line []byte) (seq, tUS uint64, ok bool) {
	if len(line) != f.opt.PacketSize-1 || line[0] != '#' || line[11] != ' ' {
		return 0, 0, false
	}
	for _, c := range line[floodHeader:] {
		if c != '.' {
			return 0, 0, false
		}
	}
	s, err1 := strconv.ParseUint(string(line[1:11]), 10, 64)
	t, err2 := strconv.ParseUint(string(line[12:floodHeader]), 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return s, t, true
}

// stats with final set also counts what never came back as lost.
func (f *flood) stats(final bool) FloodStats {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := FloodStats{Sent: f.sent, Echoed: f.echoed, Lost: f.lost, Bad: f.bad,
		RTTMinUS: f.rttMin, RTTMaxUS: f.rttMax}
	if final && f.sent > f.expected {
		if out := f.sent - f.expected; out > f.badSince {
			s.Lost += out - f.badSince
		}
	}
	if f.echoed > 0 {
		s.RTTAvgUS = f.rttSum / f.echoed
		if ms := uint64(f.lastGood.Sub(f.start) / time.Millisecond); ms > 0 {
			s.Kbps = f.echoB * 8 / ms
		}
	}
	return s
}
