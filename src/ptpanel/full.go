package ptpanel

// The full fixture test: pick which of steps 2, 4, 5 and 6 to run, start, and
// get one record per board. Ordering and stop rules are in ptstages, records
// in ptrecord; this is the HTTP surface, the calibration gate, step 5's safety
// card and step 6's wiring. $PROD/docs/modules/M4/PORTTOOL-FLOW.md C.3.3-C.3.5.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"PortTool/src/calarea"
	"PortTool/src/ptecho"
	"PortTool/src/ptitems"
	"PortTool/src/ptlib"
	"PortTool/src/ptplan"
	"PortTool/src/ptproto"
	"PortTool/src/ptrecord"
	"PortTool/src/ptreport"
	"PortTool/src/ptstages"
	"PortTool/src/simboard"
)

// DeliveryDir is where the delivery's files sit: step 6's stage6/ folder and
// the fixture image. "" is the folder of the running exe; tests set it.
var DeliveryDir string

func deliveryDir() string {
	if DeliveryDir != "" {
		return DeliveryDir
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	return "."
}

// stepWhy says why a step cannot be picked, or nil. Stages overrides
// everything (tests); step 6 otherwise needs the delivery's three files and a
// real board: the simulated one has no ST-Link and no bootloader (C.3.3).
func (s *Server) stepWhy(st ptstages.Step) *msg {
	if why := s.configWhy(st); why != nil {
		return why
	}
	if s.Stages[st] != nil {
		return nil
	}
	if st != ptstages.UserLevel {
		return ref(m("go.full.step_missing"))
	}
	s.mu.Lock()
	port, connected := s.portNam, s.board != nil
	s.mu.Unlock()
	if connected && simboard.IsSim(port) {
		return ref(m("go.full.user_sim"))
	}
	files, _ := ptstages.LocateUserLevelFiles(deliveryDir())
	if miss := files.Missing(); len(miss) > 0 {
		return ref(m("go.full.user_files", "files", strings.Join(miss, ", ")))
	}
	return nil
}

// configWhy refuses a configuration step that would need a person: a ticked
// AI / AO / DI / DO item with no automatic route, one that needs the reference
// board while none is connected, or a blank target (decision 129).
func (s *Server) configWhy(st ptstages.Step) *msg {
	stg, ok := stepStage[st]
	if !ok {
		return nil
	}
	s.mu.Lock()
	caps := s.caps
	s.mu.Unlock()
	refOn := s.refBoard() != nil
	f := loadStageCfgs()
	for _, p := range f.of(stg).picks(stg, caps, f.RefUnused, refOn) {
		switch {
		case fixedRef(p.Item) && p.Peer == ptitems.PeerHuman:
			return ref(m("go.full.no_auto", "item", m(p.Item.Name)))
		case p.Peer == ptitems.PeerRef && !refOn:
			return ref(m("go.full.no_ref", "item", m(p.Item.Name)))
		case p.Peer == ptitems.PeerRef && s.refCannot(p) != nil:
			return s.refCannot(p)
		case ptlib.TargetMissing(p.Item, p.Params):
			return ref(m("go.full.no_target", "item", m(p.Item.Name)))
		}
	}
	return nil
}

// userLevelStep is step 6 from the delivery's files, its test part being the
// user-level configuration (PORTTOOL-FLOW.md C.3.5, C.3.6).
func (s *Server) userLevelStep(uid string) ptstages.Func {
	files, _ := ptstages.LocateUserLevelFiles(deliveryDir())
	return ptstages.Stage6{
		Files: files, Demo: files.Demo, Port: ptecho.FindCDC, UID: uid,
		Test: s.configStep(ptitems.User),
	}.Step()
}

// stepStage is the configuration each full-test step runs.
var stepStage = map[ptstages.Step]ptitems.Stage{
	ptstages.Functional: ptitems.Func, ptstages.Aging: ptitems.Aging, ptstages.UserLevel: ptitems.User,
}

// calNeeded is which of AI1, AI2, AO1, AO2 the picked steps' saved
// configurations test on this board: readings are judged with the board's own
// formula, so without it there is nothing to compare (decision 97).
func calNeeded(picked []ptstages.Step, caps ptproto.Caps, refConnected bool) []string {
	f := loadStageCfgs()
	need := map[string]bool{}
	for _, st := range picked {
		stg, ok := stepStage[st]
		if !ok {
			continue
		}
		for _, p := range f.of(stg).picks(stg, caps, f.RefUnused, refConnected) {
			addCal(need, p.Item.Port, p.Item.Channel)
		}
	}
	var out []string
	for _, n := range calarea.Names {
		if need[n] {
			out = append(out, n)
		}
	}
	return out
}

// addCal marks an analog port's channel; channel 0 is the whole port.
func addCal(need map[string]bool, port string, ch int) {
	pre := map[string]string{"ain": "AI", "aout": "AO"}[port]
	if pre == "" {
		return
	}
	if ch == 0 {
		need[pre+"1"], need[pre+"2"] = true, true
		return
	}
	need[pre+strconv.Itoa(ch)] = true
}

// calGate refuses a start whose steps test an AI / AO the board carries no
// calibration for; the reply names them and the port to calibrate on.
func (s *Server) calGate(picked []ptstages.Step, caps ptproto.Caps) map[string]any {
	need := calNeeded(picked, caps, s.refBoard() != nil)
	if len(need) == 0 {
		return nil
	}
	have, err := s.CalibratedChannels()
	if err != nil {
		return map[string]any{"error": msgOf(err)}
	}
	got := map[string]bool{}
	for _, n := range have {
		got[n] = true
	}
	var miss []string
	for _, n := range need {
		if !got[n] {
			miss = append(miss, n)
		}
	}
	if len(miss) == 0 {
		return nil
	}
	port := "ain"
	if strings.HasPrefix(miss[0], "AO") {
		port = "aout"
	}
	return map[string]any{
		"error":        m("go.full.uncalibrated", "list", strings.Join(miss, ", ")),
		"uncalibrated": miss,
		"cal_port":     port,
	}
}

// The words for an outcome, on screen and in the log.
var outcomeKey = map[ptreport.Outcome]string{
	ptreport.OutcomePass:    "go.full.o.pass",
	ptreport.OutcomeFail:    "go.full.o.fail",
	ptreport.OutcomeTimeout: "go.full.o.timeout",
	ptreport.OutcomeError:   "go.full.o.error",
	ptreport.OutcomeSkipped: "go.full.o.skipped",
	ptreport.OutcomeStopped: "go.full.o.stopped",
}

type safetyAnswer struct {
	SN      string `json:"sn"`
	Answers []struct {
		Yes  *bool  `json:"yes"`
		Note string `json:"note"`
	} `json:"answers"`
	Pass *bool `json:"pass"`
}

type fullState struct {
	mu        sync.Mutex
	running   bool
	cancel    context.CancelCauseFunc
	steps     []ptstages.Step
	status    map[ptstages.Step]ptreport.Outcome
	pct       map[ptstages.Step]int // 0..100 per step (C.3.3 「进度」)
	current   ptstages.Step
	questions []string
	answer    chan safetyAnswer // set while the safety card waits
	sn        string
	last      map[string]any
	jr        *journal // the last run's numbered steps and log (journal.go)
}

func (s *Server) fullRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/full", s.handleFull)
	mux.HandleFunc("/api/full/start", s.handleFullStart)
	mux.HandleFunc("/api/full/stop", s.handleFullStop)
	mux.HandleFunc("/api/full/safety", s.handleFullSafety)
}

// fullJournal is the running or last run's journal; nil before the first.
func (s *Server) fullJournal() *journal {
	s.full.mu.Lock()
	defer s.full.mu.Unlock()
	return s.full.jr
}

// fullStage is the full-test step running now; 0 outside one.
func (s *Server) fullStage() ptstages.Step {
	s.full.mu.Lock()
	defer s.full.mu.Unlock()
	if !s.full.running {
		return 0
	}
	return s.full.current
}

func (s *Server) fullJSON() map[string]any { return s.fullJSONSince(0) }

// fullJSONSince is /api/full with the run's log lines after seq since.
func (s *Server) fullJSONSince(since uint64) map[string]any {
	steps := []map[string]any{}
	for _, st := range ptstages.Order {
		why := s.stepWhy(st)
		row := map[string]any{"step": int(st), "available": why == nil}
		if why != nil {
			row["why"] = *why
		}
		steps = append(steps, row)
	}
	f := &s.full
	f.mu.Lock()
	defer f.mu.Unlock()
	status, pct := map[string]string{}, map[string]int{}
	for st, o := range f.status {
		status[strconv.Itoa(int(st))] = string(o)
	}
	for st, n := range f.pct {
		pct[strconv.Itoa(int(st))] = n
	}
	picked := []int{}
	for _, st := range f.steps {
		picked = append(picked, int(st))
	}
	out := map[string]any{
		"steps":   steps,
		"running": f.running,
		"current": int(f.current),
		"status":  status,
		"pct":     pct,
		"picked":  picked,
		"last":    f.last,
	}
	if f.answer != nil {
		out["safety"] = map[string]any{"questions": f.questions}
	}
	if f.jr != nil {
		out["run"] = f.jr.json(since)
	}
	out["ask"] = s.askJSON()
	return out
}

func (s *Server) handleFull(w http.ResponseWriter, r *http.Request) {
	// Lines after seq log of run id run; a page that last saw another run gets them all.
	since, _ := strconv.ParseUint(r.URL.Query().Get("log"), 10, 64)
	if jr := s.fullJournal(); jr == nil || strconv.FormatInt(jr.id, 10) != r.URL.Query().Get("run") {
		since = 0
	}
	writeJSON(w, 200, s.fullJSONSince(since))
}

func (s *Server) handleFullStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Steps []int `json:"steps"`
		// "" asks first when there is progress to continue from;
		// "continue" and "restart" are the two answers.
		Resume string `json:"resume"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, m("go.full.bad_request"))
		return
	}
	s.peekMu.Lock() // a peek in flight ends first (peek.go)
	defer s.peekMu.Unlock()
	s.waitIPProbe() // its eth session must be stopped before ours start
	picked := []ptstages.Step{}
	for _, st := range ptstages.Order {
		for _, n := range body.Steps {
			if n == int(st) && s.stepWhy(st) == nil {
				picked = append(picked, st)
			}
		}
	}
	if len(picked) == 0 || len(picked) != len(body.Steps) {
		writeJSON(w, 200, map[string]any{"error": m("go.full.pick")})
		return
	}

	s.mu.Lock()
	b, port, caps := s.board, s.portNam, s.caps
	s.mu.Unlock()
	if b == nil {
		writeJSON(w, 200, map[string]any{"error": m("go.cmd.no_board")})
		return
	}
	s.full.mu.Lock()
	busy := s.full.running
	s.full.mu.Unlock()
	if busy {
		writeJSON(w, 200, map[string]any{"error": m("go.full.busy")})
		return
	}
	if refuse := s.calGate(picked, caps); refuse != nil {
		writeJSON(w, 200, refuse)
		return
	}

	// The record is keyed by UID; the simulated board's readings are invented,
	// so it gets no record and no progress (PORTTOOL-FLOW.md C.3.3).
	uid := ""
	if !simboard.IsSim(port) {
		var err error
		if uid, err = s.boardUID(); err != nil {
			writeJSON(w, 200, map[string]any{"error": msgOf(err)})
			return
		}
	}

	nums := make([]int, len(picked))
	for i, st := range picked {
		nums[i] = int(st)
	}
	from, earlier := 0, map[ptstages.Step]string{}
	if uid != "" {
		prog, err := ptrecord.LoadProgress(uid)
		if err != nil {
			writeJSON(w, 200, map[string]any{"error": m("go.full.progress_unreadable", "detail", err)})
			return
		}
		at := ptrecord.ResumeAt(nums, prog)
		switch {
		case at > 0 && body.Resume == "":
			writeJSON(w, 200, map[string]any{"ask": map[string]any{"uid": uid, "from": nums[at], "passed": nums[:at]}})
			return
		case at > 0 && body.Resume == "continue":
			from = at
			for _, st := range picked[:at] {
				earlier[st], _ = prog.Has(int(st))
			}
		default:
			if err := ptrecord.ClearProgress(uid); err != nil {
				writeJSON(w, 200, map[string]any{"error": m("go.full.progress_unreadable", "detail", err)})
				return
			}
		}
	}

	header := ptreport.Report{
		ToolVersion: s.Version,
		Firmware:    caps.Version,
		BoardUID:    uid,
		Port:        port,
		StartedAt:   time.Now(),
	}
	header.Plan, header.LimitVersion, _ = criteriaSource()

	// The journal numbers every step this run takes (decision 128).
	cfgs := loadStageCfgs()
	durs := map[ptstages.Step]string{}
	for st, stg := range stepStage {
		durs[st] = cfgs.of(stg).Duration
	}
	jr := newJournal(planTotal(picked[from:], caps, s.refBoard() != nil), durs)
	sim := simboard.IsSim(port)
	shown := uid
	if sim {
		shown, _, _ = s.askUID()
	}
	where := m("run.hdr.record", "path", ptrecord.BoardDir(uid))
	if uid == "" {
		where = m("go.full.sim_no_record")
	}
	snLine := m("run.hdr.sn_none")
	for _, st := range picked[from:] {
		if st == ptstages.Safety {
			snLine = m("run.hdr.sn_later")
		}
	}
	jr.header(jHeaderUID(shown, sim), snLine, m("run.hdr.firmware", "version", caps.Version), where)
	evs, unsub := b.Subscribe(1024)
	unsent := b.WatchSent(jr.sent)
	go func() {
		for ev := range evs {
			jr.board(ev)
		}
	}()

	ctx, cancel := context.WithCancelCause(context.Background())
	f := &s.full
	f.mu.Lock()
	f.running, f.cancel, f.steps, f.sn = true, cancel, picked, ""
	f.status, f.pct = map[ptstages.Step]ptreport.Outcome{}, map[ptstages.Step]int{}
	f.current, f.last, f.jr = 0, nil, jr
	f.mu.Unlock()

	if from > 0 {
		s.say(m("go.full.continue", "step", stepName(nums[from])))
	} else {
		s.say(m("go.full.started", "steps", stepNames(nums)))
	}

	do := map[ptstages.Step]ptstages.Func{}
	for st, fn := range s.Stages {
		do[st] = fn
	}
	if do[ptstages.UserLevel] == nil {
		do[ptstages.UserLevel] = s.userLevelStep(uid)
	}
	for st, fn := range do {
		do[st] = f.withProgress(st, fn)
	}
	run := ptstages.Run{
		Steps: picked, From: from, Earlier: earlier, Do: do,
		Progress: func(st ptstages.Step, res *ptreport.StepResult) {
			f.mu.Lock()
			if res == nil {
				f.current = st
			} else {
				f.status[st], f.pct[st] = res.Outcome, 100
			}
			f.mu.Unlock()
			if res == nil {
				jr.stageStart(st)
			} else {
				res.Items = jr.stageEnd(st, res)
				res.FailCode = stageFailCode(st, res)
			}
			if res == nil {
				s.say(m("go.full.step_start", "step", stepName(int(st))))
			} else {
				s.say(m("go.full.step_end", "step", stepName(int(st)), "outcome", m(outcomeKey[res.Outcome])))
			}
		},
	}
	go func() {
		s.finishFull(ctx, run, header, uid, nums)
		unsent()
		unsub()
	}()
	writeJSON(w, 200, map[string]any{"started": true, "full": s.fullJSON()})
}

// stageFailCode is a sub-test's code: its first numbered step's, else its own
// outcome under its name (decision 150).
func stageFailCode(st ptstages.Step, res *ptreport.StepResult) string {
	for _, it := range res.Items {
		if it.FailCode != "" {
			return it.FailCode
		}
	}
	return ptreport.FailCode(st.Name(), 0, res.Outcome, "", nil)
}

func (s *Server) finishFull(ctx context.Context, run ptstages.Run, header ptreport.Report, uid string, nums []int) {
	rep := run.Execute(ctx, header)
	rep.FailCodes = rep.CollectFailCodes()
	f := &s.full
	f.mu.Lock()
	rep.SN = f.sn
	f.mu.Unlock()

	last := map[string]any{"passed": rep.Passed()}
	var items []ptreport.ItemRun
	for _, st := range rep.Steps {
		items = append(items, st.Items...)
	}
	last["items"] = items // the report's numbering, for the page to match (decision 128)
	for _, st := range rep.Steps {
		if strings.Contains(st.Reason, ptstages.RootLeft) {
			last["root_left"] = true
			s.say(m("go.alert.root_left"))
		}
	}
	if uid == "" {
		last["record_why"] = m("go.full.sim_no_record")
	} else if path, err := ptrecord.Write(rep); err != nil {
		last["record_why"] = m("go.full.record_failed", "detail", err)
	} else {
		last["record"] = path
		name := path[strings.LastIndexAny(path, `/\`)+1:]
		if err := ptrecord.Update(uid, nums, rep, name); err != nil {
			last["record_why"] = m("go.full.progress_unreadable", "detail", err)
		}
	}
	if jr := s.fullJournal(); jr != nil {
		path, _ := last["record"].(string)
		var why *msg
		if w, ok := last["record_why"].(msg); ok {
			why = &w
		}
		jr.done(rep, path, why)
	}
	if rep.Passed() {
		s.say(m("go.full.done_pass"))
	} else {
		s.say(m("go.full.done_fail"))
	}

	f.mu.Lock()
	f.running, f.cancel, f.current, f.last = false, nil, 0, last
	f.mu.Unlock()
}

// withProgress lets fn report its share done into f.pct (stepProgress).
func (f *fullState) withProgress(st ptstages.Step, fn ptstages.Func) ptstages.Func {
	return func(ctx context.Context) ptreport.StepResult {
		return fn(context.WithValue(ctx, progressKey{}, func(frac float64) {
			f.mu.Lock()
			f.pct[st] = int(min(99, max(0, frac*100)))
			f.mu.Unlock()
		}))
	}
}

// stopFull stops a running full test; a no-op otherwise. ST-Link writes in
// progress finish first (ptstages).
func (s *Server) stopFull(why string) {
	s.full.mu.Lock()
	cancel := s.full.cancel
	s.full.mu.Unlock()
	if cancel != nil {
		cancel(errors.New(why))
	}
}

func (s *Server) handleFullStop(w http.ResponseWriter, r *http.Request) {
	s.stopFull("stopped by operator")
	s.say(m("go.full.stopping"))
	writeJSON(w, 200, s.fullJSON())
}

// safetyStep is step 5: the card waits for the person's answers (STG-09).
func (s *Server) safetyStep(ctx context.Context) ptreport.StepResult {
	res := ptreport.StepResult{}
	var questions []string
	if path, err := safePlanPath(criteriaPlanName()); err == nil {
		plan, err := ptplan.Load(path)
		if err != nil {
			res.Outcome, res.Reason = ptreport.OutcomeError, "plan: "+err.Error()
			return res
		}
		questions = plan.SafetyQuestions()
	}
	ch := make(chan safetyAnswer, 1)
	f := &s.full
	f.mu.Lock()
	f.questions, f.answer = questions, ch
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.answer, f.questions = nil, nil
		f.mu.Unlock()
	}()

	att := ptreport.Attempt{N: 1, StartedAt: time.Now()}
	select {
	case <-ctx.Done():
		res.Outcome = ptreport.OutcomeStopped
		return res
	case a := <-ch:
		for i, q := range questions {
			k := "q" + strconv.Itoa(i+1)
			att.SetExtra(k, map[bool]string{true: "yes", false: "no"}[*a.Answers[i].Yes])
			att.SetExtra(k+".text", q)
			if n := strings.TrimSpace(a.Answers[i].Note); n != "" {
				att.SetExtra(k+".note", n)
			}
		}
		att.Outcome = ptreport.OutcomeFail
		if *a.Pass {
			att.Outcome = ptreport.OutcomePass
		}
		f.mu.Lock()
		f.sn = strings.TrimSpace(a.SN)
		f.mu.Unlock()
	}
	att.DurationMS = time.Since(att.StartedAt).Milliseconds()
	res.Attempts = []ptreport.Attempt{att}
	res.Outcome = att.Outcome
	return res
}

func (s *Server) handleFullSafety(w http.ResponseWriter, r *http.Request) {
	var a safetyAnswer
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
		writeErr(w, 400, m("go.full.bad_request"))
		return
	}
	f := &s.full
	f.mu.Lock()
	ch, n := f.answer, len(f.questions)
	f.mu.Unlock()
	if ch == nil {
		writeJSON(w, 200, map[string]any{"error": m("go.full.safety_not_waiting")})
		return
	}
	complete := a.Pass != nil && len(a.Answers) == n
	for _, x := range a.Answers {
		complete = complete && x.Yes != nil
	}
	if !complete {
		writeJSON(w, 200, map[string]any{"error": m("go.full.safety_incomplete")})
		return
	}
	select {
	case ch <- a:
	default:
		writeJSON(w, 200, map[string]any{"error": m("go.full.safety_not_waiting")})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// The panel names the full test's steps without their numbers (decision 122).
var stepNameKey = map[int]string{2: "ui.full.step2", 4: "ui.full.step4", 5: "ui.full.step5", 6: "ui.full.step6"}

func stepName(n int) msg {
	if k, ok := stepNameKey[n]; ok {
		return m(k)
	}
	return m("go.full.step_n", "n", n)
}

func stepNames(v []int) []msg {
	out := make([]msg, len(v))
	for i, n := range v {
		out[i] = stepName(n)
	}
	return out
}
