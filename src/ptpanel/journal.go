package ptpanel

// The full test's journal: every step it runs gets a number, and the right
// column's 「正在进行」, result list and log, and the saved report all name a
// step by it (decision 128). $PROD/docs/modules/M4/PORTTOOL-FLOW.md C.3.6
// 「正在进行、结果、日志」 and 「步号」.

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"PortTool/src/ptboard"
	"PortTool/src/ptcheck"
	"PortTool/src/ptitems"
	"PortTool/src/ptlib"
	"PortTool/src/ptproto"
	"PortTool/src/ptreport"
	"PortTool/src/ptstages"
)

// Step kinds: an item (or one channel of it), the safety questionnaire, or a
// sub-test that ended badly with no numbered step to blame.
const (
	jItem   = "item"
	jSafety = "safety"
	jWhole  = "whole"
)

// jRunning is a step's state until it has an outcome.
const jRunning = "RUN"

// Protocol lines that repeat: at most one per step in this long (C.3.6).
const jRepeatGap = 5 * time.Second

// Lines kept; the oldest protocol lines go first.
const jMaxLines = 6000

type jStep struct {
	No      int
	Stage   ptstages.Step
	Kind    string
	Port    string
	Item    string
	Ch      int
	Peer    ptitems.Peer
	Params  map[string]string
	State   string
	Started time.Time
	Ended   time.Time
	Checks  []ptcheck.Result
	Values  [][2]string
	Answer  string
	Detail  string
	Reason  string // a ptlib Reason* code
	Seen    string // DI walk: the board's v for this channel
	Drop    *float64
	Analog  *ptlib.AnalogResult   // AI / AO walk: its points so far
	Used    []ptreport.AnalogUsed // ranges and k, b the item converted with (decision 136)
	Why     string                // whole step: the sub-test's reason, English
	Round   *ptlib.Round          // timed phase: the last finished round (decision 151)
	lastRaw time.Time             // repeat throttle
}

type jLine struct {
	Seq   uint64
	At    time.Time
	No    int
	Lv    string // info | pass | fail | timeout | ask
	Kind  string // hdr | sep | start | ask | answer | end | raw | done
	Msg   *msg
	Text  string
	Round *ptlib.Round // kind round: that round's summary
}

type journal struct {
	mu      sync.Mutex
	id      int64
	started time.Time
	total   int
	stage   ptstages.Step
	stages  map[ptstages.Step][2]time.Time // start, end
	durs    map[ptstages.Step]string       // the sub-test's configured duration key
	steps   []*jStep
	open    map[string]*jStep // stage|item|ch
	ran     map[string]bool   // stage|item: a step was opened for it
	lines   []jLine
	seq     uint64
	ended   bool
}

func newJournal(total int, durs map[ptstages.Step]string) *journal {
	now := time.Now()
	return &journal{id: now.UnixMilli(), started: now, total: total, durs: durs,
		stages: map[ptstages.Step][2]time.Time{}, open: map[string]*jStep{}, ran: map[string]bool{}}
}

func jKey(st ptstages.Step, item string, ch int) string {
	return strconv.Itoa(int(st)) + "|" + item + "|" + strconv.Itoa(ch)
}

func (j *journal) add(l jLine) {
	j.seq++
	l.Seq = j.seq
	if l.At.IsZero() {
		l.At = time.Now()
	}
	j.lines = append(j.lines, l)
	if len(j.lines) > jMaxLines {
		for i, x := range j.lines {
			if x.Kind == "raw" {
				j.lines = append(j.lines[:i], j.lines[i+1:]...)
				return
			}
		}
		j.lines = j.lines[1:]
	}
}

func (j *journal) say(no int, lv, kind string, x msg) {
	j.add(jLine{No: no, Lv: lv, Kind: kind, Msg: &x})
}

// Header is the run's first lines: board, SN, firmware, where the record goes.
func (j *journal) header(lines ...msg) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, x := range lines {
		j.say(0, "info", "hdr", x)
	}
}

func (j *journal) openStep(s *jStep) *jStep {
	s.No = len(j.steps) + 1
	s.State, s.Started = jRunning, time.Now()
	j.steps = append(j.steps, s)
	if s.No > j.total {
		j.total = s.No
	}
	if s.Kind == jItem {
		j.open[jKey(s.Stage, s.Item, s.Ch)] = s
		j.ran[jKey(s.Stage, s.Item, 0)] = true
	}
	j.say(s.No, "info", "start", m("run.log.start"))
	return s
}

// closeStep gives a step its outcome and logs the verdict line.
func (j *journal) closeStep(s *jStep, o ptreport.Outcome) {
	if s.State != jRunning {
		return
	}
	s.State, s.Ended = string(o), time.Now()
	delete(j.open, jKey(s.Stage, s.Item, s.Ch))
	j.add(jLine{No: s.No, Lv: outcomeLevel(o), Kind: "end"})
}

func outcomeLevel(o ptreport.Outcome) string {
	switch o {
	case ptreport.OutcomePass:
		return "pass"
	case ptreport.OutcomeFail, ptreport.OutcomeError:
		return "fail"
	case ptreport.OutcomeTimeout:
		return "timeout"
	}
	return "info"
}

// stageStart is a sub-test beginning; the safety questionnaire is one step.
func (j *journal) stageStart(st ptstages.Step) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.stage = st
	j.stages[st] = [2]time.Time{time.Now()}
	j.say(0, "info", "sep", m("run.log.sep", "sub", stepName(int(st))))
	if st == ptstages.Safety {
		j.openStep(&jStep{Stage: st, Kind: jSafety})
	}
}

// stageEnd closes what is still open and, when the sub-test ended badly
// without a numbered step to show for it, adds one that carries the reason.
// It returns the sub-test's numbered steps for the report.
func (j *journal) stageEnd(st ptstages.Step, res *ptreport.StepResult) []ptreport.ItemRun {
	j.mu.Lock()
	defer j.mu.Unlock()
	t := j.stages[st]
	t[1] = time.Now()
	j.stages[st] = t
	blamed := false
	for _, s := range j.steps {
		if s.Stage != st {
			continue
		}
		if s.State == jRunning {
			o := res.Outcome
			if s.Kind == jItem && o.IsPass() {
				o = ptreport.OutcomeStopped
			}
			j.closeStep(s, o)
		}
		if !ptreport.Outcome(s.State).IsPass() && s.State != string(ptlib.Recorded) {
			blamed = true
		}
	}
	if !res.Outcome.IsPass() && res.Outcome != ptreport.OutcomeSkipped && !blamed {
		s := j.openStep(&jStep{Stage: st, Kind: jWhole, Why: res.Reason})
		j.closeStep(s, res.Outcome)
	}
	var out []ptreport.ItemRun
	for _, s := range j.steps {
		if s.Stage == st {
			out = append(out, s.report())
		}
	}
	return out
}

// observe follows one item's state as the engine reports it (runItems).
func (j *journal) observe(st ptstages.Step, p ItemPick, x ptlib.State) {
	j.mu.Lock()
	defer j.mu.Unlock()
	ended := !x.Running && x.Outcome != ""
	if ptlib.ChannelWalk(p.Item.ID, p.Peer) {
		j.observeWalk(st, p, x)
		if ended {
			j.endItem(st, p, x)
		}
		return
	}
	// A multi-channel item is a step per channel, each closed on its own result (decision 129).
	if chs := ptlib.Channels(p.Item, p.Peer, p.Params); len(chs) > 0 {
		if x.Running && !j.ran[jKey(st, p.Item.ID, 0)] {
			for _, ch := range chs {
				j.openStep(j.itemStep(st, p, ch))
			}
		}
		logged := false
		for _, ch := range chs {
			if s := j.open[jKey(st, p.Item.ID, ch)]; s != nil {
				s.Checks, s.Values, s.Detail, s.Used = x.Checks, lastValues(x.Values), x.Detail, x.Used
				logged = j.round(s, x.Round, !logged) || logged
			}
		}
		if ended {
			j.endItem(st, p, x)
		}
		return
	}
	s := j.open[jKey(st, p.Item.ID, 0)]
	if s == nil && x.Running && !j.ran[jKey(st, p.Item.ID, 0)] {
		s = j.openStep(j.itemStep(st, p, 0))
	}
	if s != nil {
		s.Checks, s.Values, s.Answer, s.Detail, s.Reason = x.Checks, lastValues(x.Values), x.Answer, x.Detail, x.Reason
		s.Analog, s.Used = x.Analog, x.Used
		j.round(s, x.Round, true)
	}
	if ended {
		j.endItem(st, p, x)
	}
}

// round keeps a step's latest round and, when log is set, writes one line
// per new round; it reports whether a line was written.
func (j *journal) round(s *jStep, r *ptlib.Round, log bool) bool {
	if r == nil || (s.Round != nil && s.Round.No == r.No) {
		return false
	}
	s.Round = r
	if log {
		j.add(jLine{No: s.No, Lv: outcomeLevel(r.Outcome), Kind: "round", Round: r})
	}
	return log
}

func (j *journal) itemStep(st ptstages.Step, p ItemPick, ch int) *jStep {
	return &jStep{Stage: st, Kind: jItem, Port: p.Item.Port, Item: p.Item.ID, Ch: ch, Peer: p.Peer, Params: p.Params}
}

// observeWalk opens a step per channel as the walk reaches it and closes it
// on the channel's own verdict.
func (j *journal) observeWalk(st ptstages.Step, p ItemPick, x ptlib.State) {
	on := 0
	type row struct {
		ch   int
		pass *bool
		seen string
		drop *float64
		why  string
	}
	var rows []row
	if w := x.DinWalk; w != nil {
		on = w.On
		for _, r := range w.Rows {
			pass := r.Pass
			rows = append(rows, row{ch: r.Ch, pass: &pass, seen: r.Seen, why: r.Why})
		}
	}
	if d := x.Drop; d != nil {
		on = d.On
		for _, r := range d.Rows {
			rows = append(rows, row{ch: r.Ch, pass: r.Pass, drop: r.Drop})
		}
	}
	for _, r := range rows {
		s := j.open[jKey(st, p.Item.ID, r.ch)]
		if s == nil {
			continue
		}
		s.Seen, s.Drop, s.Detail = r.seen, r.drop, r.why
		switch {
		case r.pass == nil:
			j.closeStep(s, ptreport.OutcomeSkipped)
		case *r.pass:
			j.closeStep(s, ptreport.OutcomePass)
		default:
			j.closeStep(s, ptreport.OutcomeFail)
		}
	}
	if on > 0 && j.open[jKey(st, p.Item.ID, on)] == nil && !j.walked(st, p.Item.ID, on) {
		j.openStep(j.itemStep(st, p, on))
	}
	if w := x.DinWalk; w != nil && on > 0 {
		if s := j.open[jKey(st, p.Item.ID, on)]; s != nil {
			s.Seen = w.Now
		}
	}
}

func (j *journal) walked(st ptstages.Step, item string, ch int) bool {
	for _, s := range j.steps {
		if s.Stage == st && s.Item == item && s.Ch == ch {
			return true
		}
	}
	return false
}

// endItem closes an item's open steps with its outcome; an item that ended
// without ever showing as running (refused before it began) still gets one,
// unless it was skipped.
func (j *journal) endItem(st ptstages.Step, p ItemPick, x ptlib.State) {
	// A channel ends on its own result; the item's is only their worst (decision 129).
	own := func(ch int) (ptreport.Outcome, string, string) {
		for _, c := range x.Chans {
			if c.Ch == ch && c.Outcome != "" {
				return c.Outcome, c.Reason, firstNonEmpty(c.Detail, x.Detail)
			}
		}
		return x.Outcome, x.Reason, x.Detail
	}
	any := false
	for _, s := range j.steps {
		if s.Stage == st && s.Item == p.Item.ID {
			any = true
			if s.State == jRunning {
				o, why, detail := own(s.Ch)
				s.Reason, s.Detail = why, firstNonEmpty(s.Detail, detail)
				j.closeStep(s, o)
			}
		}
	}
	if !any && x.Outcome != ptreport.OutcomeSkipped {
		chs := []int{0}
		if len(x.Chans) > 0 {
			chs = nil
			for _, c := range x.Chans {
				chs = append(chs, c.Ch)
			}
		}
		for _, ch := range chs {
			s := j.openStep(j.itemStep(st, p, ch))
			s.Checks, s.Values, s.Answer = x.Checks, lastValues(x.Values), x.Answer
			s.Analog, s.Used = x.Analog, x.Used
			o, why, detail := own(ch)
			s.Reason, s.Detail = why, detail
			j.closeStep(s, o)
		}
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// lastValues keeps the board's latest fields, few enough for one line.
func lastValues(v [][2]string) [][2]string {
	if len(v) > 8 {
		v = v[:8]
	}
	return append([][2]string(nil), v...)
}

// ask logs a question put to the person, on the step it belongs to.
func (j *journal) ask(st ptstages.Step, q ptlib.Question, text msg) {
	j.mu.Lock()
	defer j.mu.Unlock()
	ch, _ := strconv.Atoi(q.Channel)
	j.say(j.stepNo(st, q.Item, ch), "ask", "ask", text)
}

// answered logs what the person gave; a DI walk is answered by the board.
func (j *journal) answered(st ptstages.Step, q ptlib.Question, a ptlib.Answer, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	ch, _ := strconv.Atoi(q.Channel)
	no := j.stepNo(st, q.Item, ch)
	switch {
	case err != nil:
		j.say(no, "info", "answer", m("run.log.answer_stop"))
	case q.Walk == "din":
	case a.Value != "":
		j.say(no, "info", "answer", m("run.log.answer_value", "v", a.Value))
	case a.Yes:
		j.say(no, "info", "answer", m("run.log.answer_yes"))
	default:
		j.say(no, "info", "answer", m("run.log.answer_no"))
	}
}

// stepNo is the open step of an item: that channel's, else the item's.
func (j *journal) stepNo(st ptstages.Step, item string, ch int) int {
	if s := j.open[jKey(st, item, ch)]; s != nil {
		return s.No
	}
	if s := j.open[jKey(st, item, 0)]; s != nil {
		return s.No
	}
	for i := len(j.steps) - 1; i >= 0; i-- {
		if s := j.steps[i]; s.Stage == st && s.Item == item && s.State == jRunning {
			return s.No
		}
	}
	return 0
}

// sent and board attach the protocol to the step running on that port.
func (j *journal) sent(cmd string, at time.Time) {
	if strings.HasPrefix(cmd, "pt.echo ") || strings.Contains(cmd, "rtc.read") {
		return
	}
	j.raw("→ "+cmd, at, strings.HasPrefix(cmd, "pt.hold"))
}

func (j *journal) board(ev ptboard.Event) {
	if isEchoNoise(ev) || strings.Contains(ev.Line, "rtc.read") || strings.Contains(ev.Line, "rtc ") {
		return
	}
	j.raw("← "+ev.Line, ev.At, ev.Kind == ptproto.LineFrame || strings.HasPrefix(ev.Line, "OK hold"))
}

func (j *journal) raw(line string, at time.Time, repeats bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.ended {
		return
	}
	s := j.lineStep(line)
	no := 0
	if s != nil {
		no = s.No
		if repeats {
			if !s.lastRaw.IsZero() && at.Sub(s.lastRaw) < jRepeatGap {
				return
			}
			s.lastRaw = at
		}
	} else if repeats {
		return
	}
	j.add(jLine{No: no, At: at, Lv: "info", Kind: "raw", Text: line})
}

// lineStep is the running step whose port the line names, else the only
// running step, else none.
func (j *journal) lineStep(line string) *jStep {
	var running []*jStep
	for _, s := range j.steps {
		if s.State == jRunning && s.Kind == jItem {
			running = append(running, s)
		}
	}
	words := strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == '!' || r == '=' || r == '.' })
	for i := len(running) - 1; i >= 0; i-- {
		for _, w := range words {
			if w == running[i].Port {
				return running[i]
			}
		}
	}
	if len(running) == 1 {
		return running[0]
	}
	return nil
}

// done is the run's last line: totals, and where the report went.
func (j *journal) done(rep ptreport.Report, record string, why *msg) {
	j.mu.Lock()
	defer j.mu.Unlock()
	pass, bad, to, left := j.tally()
	lv := "pass"
	if !rep.Passed() {
		lv = "fail"
	}
	where := m("run.log.record", "path", record)
	if record == "" && why != nil {
		where = *why
	}
	j.say(0, lv, "done", m("run.log.done", "pass", pass, "fail", bad, "timeout", to, "left", left, "where", where))
	j.ended = true
}

// tally is passed (recorded counts), failed (error counts), timed out, not run.
func (j *journal) tally() (pass, fail, timeout, left int) {
	run := 0
	for _, s := range j.steps {
		switch ptreport.Outcome(s.State) {
		case ptreport.OutcomePass, ptlib.Recorded:
			pass++
		case ptreport.OutcomeFail, ptreport.OutcomeError:
			fail++
		case ptreport.OutcomeTimeout:
			timeout++
		case jRunning:
			run++
		}
	}
	left = max(0, j.total-len(j.steps))
	return
}

// where is a step's place in English, for the report: files stay English
// (decision 82).
func (s *jStep) where() string {
	parts := []string{"#" + strconv.Itoa(s.No), stepName(int(s.Stage)).English()}
	switch s.Kind {
	case jSafety:
		parts = append(parts, m("run.where.safety").English())
	case jWhole:
		parts = append(parts, m("run.where.whole").English())
	default:
		for _, p := range ptitems.Ports {
			if p.Port == s.Port {
				parts = append(parts, m(p.Label).English())
			}
		}
		if it, ok := catalogueItem(s.Item); ok {
			parts = append(parts, m(it.Name).English())
		} else {
			parts = append(parts, s.Item)
		}
		if s.Ch > 0 {
			parts = append(parts, ptlib.ChanName(s.Port, s.Ch))
		}
	}
	return strings.Join(parts, " > ")
}

// value is the step's readings in one English line, for the report.
func (s *jStep) value() string {
	var out []string
	switch {
	case s.Drop != nil:
		out = append(out, strconv.FormatFloat(*s.Drop, 'f', -1, 64)+" V")
	case s.Seen != "":
		out = append(out, "v="+s.Seen)
	case lastPoint(s.Analog) != nil:
		r := lastPoint(s.Analog)
		out = append(out, "set="+fnum(r.Set)+" "+s.Analog.Unit, "got="+fnum(*r.Got))
		if r.Dev != nil {
			out = append(out, "dev="+strconv.FormatFloat(*r.Dev, 'f', 2, 64)+" % FS")
		}
	}
	for _, c := range s.Checks {
		out = append(out, c.Check.Field+"="+c.Got)
	}
	if len(s.Checks) == 0 {
		for _, v := range s.Values {
			out = append(out, v[0]+"="+v[1])
		}
	}
	if s.Answer != "" {
		out = append(out, "answer="+s.Answer)
	}
	for _, t := range []string{s.Reason, s.Detail, s.Why} {
		if t != "" {
			out = append(out, t)
		}
	}
	return strings.Join(out, ", ")
}

// lastPoint is the latest point of an analog walk that has a reading.
func lastPoint(a *ptlib.AnalogResult) *ptlib.AnalogRow {
	if a == nil {
		return nil
	}
	for i := len(a.Rows) - 1; i >= 0; i-- {
		if a.Rows[i].Got != nil {
			return &a.Rows[i]
		}
	}
	return nil
}

func fnum(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func (s *jStep) report() ptreport.ItemRun {
	end := s.Ended
	if end.IsZero() {
		end = time.Now()
	}
	return ptreport.ItemRun{No: s.No, Where: s.where(), Item: s.Item, Port: s.Port, Channel: s.Ch,
		Outcome: ptreport.Outcome(s.State), Value: s.value(), FailCode: s.failCode(), StartedAt: s.Started,
		DurationMS: end.Sub(s.Started).Milliseconds(), Analog: s.Used}
}

// failCode is the step's code in the report; a step with no item goes by its
// sub-test's name (decision 150).
func (s *jStep) failCode() string {
	where, ch := s.Stage.Name(), 0
	if s.Kind != jSafety && s.Kind != jWhole && s.Item != "" {
		where, ch = s.Item, s.Ch
		if ch > 0 {
			where += "." + ptreport.ChannelPart(ptlib.ChanName(s.Port, ch))
		}
	}
	return ptreport.FailCode(where, ch, ptreport.Outcome(s.State), s.Reason, s.Checks)
}

// json is what the right column draws; log lines after seq since.
func (j *journal) json(since uint64) map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := time.Now()
	steps := make([]map[string]any, 0, len(j.steps))
	for _, s := range j.steps {
		end := s.Ended
		if end.IsZero() {
			end = now
		}
		row := map[string]any{"no": s.No, "stage": int(s.Stage), "kind": s.Kind, "port": s.Port,
			"item": s.Item, "ch": s.Ch, "peer": s.Peer, "params": s.Params, "state": s.State,
			"started": s.Started.UnixMilli(), "dur_ms": end.Sub(s.Started).Milliseconds(),
			"checks": s.Checks, "values": s.Values, "answer": s.Answer, "detail": s.Detail,
			"seen": s.Seen, "drop": s.Drop, "analog": s.Analog, "why": s.Why, "round": s.Round}
		if k := reasonText[s.Reason]; k != "" {
			row["reason"] = reasonMsg(k, s.Reason, s.Detail)
		}
		steps = append(steps, row)
	}
	lines := []map[string]any{}
	first := uint64(0)
	if len(j.lines) > 0 {
		first = j.lines[0].Seq
	}
	i := sort.Search(len(j.lines), func(i int) bool { return j.lines[i].Seq > since })
	for _, l := range j.lines[i:] {
		row := map[string]any{"seq": l.Seq, "at": l.At.UnixMilli(), "no": l.No, "lv": l.Lv, "kind": l.Kind}
		if l.Msg != nil {
			row["msg"] = *l.Msg
		}
		if l.Text != "" {
			row["text"] = l.Text
		}
		if l.Round != nil {
			row["round"] = l.Round
		}
		lines = append(lines, row)
	}
	stages := map[string]any{}
	for st, t := range j.stages {
		row := map[string]any{"started": t[0].UnixMilli(), "dur": j.durs[st]}
		if !t[1].IsZero() {
			row["ended"] = t[1].UnixMilli()
		}
		stages[strconv.Itoa(int(st))] = row
	}
	pass, bad, to, left := j.tally()
	return map[string]any{"id": j.id, "started": j.started.UnixMilli(), "now": now.UnixMilli(),
		"total": max(j.total, len(j.steps)), "stage": int(j.stage), "stages": stages, "steps": steps,
		"log": lines, "log_first": first, "ended": j.ended,
		"tally": map[string]int{"pass": pass, "fail": bad, "timeout": to, "left": left}}
}

// planTotal is how many numbered steps the picked sub-tests should take:
// one per configured item, one per channel of a walked item, one for safety.
func planTotal(steps []ptstages.Step, caps ptproto.Caps, refConnected bool) int {
	f := loadStageCfgs()
	n := 0
	for _, st := range steps {
		if st == ptstages.Safety {
			n++
			continue
		}
		stg, ok := stepStage[st]
		if !ok {
			continue
		}
		for _, p := range f.of(stg).picks(stg, caps, f.RefUnused, refConnected) {
			if chs := ptlib.Channels(p.Item, p.Peer, p.Params); len(chs) > 0 {
				n += len(chs)
			} else {
				n++
			}
		}
	}
	return n
}

// jHeaderUID is what the first log line says about the board.
func jHeaderUID(uid string, sim bool) msg {
	if sim {
		return m("run.hdr.board_sim", "uid", firstNonEmpty(uid, "?"))
	}
	return m("run.hdr.board", "uid", uid)
}
