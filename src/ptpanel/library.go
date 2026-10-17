package ptpanel

// The top-right Start: ticked test items -> plan steps -> executor, results
// back onto the item cards. Scheduling is ptlib's; this is the HTTP surface,
// the deadman (hold.go's continuous run) and the item runner the configured
// full-test steps call (ItemRunner in stagecfg.go).
// $PROD/docs/modules/M4/PORTTOOL-FLOW.md C.3.6 "测试项怎么跑".

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"PortTool/src/calstore"
	"PortTool/src/portmap"
	"PortTool/src/ptboard"
	"PortTool/src/ptitems"
	"PortTool/src/ptlib"
	"PortTool/src/ptplan"
	"PortTool/src/ptproto"
	"PortTool/src/ptreport"
	"PortTool/src/ptseq"
	"PortTool/src/refboard"
	"PortTool/src/serialx"
	"PortTool/src/simboard"
	"PortTool/src/slcan"
)

type libState struct {
	mu       sync.Mutex
	running  bool
	stage    string
	duration string
	started  time.Time
	dur      time.Duration
	cancel   context.CancelFunc
	userStop bool
	states   map[string]ptlib.State
	items    map[string]ptitems.Item // every item run on this connection, for the port verdicts
	ended    msg                     // how the last run ended
	eng      *ptlib.Engine           // the running engine, for live card actions
}

func (s *Server) libRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/lib", s.handleLib)
	mux.HandleFunc("/api/lib/start", s.handleLibStart)
	mux.HandleFunc("/api/lib/stop", s.handleLibStop)
	mux.HandleFunc("/api/lib/live", s.handleLibLive)
	mux.HandleFunc("/api/ask", s.handleAsk)
}

// errSimPeer keeps a run on the simulated board off every real serial port and
// USB device on the bench: the simulated board plays its own peers.
var errSimPeer = errors.New("the simulated board has no real peer on this bench")

// libRunner returns the executor factory for one board.
func (s *Server) libRunner(b *ptboard.Board, port string) func() *ptseq.Runner {
	sim := simboard.IsSim(port)
	return func() *ptseq.Runner {
		r := &ptseq.Runner{Board: b, Ref: s.refBoard(), ToolVersion: s.Version, PortName: port,
			SerialPeer: portmap.Peer}
		if sim {
			r.OpenPeer = func(string, string, int) (io.ReadWriteCloser, error) { return nil, errSimPeer }
			r.FindCDC = func() (string, error) { return "", errSimPeer }
			r.FindCAN = func() (string, error) { return "", errSimPeer }
		}
		return r
	}
}

// canPeer opens this PC's end of the CAN analyser window (decision 111,
// PORTTOOL-FLOW.md C.3.7): the CANable found by its USB ids, or the panel-side
// wire on the simulated board.
func (s *Server) canPeer(port string) func(int) (ptlib.CANPeer, error) {
	if simboard.IsSim(port) {
		return func(bitrate int) (ptlib.CANPeer, error) {
			s.mu.Lock()
			rw := s.ctrlRW
			s.mu.Unlock()
			return simboard.OpenCAN(rw, bitrate)
		}
	}
	return func(bitrate int) (ptlib.CANPeer, error) {
		com, err := slcan.Find()
		if err != nil {
			return nil, err
		}
		rw, err := s.Open(com, serialx.DefaultBaud)
		if err != nil {
			return nil, fmt.Errorf("%s would not open (bound in the connection block?): %w", com, err)
		}
		a, err := slcan.Open(rw, bitrate)
		if err != nil {
			_ = rw.Close()
			return nil, err
		}
		return a, nil
	}
}

// panelHold is ptlib.Holder on hold.go's continuous run: same deadman, same
// reset and silence watch, same fault lamp (PORTTOOL-FLOW.md C.3.1).
type panelHold struct {
	s       *Server
	b       *ptboard.Board
	run     *timedRun
	stopWhy func() msg
}

func (h *panelHold) Start(ports []string, dur time.Duration) (<-chan struct{}, error) {
	h.s.mu.Lock()
	busy := h.s.run != nil
	h.s.mu.Unlock()
	if busy {
		return nil, errors.New("a continuous test is already running")
	}
	h.s.startTimedRun(h.b, ports, dur)
	h.s.mu.Lock()
	h.run = h.s.run
	h.s.mu.Unlock()
	if h.run == nil {
		return nil, errors.New("the continuous run ended as it started")
	}
	return h.run.done, nil
}

func (h *panelHold) Why() string {
	if h.run == nil {
		return ""
	}
	return h.run.why.English()
}

// Fault and Stop release the board themselves when no hold was started (a
// session refused before it): stopTimedRun only releases a running one.
func (h *panelHold) Fault(why string) {
	if h.run == nil {
		h.s.releaseBoard(h.b)
	}
	_ = h.s.raiseFault(h.b, m("go.reason.text", "text", why))
}

func (h *panelHold) Stop() {
	if h.run == nil {
		h.s.releaseBoard(h.b)
		return
	}
	h.s.stopTimedRun(h.stopWhy())
}

// libPlan is the plan the criteria come from.
// reasonMsg words a ptlib reason; one under the range's minimum names that minimum (decision 136).
func reasonMsg(key, code, detail string) msg {
	switch code {
	case ptlib.ReasonBelowMin:
		return m(key, "min", detail)
	case ptlib.ReasonDUTCalRange:
		return dutCalRangeWhy(calstore.Range(detail))
	case ptlib.ReasonBoardReset:
		return m(key, "cause", firstNonEmpty(detail, "?"))
	}
	return m(key)
}

func libPlan() (ptplan.Plan, error) {
	path, err := safePlanPath(criteriaPlanName())
	if err != nil {
		return ptplan.Plan{}, err
	}
	return ptplan.Load(path)
}

// Dictionary keys by code. Literal keys, not built from the code: the
// dictionary check only sees literals (PORTTOOL-FLOW.md C.4.1).
var (
	askText = map[string]string{
		"aout.load": "lib.ask.aout.load", "dout.blink": "lib.ask.dout.blink", "dout.square": "lib.ask.dout.square",
		"led.blink":   "lib.ask.led.blink",
		"reset.cause": "lib.ask.reset.cause",
	}
	reasonText = map[string]string{
		ptlib.ReasonNotTicked: "lib.reason.not_ticked", ptlib.ReasonCalibration: "lib.reason.calibration",
		ptlib.ReasonNoRefStep: "lib.reason.no_ref_step", ptlib.ReasonNoCriteria: "lib.reason.criteria_gone",
		ptlib.ReasonAfterFail: "lib.reason.after_fail", ptlib.ReasonStopped: "lib.reason.stopped",
		ptlib.ReasonOperatorNo: "lib.reason.operator_no", ptlib.ReasonHoldEnded: "lib.reason.hold_ended",
		ptlib.ReasonNoFrames: "lib.reason.no_frames", ptlib.ReasonNotCalibrated: "lib.reason.not_calibrated",
		ptlib.ReasonAnalogPeer: "lib.reason.analog_peer", ptlib.ReasonAnalogPoints: "lib.reason.analog_points",
		ptlib.ReasonNoRef:    "lib.reason.no_ref",
		ptlib.ReasonLiveFull: "lib.reason.live_full", ptlib.ReasonNoCANPeer: "lib.reason.can_no_adapter",
		ptlib.ReasonRefCal: "lib.reason.ref_not_calibrated", ptlib.ReasonNoTarget: "lib.reason.no_target", ptlib.ReasonRefRange: "lib.reason.ref_range",
		ptlib.ReasonDUTCalRange: "rng.kb_other_range",
		ptlib.ReasonNoReading:   "lib.reason.no_reading", ptlib.ReasonBelowMin: "lib.reason.below_min",
		ptlib.ReasonCheckChain: "lib.reason.check_chain", ptlib.ReasonRefUnwired: "lib.reason.ref_unwired",
		ptlib.ReasonNotLinked: "lib.reason.not_linked", ptlib.ReasonNoBreak: "lib.reason.no_break",
		ptlib.ReasonNoRecovery: "lib.reason.no_recovery", ptlib.ReasonBoardReset: "lib.reason.board_reset",
	}
	// recoverCable is what a recover item asks the person to pull (decision 150).
	recoverCable = map[string]string{
		"eth": "lib.cable.eth", "usb": "lib.cable.usb", "rs232": "lib.cable.rs232",
		"rs485": "lib.cable.rs485", "can": "lib.cable.can", "knx": "lib.cable.knx",
	}
	pickText = map[string]string{
		"unknown_item": "lib.err.unknown_item", "exclusive": "lib.err.exclusive",
		"param_fixed": "lib.err.param_fixed", "param_refused": "lib.err.param_refused", "points": "lib.err.points",
		"line_too_long": "lib.err.line_too_long", "no_target": "lib.err.no_target", ptlib.ReasonBadPeer: "lib.err.bad_peer",
	}
)

// askOf is how a ptlib question is put to the person.
func askOf(q ptlib.Question) askQuestion {
	out := askQuestion{Item: q.Item, Kind: string(q.Kind), Prompt: q.Prompt, Unit: q.Unit}
	set := strconv.FormatFloat(q.Set, 'f', -1, 64)
	switch {
	case q.Walk == "ai":
		out.Text = m("ana.ask_ai", "ch", q.Channel, "i", q.Point, "n", q.Of, "v", set, "unit", q.Unit)
		out.Hint = ref(m("ana.ask_ai_hint", "frames", 8))
	case q.Walk == "ao":
		out.Text = m("ana.ask_ao", "ch", q.Channel, "i", q.Point, "n", q.Of, "v", set, "unit", q.Unit)
		out.Hint = ref(m("ana.ask_ao_hint"))
	case q.Walk == "relay": // the person counts the load's cycles (decision 130)
		out.Text = m("lib.ask.relay_count", "ch", q.Channel, "n", set)
		out.Hint = ref(m("lib.ask.relay_count_hint"))
	case q.Walk == "drop": // one DO on, the person measures it (STG-32)
		out.Text = m("lib.ask.do_drop", "ch", q.Channel)
		out.Hint = ref(m("lib.ask.do_drop_hint"))
	case q.Walk == "din": // the person applies 24 V, the board answers (decision 128)
		out.Text = m("lib.ask.din_level", "ch", q.Channel)
		out.Hint = ref(m("lib.ask.din_level_hint", "wrong", ptlib.DinWrongS, "wait", ptlib.DinWaitS))
	case q.Walk == "recover_pull" || q.Walk == "recover_plug": // the board's frames end it (decision 150)
		key, hint := "lib.ask.recover_pull", m("lib.ask.recover_pull_hint", "wait", ptlib.RecoverWaitS)
		if q.Walk == "recover_plug" {
			key, hint = "lib.ask.recover_plug", m("lib.ask.recover_plug_hint", "wait", ptlib.RecoverWaitS)
		}
		out.Text = m(key, "cable", m(recoverCable[q.Channel]))
		if q.Auto {
			hint = m("lib.ask.recover_sim_hint", "ms", ptlib.SimCutMS)
		}
		out.Hint = &hint
	case askText[q.Item] != "":
		out.Text = m(askText[q.Item])
	default: // a plan step that asks for an item with no question of its own
		name := any(q.Item)
		if it, ok := catalogueItem(q.Item); ok {
			name = m(it.Name)
		}
		out.Text = m("lib.ask.generic", "item", name)
	}
	return out
}

// newEngine wires ptlib to this panel.
func (s *Server) newEngine(b *ptboard.Board, port string, caps ptproto.Caps, plan ptplan.Plan,
	items []ptitems.Item, stopOnFail bool, stopWhy func() msg) *ptlib.Engine {
	wiring := s.refWiring() // read once: each lookup would ask the reference board for its UID
	eng := &ptlib.Engine{
		Board: b, Caps: caps, Plan: plan, Items: items, StopOnFail: stopOnFail,
		Runner:        s.libRunner(b, port),
		Hold:          &panelHold{s: s, b: b, stopWhy: stopWhy},
		Analog:        s.AnalogSource(),
		AnalogNominal: s.AnalogNominal(),
		RefAnalog:     s.RefAnalogSource(),
		CAN:           s.canPeer(port),
		KNX:           s.knxPeer(port),
		RefConnected:  s.refBoard() != nil,
		Wiring:        func() refboard.Wiring { return wiring },
		Ask: func(ctx context.Context, q ptlib.Question) (ptlib.Answer, error) {
			r, err := s.ask(ctx, askOf(q))
			if err != nil {
				return ptlib.Answer{}, err
			}
			a := ptlib.Answer{}
			if r.Yes != nil {
				a.Yes = *r.Yes
			}
			if r.Value != nil {
				a.Value = *r.Value
			}
			return a, nil
		},
		Update: func(st ptlib.State) {
			s.lib.mu.Lock()
			s.lib.states[st.ID] = st
			s.lib.mu.Unlock()
		},
	}
	// The recover items (decision 150): the simulated board cuts a port
	// itself; a lost control port is opened again under its name.
	if simboard.IsSim(port) {
		eng.Cut = func(p string, ms int) error {
			s.mu.Lock()
			rw := s.ctrlRW
			s.mu.Unlock()
			return simboard.Cut(rw, p, ms)
		}
	}
	eng.Reconnect = func(ctx context.Context) (*ptboard.Board, error) {
		nb, err := s.reconnect(eng.Board, port)
		if err != nil {
			return nil, err
		}
		eng.Runner = s.libRunner(nb, port)
		eng.Hold = &panelHold{s: s, b: nb, stopWhy: stopWhy}
		return nb, nil
	}
	return eng
}

// beginLib marks a run as started and forgets the previous results of its items.
func (s *Server) beginLib(stage, duration string, dur time.Duration, items []ptitems.Item, cancel context.CancelFunc) {
	l := &s.lib
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.states == nil {
		l.states = map[string]ptlib.State{}
		l.items = map[string]ptitems.Item{}
	}
	for _, it := range items {
		delete(l.states, it.ID)
		l.items[it.ID] = it
	}
	l.running, l.stage, l.duration, l.dur = true, stage, duration, dur
	l.started, l.cancel, l.userStop, l.ended = time.Now(), cancel, false, msg{}
}

func (s *Server) endLib(ended msg) {
	s.lib.mu.Lock()
	s.lib.running, s.lib.cancel, s.lib.ended, s.lib.eng = false, nil, ended, nil
	s.lib.mu.Unlock()
}

// clearLib forgets every item result and port verdict.
func (s *Server) clearLib() {
	s.lib.mu.Lock()
	s.lib.states, s.lib.items, s.lib.ended = nil, nil, msg{}
	s.lib.mu.Unlock()
}

func (s *Server) libBusy() bool {
	s.lib.mu.Lock()
	defer s.lib.mu.Unlock()
	return s.lib.running
}

func (s *Server) handleLibStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Stage    string       `json:"stage"`
		Duration string       `json:"duration"`
		Items    []ptlib.Pick `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, m("lib.err.bad_request"))
		return
	}
	s.peekMu.Lock() // a peek in flight ends first (peek.go)
	defer s.peekMu.Unlock()
	s.waitIPProbe() // its eth session must be stopped before ours start
	s.mu.Lock()
	b, port, caps, timed := s.board, s.portNam, s.caps, s.run != nil
	s.mu.Unlock()
	s.full.mu.Lock()
	full := s.full.running
	s.full.mu.Unlock()
	switch {
	case b == nil:
		writeJSON(w, 200, map[string]any{"error": m("go.cmd.no_board")})
		return
	case s.libBusy():
		writeJSON(w, 200, map[string]any{"error": m("lib.err.busy")})
		return
	case timed:
		writeJSON(w, 200, map[string]any{"error": m("go.hold.already")})
		return
	case full:
		writeJSON(w, 200, map[string]any{"error": m("go.full.busy")})
		return
	}
	dur, ok := durations[body.Duration]
	if !ok {
		writeJSON(w, 200, map[string]any{"error": m("lib.err.bad_request")})
		return
	}
	plan, err := libPlan()
	if err != nil {
		writeJSON(w, 200, map[string]any{"error": m("lib.err.plan", "detail", err)})
		return
	}
	_, all := ptitems.ForCaps(caps)
	all = ptlib.ForPlan(all, plan) // a recover item without criteria records (decision 150)
	byID := map[string]ptitems.Item{}
	for _, it := range all {
		byID[it.ID] = it
	}
	var items []ptitems.Item
	ticked := 0
	for _, p := range body.Items {
		it, ok := byID[p.ID]
		if !ok || !it.In(ptitems.Stage(body.Stage)) {
			writeJSON(w, 200, map[string]any{"error": m("lib.err.unknown_item", "item", p.ID)})
			return
		}
		items = append(items, it)
		if p.On {
			ticked++
		}
	}
	if ticked == 0 {
		writeJSON(w, 200, map[string]any{"error": m("lib.err.none_ticked")})
		return
	}
	if body.Stage == string(ptitems.RD) {
		if err := ptlib.CheckRD(body.Items); err != nil {
			writeJSON(w, 200, map[string]any{"error": pickMsg(err, byID)})
			return
		}
	}
	if err := ptlib.Check(items, plan, caps, body.Items); err != nil {
		writeJSON(w, 200, map[string]any{"error": pickMsg(err, byID)})
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.beginLib(body.Stage, body.Duration, dur, items, cancel)
	stopWhy := func() msg {
		s.lib.mu.Lock()
		defer s.lib.mu.Unlock()
		if s.lib.userStop {
			return m("go.reason.you_stopped")
		}
		return m("go.reason.time_up")
	}
	eng := s.newEngine(b, port, caps, plan, items, body.Stage != string(ptitems.RD), stopWhy)
	eng.RD = body.Stage == string(ptitems.RD)
	s.lib.mu.Lock()
	s.lib.eng = eng
	s.lib.mu.Unlock()
	s.say(m("lib.started", "n", ticked))
	go func() {
		defer cancel()
		rep, err := eng.Run(ctx, body.Items, dur)
		var ended msg
		switch {
		case err != nil:
			ended = m("lib.err.plan", "detail", err)
		case ctx.Err() != nil:
			ended = m("lib.done.stopped")
		default:
			ended = libSummary(rep)
		}
		s.say(ended)
		s.endLib(ended)
	}()
	writeJSON(w, 200, map[string]any{"started": true, "lib": s.libJSON()})
}

// libSummary is the log line for a finished run.
func libSummary(rep ptreport.Report) msg {
	t := rep.Tally()
	bad := t[ptreport.OutcomeFail] + t[ptreport.OutcomeTimeout] + t[ptreport.OutcomeError]
	return m("lib.done.summary", "pass", t[ptreport.OutcomePass], "bad", bad,
		"recorded", t[ptlib.Recorded], "skipped", t[ptreport.OutcomeSkipped])
}

// pickMsg words a refused pick.
func pickMsg(err error, byID map[string]ptitems.Item) msg {
	var pe *ptlib.PickError
	if !errors.As(err, &pe) {
		return m("lib.err.plan", "detail", err)
	}
	name := func(id string) any {
		if it, ok := byID[id]; ok {
			return m(it.Name)
		}
		return id
	}
	return m(pickText[pe.Code], "item", name(pe.Item), "other", name(pe.Other), "param", pe.Param, "why", pe.Why)
}

func (s *Server) handleLibStop(w http.ResponseWriter, r *http.Request) {
	s.stopLib()
	writeJSON(w, 200, s.libJSON())
}

// stopLib stops a running item run; a no-op otherwise.
func (s *Server) stopLib() {
	s.lib.mu.Lock()
	cancel := s.lib.cancel
	s.lib.userStop = true
	s.lib.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Server) handleLib(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.libJSON()) }

// handleLibLive passes a live card's action (end, DO switch, AO current, DI
// reset) to the running item (decision 108, PORTTOOL-FLOW.md C.3.6 "实时卡片").
func (s *Server) handleLibLive(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
		ptlib.LiveReq
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, m("lib.err.bad_request"))
		return
	}
	s.lib.mu.Lock()
	eng := s.lib.eng
	s.lib.mu.Unlock()
	if eng == nil {
		writeJSON(w, 200, map[string]any{"error": m("lib.live.not_running")})
		return
	}
	if err := eng.Live(body.ID, body.LiveReq); err != nil {
		if errors.Is(err, ptlib.ErrAOVoltUncal) {
			writeJSON(w, 200, map[string]any{"error": m("lib.live.ao_volt_uncal")})
			return
		}
		var low ptlib.BelowMinError
		if errors.As(err, &low) {
			writeJSON(w, 200, map[string]any{"error": m("lib.reason.below_min", "min", fmt.Sprintf("%g %s", low.Min, low.Unit))})
			return
		}
		if errors.Is(err, ptlib.ErrAONoRange) {
			writeJSON(w, 200, map[string]any{"error": m("lib.live.ao_no_range")})
			return
		}
		writeJSON(w, 200, map[string]any{"error": m("lib.live.refused", "detail", err.Error())})
		return
	}
	writeJSON(w, 200, s.libJSON())
}

// libJSON is what the cards and the port list draw.
func (s *Server) libJSON() map[string]any {
	s.mu.Lock()
	caps, connected := s.caps, s.board != nil
	logPath := ""
	if s.run != nil && s.run.log != nil {
		logPath = s.run.log.path
	}
	s.mu.Unlock()

	l := &s.lib
	l.mu.Lock()
	states := map[string]any{}
	var list []ptlib.State
	var items []ptitems.Item
	for id, st := range l.states {
		row := map[string]any{"outcome": st.Outcome, "running": st.Running, "rounds": st.Rounds,
			"checks": st.Checks, "values": st.Values, "answer": st.Answer, "detail": st.Detail}
		if len(st.Chans) > 0 {
			// Each channel's reason in words, on its own row (decision 129).
			cs := make([]map[string]any, 0, len(st.Chans))
			for _, c := range st.Chans {
				r := map[string]any{"ch": c.Ch, "name": c.Name, "outcome": c.Outcome}
				if k := reasonText[c.Reason]; k != "" {
					r["why"] = reasonMsg(k, c.Reason, c.Detail)
				}
				cs = append(cs, r)
			}
			row["chans"] = cs
		}
		if k := reasonText[st.Reason]; k != "" {
			row["reason"], row["code"] = reasonMsg(k, st.Reason, st.Detail), st.Reason
		}
		if st.Analog != nil {
			row["analog"] = st.Analog
		}
		if len(st.Events) > 0 {
			row["events"] = st.Events
		}
		if st.Live != nil {
			row["live"] = st.Live
		}
		if st.Drop != nil {
			row["drop"] = st.Drop
		}
		if st.DinWalk != nil {
			row["din_walk"] = st.DinWalk
		}
		if st.Relay != nil {
			row["relay"] = st.Relay
		}
		if st.Round != nil {
			row["round"] = st.Round // decision 151
		}
		states[id] = row
		list = append(list, st)
	}
	for _, it := range l.items {
		items = append(items, it)
	}
	out := map[string]any{"running": l.running, "stage": l.stage, "duration": l.duration,
		"states": states, "ports": ptlib.PortVerdicts(items, list), "ended": l.ended}
	if l.running {
		el := time.Since(l.started)
		out["elapsed_s"] = int(el.Seconds())
		if l.dur > 0 {
			out["left_s"] = int(max(0, (l.dur - el).Seconds()))
			out["total_s"] = int(l.dur.Seconds())
		}
		if logPath != "" {
			out["log"] = logPath // shown under Start for the whole run (decision 106)
		}
	}
	l.mu.Unlock()

	out["ask"] = s.askJSON()
	defaults := map[string]map[string]string{}
	if connected {
		if plan, err := libPlan(); err == nil {
			_, all := ptitems.ForCaps(caps)
			for _, it := range all {
				if len(it.Params) > 0 {
					defaults[it.ID] = ptlib.Defaults(it, plan, caps)
				}
			}
		}
	}
	out["defaults"] = defaults
	return out
}

// runItems is the ItemRunner the configured full-test steps call
// (stagecfg.go): timed=false runs the picks once, timed=true runs them
// together until the time is up (stageTimeUp), ctx ends or one fails.
func (s *Server) runItems(ctx context.Context, picks []ItemPick, timed bool) ptreport.StepResult {
	res := ptreport.StepResult{Outcome: ptreport.OutcomePass}
	s.mu.Lock()
	b, port, caps := s.board, s.portNam, s.caps
	s.mu.Unlock()
	if b == nil {
		res.Outcome, res.Reason = ptreport.OutcomeError, "no board connected"
		return res
	}
	plan, err := libPlan()
	if err != nil {
		res.Outcome, res.Reason = ptreport.OutcomeError, "criteria plan: "+err.Error()
		return res
	}
	var items []ptitems.Item
	var lp []ptlib.Pick
	for _, p := range picks {
		items = append(items, p.Item)
		lp = append(lp, ptlib.Pick{ID: p.Item.ID, On: true, Params: p.Params, Peer: p.Peer, Points: p.Points})
	}
	dur := time.Duration(0)
	if timed {
		dur = -1
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.beginLib("full", "", 0, items, cancel)
	up := stageTimeUp(ctx)
	stopWhy := func() msg {
		select {
		case <-up:
			if ctx.Err() == nil {
				return m("go.reason.time_up")
			}
		default:
		}
		return m("go.reason.you_stopped")
	}
	eng := s.newEngine(b, port, caps, plan, items, true, stopWhy)
	eng.NoLive = true // nobody is there to end a live card (decision 108)
	// The board under test on the configurations' shared ranges (decision 139).
	ranges := loadStageCfgs().Ranges
	eng.Analog, eng.AnalogNominal = s.analogSourceOn(ranges), s.analogNominalOn(ranges)
	// Every step of the run gets a number in the journal (decision 128).
	if jr, stage := s.fullJournal(), s.fullStage(); jr != nil && stage != 0 {
		byID := map[string]ItemPick{}
		for _, p := range picks {
			byID[p.Item.ID] = p
		}
		update, ask := eng.Update, eng.Ask
		eng.Update = func(st ptlib.State) {
			update(st)
			jr.observe(stage, byID[st.ID], st)
		}
		eng.Ask = func(ctx context.Context, q ptlib.Question) (ptlib.Answer, error) {
			jr.ask(stage, q, askOf(q).Text)
			a, err := ask(ctx, q)
			jr.answered(stage, q, a, err)
			return a, err
		}
	}
	eng.TimeUp = up
	rep, err := eng.Run(runCtx, lp, dur)
	s.endLib(msg{})
	if err != nil {
		res.Outcome, res.Reason = ptreport.OutcomeError, err.Error()
		return res
	}
	for _, st := range rep.Steps {
		for _, a := range st.Attempts {
			a.N = len(res.Attempts) + 1
			res.Attempts = append(res.Attempts, a)
		}
		// A multi-channel item has no result of its own: its channels count (decision 129).
		o := st.Outcome
		if len(st.Channels) > 0 {
			o = ptlib.Worst(st.Channels)
		}
		switch {
		case o == ptreport.OutcomePass, o == ptlib.Recorded, o == ptreport.OutcomeSkipped:
		case o == ptreport.OutcomeStopped && ctx.Err() != nil:
		default:
			if res.Outcome.IsPass() {
				res.Outcome, res.Reason = o, fmt.Sprintf("%s: %s", st.ID, st.Reason)
			}
		}
	}
	return res
}
