package ptseq

// The reference board as a peer (decision 103, STG-18): a PtSession with on
// "ref" runs on the second board connection beside the step after it -
// started before, stopped after, its deadman renewed meanwhile. The rules are
// in $PROD/docs/modules/M4/PRODUCTION-FRAMEWORK.md "在标准机上跑（on）".

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"PortTool/src/ptboard"
	"PortTool/src/ptcheck"
	"PortTool/src/pthold"
	"PortTool/src/ptplan"
	"PortTool/src/ptproto"
	"PortTool/src/ptreport"
	"PortTool/src/serialx"
	"PortTool/src/slcan"
)

const errNoRef = "this step runs on the reference board, and none is connected"

// settleFrames is how many reference frames to wait for after the accompanied
// step has its reading, so the reference has sampled the state at that moment
// (one for the sampling period, one for a frame already in flight).
const settleFrames = 2

// settleCap bounds that wait, so a reference that went quiet still ends in a
// verdict.
const settleCap = 5 * time.Second

// boardFor is the connection a step talks to.
func (r *Runner) boardFor(step ptplan.Step) *ptboard.Board {
	if step.OnRef() {
		return r.Ref
	}
	return r.Board
}

// refFrames collects one port's frames from the reference board.
type refFrames struct {
	mu     sync.Mutex
	frames []ptproto.Frame
	lines  []string
	frozen int // frames judged; 0 means all
	more   chan struct{}
}

func (c *refFrames) add(ev ptboard.Event) {
	c.mu.Lock()
	c.frames = append(c.frames, ev.Frame)
	c.lines = append(c.lines, ev.Line)
	c.mu.Unlock()
	select {
	case c.more <- struct{}{}:
	default:
	}
}

func (c *refFrames) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.frames)
}

// waitFor blocks until there are n frames or the time is up.
func (c *refFrames) waitFor(n int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for c.count() < n {
		left := time.Until(deadline)
		if left <= 0 {
			return false
		}
		select {
		case <-c.more:
		case <-time.After(left):
		}
	}
	return true
}

func (c *refFrames) judged() ([]ptproto.Frame, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(c.frames)
	if c.frozen > 0 {
		n = c.frozen
	}
	return append([]ptproto.Frame(nil), c.frames[:n]...), append([]string(nil), c.lines...)
}

// runPair runs a reference-board session and the step it accompanies. The
// returned outcome is what the next step's gate sees: the first of the two
// that did not pass, or the accompanied step's.
func (r *Runner) runPair(ref, dut ptplan.Step, lastRan ptreport.Outcome, anyRan bool) (refRes, dutRes ptreport.StepResult, out ptreport.Outcome) {
	refRes = ptreport.StepResult{ID: ref.ID, Type: string(ref.Type), StartedAt: r.now()}
	dutRes = ptreport.StepResult{ID: dut.ID, Type: string(dut.Type)}
	skipBoth := func(why, dutWhy string) (ptreport.StepResult, ptreport.StepResult, ptreport.Outcome) {
		refRes.Outcome, refRes.Reason = ptreport.OutcomeSkipped, why
		dutRes.Outcome, dutRes.Reason, dutRes.StartedAt = ptreport.OutcomeSkipped, dutWhy, r.now()
		r.logf("SKIP %s - %s", ref.ID, why)
		r.logf("SKIP %s - %s", dut.ID, dutWhy)
		return refRes, dutRes, ptreport.OutcomeSkipped
	}
	if !ref.IsEnabled() {
		return skipBoth("switched off in the plan", "switched off in the plan")
	}
	if reason, ok := gate(ref.Condition(), lastRan, anyRan); !ok {
		return skipBoth(reason, "its reference-board step "+ref.ID+" was skipped")
	}
	// The accompanied step cannot run without its peer, so it is skipped with
	// the reason rather than run to a misleading failure.
	refFailed := func(att ptreport.Attempt) (ptreport.StepResult, ptreport.StepResult, ptreport.Outcome) {
		att.DurationMS = r.now().Sub(att.StartedAt).Milliseconds()
		refRes.Attempts = []ptreport.Attempt{att}
		refRes.Outcome = att.Outcome
		refRes.DurationMS = r.now().Sub(refRes.StartedAt).Milliseconds()
		dutRes.Outcome, dutRes.StartedAt = ptreport.OutcomeSkipped, r.now()
		dutRes.Reason = "its reference-board step " + ref.ID + " did not start"
		r.logf("%s %s (on ref) - %s", refRes.Outcome, ref.ID, att.Err)
		r.logf("SKIP %s - %s", dut.ID, dutRes.Reason)
		return refRes, dutRes, refRes.Outcome
	}

	att := ptreport.Attempt{N: 1, StartedAt: r.now()}
	if r.Ref == nil {
		att.Outcome, att.Err = ptreport.OutcomeError, errNoRef
		return refFailed(att)
	}
	r.sleep(time.Duration(ref.SleepBeforeMS) * time.Millisecond)
	timeout := time.Duration(ref.Timeout()) * time.Millisecond

	// Armed before the outputs are, for the same reason as the panel's timed
	// run: from pt.start on, the reference may drive 24 V.
	att.Sent = append(att.Sent, pthold.Command())
	keeper, err := pthold.Arm(r.Ref)
	if err != nil {
		att.Outcome, att.Err = ptreport.OutcomeError, "could not arm the reference board's deadman: "+err.Error()
		return refFailed(att)
	}
	events, unsubscribe := r.Ref.Subscribe(1024)
	col := &refFrames{more: make(chan struct{}, 1)}
	quit := make(chan struct{})
	collected := make(chan struct{})
	go func() {
		defer close(collected)
		for {
			select {
			case <-quit:
				return
			case ev, ok := <-events:
				if !ok {
					return
				}
				if ev.Kind == ptproto.LineFrame && ev.Frame.Port == ref.Port {
					col.add(ev)
				}
			}
		}
	}()
	release := func() {
		stopOn(r.Ref, ref.Port, &att)
		att.Sent = append(att.Sent, "pt.hold 0")
		keeper.Release()
		close(quit)
		<-collected
		unsubscribe()
	}

	start := "pt.start " + ref.Port
	if args := ref.ParamArgs(); len(args) > 0 {
		start += " " + strings.Join(args, " ")
	}
	att.Sent = append(att.Sent, start)
	deadline := r.now().Add(timeout)
	lines, err := r.Ref.Send(start, ptboard.ExpectOne, timeout)
	att.Raw = append(att.Raw, lines...)
	if err != nil {
		release()
		r.classifyLinkError(err, deadline, &att)
		return refFailed(att)
	}
	if !col.waitFor(1, timeout) {
		release()
		att.Outcome = ptreport.OutcomeTimeout
		att.Err = "the reference board's " + ref.Port + " session sent no frame within " + timeout.String()
		return refFailed(att)
	}
	r.logf("REF %s started on the reference board", ref.ID)

	r.pairHook = func() {
		col.waitFor(col.count()+settleFrames, settleCap)
		col.mu.Lock()
		col.frozen = len(col.frames)
		col.mu.Unlock()
	}
	dutRes = r.runStepGated(dut, lastRan, anyRan, false)
	r.pairHook = nil

	release()
	frames, raw := col.judged()
	att.Raw = append(att.Raw, raw...)
	if len(frames) == 0 {
		att.Outcome, att.Err = ptreport.OutcomeError, "no frame from the reference board to judge"
	} else {
		r.judge(ref.Checks, WithChanges(FrameLookup(frames[len(frames)-1]), frames), &att)
	}
	att.DurationMS = r.now().Sub(att.StartedAt).Milliseconds()
	refRes.Attempts = []ptreport.Attempt{att}
	refRes.Outcome = att.Outcome
	r.sleep(time.Duration(ref.SleepAfterMS) * time.Millisecond)
	refRes.DurationMS = r.now().Sub(refRes.StartedAt).Milliseconds()
	if refRes.Outcome.IsPass() {
		r.logf("PASS %s (on ref)", ref.ID)
	} else {
		r.logf("%s %s (on ref) - %s", refRes.Outcome, ref.ID, attemptWhy(att))
	}

	out = dutRes.Outcome
	if !refRes.Outcome.IsPass() {
		out = refRes.Outcome
	}
	return refRes, dutRes, out
}

// WithChanges adds _changes_<field>: how many times the field differed between
// consecutive frames. A DO square wave read by the reference DI is judged on
// it (STG-03).
func WithChanges(look ptcheck.Lookup, frames []ptproto.Frame) ptcheck.Lookup {
	return func(field string) (string, bool) {
		name, ok := strings.CutPrefix(field, "_changes_")
		if !ok {
			return look(field)
		}
		changes, seen := 0, false
		var prev string
		for _, f := range frames {
			v, ok := ptproto.Get(f.Fields, name)
			if !ok {
				continue
			}
			if seen && v != prev {
				changes++
			}
			prev, seen = v, true
		}
		if !seen {
			return "", false
		}
		return strconv.Itoa(changes), true
	}
}

// openCANPeer starts this machine's CANable answering like a reference board
// in can mode=echo, for the step's duration. The returned stop records what
// it did on the attempt; calling it twice is harmless.
func (r *Runner) openCANPeer(step ptplan.Step, att *ptreport.Attempt) (func(), error) {
	find := r.FindCAN
	if find == nil {
		find = slcan.Find
	}
	com, err := find()
	if err != nil {
		return nil, err
	}
	bitrate := slcan.DefaultBitrate
	if b, ok := step.Params["baud"]; ok {
		if v, err := strconv.Atoi(b); err == nil {
			bitrate = v
		}
	}
	open := r.OpenPeer
	if open == nil {
		open = realPeerOpener
	}
	rw, err := open("serial", com, serialx.DefaultBaud)
	if err != nil {
		return nil, err
	}
	a, err := slcan.Open(rw, bitrate)
	if err != nil {
		return nil, err
	}
	note(att, "peer", "can "+com)
	stopEcho := a.Echo()
	var once sync.Once
	return func() {
		once.Do(func() {
			st := stopEcho()
			_ = a.Close()
			att.SetExtra("can_peer_received", strconv.Itoa(st.Received))
			att.SetExtra("can_peer_replied", strconv.Itoa(st.Replied))
		})
	}, nil
}
