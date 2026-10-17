package simboard

// Two simulated boards wired as the board under test and the reference board
// (STG-02, decision 133): the reference DO1-8 drive DI1-8 under test; DO1-6
// under test drive the reference DI1-6 through relays RY1-RY6 under test, and
// DO7 / DO8 drive the reference DI7 / DI8 directly (STG-36 A). The two
// simulator processes have no wire between them, so it is played here: !dout
// and !relay frames from one become a sim.din on the other, and an !aout
// frame's current a sim.ain on the other's current-range input, AOn -> AIn
// both ways (refboard.Default). Only current is played: a voltage-range AO
// cannot be tested here (PORTTOOL-FLOW.md C.3.7). CAN is not wired between
// the two; each simulator's CAN answers itself, except to the CAN analyser
// window's end (can.go).

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"PortTool/src/ptproto"
)

// OpenPair opens the board under test and the reference board by name. When
// both are the simulated board they are wired to each other; otherwise each
// is opened as OpenPort would.
func OpenPair(dut, ref string, baud int) (io.ReadWriteCloser, io.ReadWriteCloser, error) {
	a, err := OpenPort(dut, baud)
	if err != nil {
		return nil, nil, err
	}
	b, err := OpenPort(ref, baud)
	if err != nil {
		_ = a.Close()
		return nil, nil, fmt.Errorf("reference board: %w", err)
	}
	Link(a, b)
	return a, b, nil
}

// Link wires two simulated boards opened by OpenPort, DO to each other's DI,
// and reports whether it did: anything else is left alone. The panel links a
// reference board connected after the board under test (PORTTOOL-FLOW.md
// C.3.7). Both inputs start at 0: nothing is driving them yet.
func Link(a, b io.ReadWriteCloser) bool {
	wa, ok := a.(*wired)
	wb, ok2 := b.(*wired)
	if !ok || !ok2 || wa == wb {
		return false
	}
	wb.isRef.Store(true)
	wa.peer.Store(wb)
	wb.peer.Store(wa)
	wa.inject("sim.din 0x00")
	wb.inject("sim.din 0x00")
	return true
}

// relayMask is the DOs under test that reach the reference DI through a
// relay: 1-6; DO7 / DO8 go straight (decision 133, STG-36 A).
const relayMask = 0x3F

// wired is one simulated board's end. Its reads are watched for !dout and
// !relay frames and its writes for the commands that stop them; with no peer
// linked it only passes bytes through.
type wired struct {
	inner io.ReadWriteCloser
	peer  atomic.Pointer[wired]
	isRef atomic.Bool         // the second board given to Link
	can   atomic.Pointer[CAN] // the CAN analyser window's end, while open (can.go)

	wmu  sync.Mutex // one line at a time into the simulator's stdin
	line []byte     // touched only by the reader

	omu   sync.Mutex // the outputs as last reported, for the other board's DI
	douts int        // DO channels driven, bit0 = DO1
	relay int        // relays closed, bit0 = RY1
}

// drive puts this board's outputs on the other board's DI.
func (w *wired) drive(peer *wired) {
	w.omu.Lock()
	bits := w.douts
	if !w.isRef.Load() {
		bits &= w.relay | ^relayMask
	}
	w.omu.Unlock()
	peer.inject(fmt.Sprintf("sim.din 0x%02X", bits))
}

func (w *wired) setOut(douts, relay *int) {
	w.omu.Lock()
	if douts != nil {
		w.douts = *douts
	}
	if relay != nil {
		w.relay = *relay
	}
	w.omu.Unlock()
}

func (w *wired) Read(p []byte) (int, error) {
	n, err := w.inner.Read(p)
	for _, c := range p[:n] {
		if c == '\n' {
			w.seen(strings.TrimRight(string(w.line), "\r"))
			w.line = w.line[:0]
			continue
		}
		w.line = append(w.line, c)
	}
	return n, err
}

func (w *wired) Write(p []byte) (int, error) {
	w.wmu.Lock()
	n, err := w.inner.Write(p)
	w.wmu.Unlock()
	cmd := strings.TrimSpace(string(p))
	zero := 0
	if cmd == "pt.stop dout" || cmd == "pt.stop all" {
		w.setOut(&zero, nil)
	}
	if cmd == "pt.stop relay" || cmd == "pt.stop all" {
		w.setOut(nil, &zero)
	}
	if p := w.peer.Load(); p != nil && (cmd == "pt.stop dout" || cmd == "pt.stop relay" || cmd == "pt.stop all") {
		w.drive(p)
	}
	if p := w.peer.Load(); p != nil && (cmd == "pt.stop aout" || cmd == "pt.stop all") {
		for ai := 1; ai <= 2; ai++ {
			if ch := w.aoutTo(ai); ch > 0 {
				p.inject(fmt.Sprintf("sim.ain %d 0", ai))
			}
		}
	}
	return n, err
}

// Close unlinks first: a board that went away drives nothing.
func (w *wired) Close() error {
	if p := w.peer.Swap(nil); p != nil {
		p.peer.CompareAndSwap(w, nil)
		p.inject("sim.din 0x00")
	}
	return w.inner.Close()
}

// Cut takes one port of a simulated board opened by OpenPort off its cable for
// ms, after which it comes back by itself (sim.cut; PORTTOOL-FLOW.md C.3.6
// 「断开恢复」).
func Cut(port io.ReadWriteCloser, boardPort string, ms int) error {
	w, ok := port.(*wired)
	if !ok {
		return errors.New("not the simulated board")
	}
	w.inject(fmt.Sprintf("sim.cut %s %d", boardPort, ms))
	return nil
}

func (w *wired) inject(cmd string) {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	_, _ = io.WriteString(w.inner, cmd+"\n")
}

// seen turns this board's DO and relays, as their frames report them, into
// the other board's DI: a DO with a non-zero duty, a relay at 1.
func (w *wired) seen(line string) {
	peer, can := w.peer.Load(), w.can.Load()
	if peer == nil && can == nil {
		return
	}
	kind, body := ptproto.Classify(line)
	if kind != ptproto.LineFrame {
		return
	}
	f, ok := ptproto.ParseFrame(body)
	if ok && f.Port == "can.tx" && can != nil {
		can.sent(f)
		return
	}
	if peer == nil {
		return
	}
	if ok && f.Port == "aout" {
		w.seenAout(peer, f)
		return
	}
	if !ok || (f.Port != "dout" && f.Port != "relay") {
		return
	}
	bits := 0
	for ch := 1; ch <= 8; ch++ {
		v, ok := ptproto.Get(f.Fields, "ch"+strconv.Itoa(ch))
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			bits |= 1 << (ch - 1)
		}
	}
	if f.Port == "dout" {
		w.setOut(&bits, nil)
	} else {
		w.setOut(nil, &bits)
	}
	w.drive(peer)
}

// aoutTo is which of this board's AO channels drives the other board's AI ai:
// the same number, on either board (refboard.Default).
func (w *wired) aoutTo(ai int) int { return ai }

// seenAout puts an !aout frame's terminal current (ch<n>=asked/quantised/uA)
// on the other board's AI as the pin voltage a current range makes of it:
// 124 R to ground (HARDWARE-FACTS.md "模拟输入前端").
func (w *wired) seenAout(peer *wired, f ptproto.Frame) {
	for ai := 1; ai <= 2; ai++ {
		ch := w.aoutTo(ai)
		if ch == 0 {
			continue
		}
		v, ok := ptproto.Get(f.Fields, "ch"+strconv.Itoa(ch))
		if !ok {
			continue
		}
		parts := strings.Split(v, "/")
		ua, err := strconv.Atoi(parts[len(parts)-1])
		if len(parts) != 3 || err != nil {
			continue
		}
		peer.inject(fmt.Sprintf("sim.ain %d %d", ai, (ua*124+500)/1000))
	}
}

// Unwrap is the simulator behind a wired end, so Exited and Path still see it.
func Unwrap(port io.ReadWriteCloser) io.ReadWriteCloser {
	if w, ok := port.(*wired); ok {
		return w.inner
	}
	return port
}
