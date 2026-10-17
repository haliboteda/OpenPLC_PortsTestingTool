package ptlib

// The fixture tests' DO -> relay -> reference DI chain (decision 133,
// PORTTOOL-FLOW.md C.3.6 「DO、继电器一条链」): each DO under test goes, direct
// or through one of its relays, to the reference DI the registration names
// (decision 135, refboard). The reference DI reads high or low only; matching
// what the DO should be is the whole criterion.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"PortTool/src/ptboard"
	"PortTool/src/ptcheck"
	"PortTool/src/pthold"
	"PortTool/src/ptitems"
	"PortTool/src/ptproto"
	"PortTool/src/ptreport"
	"PortTool/src/ptseq"
	"PortTool/src/refboard"
)

// errNoRef: a chain item needs the reference board and none is connected.
var errNoRef = errors.New("no reference board is connected")

// chainDO is the item that tests the DOs on the chain.
const chainDO = "dout.load"

const (
	// ReasonCheckChain: the chain cannot tell a broken DO from a broken relay;
	// check both on their own in R&D (decision 133).
	ReasonCheckChain = "check_chain"
	// ReasonRefUnwired: the registration wires this channel to no reference
	// channel, or the relay sits on no DO (decision 135).
	ReasonRefUnwired = "ref_unwired"
)

// wiring is the reference board's registration this run uses.
func (e *Engine) wiring() refboard.Wiring {
	if e.Wiring != nil {
		return e.Wiring()
	}
	return refboard.Default()
}

// onChain says a pick tests the DOs through the relays into the reference DI.
func onChain(it ptitems.Item, peer ptitems.Peer) bool {
	return it.ID == chainDO && peer == ptitems.PeerRef
}

func intList(chs []int) string {
	s := make([]string, len(chs))
	for i, c := range chs {
		s[i] = strconv.Itoa(c)
	}
	return strings.Join(s, ",")
}

// beatFrames is how many reference frames a DO beat waits: its hold, at
// least two frames so the reading is one sampled after the change.
func beatFrames(p refboard.Params) int {
	n := (p.DOHold + p.DIPeriod - 1) / max(p.DIPeriod, 1)
	return max(n, 2)
}

// runChainDO closes the relays the ticked DOs run through, drives those DOs
// high one at a time and then all low, and has the reference DI each is wired
// to read every beat.
func (e *Engine) runChainDO(ctx context.Context, j *job) (ptreport.Outcome, string, string) {
	w := e.wiring()
	p := w.Params
	att := ptreport.Attempt{N: 1, StartedAt: time.Now()}
	var shown []ptcheck.Result
	finish := func(o ptreport.Outcome, reason, why string) (ptreport.Outcome, string, string) {
		att.Outcome, att.Err = o, why
		att.DurationMS = time.Since(att.StartedAt).Milliseconds()
		e.addAttempts(j, att)
		vals := valuesOf(att)
		if shown == nil {
			shown = att.Checks
		}
		e.change(j.item.ID, func(s *State) { s.Checks, s.Values = shown, vals })
		return o, reason, why
	}
	var chs, rys, refs []int
	to := map[int]int{} // DO under test -> reference DI
	for _, c := range dropChannels(j.params) {
		l := w.DOLink(c)
		if l.To == 0 {
			e.setChan(j, c, ptreport.OutcomeError, ReasonRefUnwired)
			continue
		}
		chs, refs, to[c] = append(chs, c), append(refs, l.To), l.To
		if l.Via > 0 {
			rys = append(rys, l.Via)
		}
	}
	if len(chs) == 0 {
		return finish(ptreport.OutcomeError, ReasonRefUnwired, "")
	}
	ref := e.Runner().Ref
	if ref == nil {
		return finish(ptreport.OutcomeError, ReasonNoRef, "")
	}
	send := func(b *ptboard.Board, cmd string) error {
		att.Sent = append(att.Sent, cmd)
		lines, err := b.Send(cmd, ptboard.ExpectOne, ptboard.DefaultTimeout)
		att.Raw = append(att.Raw, lines...)
		return err
	}

	// 24 V on the outputs: the deadman drops them if the PC goes away.
	att.Sent = append(att.Sent, pthold.Command())
	keeper, err := pthold.Arm(e.Board)
	if err != nil {
		return finish(ptreport.OutcomeError, "", "could not arm the deadman: "+err.Error())
	}
	defer keeper.Release()
	dutEv, unsubDut := e.Board.Subscribe(256)
	defer unsubDut()
	refEv, unsubRef := ref.Subscribe(256)
	defer unsubRef()

	if len(rys) > 0 {
		if err := send(e.Board, "pt.start relay ch="+intList(rys)+" mode=hold on=1"); err != nil {
			_ = send(e.Board, "pt.stop relay")
			return finish(ptreport.OutcomeError, "", err.Error())
		}
		defer func() { _ = send(e.Board, "pt.stop relay") }()
		if _, err := waitFrames(ctx, dutEv, "relay", 1, 5*time.Second); err != nil {
			return finish(frameOutcome(ctx, err), "", err.Error())
		}
	}
	if err := send(ref, fmt.Sprintf("pt.start din ch=%s period=%d", intList(refs), p.DIPeriod)); err != nil {
		_ = send(ref, "pt.stop din")
		return finish(ptreport.OutcomeError, "", "reference board: "+err.Error())
	}
	defer func() { _ = send(ref, "pt.stop din") }()
	defer func() { _ = send(e.Board, "pt.stop dout") }()

	// One DO high per beat, then all low: only the matching DI may read high,
	// so swapped or shorted wires show (decision 133).
	beats := append(append([]int(nil), chs...), 0)
	frames := beatFrames(p)
	wait := time.Duration(frames*p.DIPeriod)*time.Millisecond + 5*time.Second
	pass := true
	var mine []ptcheck.Result // each beat's own channel, and the all-low beat
	for i, on := range beats {
		duty := make([]string, len(chs))
		for k, c := range chs {
			duty[k] = strconv.Itoa(c) + ":0"
			if c == on {
				duty[k] = strconv.Itoa(c) + ":255"
			}
		}
		cmd := "pt.set dout duty=" + strings.Join(duty, ",")
		if i == 0 {
			cmd = "pt.start dout mode=hold ch=" + intList(chs) + " duty=" + strings.Join(duty, ",")
		}
		if err := send(e.Board, cmd); err != nil {
			return finish(ptreport.OutcomeError, "", err.Error())
		}
		if _, err := waitFrames(ctx, dutEv, "dout", 1, 5*time.Second); err != nil {
			return finish(frameOutcome(ctx, err), "", err.Error())
		}
		got, err := waitFrames(ctx, refEv, "din", frames, wait)
		if err != nil {
			return finish(frameOutcome(ctx, err), "", "reference board: "+err.Error())
		}
		f := got[len(got)-1]
		att.Raw = append(att.Raw, f.Raw)
		checks := make([]ptcheck.Check, len(chs))
		for k, c := range chs {
			want := "0"
			if c == on {
				want = "1"
			}
			checks[k] = ptcheck.Check{Field: "ch" + strconv.Itoa(to[c]), Op: ptcheck.OpEq, Value: want}
		}
		res, ok := ptcheck.EvalAll(checks, ptseq.FrameLookup(f))
		att.Checks = append(att.Checks, res...)
		pass = pass && ok
		for k, r := range res {
			if chs[k] == on || on == 0 {
				mine = append(mine, r)
			}
		}
	}
	// Each DO its own result: every beat's reading of its DI (decision 129 「多路」).
	for _, c := range chs {
		o, why := ptreport.OutcomePass, ""
		for _, r := range att.Checks {
			if r.Check.Field == "ch"+strconv.Itoa(to[c]) && !r.Pass {
				o, why = ptreport.OutcomeFail, ReasonCheckChain
			}
		}
		e.setChan(j, c, o, why)
	}
	// The card lists what failed, else each channel's own beat and the all-low one.
	shown = mine
	if !pass {
		shown = nil
		for _, r := range att.Checks {
			if !r.Pass {
				shown = append(shown, r)
			}
		}
		return finish(ptreport.OutcomeFail, ReasonCheckChain, "")
	}
	return finish(ptreport.OutcomePass, "", "")
}

// chainCount counts each relay's cycles on the reference DI its DO is wired
// to while the relays switch: a fall from high to low is one, a level counts
// once it held for settle frames.
type chainCount struct {
	mu      sync.Mutex
	refOf   map[int]int    // relay -> reference DI
	settle  int            // frames a new level must hold
	period  int            // the reference DI's frame period, ms
	level   map[int]string // settled level per relay
	cand    map[int]string // level waiting to settle
	held    map[int]int    // frames the candidate has held
	falls   map[int]uint64
	frames  int
	last    uint32
	maxGap  uint32 // ms between two frames before a cycle could hide in the gap
	gapErr  string
	more    chan struct{}
	stopped chan struct{}
}

func (c *chainCount) add(f ptproto.Frame) {
	c.mu.Lock()
	if c.frames > 0 && f.Tick-c.last > c.maxGap && c.gapErr == "" {
		c.gapErr = fmt.Sprintf("the reference DI went %d ms without a frame; a cycle may have been missed", f.Tick-c.last)
	}
	c.frames++
	c.last = f.Tick
	for ry, di := range c.refOf {
		v, ok := ptproto.Get(f.Fields, "ch"+strconv.Itoa(di))
		if !ok {
			continue
		}
		cur, seen := c.level[ry]
		switch {
		case !seen:
			c.level[ry] = v
		case v == cur:
			c.held[ry] = 0
		case v == c.cand[ry] && c.held[ry] > 0:
			c.held[ry]++
		default:
			c.cand[ry], c.held[ry] = v, 1
		}
		if seen && c.held[ry] >= c.settle {
			if cur == "1" && v == "0" {
				c.falls[ry]++
			}
			c.level[ry], c.held[ry] = v, 0
		}
	}
	c.mu.Unlock()
	select {
	case c.more <- struct{}{}:
	default:
	}
}

func (c *chainCount) count(ry int) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.falls[ry]
}

// settleWait waits for n more frames, so the last fall has had time to count.
func (c *chainCount) settleWait(ctx context.Context, n int) {
	c.mu.Lock()
	want := c.frames + n
	c.mu.Unlock()
	deadline := time.After(time.Duration(n*c.period)*time.Millisecond + 2*time.Second)
	for {
		c.mu.Lock()
		got := c.frames
		c.mu.Unlock()
		if got >= want {
			return
		}
		select {
		case <-c.more:
		case <-deadline:
			return
		case <-ctx.Done():
			return
		}
	}
}

// startChain prepares the relay item's chain: the DO each ticked relay sits
// on held high and the reference DI that DO is wired to counting. A relay on
// no DO, or on a DO wired to nothing, gets Ref 0 in chans and is not counted.
// held says the timed phase already holds the deadman. The returned stop ends
// both.
func (e *Engine) startChain(chans []RelayChan, held bool, att *ptreport.Attempt) (*chainCount, func(), error) {
	w := e.wiring()
	p := w.Params
	refOf := map[int]int{}
	var dos, refs []int
	minHold := uint64(0)
	for i := range chans {
		c := &chans[i]
		do := w.DOOfRelay(c.Ch)
		if do == 0 || w.DOLink(do).To == 0 {
			continue
		}
		c.Ref = w.DOLink(do).To
		refOf[c.Ch] = c.Ref
		dos, refs = append(dos, do), append(refs, c.Ref)
		for _, h := range []uint64{c.CloseMS, c.OpenMS} {
			if minHold == 0 || h < minHold {
				minHold = h
			}
		}
	}
	var stops []func()
	stop := func() {
		for i := len(stops) - 1; i >= 0; i-- {
			stops[i]()
		}
	}
	if len(dos) == 0 {
		return &chainCount{refOf: refOf}, stop, nil
	}
	ref := e.Runner().Ref
	if ref == nil {
		return nil, stop, errNoRef
	}
	send := func(b *ptboard.Board, cmd string) error {
		att.Sent = append(att.Sent, cmd)
		lines, err := b.Send(cmd, ptboard.ExpectOne, ptboard.DefaultTimeout)
		att.Raw = append(att.Raw, lines...)
		return err
	}
	if !held {
		att.Sent = append(att.Sent, pthold.Command())
		keeper, err := pthold.Arm(e.Board)
		if err != nil {
			return nil, stop, fmt.Errorf("could not arm the deadman: %w", err)
		}
		stops = append(stops, keeper.Release)
	}
	duty := make([]string, len(dos))
	for i, d := range dos {
		duty[i] = strconv.Itoa(d) + ":255"
	}
	stops = append(stops, func() { _ = send(e.Board, "pt.stop dout") })
	if err := send(e.Board, "pt.start dout mode=hold ch="+intList(dos)+" duty="+strings.Join(duty, ",")); err != nil {
		return nil, stop, err
	}
	c := &chainCount{refOf: refOf, settle: p.DISettle, period: p.DIPeriod,
		level: map[int]string{}, cand: map[int]string{}, held: map[int]int{}, falls: map[int]uint64{},
		maxGap: uint32(minHold / 2), more: make(chan struct{}, 1), stopped: make(chan struct{})}
	events, unsub := ref.Subscribe(1024)
	go func() {
		defer close(c.stopped)
		for ev := range events {
			if ev.Kind == ptproto.LineFrame && ev.Frame.Port == "din" {
				c.add(ev.Frame)
			}
		}
	}()
	stops = append(stops, func() { unsub(); <-c.stopped })
	stops = append(stops, func() { _ = send(ref, "pt.stop din") })
	if err := send(ref, fmt.Sprintf("pt.start din ch=%s period=%d", intList(refs), p.DIPeriod)); err != nil {
		return nil, stop, fmt.Errorf("reference board: %w", err)
	}
	return c, stop, nil
}
