package ptlib

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"PortTool/src/ptboard"
	"PortTool/src/ptcheck"
	"PortTool/src/ptitems"
	"PortTool/src/ptplan"
	"PortTool/src/ptproto"
	"PortTool/src/ptreport"
	"PortTool/src/ptseq"
	"PortTool/src/refboard"
)

// ErrStopped is what Ask returns when the person stopped the run instead of
// answering.
var ErrStopped = errors.New("stopped by the operator")

// Question is what a person is asked about one item.
type Question struct {
	Item   string
	Kind   ptitems.Answer
	Prompt string // the plan's own prompt, when a plan step asks; "" otherwise
	Unit   string
	Before bool // asked before the board is read (press the key first)
	// An AI / AO walk's point: Point of Of, target Set in Unit on Channel;
	// Walk is "ai" (set the source, type its value), "ao" (type the meter),
	// "drop" (DO on, type the drop) or "din" (apply 24 V, the board answers).
	Point, Of int
	Set       float64
	Channel   string
	Walk      string
	// Auto: nobody needs to act, the simulated board cuts the port itself (recover.go).
	Auto bool
}

// Answer is a person's reply: Yes for a yes/no, Value as typed for a number.
type Answer struct {
	Yes   bool
	Value string
}

// Holder is the timed phase's deadman and watchdog: the panel's continuous
// run (ptpanel/hold.go), so a timed library run stops on a reset or a silent
// port (PORTTOOL-FLOW.md C.3.1).
type Holder interface {
	// Start arms the deadman and watches the given always-on ports. dur is
	// the run's length for the run log and countdown (< 0: until stopped);
	// the engine owns the deadline. The channel closes when the hold ends,
	// on its own or by Stop / Fault.
	Start(ports []string, dur time.Duration) (ended <-chan struct{}, err error)
	// Why says why a hold that ended on its own ended, in English.
	Why() string
	// Fault stops every port, lights the fault lamp and says why.
	Fault(why string)
	// Stop ends the hold normally: every port stopped, deadman disarmed.
	Stop()
}

// State is one item's progress, for the card.
type State struct {
	ID      string           `json:"id"`
	Port    string           `json:"port"`
	Running bool             `json:"running"`
	Outcome ptreport.Outcome `json:"outcome,omitempty"`
	Reason  string           `json:"reason,omitempty"` // a Reason* code
	Detail  string           `json:"detail,omitempty"` // English, from the executor or the board
	Rounds  int              `json:"rounds"`
	Checks  []ptcheck.Result `json:"checks,omitempty"`
	Values  [][2]string      `json:"values,omitempty"`
	Answer  string           `json:"answer,omitempty"`
	Analog  *AnalogResult    `json:"analog,omitempty"`
	// Used: the range and k, b each analog channel was converted with (decision 136).
	Used    []ptreport.AnalogUsed `json:"used,omitempty"`
	Events  []string              `json:"events,omitempty"`   // a watched session's event frames, newest last
	Live    *LiveState            `json:"live,omitempty"`     // a person standing in for the reference board (live.go)
	Drop    *DropResult           `json:"drop,omitempty"`     // DO under load measured by a person, channel by channel (drop.go)
	DinWalk *DinWalkResult        `json:"din_walk,omitempty"` // DI level, a person applying 24 V input by input (dinwalk.go)
	Relay   *RelayState           `json:"relay,omitempty"`    // relays switched to their counts (relay.go)
	// Chans: a multi-channel item's own result per channel (chans.go).
	Chans []ptreport.ChannelResult `json:"chans,omitempty"`
	// Round: the last finished round of the timed phase (timed.go, decision 151).
	Round *Round `json:"round,omitempty"`
}

// maxEvents is how many event frames a card keeps (PORTTOOL-FLOW.md C.3.6).
const maxEvents = 200

// Engine runs ticked items against one board.
type Engine struct {
	Board *ptboard.Board
	Caps  ptproto.Caps
	Plan  ptplan.Plan
	Items []ptitems.Item // the items in scope, in catalogue order

	// Runner returns a fresh executor wired to the board, the reference board
	// and the peers. Confirm and Ask are set here.
	Runner func() *ptseq.Runner
	Ask    func(ctx context.Context, q Question) (Answer, error)
	Hold   Holder
	// Analog is the board's calibration and ranges for AI / AO items; nil
	// leaves them ERROR (analog.go).
	Analog AnalogSource
	// RefAnalog is the reference board's own calibration and ranges, for an
	// AI read / AO output walk with it as the peer (STG-31, decision 134); nil
	// leaves those ERROR.
	RefAnalog AnalogSource
	// AnalogNominal is a channel's range with k = 1, b = 0, for a live card
	// on a board that carries no calibration (live.go).
	AnalogNominal AnalogSource
	// CAN opens this PC's end of the bus for the CAN analyser window at a bit
	// rate (canbus.go); nil leaves that item ERROR.
	CAN func(bitrate int) (CANPeer, error)
	// KNX opens this PC's end of the KNX window (knxwin.go); an error,
	// preferably KNXUnavailable, or nil leaves only the board's half.
	KNX func() (KNXPeer, error)
	// NoLive skips live cards: the unattended full test cannot wait for a
	// person to end one (decision 108); its DI level runs against the
	// reference board (decision 129).
	NoLive bool
	// Wiring is the reference board's registration: which of its channels is
	// wired where (decision 135); nil takes refboard.Default.
	Wiring func() refboard.Wiring
	// RefConnected decides the peer of a configuration pick that names none
	// (ptitems.DefaultPeer, decision 112).
	RefConnected bool

	// TimeUp ends the timed phase when the caller owns the clock (the full
	// test's configured duration) instead of dur; nil when it does not.
	TimeUp <-chan struct{}

	// StopOnFail ends a run at the first failed item outside the timed phase
	// too: configurations stop, R&D keeps going (PORTTOOL-FLOW.md C.3.6).
	StopOnFail bool

	// RD is an R&D run: a reading a person left blank stops the item rather
	// than leaving it unjudged (decision 129).
	RD bool

	// Cut breaks a port of the simulated board for ms and lets it come back
	// (sim.cut), standing in for a person's hands on a recover item; nil on a
	// real board, which also means the recover items open their PC ends.
	Cut func(port string, ms int) error
	// Reconnect opens the control port again under its name after it was
	// lost, for the RS232 recover item; the caller rewires Runner and Hold to
	// the new board, Board is set here. Nil leaves that item ERROR.
	Reconnect func(ctx context.Context) (*ptboard.Board, error)

	// Update receives every change to an item.
	Update func(State)

	mu      sync.Mutex
	states  map[string]*State
	results map[string]*ptreport.StepResult
	lives   map[string]chan liveMsg // running live items, by id
}

// withDefaultPeers fills the peer of every pick that names none; R&D never
// defaults to the reference board (decision 133).
func (e *Engine) withDefaultPeers(picks []Pick) []Pick {
	out := make([]Pick, len(picks))
	for i, p := range picks {
		if p.Peer == "" {
			for _, it := range e.Items {
				switch {
				case it.ID != p.ID:
				case e.RD && len(it.RDPeers()) > 0:
					p.Peer = it.RDPeers()[0]
				case !e.RD:
					p.Peer = ptitems.DefaultPeer(it, e.RefConnected)
				}
			}
		}
		out[i] = p
	}
	return out
}

// Run builds the jobs and runs them. dur 0 runs every ticked item once; a
// negative dur runs the timed phase until ctx is cancelled. The error is a
// pick the run refused before touching the board.
func (e *Engine) Run(ctx context.Context, picks []Pick, dur time.Duration) (ptreport.Report, error) {
	picks = e.withDefaultPeers(picks)
	if e.RD {
		if err := CheckRD(picks); err != nil {
			return ptreport.Report{}, err
		}
	}
	jobs, err := build(e.Items, e.Plan, e.Caps, picks)
	if err != nil {
		return ptreport.Report{}, err
	}
	// The reference board echoes CAN at its registered rate (decision 135).
	for _, j := range jobs {
		for i, st := range j.steps {
			if st.OnRef() && st.Port == "can" {
				p := ptplan.Params{}
				for k, v := range st.Params {
					p[k] = v
				}
				p["baud"] = strconv.Itoa(e.wiring().Params.CANBaud)
				j.steps[i].Params = p
			}
		}
	}
	e.states = map[string]*State{}
	e.results = map[string]*ptreport.StepResult{}
	rep := ptreport.Report{Plan: e.Plan.Name, LimitVersion: e.Plan.LimitVersion,
		Firmware: e.Caps.Version, StartedAt: time.Now()}

	for _, j := range jobs {
		e.states[j.item.ID] = &State{ID: j.item.ID, Port: j.port()}
		e.results[j.item.ID] = &ptreport.StepResult{ID: j.item.ID, Type: "Item"}
		if j.skip != "" {
			e.finish(j, ptreport.OutcomeSkipped, j.skip, "")
		}
	}

	// With a duration the AO load output holds its current until the run ends
	// instead of asking for a reading (decision 114, PORTTOOL-FLOW.md C.3.2).
	if dur != 0 {
		for _, j := range jobs {
			if j.skip != "" || !j.item.Load {
				continue
			}
			j.askNum = false
			if o, r, d := e.setLoad(j); o != "" {
				e.finish(j, o, r, d)
				j.skip = r
			}
		}
	}

	var first, timed []*job
	for _, j := range jobs {
		switch {
		case j.skip != "":
		case dur != 0 && j.relay && j.fail == "":
			// Starts with the timed items and runs to its count; judged at the end
			// (decisions 123, 130, PORTTOOL-FLOW.md C.3.6 「选了时长」).
			timed = append(timed, j)
		// The DO chain runs before the relays share it (decision 133, an exception to decision 102).
		case dur == 0 || j.item.Once || (e.RD && j.item.OnceRD) || j.askYesNo || j.askNum || j.analog || j.live || j.dinWalk || j.chain || hasHumanStep(j):
			first = append(first, j)
		default:
			timed = append(timed, j)
		}
	}

	stopped := false
	for i, j := range first {
		if ctx.Err() != nil {
			stopped = true
		}
		if stopped {
			e.finish(j, ptreport.OutcomeSkipped, ReasonStopped, "")
			continue
		}
		o := e.runJob(ctx, j)
		if ctx.Err() != nil || o == ptreport.OutcomeStopped {
			stopped = true
			continue
		}
		if failed(o) && e.StopOnFail {
			for _, rest := range first[i+1:] {
				e.finish(rest, ptreport.OutcomeSkipped, ReasonAfterFail, "")
			}
			for _, rest := range timed {
				e.finish(rest, ptreport.OutcomeSkipped, ReasonAfterFail, "")
			}
			timed = nil
			break
		}
	}
	if stopped {
		for _, j := range timed {
			e.finish(j, ptreport.OutcomeSkipped, ReasonStopped, "")
		}
		timed = nil
	}
	if len(timed) > 0 {
		e.runTimed(ctx, timed, dur)
	}

	for _, j := range jobs {
		rep.Steps = append(rep.Steps, *e.results[j.item.ID])
	}
	rep.FailCodes = rep.CollectFailCodes()
	rep.EndedAt = time.Now()
	return rep, nil
}

func hasHumanStep(j *job) bool {
	for _, st := range j.steps {
		if st.Type == ptplan.TypeUserConfirm || st.Type == ptplan.TypeUserReading {
			return true
		}
	}
	return false
}

func failed(o ptreport.Outcome) bool {
	return o == ptreport.OutcomeFail || o == ptreport.OutcomeTimeout || o == ptreport.OutcomeError
}

// States is a snapshot of every item's progress.
func (e *Engine) States() []State {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]State, 0, len(e.states))
	for _, s := range e.states {
		out = append(out, *s)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out
}

func (e *Engine) change(id string, f func(s *State)) {
	e.mu.Lock()
	s := e.states[id]
	f(s)
	cp := *s
	e.mu.Unlock()
	if e.Update != nil {
		e.Update(cp)
	}
}

func (e *Engine) finish(j *job, o ptreport.Outcome, reason, detail string) {
	o = e.fillChans(j, o, reason)
	e.mu.Lock()
	r := e.results[j.item.ID]
	r.Outcome = o
	if reason != "" {
		r.Reason = strings.TrimSpace(reason + " " + detail)
	}
	checks := append(ptreport.AttemptChecks(r.Attempts), e.states[j.item.ID].Checks...)
	if len(r.Channels) == 0 {
		r.FailCode = ptreport.FailCode(j.item.ID, 0, o, reason, checks)
	}
	for i, c := range r.Channels {
		r.Channels[i].FailCode = ptreport.FailCode(j.item.ID+"."+ptreport.ChannelPart(c.Name), c.Ch, c.Outcome, c.Reason, checks)
	}
	e.mu.Unlock()
	e.change(j.item.ID, func(s *State) {
		s.Running, s.Outcome, s.Reason, s.Detail = false, o, reason, detail
	})
}

func (e *Engine) addAttempts(j *job, atts ...ptreport.Attempt) {
	e.mu.Lock()
	r := e.results[j.item.ID]
	if r.StartedAt.IsZero() && len(atts) > 0 {
		r.StartedAt = atts[0].StartedAt
	}
	for _, a := range atts {
		a.N = len(r.Attempts) + 1
		r.Attempts = append(r.Attempts, a)
		r.DurationMS += a.DurationMS
	}
	// A timed run repeats for hours: keep the first failure and the latest
	// round, not every round (ptreport keeps every attempt of one step).
	if len(r.Attempts) > 64 {
		keep := []ptreport.Attempt{}
		for _, a := range r.Attempts {
			if a.Outcome != ptreport.OutcomePass {
				keep = append(keep, a)
				break
			}
		}
		keep = append(keep, r.Attempts[len(r.Attempts)-32:]...)
		r.Attempts = keep
	}
	e.mu.Unlock()
}

// runJob runs one item once and returns its outcome.
func (e *Engine) runJob(ctx context.Context, j *job) ptreport.Outcome {
	e.change(j.item.ID, func(s *State) { s.Running = true })
	o, reason, detail := e.once(ctx, j)
	e.change(j.item.ID, func(s *State) { s.Rounds++ })
	e.finish(j, o, reason, detail)
	return o
}

// once is one round of an item.
func (e *Engine) once(ctx context.Context, j *job) (ptreport.Outcome, string, string) {
	if j.fail != "" {
		return ptreport.OutcomeError, j.fail, j.detail
	}
	if j.live {
		return e.runLive(ctx, j)
	}
	if j.drop {
		return e.runDrop(ctx, j)
	}
	if j.load {
		return e.runLoad(ctx, j)
	}
	if j.dinWalk {
		return e.runDinWalk(ctx, j)
	}
	if j.analog {
		return e.runAnalog(ctx, j)
	}
	if j.chain {
		return e.runChainDO(ctx, j)
	}
	if j.recover {
		return e.runRecover(ctx, j)
	}
	verdict := ptreport.OutcomePass
	if j.record {
		verdict = Recorded
	}
	if j.item.Load {
		if o, r, d := e.setLoad(j); o != "" {
			return o, r, d
		}
	}
	if j.askFirst {
		o, r := e.askYesNo(ctx, j, true)
		if o != ptreport.OutcomePass {
			return o, r, ""
		}
	}
	if j.relay {
		o, reason, detail := e.runRelay(ctx, j, false)
		if o != Recorded {
			return o, reason, detail
		}
		o, r := e.relayVerdict(ctx, j)
		return o, r, ""
	} else if j.record {
		o, detail := e.record(ctx, j)
		if o != Recorded {
			return o, "", detail
		}
	} else {
		if j.item.ID == "din.level" && j.peer == ptitems.PeerRef {
			stop, o, detail := e.rewireDin(ctx, j)
			defer stop()
			if o != "" {
				return o, "", detail
			}
		}
		o, detail := e.runSteps(ctx, j)
		if o != ptreport.OutcomePass {
			return o, "", detail
		}
	}
	if j.askYesNo && !j.askFirst {
		o, r := j.onAnswer, j.onReason
		if !j.askOn {
			stop := func() {}
			if blinkWhileAsk[j.item.ID] {
				stop = e.blinkLamp()
			}
			o, r = e.askYesNo(ctx, j, false)
			stop()
		}
		if o != ptreport.OutcomePass {
			return o, r, ""
		}
		verdict = ptreport.OutcomePass // the person's answer is this item's verdict
	}
	if j.askNum && !j.record {
		if o, r := e.askNumber(ctx, j); o != "" {
			return o, r, ""
		}
	}
	return verdict, "", ""
}

// blinkLamp toggles the board's indicator from the PC every half second until
// the returned func is called, which leaves it off.
func (e *Engine) blinkLamp() func() {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		on := true
		t := time.NewTicker(lampHalf)
		defer t.Stop()
		for {
			_, _ = e.Board.Send(lampCmd(on), ptboard.ExpectOne, ptboard.DefaultTimeout)
			on = !on
			select {
			case <-done:
				return
			case <-t.C:
			}
		}
	}()
	return func() {
		close(done)
		<-finished
		_, _ = e.Board.Send(lampCmd(false), ptboard.ExpectOne, ptboard.DefaultTimeout)
	}
}

// lampHalf is half a blink period of the PC-driven indicator.
const lampHalf = 500 * time.Millisecond

func lampCmd(on bool) string {
	if on {
		return ptproto.LEDFaultOn
	}
	return ptproto.LEDFaultOff
}

func (e *Engine) askYesNo(ctx context.Context, j *job, before bool) (ptreport.Outcome, string) {
	att := ptreport.Attempt{StartedAt: time.Now(), Sent: []string{"ask the operator: " + j.item.ID}}
	a, err := e.Ask(ctx, Question{Item: j.item.ID, Kind: ptitems.AnswerYesNo, Before: before})
	att.DurationMS = time.Since(att.StartedAt).Milliseconds()
	defer func() { e.addAttempts(j, att) }()
	if err != nil {
		att.Outcome, att.Err = ptreport.OutcomeStopped, err.Error()
		return ptreport.OutcomeStopped, ReasonStopped
	}
	ans := map[bool]string{true: "yes", false: "no"}[a.Yes]
	att.SetExtra("answer", ans)
	e.change(j.item.ID, func(s *State) { s.Answer = ans })
	if !a.Yes {
		att.Outcome = ptreport.OutcomeFail
		return ptreport.OutcomeFail, ReasonOperatorNo
	}
	att.Outcome = ptreport.OutcomePass
	return ptreport.OutcomePass, ""
}

// askNumber asks for a reading nobody's criteria judge yet; it is kept as typed.
func (e *Engine) askNumber(ctx context.Context, j *job) (ptreport.Outcome, string) {
	att := ptreport.Attempt{StartedAt: time.Now(), Sent: []string{"ask the operator for a number: " + j.item.ID}}
	a, err := e.Ask(ctx, Question{Item: j.item.ID, Kind: ptitems.AnswerNumber})
	att.DurationMS = time.Since(att.StartedAt).Milliseconds()
	defer func() { e.addAttempts(j, att) }()
	if err != nil {
		att.Outcome, att.Err = ptreport.OutcomeStopped, err.Error()
		return ptreport.OutcomeStopped, ReasonStopped
	}
	v := strings.TrimSpace(a.Value)
	att.SetExtra("value", v)
	att.Outcome = Recorded
	e.change(j.item.ID, func(s *State) { s.Answer = v })
	return "", ""
}

// runSteps hands an item's plan steps to the executor.
func (e *Engine) runSteps(ctx context.Context, j *job) (ptreport.Outcome, string) {
	r := e.Runner()
	r.Confirm = func(prompt string) (bool, error) {
		a, err := e.Ask(ctx, Question{Item: j.item.ID, Kind: ptitems.AnswerYesNo, Prompt: prompt})
		return a.Yes, err
	}
	blank := false
	r.Ask = func(prompt, unit string) (string, error) {
		a, err := e.Ask(ctx, Question{Item: j.item.ID, Kind: ptitems.AnswerNumber, Prompt: prompt, Unit: unit})
		blank = blank || (err == nil && strings.TrimSpace(a.Value) == "")
		return a.Value, err
	}
	rep, err := r.Run(ptplan.Plan{Schema: ptplan.Schema, Name: e.Plan.Name, LimitVersion: e.Plan.LimitVersion, Steps: j.steps})
	if err != nil {
		return ptreport.OutcomeError, err.Error()
	}
	worst, detail := ptreport.OutcomePass, ""
	stepChans(e, j, rep.Steps)
	var checks []ptcheck.Result
	var values [][2]string
	rates := map[string]string{} // derived rates of every step, not only the last
	for _, st := range rep.Steps {
		e.addAttempts(j, st.Attempts...)
		if n := len(st.Attempts); n > 0 {
			last := st.Attempts[n-1]
			checks = append(checks, last.Checks...)
			if v := valuesOf(last); len(v) > 0 {
				values = v
			}
			for _, k := range ptseq.RateFields {
				if v, ok := last.Extra[k]; ok {
					rates[k] = v
				}
			}
			if !last.Outcome.IsPass() && detail == "" {
				detail = last.Err
			}
		}
		if rank(st.Outcome) > rank(worst) {
			worst = st.Outcome
		}
	}
	if ctx.Err() != nil && worst != ptreport.OutcomePass {
		worst = ptreport.OutcomeStopped
	}
	// R&D: a reading left blank is not measured to the end, not an error (decision 129).
	if blank && e.RD && worst != ptreport.OutcomeFail {
		worst, detail = ptreport.OutcomeStopped, ""
	}
	values = withRates(values, rates)
	e.change(j.item.ID, func(s *State) { s.Checks, s.Values = checks, values })
	return worst, detail
}

// withRates adds the derived rates values lacks, in ptseq.RateFields order.
func withRates(values [][2]string, rates map[string]string) [][2]string {
	have := map[string]bool{}
	for _, kv := range values {
		have[kv[0]] = true
	}
	for _, k := range ptseq.RateFields {
		if v, ok := rates[k]; ok && !have[k] {
			values = append(values, [2]string{k, v})
		}
	}
	return values
}

// rank orders outcomes so the worst of an item's steps is its verdict.
func rank(o ptreport.Outcome) int {
	switch o {
	case ptreport.OutcomePass, ptreport.OutcomeSkipped:
		return 0
	case ptreport.OutcomeStopped:
		return 1
	case ptreport.OutcomeTimeout:
		return 2
	case ptreport.OutcomeError:
		return 3
	}
	return 4 // FAIL
}

// watchSession keeps a started session open for the item's WatchS, listing
// its event frames (!<port>.<event>) and refreshing the values from its
// periodic frame. ok is false when it ended early; the outcome is recorded.
func (e *Engine) watchSession(ctx context.Context, j *job, port string, events <-chan ptboard.Event, att *ptreport.Attempt) (ptreport.Outcome, string, bool) {
	end := time.NewTimer(time.Duration(j.item.WatchS) * time.Second)
	defer end.Stop()
	var list []string
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				o, d := e.recordErr(j, *att, errors.New("the board stopped answering"))
				return o, d, false
			}
			if ev.Kind != ptproto.LineFrame {
				continue
			}
			switch {
			case ev.Frame.Port == port:
				att.Raw = append(att.Raw, ev.Line)
				vals := valuesOf(ptreport.Attempt{Raw: []string{ev.Line}})
				e.change(j.item.ID, func(s *State) { s.Values = vals })
			case strings.HasPrefix(ev.Frame.Port, port+"."):
				att.Raw = append(att.Raw, ev.Line)
				if list = append(list, ev.Line); len(list) > maxEvents {
					list = list[len(list)-maxEvents:]
				}
				cp := append([]string(nil), list...)
				e.change(j.item.ID, func(s *State) { s.Events = cp })
			}
		case <-end.C:
			return "", "", true
		case <-ctx.Done():
			att.Outcome = ptreport.OutcomeStopped
			e.addAttempts(j, *att)
			return ptreport.OutcomeStopped, "", false
		}
	}
}

// valuesOf is what to show for an attempt: the last frame's fields, else the
// reply's, plus what the executor added (peer statistics, a typed value).
func valuesOf(a ptreport.Attempt) [][2]string {
	var out [][2]string
	for i := len(a.Raw) - 1; i >= 0; i-- {
		kind, body := ptproto.Classify(a.Raw[i])
		if kind == ptproto.LineFrame {
			if f, ok := ptproto.ParseFrame(body); ok {
				for _, p := range f.Fields {
					out = append(out, [2]string{p.Key, p.Value})
				}
				break
			}
		}
		if kind == ptproto.LineOK && i == firstOK(a.Raw) {
			for _, p := range ptproto.Fields(body) {
				out = append(out, [2]string{p.Key, p.Value})
			}
			break
		}
	}
	keys := make([]string, 0, len(a.Extra))
	for k := range a.Extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, [2]string{k, a.Extra[k]})
	}
	return out
}

// get is a frame field's value, "" when the board did not report it.
func get(f ptproto.Frame, k string) string {
	v, _ := ptproto.Get(f.Fields, k)
	return v
}

func firstOK(raw []string) int {
	for i, l := range raw {
		if k, _ := ptproto.Classify(l); k == ptproto.LineOK {
			return i
		}
	}
	return -1
}

// record runs an item with no criteria: its targets are sent, readings kept,
// nothing judged.
func (e *Engine) record(ctx context.Context, j *job) (ptreport.Outcome, string) {
	for _, t := range j.item.Targets {
		att := ptreport.Attempt{StartedAt: time.Now()}
		if t.Run != "" {
			cmd := "pt.run " + t.Run
			att.Sent = append(att.Sent, cmd)
			lines, err := e.Board.Send(cmd, ptboard.ExpectOne, time.Duration(ptplan.DefaultTimeoutMS)*time.Millisecond)
			att.Raw = append(att.Raw, lines...)
			att.DurationMS = time.Since(att.StartedAt).Milliseconds()
			if err != nil {
				return e.recordErr(j, att, err)
			}
			att.Outcome = Recorded
			e.addAttempts(j, att)
			e.change(j.item.ID, func(s *State) { s.Values = valuesOf(att) })
			continue
		}
		if o, d := e.recordSession(ctx, j, t, &att); o != Recorded {
			return o, d
		}
	}
	return Recorded, ""
}

func (e *Engine) recordErr(j *job, att ptreport.Attempt, err error) (ptreport.Outcome, string) {
	var refused *ptboard.RefusedError
	att.Outcome, att.Err = ptreport.OutcomeError, err.Error()
	if errors.As(err, &refused) {
		att.Outcome, att.Err = ptreport.OutcomeFail, refused.Reason
	}
	e.addAttempts(j, att)
	return att.Outcome, att.Err
}

// recordSession starts a session, waits for one frame, asks for a number when
// the item wants one (with the session still running, so there is something to
// measure), then stops it.
func (e *Engine) recordSession(ctx context.Context, j *job, t ptitems.Target, att *ptreport.Attempt) (ptreport.Outcome, string) {
	st := ptplan.Step{Port: t.Session, Params: ptplan.Params{}}
	for k, v := range t.Set {
		st.Params[k] = v
	}
	for k, v := range boardParams(j.params) {
		st.Params[k] = v
	}
	events, unsub := e.Board.Subscribe(256)
	defer unsub()
	cmd := startLine(st)
	att.Sent = append(att.Sent, cmd)
	timeout := time.Duration(ptplan.DefaultTimeoutMS) * time.Millisecond
	lines, err := e.Board.Send(cmd, ptboard.ExpectOne, timeout)
	att.Raw = append(att.Raw, lines...)
	stop := func() {
		c := "pt.stop " + t.Session
		att.Sent = append(att.Sent, c)
		l, _ := e.Board.Send(c, ptboard.ExpectOne, ptboard.DefaultTimeout)
		att.Raw = append(att.Raw, l...)
	}
	if err != nil {
		stop()
		return e.recordErr(j, *att, err)
	}
	if j.item.WatchS > 0 {
		// Collected from the start: event frames can come before the first periodic one.
		if o, d, ok := e.watchSession(ctx, j, t.Session, events, att); !ok {
			stop()
			return o, d
		}
		stop()
		att.Outcome = Recorded
		att.DurationMS = time.Since(att.StartedAt).Milliseconds()
		e.addAttempts(j, *att)
		return Recorded, ""
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for got := 0; got < 1; {
		select {
		case ev, ok := <-events:
			if !ok {
				stop()
				return e.recordErr(j, *att, errors.New("the board stopped answering"))
			}
			if ev.Kind == ptproto.LineFrame && ev.Frame.Port == t.Session {
				att.Raw = append(att.Raw, ev.Line)
				got++
			}
		case <-timer.C:
			stop()
			att.Outcome = ptreport.OutcomeTimeout
			att.Err = fmt.Sprintf("no frame from %s within %s", t.Session, timeout)
			e.addAttempts(j, *att)
			return ptreport.OutcomeTimeout, att.Err
		case <-ctx.Done():
			stop()
			att.Outcome = ptreport.OutcomeStopped
			e.addAttempts(j, *att)
			return ptreport.OutcomeStopped, ""
		}
	}
	vals := valuesOf(*att)
	e.change(j.item.ID, func(s *State) { s.Values = vals })
	if j.askOn {
		j.onAnswer, j.onReason = e.askYesNo(ctx, j, false)
	}
	if j.askNum {
		a, err := e.Ask(ctx, Question{Item: j.item.ID, Kind: ptitems.AnswerNumber})
		if err != nil {
			stop()
			att.Outcome = ptreport.OutcomeStopped
			e.addAttempts(j, *att)
			return ptreport.OutcomeStopped, ""
		}
		v := strings.TrimSpace(a.Value)
		att.SetExtra("value", v)
		e.change(j.item.ID, func(s *State) { s.Answer = v })
	}
	stop()
	att.Outcome = Recorded
	att.DurationMS = time.Since(att.StartedAt).Milliseconds()
	e.addAttempts(j, *att)
	return Recorded, ""
}
