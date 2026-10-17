package ptlib

// Pull and plug back (decision 150, PORTTOOL-FLOW.md C.3.6 「断开恢复」): the
// port's session runs, a person pulls its cable and plugs it back, and the
// board's own frames say when it broke, when the line came back and when the
// traffic caught up. Judged by the plan's <port>-recover checks when it has
// them, else recorded: the limits are the hardware engineers' (decision 44).

import (
	"context"
	"strconv"
	"strings"
	"time"

	"PortTool/src/ptboard"
	"PortTool/src/ptcheck"
	"PortTool/src/ptitems"
	"PortTool/src/ptplan"
	"PortTool/src/ptproto"
	"PortTool/src/ptreport"
)

// Why a recover item did not finish.
const (
	ReasonNotLinked  = "not_linked"  // the traffic never caught up before the pull
	ReasonNoBreak    = "no_break"    // nothing broke within RecoverWaitS of asking
	ReasonNoRecovery = "no_recovery" // not caught up within RecoverWaitS of asking
	ReasonBoardReset = "board_reset" // the port's session was gone: the board restarted
)

// The recover item's timing; the values are in PORTTOOL-FLOW.md C.3.6.
const (
	RecoverWaitS = 60 // each question waits this long for the board to show it was done
	recoverWarm  = 2  // frames in a row caught up before the person is asked to pull
	SimCutMS     = 3000
)

var (
	recoverWait    = RecoverWaitS * time.Second
	recoverQuiet   = 2 * time.Second // no frame this long is a break (RS232: the frames ride the cable)
	simCutAfter    = time.Second
	reconnectEvery = time.Second
)

// recoverPeer is the PC end of each port's session on a real board, the same
// as its traffic item's; RS232 and KNX need none.
var recoverPeer = map[string]*ptplan.Peer{
	"eth": {TCP: "auto"}, "usb": {USB: true}, "rs485": {Serial: true}, "can": {CAN: true},
}

// Phases of a recover walk.
const (
	rwWarm = iota // waiting for the traffic to be caught up
	rwPull        // asked to pull, waiting for the break
	rwDown        // broken, asked to plug back
	rwDone        // caught up again
)

// recoverWatch follows one port's frames through a pull and a plug.
type recoverWatch struct {
	port  string
	phase int
	good  int

	lastAt time.Time // when the last frame came
	ticks  ptproto.TickUnwrapper
	lastT  uint64 // board ms of the last frame

	quietBreak bool   // broke by silence rather than by a field
	breakRx    string // rx when it broke
	downT      uint64
	upT        uint64
	up         bool
	okT        uint64
	lost       int
	reset      bool
}

// Events see returns.
const (
	rwNone = iota
	rwWarmed
	rwBroke
	rwCaughtUp
)

func frameNum(f ptproto.Frame, k string) (int64, bool) {
	v, ok := ptproto.Get(f.Fields, k)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	return n, err == nil
}

// lineDown is the port's own sign that its cable is out.
func lineDown(port string, f ptproto.Frame) bool {
	switch port {
	case "eth":
		return get(f, "link") == "0"
	case "usb":
		return get(f, "state") != "cfg"
	case "knx":
		return get(f, "bus") != "ok"
	case "can":
		return get(f, "lec") != "" && get(f, "lec") != "0"
	}
	return false
}

// caughtUp says the loop closes again: no miss, the last number came back,
// and the port's own line is up.
func caughtUp(port string, f ptproto.Frame) bool {
	seq, ok1 := frameNum(f, "seq")
	rx, ok2 := frameNum(f, "rx")
	miss, ok3 := frameNum(f, "miss")
	if !ok1 || !ok2 || !ok3 || miss != 0 || seq-rx != 1 {
		return false
	}
	if port == "eth" && get(f, "conn") != "1" {
		return false
	}
	return !lineDown(port, f)
}

// lineBack says the cable is in again, before the traffic has caught up.
func (w *recoverWatch) lineBack(f ptproto.Frame) bool {
	if w.quietBreak {
		return true // a frame came at all
	}
	switch w.port {
	case "eth", "usb", "knx":
		return !lineDown(w.port, f)
	}
	return get(f, "rx") != w.breakRx // a reply got through
}

// see takes one of the port's frames, or nil when none came for a while.
func (w *recoverWatch) see(f *ptproto.Frame, now time.Time) int {
	if f == nil {
		if w.phase == rwPull && !w.lastAt.IsZero() && now.Sub(w.lastAt) >= recoverQuiet {
			w.phase, w.quietBreak, w.downT = rwDown, true, w.lastT
			return rwBroke
		}
		return rwNone
	}
	t, restarted := w.ticks.Unwrap(f.Tick)
	w.reset = w.reset || restarted
	w.lastAt, w.lastT = now, t
	miss, _ := frameNum(*f, "miss")
	switch w.phase {
	case rwWarm:
		if caughtUp(w.port, *f) {
			w.good++
		} else {
			w.good = 0
		}
		if w.good >= recoverWarm {
			w.phase = rwPull
			return rwWarmed
		}
	case rwPull:
		if lineDown(w.port, *f) || miss >= 2 {
			w.phase, w.downT, w.breakRx = rwDown, t, get(*f, "rx")
			w.lost = int(miss)
			return rwBroke
		}
	case rwDown:
		if int(miss) > w.lost {
			w.lost = int(miss)
		}
		if !w.up && w.lineBack(*f) {
			w.up, w.upT = true, t
		}
		if caughtUp(w.port, *f) {
			if !w.up {
				w.up, w.upT = true, t
			}
			w.phase, w.okT = rwDone, t
			return rwCaughtUp
		}
	}
	return rwNone
}

// values is what the card and the report show.
func (w *recoverWatch) values() [][2]string {
	reset := "0"
	if w.reset {
		reset = "1"
	}
	out := [][2]string{}
	if w.phase == rwDone {
		out = append(out, [2]string{"recover_ms", strconv.FormatUint(w.okT-w.upT, 10)},
			[2]string{"down_ms", strconv.FormatUint(w.upT-w.downT, 10)})
	}
	if w.phase >= rwDown {
		out = append(out, [2]string{"lost", strconv.Itoa(w.lost)})
	}
	return append(out, [2]string{"reset", reset})
}

// recoverAsk is one question put while the board decides when it is done.
type recoverAsk struct {
	cancel func()
	done   chan error
	over   bool
	err    error
}

func (e *Engine) askRecover(ctx context.Context, j *job, walk string) *recoverAsk {
	actx, cancel := context.WithCancel(ctx)
	a := &recoverAsk{cancel: cancel, done: make(chan error, 1)}
	go func() {
		_, err := e.Ask(actx, Question{Item: j.item.ID, Kind: ptitems.AnswerWait, Channel: j.item.Port, Walk: walk, Auto: e.Cut != nil})
		a.done <- err
	}()
	return a
}

// stopped says the person pressed stop.
func (a *recoverAsk) stopped() bool {
	if a == nil {
		return false
	}
	if !a.over {
		select {
		case a.err = <-a.done:
			a.over = true
		default:
		}
	}
	return a.over && a.err != nil
}

// end takes the question down.
func (a *recoverAsk) end() {
	if a == nil || a.over {
		return
	}
	a.cancel()
	a.err, a.over = <-a.done, true
	a.err = nil // taken down by the board, not stopped by the person
}

// recoverStep is the session a recover item runs, its PC end only on a real
// board: the simulated board plays every peer itself.
func (e *Engine) recoverStep(j *job) ptplan.Step {
	t := j.item.Targets[0]
	st := ptplan.Step{ID: j.item.ID, Type: ptplan.TypePtSession, Port: t.Session, Params: ptplan.Params{},
		TimeoutMS: int((3*recoverWait + time.Minute) / time.Millisecond)}
	for k, v := range t.Set {
		st.Params[k] = v
	}
	if e.Cut == nil {
		st.Peer = recoverPeer[t.Session]
	}
	return st
}

func (e *Engine) runRecover(ctx context.Context, j *job) (ptreport.Outcome, string, string) {
	port := j.item.Port
	w := &recoverWatch{port: port}
	var ask *recoverAsk
	var askedAt, cutAt time.Time
	started := time.Now()
	why, detail, stopped, lost := "", "", false, false
	defer func() { ask.end() }()

	watch := func(f *ptproto.Frame) bool {
		now := time.Now()
		if ctx.Err() != nil || ask.stopped() {
			stopped = true
			return true
		}
		select {
		case <-e.Board.Lost():
			lost = true // the control port itself went; reconnectRecover takes over
			return true
		default:
		}
		if !cutAt.IsZero() && !now.Before(cutAt) {
			cutAt = time.Time{}
			go func() { _ = e.Cut(port, SimCutMS) }()
		}
		switch w.see(f, now) {
		case rwWarmed:
			ask, askedAt = e.askRecover(ctx, j, "recover_pull"), now
			if e.Cut != nil {
				cutAt = now.Add(simCutAfter)
			}
		case rwBroke:
			ask.end()
			ask, askedAt = e.askRecover(ctx, j, "recover_plug"), now
		case rwCaughtUp:
			return true
		}
		e.change(j.item.ID, func(s *State) { s.Values = w.values() })
		switch {
		case w.phase == rwPull && now.Sub(askedAt) >= recoverWait:
			why = ReasonNoBreak
			return true
		case w.phase == rwDown && now.Sub(askedAt) >= recoverWait:
			why, detail = e.recoverGone(port)
			return true
		case w.phase == rwWarm && now.Sub(started) >= recoverWait:
			why = ReasonNotLinked
			return true
		}
		return false
	}

	r := e.Runner()
	r.Watch = watch
	// The control-port ports are answered here as well as by the panel's
	// responder: this item must not depend on that switch.
	caps := e.Caps
	r.Caps = &caps
	e.change(j.item.ID, func(s *State) { s.Values = nil })
	rep, err := r.Run(ptplan.Plan{Schema: ptplan.Schema, Name: e.Plan.Name, LimitVersion: e.Plan.LimitVersion,
		Steps: []ptplan.Step{e.recoverStep(j)}})
	if err != nil {
		return ptreport.OutcomeError, "", err.Error()
	}
	var att ptreport.Attempt
	for _, st := range rep.Steps {
		e.addAttempts(j, st.Attempts...)
		if n := len(st.Attempts); n > 0 {
			att = st.Attempts[n-1]
		}
	}
	gone := lost || att.Outcome == ptreport.OutcomeError
	// RS232 is the control port: pulling its adapter closes it (PORTTOOL-FLOW.md C.3.6).
	if gone && port == "rs232" && w.phase == rwPull && e.Reconnect != nil && !stopped {
		w.phase, w.quietBreak, w.downT = rwDown, true, w.lastT // the adapter itself was pulled
		ask.end()
		ask, askedAt = e.askRecover(ctx, j, "recover_plug"), time.Now()
	}
	if gone && port == "rs232" && w.phase == rwDown && e.Reconnect != nil && !stopped {
		switch e.reconnectRecover(ctx, j, askedAt, watch) {
		case "":
			gone = false
		case ReasonNoRecovery:
			gone, why = false, ReasonNoRecovery
		case ReasonStopped:
			gone, stopped = false, true
		}
	}
	ask.end()
	vals := w.values()
	if w.phase == rwDone && port == "rs232" {
		vals = append(vals, [2]string{"cause", e.resetCause()})
	}
	e.change(j.item.ID, func(s *State) { s.Values = vals })
	a := ptreport.Attempt{StartedAt: time.Now(), Outcome: Recorded}
	for _, kv := range vals {
		a.SetExtra(kv[0], kv[1])
	}
	switch {
	case stopped:
		a.Outcome = ptreport.OutcomeStopped
		e.addAttempts(j, a)
		return ptreport.OutcomeStopped, ReasonStopped, ""
	case why != "":
		a.Outcome = ptreport.OutcomeStopped
		e.addAttempts(j, a)
		return ptreport.OutcomeStopped, why, detail
	case gone && att.Err != "":
		return ptreport.OutcomeError, "", att.Err
	case gone:
		return ptreport.OutcomeError, "", "the control port was lost"
	}
	if j.record {
		e.addAttempts(j, a)
		return Recorded, "", ""
	}
	look := func(f string) (string, bool) {
		for _, kv := range vals {
			if kv[0] == f {
				return kv[1], true
			}
		}
		return "", false
	}
	var checks []ptcheck.Result
	for _, st := range j.steps {
		res, _ := ptcheck.EvalAll(st.Checks, look)
		checks = append(checks, res...)
	}
	a.Checks, a.Outcome = checks, ptreport.OutcomePass
	for _, c := range checks {
		if !c.Pass {
			a.Outcome = ptreport.OutcomeFail
		}
	}
	e.addAttempts(j, a)
	e.change(j.item.ID, func(s *State) { s.Checks = checks })
	return a.Outcome, "", ""
}

// recoverGone is why nothing caught up: the board restarted when the port's
// session is no longer running, with the cause it gives; else no recovery.
func (e *Engine) recoverGone(port string) (string, string) {
	lines, _ := e.Board.Send("pt.list", ptboard.ExpectMany, ptboard.DefaultTimeout)
	answered := false
	for _, l := range lines { // one OK running=<port> line per running port
		kind, body := ptproto.Classify(l)
		if kind != ptproto.LineOK {
			continue
		}
		answered = true
		if v, _ := ptproto.Get(ptproto.Fields(body), "running"); v == port {
			return ReasonNoRecovery, ""
		}
	}
	if !answered {
		return ReasonNoRecovery, ""
	}
	return ReasonBoardReset, e.resetCause()
}

// resetCause is the board's last reset cause, "" when it does not say.
func (e *Engine) resetCause() string {
	lines, err := e.Board.Send("pt.run reset.cause", ptboard.ExpectOne, ptboard.DefaultTimeout)
	if err != nil || len(lines) == 0 {
		return ""
	}
	_, body := ptproto.Classify(lines[len(lines)-1])
	v, _ := ptproto.Get(ptproto.Fields(body), "cause")
	return v
}

// reconnectRecover opens the control port again under the same name until it
// comes back or the plug-back question runs out, then feeds watch the RS232
// frames of the new connection until it is done. "" when watch ended it,
// else ReasonNoRecovery, ReasonStopped, or "gone" for a new connection lost too.
func (e *Engine) reconnectRecover(ctx context.Context, j *job, askedAt time.Time, watch func(*ptproto.Frame) bool) string {
	var nb *ptboard.Board
	for nb == nil {
		if ctx.Err() != nil {
			return ReasonStopped
		}
		if time.Since(askedAt) >= recoverWait {
			return ReasonNoRecovery
		}
		if b, err := e.Reconnect(ctx); err == nil {
			nb = b
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(reconnectEvery):
		}
	}
	e.Board = nb
	att := ptreport.Attempt{StartedAt: time.Now(), Sent: []string{"reopen the control port"}}
	defer func() {
		stop := "pt.stop " + j.item.Port
		att.Sent = append(att.Sent, stop)
		l, _ := nb.Send(stop, ptboard.ExpectOne, ptboard.DefaultTimeout)
		att.Raw = append(att.Raw, l...)
		att.DurationMS = time.Since(att.StartedAt).Milliseconds()
		att.Outcome = Recorded
		e.addAttempts(j, att)
	}()
	events, unsub := nb.Subscribe(256)
	defer unsub()
	tick := time.NewTicker(watchTick)
	defer tick.Stop()
	for {
		select {
		case ev, open := <-events:
			if !open {
				return "gone"
			}
			if ev.Kind != ptproto.LineFrame || ev.Frame.Port != j.item.Port {
				continue
			}
			att.Raw = append(att.Raw, ev.Line)
			if cmd, ok := e.Caps.EchoCommand(ev.Frame); ok {
				_, _ = nb.Send(cmd, ptboard.ExpectOne, ptboard.DefaultTimeout)
			}
			f := ev.Frame
			if watch(&f) {
				return ""
			}
		case <-tick.C:
			if watch(nil) {
				return ""
			}
		}
	}
}

// watchTick is how often the reconnected watch hears that no frame came.
const watchTick = 250 * time.Millisecond
