package ptlib

// Live cards: a person stands in for the reference board (decision 108,
// PORTTOOL-FLOW.md C.3.6 "实时卡片"). The session stays open, the card follows
// every frame, and the person sets DO / AO outputs from the card until they
// end the item or stop the run. Ending judges the typed reference values.

import (
	"context"
	"errors"
	"fmt"
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

// ReasonLiveFull: a live card waits for a person, so the unattended full test skips it.
const ReasonLiveFull = "live_full"

// What a person can do on a live card.
const (
	LiveEnd   = "end"   // end this item, the run goes on
	LiveDO    = "do"    // switch one DO on or off
	LiveAO    = "ao"    // drive one AO at a current
	LiveReset = "reset" // restart the DI count from zero
	LiveRef   = "ref"   // the reference value a person typed for one channel
	LiveSet   = "set"   // change the session's parameters (decision 137)
)

// The tables of an R&D card other than the session item's own, and the
// catalogue item each one's results are recorded on (decision 137,
// PORTTOOL-FLOW.md C.3.6 「研发四张卡的会话」).
var liveBlockItem = map[string]string{
	"level": "din.level", "drop": "dout.load", "blink": "dout.blink", "pwm": "dout.square",
}

// What LiveSet may change, per port; anything else stays as the session opened it.
var liveSetKeys = map[string][]string{"din": {"period"}, "dout": {"mode", "res", "duty", "freq", "period"}}

// LiveReq is one action from the card.
type LiveReq struct {
	Op    string  `json:"op"`
	Ch    int     `json:"ch"`
	On    bool    `json:"on"`
	MA    float64 `json:"ma"`    // LiveAO: the value in the channel's range unit, mA or mV
	Value string  `json:"value"` // LiveRef: as typed, "" clears it
	Block string  `json:"block,omitempty"` // LiveRef: which table; "" is the session item's own

	Args map[string]string `json:"args,omitempty"` // LiveSet
	// LiveEnd: the rows ticked per table; nil judges every row of the item's own table.
	Ticked map[string][]int `json:"ticked,omitempty"`

	// The CAN analyser window (canbus.go).
	Side  string `json:"side,omitempty"`   // CANBoard | CANPC
	CANID string `json:"can_id,omitempty"` // hex
	Data  string `json:"data,omitempty"`   // hex bytes
	Every int    `json:"every,omitempty"`  // ms between repeats, 0 = once
	Count int    `json:"count,omitempty"`  // how many in all
	Baud  int    `json:"baud,omitempty"`

	// The KNX window (knxwin.go); Side, Every, Count and Value (0-63) as above.
	GA  string `json:"ga,omitempty"`  // main/middle/sub
	Src string `json:"src,omitempty"` // board: area.line.device, "" keeps the session's
}

// LiveRefRow is a typed reference value beside the board's own (decision 108
// 「测量值和标准值」): AI / AO with the deviation in % FS, judged by the plan's
// read / output step when it has one; DI / DO / counts matched or not.
type LiveRefRow struct {
	Ch    int      `json:"ch"`
	Value string   `json:"value"`
	Board *float64 `json:"board,omitempty"` // the board's side, same unit
	Dev   *float64 `json:"dev,omitempty"`   // % FS
	Pass  *bool    `json:"pass,omitempty"`
}

// LiveAnalog is one AI / AO channel on a live card.
type LiveAnalog struct {
	Ch         int      `json:"ch"`
	Unit       string   `json:"unit,omitempty"` // empty: only the pin's mV is known
	Value      *float64 `json:"value,omitempty"`
	PinMV      *float64 `json:"pin_mv,omitempty"`
	Raw        *float64 `json:"raw,omitempty"` // AI: ADC count; AO: DAC mV the board landed on
	Set        *float64 `json:"set,omitempty"` // AO: the value asked for, in Unit
	Min        float64  `json:"min,omitempty"` // AO: the lowest value it takes (calstore Range.Min)
	FS         float64  `json:"fs,omitempty"`  // AO: the highest, the range's full scale
	Calibrated bool     `json:"calibrated"`
	// The board's k, b as pt.cal read them when the session opened (decision 151); nil uncalibrated.
	K *float64 `json:"k,omitempty"`
	B *float64 `json:"b,omitempty"`
}

// LiveQuad is one encoder's running count.
type LiveQuad struct {
	Count int `json:"count"`
	Dir   int `json:"dir"`
}

// LiveState is what a live card draws.
type LiveState struct {
	Kind   string       `json:"kind"` // din | quad | ain | aout | dout | can
	Frames int          `json:"frames"`
	Bits   []int        `json:"bits,omitempty"` // DI or DO 1-8, 1 = high / on
	Duty   []int        `json:"duty,omitempty"` // DO 1-8 as sent: the raw duty count
	Groups []LiveQuad   `json:"groups,omitempty"`
	Err    int          `json:"err"`
	Analog []LiveAnalog `json:"analog,omitempty"`
	Ref    []LiveRefRow `json:"ref,omitempty"`
	CAN    *CANView     `json:"can,omitempty"` // the CAN analyser window (canbus.go)
	KNX    *KNXView     `json:"knx,omitempty"` // the KNX analyser window (knxwin.go)
	// The card's other tables, by liveBlockItem key.
	Blocks map[string][]LiveRefRow `json:"blocks,omitempty"`
	// DO: the frame's mode, and the toggles of a blink the card asked for.
	Mode    string `json:"mode,omitempty"`
	Blink   int    `json:"blink,omitempty"`
	BlinkOf int    `json:"blink_of,omitempty"`
}

type liveMsg struct {
	req   LiveReq
	reply chan error
}

// Live passes a card action to a running live item.
func (e *Engine) Live(id string, r LiveReq) error {
	e.mu.Lock()
	ch := e.lives[id]
	e.mu.Unlock()
	if ch == nil {
		return errors.New("that item is not running live")
	}
	m := liveMsg{req: r, reply: make(chan error, 1)}
	select {
	case ch <- m:
	case <-time.After(2 * time.Second):
		return errors.New("the item did not take the action")
	}
	select {
	case err := <-m.reply:
		return err
	case <-time.After(5 * time.Second):
		return errors.New("the board did not answer in time")
	}
}

// liveCal is one analog channel's conversion: the board's k, b, or the
// nominal line when the board carries none.
type liveCal struct {
	cal AnalogCal
	ok  bool // a range is known
	kb  bool // the board's own k, b
}

func (e *Engine) liveCalOf(channel string) liveCal {
	if e.Analog != nil {
		if c, err := e.Analog(channel); err == nil {
			return liveCal{cal: c, ok: true, kb: true}
		}
	}
	if e.AnalogNominal != nil {
		if c, err := e.AnalogNominal(channel); err == nil {
			return liveCal{cal: c, ok: true}
		}
	}
	return liveCal{}
}

// runLive keeps the item's session open until the person ends it.
func (e *Engine) runLive(ctx context.Context, j *job) (ptreport.Outcome, string, string) {
	if j.item.Port == "can" {
		return e.runCANBus(ctx, j)
	}
	if j.item.Port == "knx" {
		return e.runKNXWin(ctx, j)
	}
	if e.NoLive {
		return ptreport.OutcomeSkipped, ReasonLiveFull, ""
	}
	port := j.item.Port
	kind := port
	t := j.item.Targets[0]
	args := map[string]string{}
	for k, v := range t.Set {
		args[k] = v
	}
	for k, v := range boardParams(j.params) {
		args[k] = v
	}
	if port == "din" && args["mode"] == "quad" {
		kind = "quad"
	}
	// Every channel, said outright: a session keeps the last ch= it was given
	// ($BOOT porttool_ain.c ain_apply), so an earlier one-channel item would
	// otherwise leave the live card blind on the rest.
	if args["ch"] == "" {
		args["ch"] = map[string]string{"ain": "1,2", "aout": "1,2", "dout": "1,2,3,4,5,6,7,8"}[port]
		if args["ch"] == "" {
			delete(args, "ch")
		}
	}
	switch port {
	case "dout":
		args["mode"], args["res"], args["duty"] = "hold", "8", "0"
	case "aout":
		args["mv"] = "0"
	case "ain":
		if args["period"] == "" {
			args["period"] = "200"
		}
	}
	keys := make([]string, 0, len(args))
	for _, k := range []string{"mode", "ch", "res", "duty", "mv", "period"} {
		if v, ok := args[k]; ok {
			keys = append(keys, k+"="+v)
			delete(args, k)
		}
	}
	for k, v := range args {
		keys = append(keys, k+"="+v)
	}
	start := strings.TrimSpace("pt.start " + port + " " + strings.Join(keys, " "))

	cals := map[int]liveCal{}
	if port == "ain" || port == "aout" {
		pre := "AI"
		if port == "aout" {
			pre = "AO"
		}
		for n := 1; n <= 2; n++ {
			cals[n] = e.liveCalOf(pre + strconv.Itoa(n))
		}
	}

	att := ptreport.Attempt{N: 1, StartedAt: time.Now()}
	send := func(cmd string) error {
		att.Sent = append(att.Sent, cmd)
		lines, err := e.Board.Send(cmd, ptboard.ExpectOne, ptboard.DefaultTimeout)
		att.Raw = append(att.Raw, lines...)
		return err
	}
	events, unsub := e.Board.Subscribe(256)
	defer unsub()
	if err := send(start); err != nil {
		_ = send("pt.stop " + port)
		o, d := e.recordErr(j, att, err)
		return o, "", d
	}
	stop := func() {
		if port == "aout" {
			_ = send("pt.set aout mv=1:0,2:0")
		}
		_ = send("pt.stop " + port)
	}

	reqs := make(chan liveMsg)
	e.mu.Lock()
	if e.lives == nil {
		e.lives = map[string]chan liveMsg{}
	}
	e.lives[j.item.ID] = reqs
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.lives, j.item.ID)
		e.mu.Unlock()
	}()

	st := LiveState{Kind: kind}
	do := make([]int, 8)
	aoSet := map[int]float64{}
	refs := map[string]map[int]string{}
	blinking := false
	show := func() { c := st; e.change(j.item.ID, func(s *State) { s.Live = &c }) }
	show()
	end := func(o ptreport.Outcome) (ptreport.Outcome, string, string) {
		stop()
		att.Outcome = o
		att.DurationMS = time.Since(att.StartedAt).Milliseconds()
		e.addAttempts(j, att)
		return o, "", ""
	}
	for {
		select {
		case <-ctx.Done():
			return end(ptreport.OutcomeStopped)
		case ev, ok := <-events:
			if !ok {
				o, d := e.recordErr(j, att, errors.New("the board stopped answering"))
				return o, "", d
			}
			if ev.Kind != ptproto.LineFrame || ev.Frame.Port != port {
				continue
			}
			liveFrame(&st, ev.Frame, cals, aoSet)
			// A blink the card asked for stops itself after the plan's toggles (decision 137).
			if blinking && st.Mode == "blink" {
				st.Blink++
				if st.Blink >= st.BlinkOf {
					blinking = false
					_ = send("pt.set dout mode=hold duty=" + dutyList(make([]int, 8)))
				}
			}
			e.liveAllRefs(j, &st, refs, cals)
			st.Frames++
			vals := valuesOf(ptreport.Attempt{Raw: []string{ev.Line}})
			c := st
			e.change(j.item.ID, func(s *State) { s.Live, s.Values = &c, vals })
		case m := <-reqs:
			switch m.req.Op {
			case LiveEnd:
				m.reply <- nil
				if m.req.Ticked == nil {
					e.liveChans(j, st)
					return end(liveVerdict(st))
				}
				o, _, _ := end(e.liveTicked(j, st, m.req.Ticked))
				if o == ptreport.OutcomeSkipped {
					return o, ReasonNotTicked, ""
				}
				return o, "", ""
			case LiveDO:
				if port != "dout" || m.req.Ch < 1 || m.req.Ch > 8 {
					m.reply <- errors.New("no such output")
					continue
				}
				do[m.req.Ch-1] = 0
				if m.req.On {
					do[m.req.Ch-1] = 255
				}
				var d []string
				for i, v := range do {
					d = append(d, fmt.Sprintf("%d:%d", i+1, v))
				}
				m.reply <- send("pt.set dout duty=" + strings.Join(d, ","))
			case LiveAO:
				if port != "aout" || m.req.Ch < 1 || m.req.Ch > 2 {
					m.reply <- errors.New("no such AO channel")
					continue
				}
				lc := cals[m.req.Ch]
				c := lc.cal
				// Without the registered range the unit is unknown: do not guess current (decision 134).
				if !lc.ok {
					m.reply <- ErrAONoRange
					continue
				}
				// A voltage AO's nominal is only the DAC mV; without k, b nothing maps it (decision 134).
				if !c.Current() && !lc.kb {
					m.reply <- ErrAOVoltUncal
					continue
				}
				if m.req.MA >= 0 && m.req.MA < c.floor() {
					m.reply <- BelowMinError{Min: c.floor(), Unit: c.Unit}
					continue
				}
				if m.req.MA < 0 || m.req.MA > c.FS {
					m.reply <- fmt.Errorf("AO%d takes 0-%g %s", m.req.Ch, c.FS, c.Unit)
					continue
				}
				err := send(fmt.Sprintf("pt.set aout mv=%d:%d", m.req.Ch, aoMV(m.req.MA, c)))
				if err == nil {
					aoSet[m.req.Ch] = m.req.MA
				}
				m.reply <- err
			case LiveRef:
				b := m.req.Block
				if _, ok := liveBlockItem[b]; (b != "" && !ok) || m.req.Ch < 1 || m.req.Ch > 8 {
					m.reply <- errors.New("no such channel")
					continue
				}
				v := strings.TrimSpace(m.req.Value)
				if refs[b] == nil {
					refs[b] = map[int]string{}
				}
				if v == "" {
					delete(refs[b], m.req.Ch)
				} else {
					if _, err := strconv.ParseFloat(v, 64); err != nil {
						m.reply <- errors.New("a reference value is a number")
						continue
					}
					refs[b][m.req.Ch] = v
				}
				att.SetExtra(fmt.Sprintf("ref%s%d", b, m.req.Ch), v)
				e.liveAllRefs(j, &st, refs, cals)
				show()
				m.reply <- nil
			case LiveSet:
				cmd, err := e.liveSetCmd(port, m.req.Args)
				if err == nil {
					err = send(cmd)
				}
				if mode, ok := m.req.Args["mode"]; ok && err == nil && port == "dout" {
					blinking = mode == "blink"
					st.Blink, st.BlinkOf = 0, blinkToggles(e.Plan)
				}
				show()
				m.reply <- err
			case LiveReset:
				if port != "din" {
					m.reply <- errors.New("only the DI count resets")
					continue
				}
				_ = send("pt.stop din")
				m.reply <- send(start)
			default:
				m.reply <- errors.New("unknown action " + m.req.Op)
			}
		}
	}
}

// liveVerdict judges a live card by the reference values the person typed:
// any mismatch fails (a quad count also needs no missed steps); otherwise a
// row left blank counts as stopped, every row matching passes
// (PORTTOOL-FLOW.md C.3.6, decision 129).
func liveVerdict(st LiveState) ptreport.Outcome {
	judged, bad := 0, false
	for _, r := range st.Ref {
		if r.Pass != nil {
			judged++
			bad = bad || !*r.Pass
		}
	}
	if st.Kind == "quad" && judged > 0 && st.Err > 0 {
		bad = true
	}
	rows := len(st.Bits)
	switch st.Kind {
	case "quad":
		rows = len(st.Groups)
	case "ain", "aout":
		rows = len(st.Analog)
	}
	switch {
	case bad:
		return ptreport.OutcomeFail
	case judged > 0 && judged >= rows:
		return ptreport.OutcomePass
	}
	return ptreport.OutcomeStopped
}

// liveFrame folds one frame into the card's state.
func liveFrame(st *LiveState, f ptproto.Frame, cals map[int]liveCal, aoSet map[int]float64) {
	switch st.Kind {
	case "din", "quad":
		if v, err := strconv.ParseUint(get(f, "v"), 0, 8); err == nil {
			st.Bits = bitsOf(int(v))
		}
		if st.Kind == "quad" {
			st.Groups = st.Groups[:0]
			for n := 1; n <= 4; n++ {
				g := LiveQuad{}
				g.Count, _ = strconv.Atoi(get(f, "g"+strconv.Itoa(n)))
				g.Dir, _ = strconv.Atoi(get(f, "d"+strconv.Itoa(n)))
				st.Groups = append(st.Groups, g)
			}
			st.Err, _ = strconv.Atoi(get(f, "err"))
		}
	case "dout":
		bits, duty := make([]int, 8), make([]int, 8)
		for n := 1; n <= 8; n++ {
			if d, err := strconv.Atoi(get(f, "ch"+strconv.Itoa(n))); err == nil {
				duty[n-1] = d
				if d > 0 {
					bits[n-1] = 1
				}
			}
		}
		st.Bits, st.Duty = bits, duty
		st.Mode = get(f, "mode")
	case "ain", "aout":
		st.Analog = st.Analog[:0]
		for n := 1; n <= 2; n++ {
			parts := strings.Split(get(f, "ch"+strconv.Itoa(n)), "/")
			c := cals[n]
			a := LiveAnalog{Ch: n, Calibrated: c.kb}
			if c.kb {
				k, b := c.cal.K, c.cal.B
				a.K, a.B = &k, &b
			}
			if c.ok {
				a.Unit = c.cal.Unit
				if st.Kind == "aout" {
					a.Min, a.FS = c.cal.floor(), c.cal.FS
				}
			}
			if st.Kind == "ain" && len(parts) >= 2 {
				raw, err1 := strconv.ParseFloat(parts[0], 64)
				pin, err2 := strconv.ParseFloat(parts[1], 64)
				if err2 == nil {
					a.PinMV = &pin
				}
				if err1 == nil {
					a.Raw = &raw
				}
				if err1 == nil && c.ok && c.cal.Nominal != nil {
					v := c.cal.K*c.cal.Nominal(raw) + c.cal.B
					a.Value = &v
				}
			}
			if st.Kind == "aout" {
				if len(parts) >= 2 {
					if mv, err := strconv.ParseFloat(parts[1], 64); err == nil {
						a.Raw = &mv
						// The terminal value by the registered range's formula; the
						// frame's microamps assume 0-20 mA (PORTTOOL-FLOW.md C.3.6 「AO 手动」).
						if c.ok {
							k, b := 1.0, 0.0
							if c.kb {
								k, b = c.cal.K, c.cal.B
							}
							v := k*c.cal.Range.AONominal(mv) + b
							a.Value = &v
						}
					}
				}
				if s, ok := aoSet[n]; ok {
					a.Set = &s
				}
			}
			st.Analog = append(st.Analog, a)
		}
	}
}

func bitsOf(v int) []int {
	out := make([]int, 8)
	for i := range out {
		out[i] = (v >> i) & 1
	}
	return out
}

// liveAllRefs sets every table's typed values beside the board's side.
func (e *Engine) liveAllRefs(j *job, st *LiveState, refs map[string]map[int]string, cals map[int]liveCal) {
	st.Ref = e.liveRefs(j, st, refs[""], cals)
	st.Blocks = nil
	for b := range liveBlockItem {
		if len(refs[b]) == 0 {
			continue
		}
		if st.Blocks == nil {
			st.Blocks = map[string][]LiveRefRow{}
		}
		st.Blocks[b] = e.blockRefs(b, st, refs[b])
	}
}

// blockRefs judges one of a card's other tables (decision 137): DI levels
// match the board's, the DO ones by the plan's step for what a person typed.
func (e *Engine) blockRefs(block string, st *LiveState, refs map[int]string) []LiveRefRow {
	var out []LiveRefRow
	for ch := 1; ch <= 8; ch++ {
		v, ok := refs[ch]
		if !ok {
			continue
		}
		want, _ := strconv.ParseFloat(v, 64)
		row := LiveRefRow{Ch: ch, Value: v}
		switch block {
		case "level":
			if ch <= len(st.Bits) {
				b := float64(st.Bits[ch-1])
				row.Board = &b
				p := b == want
				row.Pass = &p
			}
		case "drop":
			row.Pass = e.planJudge("high-side-drop", want)
		case "blink":
			b := float64(st.Blink)
			row.Board = &b
			row.Pass = e.planJudge("dout-blink-seen", want)
		case "pwm": // the person's look on the scope: 1 right, 0 wrong
			p := want == 1
			row.Pass = &p
		}
		out = append(out, row)
	}
	return out
}

// liveRefs sets each typed reference value beside the board's side.
func (e *Engine) liveRefs(j *job, st *LiveState, refs map[int]string, cals map[int]liveCal) []LiveRefRow {
	var out []LiveRefRow
	for ch := 1; ch <= 8; ch++ {
		v, ok := refs[ch]
		if !ok {
			continue
		}
		want, _ := strconv.ParseFloat(v, 64)
		row := LiveRefRow{Ch: ch, Value: v}
		var board *float64
		switch st.Kind {
		case "din", "dout":
			if ch <= len(st.Bits) {
				b := float64(st.Bits[ch-1])
				board = &b
			}
		case "quad":
			if ch <= len(st.Groups) {
				b := float64(st.Groups[ch-1].Count)
				board = &b
			}
		case "ain", "aout":
			for _, a := range st.Analog {
				if a.Ch == ch && a.Value != nil {
					b := *a.Value
					board = &b
				}
			}
		}
		row.Board = board
		if board != nil {
			switch st.Kind {
			case "ain", "aout":
				if c := cals[ch]; c.ok && c.cal.FS > 0 {
					// Same sign as the walk (analog.go): AI is board minus source, AO meter minus board.
					dev := (*board - want) / c.cal.FS * 100
					if st.Kind == "aout" {
						dev = -dev
					}
					row.Dev = &dev
					row.Pass = e.liveJudge(st.Kind, ch, dev)
				}
			default:
				p := *board == want
				row.Pass = &p
			}
		}
		out = append(out, row)
	}
	return out
}

// liveJudge evaluates an analog deviation with the plan's read / output step
// (ai<n>-read, ao<n>-out); nil when the plan has none (decision 77: judging is ptcheck's).
func (e *Engine) liveJudge(kind string, ch int, dev float64) *bool {
	id := fmt.Sprintf("ai%d-read", ch)
	if kind == "aout" {
		id = fmt.Sprintf("ao%d-out", ch)
	}
	return e.planJudge(id, dev)
}

// planJudge evaluates one value with a plan step's checks on "value"; nil
// when the plan has no such step.
func (e *Engine) planJudge(id string, v float64) *bool {
	for _, st := range e.Plan.Steps {
		if st.ID != id || len(st.Checks) == 0 {
			continue
		}
		_, pass := ptcheck.EvalAll(st.Checks, func(f string) (string, bool) {
			if f == "value" {
				return strconv.FormatFloat(v, 'f', 4, 64), true
			}
			return "", false
		})
		return &pass
	}
	return nil
}

// blinkToggles is how many toggles the card's blink makes: the plan's
// dout-blink frames, the count a person is judged against.
func blinkToggles(plan ptplan.Plan) int {
	for _, st := range plan.Steps {
		if st.ID == "dout-blink" && st.Frames > 0 {
			return st.Frames
		}
	}
	return 6
}

func dutyList(duty []int) string {
	var d []string
	for i, v := range duty {
		d = append(d, fmt.Sprintf("%d:%d", i+1, v))
	}
	return strings.Join(d, ",")
}

// liveSetCmd is the pt.set for a card's parameter change, refused when a key
// is not the card's or a value is outside the firmware's stated limit.
func (e *Engine) liveSetCmd(port string, args map[string]string) (string, error) {
	p, _ := e.Caps.Port(port)
	var parts []string
	for _, k := range liveSetKeys[port] {
		v, ok := args[k]
		if !ok {
			continue
		}
		if v == "" || strings.ContainsAny(v, " \t=") {
			return "", fmt.Errorf("%s=%q is not a value", k, v)
		}
		if l, ok := p.Limits[k]; ok {
			if fine, why := l.Accepts(v); !fine {
				return "", fmt.Errorf("%s %s", k, why)
			}
		}
		parts = append(parts, k+"="+v)
	}
	if len(parts) == 0 || len(parts) != len(args) {
		return "", errors.New("nothing the card may change")
	}
	return "pt.set " + port + " " + strings.Join(parts, " "), nil
}

// liveTicked ends an R&D card's session (decision 137): every table's
// ticked rows get their own result, on the item the table belongs to; a row
// ticked but left blank is stopped (decision 129), except on DO under load,
// where it is left out (decision 151). It returns the session
// item's own outcome, SKIPPED when none of its own rows was ticked.
func (e *Engine) liveTicked(j *job, st LiveState, ticked map[string][]int) ptreport.Outcome {
	rowsOf := func(b string) []LiveRefRow {
		if b == "" {
			return st.Ref
		}
		return st.Blocks[b]
	}
	own := ptreport.OutcomeSkipped
	for b, chs := range ticked {
		id := j.item.ID
		if b != "" {
			id = liveBlockItem[b]
		}
		if len(chs) == 0 || id == "" {
			continue
		}
		pass := map[int]*bool{}
		for _, r := range rowsOf(b) {
			pass[r.Ch] = r.Pass
		}
		var rs []ptreport.ChannelResult
		for _, c := range chs {
			p, typed := pass[c]
			// DO under load: a row left blank is not judged (decision 151 item 9).
			if !typed && b == "drop" {
				continue
			}
			o, why := ptreport.OutcomePass, ""
			switch {
			case !typed || p == nil:
				o, why = ptreport.OutcomeStopped, ReasonStopped
			case !*p || (b == "" && st.Kind == "quad" && st.Err > 0):
				o = ptreport.OutcomeFail
			}
			rs = append(rs, ptreport.ChannelResult{Ch: c, Outcome: o, Reason: why})
		}
		if len(rs) == 0 {
			continue
		}
		if id != j.item.ID {
			e.recordOn(id, rs)
			continue
		}
		for _, r := range rs {
			e.setChan(j, r.Ch, r.Outcome, r.Reason)
		}
		own = worst(rs)
	}
	// Only the ticked rows: fillChans must not hand the rest the outcome.
	var mine []string
	for _, c := range ticked[""] {
		mine = append(mine, strconv.Itoa(c))
	}
	if j.params == nil {
		j.params = map[string]string{}
	}
	j.params["ch"] = strings.Join(mine, ",")
	return own
}

// recordOn writes a table's channel results onto another item of the same
// run: one the page sent unticked beside the session item.
func (e *Engine) recordOn(id string, rs []ptreport.ChannelResult) {
	var it ptitems.Item
	for _, x := range e.Items {
		if x.ID == id {
			it = x
		}
	}
	e.mu.Lock()
	r, ok := e.results[id]
	_, has := e.states[id]
	e.mu.Unlock()
	if !ok || !has || it.ID == "" {
		return
	}
	for i := range rs {
		rs[i].Name = chanLabel(it, rs[i].Ch)
	}
	o := worst(rs)
	e.mu.Lock()
	r.Outcome, r.Reason, r.Channels = o, "", rs
	e.mu.Unlock()
	e.change(id, func(s *State) {
		s.Running, s.Outcome, s.Reason, s.Detail, s.Chans = false, o, "", "", rs
	})
}

// PeekAnalog converts one ain frame the way a live card does, for the AI
// reading card's value before anything runs (decision 114).
func (e *Engine) PeekAnalog(f ptproto.Frame) []LiveAnalog {
	cals := map[int]liveCal{1: e.liveCalOf("AI1"), 2: e.liveCalOf("AI2")}
	st := &LiveState{Kind: "ain"}
	liveFrame(st, f, cals, nil)
	return st.Analog
}
