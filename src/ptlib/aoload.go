package ptlib

// AO under load is judged like AO output: each channel's reading against its
// own target (out1 / out2, STG-38), +-1 % FS by the plan's ao<n>-out step. A person types the
// meter; the reference board's AI1 / AI2 read AO1 / AO2. With a duration the
// current holds to the end and is read once before it goes off.
// PORTTOOL-FLOW.md C.3.6 「AO 带负载输出」; decision 128.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"PortTool/src/ptboard"
	"PortTool/src/ptcheck"
	"PortTool/src/ptitems"
	"PortTool/src/ptplan"
	"PortTool/src/ptreport"
)

// fillLoad readies an AO load job: its two criteria steps, one per channel.
func fillLoad(j *job, plan ptplan.Plan) {
	for _, id := range j.item.Criteria {
		for _, st := range plan.Steps {
			if st.ID == id {
				j.loadSteps = append(j.loadSteps, st)
			}
		}
	}
	if len(j.loadSteps) != 2 {
		j.fail, j.detail = ReasonNoCriteria, strings.Join(j.item.Criteria, ",")
	}
	if j.peer != ptitems.PeerHuman && !(j.peer == ptitems.PeerRef && hasPeer(j.item, ptitems.PeerRef)) {
		j.fail = ReasonAnalogPeer
	}
	// Frames are not judged; the readings are (measureLoad).
	j.load, j.record = true, true
}

// TargetMissing says a pick lacks an AO load target: both are set before the
// run, never defaulted, in every test type (decisions 129, 134).
func TargetMissing(it ptitems.Item, params map[string]string) bool {
	return it.Load && (strings.TrimSpace(params[ptitems.ParamOut1]) == "" || strings.TrimSpace(params[ptitems.ParamOut2]) == "")
}

// loadTarget is AO channel n's target, in its registered unit.
func loadTarget(j *job, n int) float64 {
	k := ptitems.ParamOut1
	if n == 2 {
		k = ptitems.ParamOut2
	}
	v, _ := strconv.ParseFloat(strings.TrimSpace(j.params[k]), 64)
	return v
}

// runLoad switches the current on, reads both channels, switches it off.
func (e *Engine) runLoad(ctx context.Context, j *job) (ptreport.Outcome, string, string) {
	if o, r, d := e.setLoad(j); o != "" {
		return o, r, d
	}
	events, unsub := e.Board.Subscribe(256)
	defer unsub()
	cmd := "pt.start aout ch=1,2 mv=" + j.params["mv"] + " period=500"
	att := ptreport.Attempt{StartedAt: time.Now(), Sent: []string{cmd}}
	lines, err := e.Board.Send(cmd, ptboard.ExpectOne, ptboard.DefaultTimeout)
	att.Raw = lines
	if err != nil {
		o, d := e.recordErr(j, att, err)
		return o, "", d
	}
	defer func() {
		_, _ = e.Board.Send("pt.set aout mv=1:0,2:0", ptboard.ExpectOne, ptboard.DefaultTimeout)
		_, _ = e.Board.Send("pt.stop aout", ptboard.ExpectOne, ptboard.DefaultTimeout)
	}()
	if _, err := waitFrames(ctx, events, "aout", 2, 8*time.Second); err != nil {
		return frameOutcome(ctx, err), "", err.Error()
	}
	return e.measureLoad(ctx, j)
}

// measureLoad reads both AO channels while the load current is on.
func (e *Engine) measureLoad(ctx context.Context, j *job) (ptreport.Outcome, string, string) {
	if e.Analog == nil {
		return ptreport.OutcomeError, ReasonNotCalibrated, "no calibration source"
	}
	var ref *ptboard.Board
	var refEv <-chan ptboard.Event
	if j.peer == ptitems.PeerRef {
		if e.Runner != nil {
			if run := e.Runner(); run != nil {
				ref = run.Ref
			}
		}
		if ref == nil {
			return ptreport.OutcomeError, ReasonNoRef, ""
		}
		if e.RefAnalog == nil {
			return ptreport.OutcomeError, ReasonRefCal, "no calibration source"
		}
		ev, unsub := ref.Subscribe(256)
		defer unsub()
		refEv = ev
		// Each AO's reference AI as the registration wires it (decision 135); an
		// AO wired to none is that channel's result only (decision 129).
		var refAI []string
		for n := 1; n <= 2; n++ {
			if r := e.wiring().RefFor("AO" + strconv.Itoa(n)); r != "" {
				refAI = append(refAI, r[2:])
			}
		}
		if len(refAI) == 0 {
			return ptreport.OutcomeError, ReasonRefUnwired, "AO1, AO2"
		}
		start := fmt.Sprintf("pt.start ain ch=%s period=%d", strings.Join(refAI, ","), e.wiring().Params.AIPeriod)
		if _, err := ref.Send(start, ptboard.ExpectOne, ptboard.DefaultTimeout); err != nil {
			return ptreport.OutcomeError, "", "reference board: " + err.Error()
		}
		defer func() { _, _ = ref.Send("pt.stop ain", ptboard.ExpectOne, ptboard.DefaultTimeout) }()
	}

	var all []ptcheck.Result
	var vals [][2]string
	judged, failed := 0, false
	for n := 1; n <= 2; n++ {
		ch := "AO" + strconv.Itoa(n)
		set := loadTarget(j, n)
		cal, err := e.Analog(ch)
		if err != nil {
			return analogErr(err)
		}
		att := ptreport.Attempt{StartedAt: time.Now()}
		att.SetExtra("set", fmtNum(set))
		att.SetExtra("channel", ch)
		var got float64
		have := false
		if ref != nil {
			refCh := e.wiring().RefFor(ch)
			if refCh == "" {
				e.setChan(j, n, ptreport.OutcomeError, ReasonRefUnwired)
				continue
			}
			rc, err := e.RefAnalog(refCh)
			if err != nil {
				e.setChan(j, n, ptreport.OutcomeError, ReasonRefCal)
				continue
			}
			// Only this channel is not tested; the others run (STG-40 补充).
			if cal.Calibrated != "" && cal.Calibrated != cal.Range {
				e.setChanDetail(j, n, ptreport.OutcomeError, ReasonDUTCalRange, string(cal.Calibrated))
				continue
			}
			if rc.Current() != cal.Current() {
				e.setChan(j, n, ptreport.OutcomeError, ReasonRefRange)
				continue
			}
			raw, err := averageRaw(ctx, refEv, refCh[2:], e.wiring().Params.AIFrames)
			if err != nil {
				return frameOutcome(ctx, err), "", "reference board: " + err.Error()
			}
			got, have = rc.K*rc.Nominal(raw)+rc.B, true
			e.noteUsed(j, rc.used("ref", refCh))
			att.SetExtra("ref_raw", fmtNum(raw))
		} else {
			a, err := e.Ask(ctx, Question{Item: j.item.ID, Kind: ptitems.AnswerNumber, Unit: cal.Unit,
				Point: n, Of: 2, Set: set, Channel: ch, Walk: "ao"})
			if err != nil {
				att.Outcome = ptreport.OutcomeStopped
				e.addAttempts(j, att)
				return ptreport.OutcomeStopped, ReasonStopped, ""
			}
			if v, perr := strconv.ParseFloat(strings.TrimSpace(a.Value), 64); perr == nil {
				got, have = v, true
			}
		}
		if have {
			dev := (got - set) / cal.FS * 100
			att.SetExtra("value", fmtNum(got))
			att.SetExtra("dev_pct_fs", fmtNum(dev))
			checks, pass := ptcheck.EvalAll(j.loadSteps[n-1].Checks, func(f string) (string, bool) {
				if f == "value" {
					return strconv.FormatFloat(dev, 'f', 4, 64), true
				}
				return "", false
			})
			att.Checks = checks
			all = append(all, checks...)
			vals = append(vals, [2]string{ch, fmtNum(got) + " " + cal.Unit})
			att.Outcome = ptreport.OutcomePass
			if !pass {
				att.Outcome, failed = ptreport.OutcomeFail, true
			}
			e.setChan(j, n, att.Outcome, "")
			judged++
		} else {
			att.Outcome = ptreport.OutcomeSkipped
			if e.RD {
				e.setChan(j, n, ptreport.OutcomeStopped, ReasonStopped)
			} else {
				e.setChan(j, n, ptreport.OutcomeError, ReasonNoReading)
			}
		}
		att.DurationMS = time.Since(att.StartedAt).Milliseconds()
		e.addAttempts(j, att)
		e.change(j.item.ID, func(s *State) { s.Checks, s.Values = all, vals })
	}
	switch {
	case failed:
		return ptreport.OutcomeFail, "", ""
	case judged < 2 && e.RD:
		return ptreport.OutcomeStopped, ReasonStopped, "" // not measured to the end (decision 129)
	case judged < 2:
		// A reading left out is not a pass: nothing judged that channel.
		return ptreport.OutcomeError, ReasonNoReading, ""
	}
	return ptreport.OutcomePass, "", ""
}
