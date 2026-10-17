package ptlib

// AI read and AO output items: a walk over 3-5 points, each converted with
// the board's own k, b and judged by the plan step's checks on the error in
// % of full scale. PORTTOOL-FLOW.md C.3.2 "读数 / 输出项"; limits live in the
// plan (decisions 30, 44), ±1 % FS (decisions 90, 99).

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"PortTool/src/calstore"
	"PortTool/src/ptboard"
	"PortTool/src/ptcheck"
	"PortTool/src/ptitems"
	"PortTool/src/ptplan"
	"PortTool/src/ptproto"
	"PortTool/src/ptreport"
)

// AnalogCal is what one channel's walk needs from the board: its own k, b
// (pt.cal) and the registered range, whose formula converts (decision 136).
type AnalogCal struct {
	K, B   float64
	Unit   string
	Lo, FS float64
	Min    float64 // the lowest settable value when above Lo (calstore Range.Min)
	Range  calstore.Range
	// Calibrated is the range the board's archive says the channel was
	// calibrated on; "" when unknown (STG-40 row 11).
	Calibrated calstore.Range
	// Nominal turns an AI raw reading into the range's unit, as the app does
	// before applying k, b. Nil for AO.
	Nominal func(raw float64) float64
}

// floor is the lowest point or target the channel takes (decision 136).
func (c AnalogCal) floor() float64 { return math.Max(c.Lo, c.Min) }

// belowMin ends an item whose point or target is under what the range can
// output; the detail is that minimum, which the page words.
func belowMin(c AnalogCal) (ptreport.Outcome, string, string) {
	return ptreport.OutcomeError, ReasonBelowMin, fmtNum(c.floor()) + " " + c.Unit
}

// Current says the range carries a current; both ends of a wire must agree (decision 134).
func (c AnalogCal) Current() bool { return c.Range != "" && c.Range.Current() }

// used is what the report keeps of this conversion (decision 136).
func (c AnalogCal) used(board, channel string) ptreport.AnalogUsed {
	return ptreport.AnalogUsed{Board: board, Channel: channel, Range: string(c.Range), Unit: c.Unit, K: c.K, B: c.B}
}

// AnalogSource looks up a channel ("AI1".."AO2"). An error means the board
// carries no usable calibration for it, so nothing can be judged.
type AnalogSource func(channel string) (AnalogCal, error)

// AnalogRow is one point of a walk, for the card.
type AnalogRow struct {
	Set   float64  `json:"set"`             // the target, in the range's unit
	Raw   *float64 `json:"raw,omitempty"`   // what the board works in: AO DAC mV sent, AI ADC count read
	Shown *float64 `json:"shown,omitempty"` // what the board's formula makes of it
	Got   *float64 `json:"got"`             // the instrument; nil when skipped
	Dev   *float64 `json:"dev,omitempty"`   // error in % of full scale
	Pass  *bool    `json:"pass,omitempty"`
}

// AnalogResult is a walk's outcome, for the card.
type AnalogResult struct {
	Channel string  `json:"channel"`
	Range   string  `json:"range"`
	Unit    string  `json:"unit"`
	FS      float64 `json:"full_scale"`
	K       float64 `json:"k"`
	B       float64 `json:"b"`
	// The reference board's channel, range and own k, b when it gave the reference value.
	RefCh    string      `json:"ref_ch,omitempty"`
	RefRange string      `json:"ref_range,omitempty"`
	RefK     float64     `json:"ref_k,omitempty"`
	RefB     float64     `json:"ref_b,omitempty"`
	Rows     []AnalogRow `json:"rows"`
}

// Why an analog item could not be judged.
const (
	ReasonNotCalibrated = "not_calibrated"
	ReasonAnalogPeer    = "analog_peer"   // a peer this item has no walk for
	ReasonAnalogPoints  = "analog_points" // not 3-6 points
	ReasonRefCal        = "ref_not_calibrated"
	ReasonNoTarget      = "no_target"     // an AO load target outside the AO's registered range
	ReasonRefRange      = "ref_range"     // the two ends of a reference-board wire are on different ranges (decision 134)
	ReasonBelowMin      = "below_min"     // a point or target under what the range can output; detail is that minimum (decision 136)
	ReasonDUTCalRange   = "dut_cal_range" // calibrated on another range than set; detail is that range (STG-40 row 11)
)

// aiFrames is how many AI frames are averaged per point (C.3.2).
const aiFrames = 8

// IsAnalogReading says an item is an AI read or AO output item.
func IsAnalogReading(it ptitems.Item) bool {
	return (it.Port == "ain" || it.Port == "aout") && it.Channel > 0 && !IsCalibration(it)
}

// AnalogChannel is the calibration area's name for an item's channel.
func AnalogChannel(it ptitems.Item) string {
	if it.Port == "ain" {
		return "AI" + strconv.Itoa(it.Channel)
	}
	return "AO" + strconv.Itoa(it.Channel)
}

// FitPoints are the calibration walk's defaults: AI five, AO three, evenly
// from the range's low end to its full scale (decisions 90, 94; C.3.2).
func FitPoints(port string, lo, fs float64) []float64 {
	n := 3
	if port == "ain" {
		n = 5
	}
	out := make([]float64, n)
	for k := range out {
		out[k] = math.Round((lo+(fs-lo)*float64(k)/float64(n-1))*100) / 100
	}
	return out
}

// DefaultPoints are the read / output items' defaults: midway between
// neighbouring fit points, topped up with 90 % of full scale to reach three.
// A point on a fit point only checks the residual (decision 107; C.3.2).
func DefaultPoints(port string, lo, fs float64) []float64 {
	fit := FitPoints(port, lo, fs)
	out := make([]float64, 0, len(fit))
	for k := 1; k < len(fit); k++ {
		out = append(out, math.Round((fit[k-1]+fit[k])/2*100)/100)
	}
	if len(out) < 3 {
		out = append(out, math.Round(fs*0.9*100)/100)
	}
	return out
}

// PointsOK says a walk has 3-6 points (decision 90, widened to 6 by decision 117).
func PointsOK(pts []float64) bool { return len(pts) >= 3 && len(pts) <= 6 }

// fillAnalog works out an analog job: its one criteria step, judged per point.
func fillAnalog(j *job, plan ptplan.Plan) {
	if len(j.item.Criteria) != 1 {
		j.fail, j.detail = ReasonNoCriteria, strings.Join(j.item.Criteria, ",")
		return
	}
	for _, st := range plan.Steps {
		if st.ID == j.item.Criteria[0] {
			j.steps = []ptplan.Step{st}
		}
	}
	if len(j.steps) == 0 {
		j.fail, j.detail = ReasonNoCriteria, j.item.Criteria[0]
		return
	}
	j.analog = true
	switch {
	case j.peer == ptitems.PeerHuman:
	case j.peer == ptitems.PeerRef && hasPeer(j.item, ptitems.PeerRef):
	default:
		j.fail = ReasonAnalogPeer
	}
}

func hasPeer(it ptitems.Item, p ptitems.Peer) bool {
	for _, q := range it.Peers {
		if q == p {
			return true
		}
	}
	return false
}

// refSide is the reference board's half of a walk: the channel the
// registration wires to this one (decision 135, STG-31 A).
type refSide struct {
	b   *ptboard.Board
	cal AnalogCal // the reference board's own k, b and range for ch
	ch  string    // the reference board's channel, AO1 .. AI2
	ev  <-chan ptboard.Event
}

// openRef checks the reference side of an AI read / AO output walk; a
// non-empty outcome ends the item.
func (e *Engine) openRef(j *job, cal AnalogCal) (refSide, ptreport.Outcome, string, string) {
	var r refSide
	if e.Runner != nil {
		if run := e.Runner(); run != nil {
			r.b = run.Ref
		}
	}
	if r.b == nil {
		return r, ptreport.OutcomeError, ReasonNoRef, ""
	}
	// The registration says which reference channel is wired here (decision 135).
	if r.ch = e.wiring().RefFor(AnalogChannel(j.item)); r.ch == "" {
		return r, ptreport.OutcomeError, ReasonRefUnwired, AnalogChannel(j.item)
	}
	if e.RefAnalog == nil {
		return r, ptreport.OutcomeError, ReasonRefCal, "no calibration source"
	}
	c, err := e.RefAnalog(r.ch)
	if err != nil {
		return r, ptreport.OutcomeError, ReasonRefCal, err.Error()
	}
	if c.Current() != cal.Current() {
		return r, ptreport.OutcomeError, ReasonRefRange, AnalogChannel(j.item) + " / " + r.ch
	}
	r.cal = c
	return r, "", "", ""
}

// runAnalog walks one AI / AO item. A person types each point's reading (R&D);
// with the reference board as the peer its side gives the reference value,
// converted with its own k, b (decisions 129, 133).
func (e *Engine) runAnalog(ctx context.Context, j *job) (ptreport.Outcome, string, string) {
	ch := AnalogChannel(j.item)
	if e.Analog == nil {
		return ptreport.OutcomeError, ReasonNotCalibrated, "no calibration source"
	}
	cal, err := e.Analog(ch)
	if err != nil {
		return analogErr(err)
	}
	via := j.peer == ptitems.PeerRef
	var rs refSide
	if via {
		var o ptreport.Outcome
		var why, detail string
		if rs, o, why, detail = e.openRef(j, cal); o != "" {
			return o, why, detail
		}
	}
	pts := j.points
	if len(pts) == 0 {
		pts = DefaultPoints(j.item.Port, cal.floor(), cal.FS)
	}
	if !PointsOK(pts) {
		return ptreport.OutcomeError, ReasonAnalogPoints, ""
	}
	// An AO, the board's or the reference board's, cannot go under its range's minimum.
	for _, p := range pts {
		switch {
		case j.item.Port == "aout" && p < cal.floor():
			return belowMin(cal)
		case via && j.item.Port == "ain" && p < rs.cal.floor():
			return belowMin(rs.cal)
		}
	}
	res := AnalogResult{Channel: ch, Range: string(cal.Range), Unit: cal.Unit, FS: cal.FS, K: cal.K, B: cal.B}
	used := []ptreport.AnalogUsed{cal.used("dut", ch)}
	if via {
		res.RefCh, res.RefRange, res.RefK, res.RefB = rs.ch, string(rs.cal.Range), rs.cal.K, rs.cal.B
		used = append(used, rs.cal.used("ref", rs.ch))
	}
	e.change(j.item.ID, func(s *State) { s.Analog = cloneAnalog(res) })
	e.noteUsed(j, used...)

	n := strconv.Itoa(j.item.Channel)
	events, unsub := e.Board.Subscribe(256)
	defer unsub()
	var started []string
	stop := func() {
		if j.item.Port == "aout" {
			_, _ = e.Board.Send("pt.set aout mv="+n+":0", ptboard.ExpectOne, ptboard.DefaultTimeout)
		}
		for _, p := range started {
			_, _ = e.Board.Send("pt.stop "+p, ptboard.ExpectOne, ptboard.DefaultTimeout)
		}
	}
	defer stop()
	if via {
		ev, unsubRef := rs.b.Subscribe(256)
		defer unsubRef()
		rs.ev = ev
		p := e.wiring().Params
		start := fmt.Sprintf("pt.start ain ch=%s period=%d", rs.ch[2:], p.AIPeriod)
		if j.item.Port == "ain" {
			start = "pt.start aout ch=" + rs.ch[2:] + " mv=" + rs.ch[2:] + ":0 period=500"
		}
		att := ptreport.Attempt{StartedAt: time.Now(), Sent: []string{start}}
		lines, err := rs.b.Send(start, ptboard.ExpectOne, ptboard.DefaultTimeout)
		att.Raw = lines
		if err != nil {
			att.Outcome, att.Err = ptreport.OutcomeError, "reference board: "+err.Error()
			e.addAttempts(j, att)
			return ptreport.OutcomeError, "", att.Err
		}
		port := strings.Fields(start)[1]
		defer func() {
			if port == "aout" {
				_, _ = rs.b.Send("pt.set aout mv="+rs.ch[2:]+":0", ptboard.ExpectOne, ptboard.DefaultTimeout)
			}
			_, _ = rs.b.Send("pt.stop "+port, ptboard.ExpectOne, ptboard.DefaultTimeout)
		}()
	}
	starts := []string{"pt.start ain ch=" + n + " period=200"}
	if j.item.Port == "aout" {
		starts = []string{"pt.start aout ch=" + n + " mv=" + n + ":0 period=500"}
	}
	for _, cmd := range starts {
		att := ptreport.Attempt{StartedAt: time.Now(), Sent: []string{cmd}}
		lines, err := e.Board.Send(cmd, ptboard.ExpectOne, ptboard.DefaultTimeout)
		att.Raw = lines
		if err != nil {
			o, d := e.recordErr(j, att, err)
			return o, "", d
		}
		started = append(started, strings.Fields(cmd)[1])
	}
	if _, err := waitFrames(ctx, events, j.item.Port, 1, 8*time.Second); err != nil {
		return frameOutcome(ctx, err), "", err.Error()
	}

	judged, blank, failed := 0, 0, false
	for i, set := range pts {
		att := ptreport.Attempt{StartedAt: time.Now()}
		att.SetExtra("set", fmtNum(set))
		row := AnalogRow{Set: set}
		var shown, std float64
		have := false
		if j.item.Port == "aout" {
			mv := float64(aoMV(set, cal))
			row.Raw = &mv
			cmd := "pt.set aout mv=" + n + ":" + strconv.Itoa(aoMV(set, cal))
			att.Sent = append(att.Sent, cmd)
			lines, err := e.Board.Send(cmd, ptboard.ExpectOne, ptboard.DefaultTimeout)
			att.Raw = append(att.Raw, lines...)
			if err != nil {
				o, d := e.recordErr(j, att, err)
				return o, "", d
			}
			if _, err := waitFrames(ctx, events, "aout", 2, 5*time.Second); err != nil {
				return frameOutcome(ctx, err), "", err.Error()
			}
		}
		if via && j.item.Port == "ain" {
			mv := aoMV(set, rs.cal)
			cmd := "pt.set aout mv=" + rs.ch[2:] + ":" + strconv.Itoa(mv)
			att.Sent = append(att.Sent, cmd)
			lines, err := rs.b.Send(cmd, ptboard.ExpectOne, ptboard.DefaultTimeout)
			att.Raw = append(att.Raw, lines...)
			if err != nil {
				att.Outcome, att.Err = ptreport.OutcomeError, "reference board: "+err.Error()
				e.addAttempts(j, att)
				return ptreport.OutcomeError, "", att.Err
			}
			att.SetExtra("ref_mv", strconv.Itoa(mv))
			if _, err := waitFrames(ctx, rs.ev, "aout", 2, 5*time.Second); err != nil {
				return frameOutcome(ctx, err), "", "reference board: " + err.Error()
			}
		}
		switch {
		case via && j.item.Port == "aout":
			raw, err := averageRaw(ctx, rs.ev, rs.ch[2:], e.wiring().Params.AIFrames)
			if err != nil {
				return frameOutcome(ctx, err), "", "reference board: " + err.Error()
			}
			got := rs.cal.K*rs.cal.Nominal(raw) + rs.cal.B
			att.SetExtra("ref_raw", fmtNum(raw))
			att.SetExtra("value", fmtNum(got))
			row.Got, shown, std, have = &got, got, set, true
		case via:
			raw, err := averageRaw(ctx, events, n, aiFrames)
			if err != nil {
				return frameOutcome(ctx, err), "", err.Error()
			}
			v := set // what the reference AO drives, through its own k, b
			att.SetExtra("raw", fmtNum(raw))
			att.SetExtra("value", fmtNum(v))
			row.Raw, row.Got, have = &raw, &v, true
			shown, std = cal.K*cal.Nominal(raw)+cal.B, v
		default:
			kind := "ai"
			if j.item.Port == "aout" {
				kind = "ao"
			}
			a, err := e.Ask(ctx, Question{Item: j.item.ID, Kind: ptitems.AnswerNumber, Unit: cal.Unit,
				Point: i + 1, Of: len(pts), Set: set, Channel: ch, Walk: kind})
			if err != nil {
				att.Outcome = ptreport.OutcomeStopped
				e.addAttempts(j, att)
				return ptreport.OutcomeStopped, ReasonStopped, ""
			}
			typed := strings.TrimSpace(a.Value)
			att.SetExtra("value", typed)
			v, perr := strconv.ParseFloat(typed, 64)
			if typed == "" || perr != nil {
				blank++
				break // skipped: nothing to judge at this point
			}
			row.Got, have = &v, true
			if j.item.Port == "ain" {
				raw, err := averageRaw(ctx, events, n, aiFrames)
				if err != nil {
					return frameOutcome(ctx, err), "", err.Error()
				}
				att.SetExtra("raw", fmtNum(raw))
				row.Raw = &raw
				shown, std = cal.K*cal.Nominal(raw)+cal.B, v
			} else {
				shown, std = v, set
			}
		}
		if have {
			dev := (shown - std) / cal.FS * 100
			row.Shown, row.Dev = &shown, &dev
			att.SetExtra("dev_pct_fs", fmtNum(dev))
			checks, pass := ptcheck.EvalAll(j.steps[0].Checks, func(f string) (string, bool) {
				if f == "value" {
					return strconv.FormatFloat(dev, 'f', 4, 64), true
				}
				return "", false
			})
			row.Pass = &pass
			att.Checks = checks
			att.Outcome = ptreport.OutcomePass
			if !pass {
				att.Outcome, failed = ptreport.OutcomeFail, true
			}
			judged++
		} else {
			att.Outcome = ptreport.OutcomeSkipped
		}
		att.DurationMS = time.Since(att.StartedAt).Milliseconds()
		e.addAttempts(j, att)
		res.Rows = append(res.Rows, row)
		e.change(j.item.ID, func(s *State) { s.Analog = cloneAnalog(res) })
	}
	switch {
	case failed:
		return ptreport.OutcomeFail, "", ""
	case blank > 0 && e.RD:
		return ptreport.OutcomeStopped, ReasonStopped, "" // not measured to the end (decision 129)
	case judged == 0:
		return Recorded, "", ""
	}
	return ptreport.OutcomePass, "", ""
}

// aoMV is the DAC millivolts that make an AO drive v (mA or mV, the range's
// unit): nominal = (v - b) / k, then the range's formula backwards (decision 136).
func aoMV(v float64, cal AnalogCal) int {
	return int(math.Max(0, math.Round(cal.Range.AODACmV((v-cal.B)/cal.K))))
}

// noteUsed records the conversions an item used, once each, for its card and
// the report; a repeated run adds only what changed.
func (e *Engine) noteUsed(j *job, u ...ptreport.AnalogUsed) {
	add := func(list []ptreport.AnalogUsed) []ptreport.AnalogUsed {
		for _, x := range u {
			if !slices.Contains(list, x) {
				list = append(list, x)
			}
		}
		return list
	}
	e.mu.Lock()
	if r := e.results[j.item.ID]; r != nil {
		r.Analog = add(r.Analog)
	}
	e.mu.Unlock()
	e.change(j.item.ID, func(s *State) { s.Used = add(s.Used) })
}

// setLoad turns an AO load item's targets, each in its AO's registered unit,
// into both channels' DAC mV, each with its own k, b (decisions 106, 134,
// STG-38); a non-empty outcome ends the item.
func (e *Engine) setLoad(j *job) (ptreport.Outcome, string, string) {
	if e.Analog == nil {
		return ptreport.OutcomeError, ReasonNotCalibrated, "no calibration source"
	}
	var mv []string
	var used []ptreport.AnalogUsed
	for i, n := range []string{"1", "2"} {
		ma := loadTarget(j, i+1)
		cal, err := e.Analog("AO" + n)
		if err != nil {
			return analogErr(err)
		}
		if ma < cal.floor() && ma >= 0 {
			return belowMin(cal)
		}
		if ma < 0 || ma > cal.FS {
			return ptreport.OutcomeError, ReasonNoTarget, fmt.Sprintf("AO%s takes 0-%g %s", n, cal.FS, cal.Unit)
		}
		mv = append(mv, n+":"+strconv.Itoa(aoMV(ma, cal)))
		used = append(used, cal.used("dut", "AO"+n))
	}
	e.noteUsed(j, used...)
	p := map[string]string{"mv": strings.Join(mv, ",")}
	for k, v := range j.params {
		if k != "mv" {
			p[k] = v
		}
	}
	j.params = p
	return "", "", ""
}

// analogErr is how a failed channel lookup ends an item.
func analogErr(err error) (ptreport.Outcome, string, string) {
	return ptreport.OutcomeError, ReasonNotCalibrated, err.Error()
}

func cloneAnalog(r AnalogResult) *AnalogResult {
	r.Rows = append([]AnalogRow(nil), r.Rows...)
	return &r
}

func fmtNum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

var errBoardGone = errors.New("the board stopped answering")

// ErrAOVoltUncal: a voltage AO has no nominal stage to drive by, only its
// calibration (decision 134).
var ErrAOVoltUncal = errors.New("a voltage AO needs its calibration to output")

// BelowMinError: a live AO target under what its range can output (decision 136).
type BelowMinError struct {
	Min  float64
	Unit string
}

func (e BelowMinError) Error() string {
	return fmt.Sprintf("below the range's minimum %g %s", e.Min, e.Unit)
}

// ErrAONoRange: the AO's registered range could not be read, so its unit is unknown.
var ErrAONoRange = errors.New("the AO's range is unknown")

func frameOutcome(ctx context.Context, err error) ptreport.Outcome {
	switch {
	case ctx.Err() != nil:
		return ptreport.OutcomeStopped
	case errors.Is(err, errBoardGone):
		return ptreport.OutcomeError
	}
	return ptreport.OutcomeTimeout
}

// waitFrames waits for n frames of port that arrive from now on: what queued
// up before (while a person was typing, before an output changed) is dropped.
func waitFrames(ctx context.Context, events <-chan ptboard.Event, port string, n int, d time.Duration) ([]ptproto.Frame, error) {
	for drained := false; !drained; {
		select {
		case _, ok := <-events:
			if !ok {
				return nil, errBoardGone
			}
		default:
			drained = true
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	var out []ptproto.Frame
	for len(out) < n {
		select {
		case ev, ok := <-events:
			if !ok {
				return out, errBoardGone
			}
			if ev.Kind == ptproto.LineFrame && ev.Frame.Port == port {
				out = append(out, ev.Frame)
			}
		case <-timer.C:
			return out, fmt.Errorf("no frame from %s within %s", port, d)
		case <-ctx.Done():
			return out, ctx.Err()
		}
	}
	return out, nil
}

// averageRaw is the mean raw reading of AI channel ch over n fresh frames;
// the field is "raw/mV/..." (PORTTOOL-FLOW.md A.5 "!ain").
func averageRaw(ctx context.Context, events <-chan ptboard.Event, ch string, n int) (float64, error) {
	frames, err := waitFrames(ctx, events, "ain", n, time.Duration(n)*2*time.Second)
	if err != nil {
		return 0, err
	}
	sum, got := 0.0, 0
	for _, f := range frames {
		v, _ := ptproto.Get(f.Fields, "ch"+ch)
		if raw, err := strconv.ParseFloat(strings.SplitN(v, "/", 2)[0], 64); err == nil && strings.Contains(v, "/") {
			sum += raw
			got++
		}
	}
	if got == 0 {
		return 0, fmt.Errorf("no ch%s reading in %d ain frames", ch, len(frames))
	}
	return sum / float64(got), nil
}
