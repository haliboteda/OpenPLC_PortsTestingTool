package testcase

// AI2 read and AO output with the reference board as the peer (STG-31 A,
// PORTTOOL-FLOW.md C.3.2 "读数 / 输出项"): the reference side gives the
// reference value through its own k, b, nobody is asked.
//
// Bench: two simulated boards; simboard plays the default same-number wires
// (reference AOn -> AIn under test, AOn under test -> reference AIn) from each !aout
// frame's computed current. Calibration sources are identity fakes. Neither
// sees a real XTR111 current or a real AI front end.

import (
	"PortTool/src/calstore"
	"context"
	"errors"
	"math"
	"testing"

	"PortTool/src/ptboard"
	"PortTool/src/ptitems"
	"PortTool/src/ptlib"
	"PortTool/src/ptplan"
	"PortTool/src/ptreport"
	"PortTool/src/ptseq"
	"PortTool/src/refboard"
)

const refAnalogPlanJSON = `{"schema":1,"name":"ref-analog","limit_version":"t","steps":[
 {"id":"ai2-read","type":"UserReading","enabled":false,"prompt":"p","unit":"% FS",
  "checks":[{"field":"value","op":"range","min":-1,"max":1,"unit":"% FS"}]},
 {"id":"ao1-out","type":"UserReading","enabled":false,"prompt":"p","unit":"% FS",
  "checks":[{"field":"value","op":"range","min":-1,"max":1,"unit":"% FS"}]},
 {"id":"ao2-out","type":"UserReading","enabled":false,"prompt":"p","unit":"% FS",
  "checks":[{"field":"value","op":"range","min":-1,"max":1,"unit":"% FS"}]}]}`

// identity is k = 1, b = 0 on 0-20 mA; an AI reads current through 124 R.
func identity(current bool) ptlib.AnalogSource {
	return func(ch string) (ptlib.AnalogCal, error) {
		c := ptlib.AnalogCal{K: 1, Unit: "mA", FS: 20, Range: calstore.RangeAO020}
		if ch[:2] == "AI" {
			if !current {
				c = ptlib.AnalogCal{K: 1, Unit: "mV", FS: 10000, Range: calstore.RangeVoltage}
				c.Nominal = func(raw float64) float64 { return raw * 2500 / 65535 * 90.6 / 22.6 }
			} else {
				c.Nominal = func(raw float64) float64 { return raw * 2500 / 65535 / 124 }
			}
		}
		return c, nil
	}
}

func refAnalogEngine(t *testing.T, dut, ref *ptboard.Board, refSrc ptlib.AnalogSource, asked *int) *ptlib.Engine {
	t.Helper()
	plan, err := ptplan.Parse([]byte(refAnalogPlanJSON))
	if err != nil {
		t.Fatal(err)
	}
	return &ptlib.Engine{Board: dut, Plan: plan, Hold: &fakeHold{b: dut},
		Items:     []ptitems.Item{catalogueItem(t, "ain.read2"), catalogueItem(t, "aout.out1"), catalogueItem(t, "aout.load")},
		Analog:    identity(true),
		RefAnalog: refSrc,
		Runner:    func() *ptseq.Runner { return &ptseq.Runner{Board: dut, Ref: ref} },
		Ask: func(ctx context.Context, q ptlib.Question) (ptlib.Answer, error) {
			*asked++
			return ptlib.Answer{}, ptlib.ErrStopped
		},
	}
}

func TestReferenceBoardFillsTheReferenceValueOnTwoWiredSimulatedBoards(t *testing.T) {
	dut, ref := openSimPair(t)
	for _, id := range []string{"aout.out1", "ain.read2"} {
		t.Run(id, func(t *testing.T) {
			asked := 0
			e := refAnalogEngine(t, dut, ref, identity(true), &asked)
			_, st := runOne(t, e, ptlib.Pick{ID: id, On: true, Peer: ptitems.PeerRef})
			if st.Outcome != ptreport.OutcomePass {
				t.Fatalf("%s is %s (%s %s), want PASS", id, st.Outcome, st.Reason, st.Detail)
			}
			if asked != 0 {
				t.Errorf("a person was asked %d times; the reference board gives the value", asked)
			}
			if st.Analog == nil || len(st.Analog.Rows) < 3 {
				t.Fatalf("rows %+v, want the default points", st.Analog)
			}
			for _, r := range st.Analog.Rows {
				if r.Got == nil || r.Raw == nil || r.Dev == nil || math.Abs(*r.Got-r.Set) > 0.2 {
					t.Errorf("point %v: reference %v raw %v dev %v, want a reference within 0.2 mA of the target",
						r.Set, r.Got, r.Raw, r.Dev)
				}
			}
		})
	}
}

// AO under load with the reference board: its AI1 / AI2 read AO1 / AO2 at the
// set current, filled in and judged, nobody asked (decision 128).
func TestReferenceBoardReadsTheAOLoad(t *testing.T) {
	dut, ref := openSimPair(t)
	asked := 0
	e := refAnalogEngine(t, dut, ref, identity(true), &asked)
	_, st := runOne(t, e, ptlib.Pick{ID: "aout.load", On: true, Peer: ptitems.PeerRef, Params: map[string]string{ptitems.ParamOut1: "20", ptitems.ParamOut2: "20"}})
	if st.Outcome != ptreport.OutcomePass || asked != 0 || len(st.Checks) != 2 {
		t.Fatalf("aout.load is %s (%s %s), asked %d, checks %+v", st.Outcome, st.Reason, st.Detail, asked, st.Checks)
	}
}

// Decision 129 「多路」: with AO2 wired to no reference AI, AO1 is still read
// and judged, AO2 says it is not wired (decision 135).
func TestAOLoadWithOneChannelUnwired(t *testing.T) {
	dut, ref := openSimPair(t)
	asked := 0
	e := refAnalogEngine(t, dut, ref, identity(true), &asked)
	w := refboard.Default()
	w.Analog["AI2"] = ""
	e.Wiring = func() refboard.Wiring { return w }
	_, st := runOne(t, e, ptlib.Pick{ID: "aout.load", On: true, Peer: ptitems.PeerRef, Params: map[string]string{ptitems.ParamOut1: "20", ptitems.ParamOut2: "20"}})
	got := map[string]string{}
	for _, c := range st.Chans {
		got[c.Name] = string(c.Outcome) + "/" + c.Reason
	}
	if got["AO1"] != "PASS/" || got["AO2"] != "ERROR/"+ptlib.ReasonRefUnwired || len(st.Checks) != 1 {
		t.Fatalf("channels %v, checks %d; want AO1 PASS and AO2 not wired", got, len(st.Checks))
	}
}

// STG-40 补充: AO2 calibrated on another range than set is that channel's
// error, named with the range it was calibrated on; AO1 is still judged.
func TestAOLoadWithOneChannelCalibratedOnAnotherRange(t *testing.T) {
	dut, ref := openSimPair(t)
	asked := 0
	e := refAnalogEngine(t, dut, ref, identity(true), &asked)
	src := e.Analog
	e.Analog = func(ch string) (ptlib.AnalogCal, error) {
		c, err := src(ch)
		c.Calibrated = c.Range
		if ch == "AO2" {
			c.Calibrated = calstore.RangeAO420
		}
		return c, err
	}
	_, st := runOne(t, e, ptlib.Pick{ID: "aout.load", On: true, Peer: ptitems.PeerRef, Params: map[string]string{ptitems.ParamOut1: "20", ptitems.ParamOut2: "20"}})
	got := map[string]string{}
	for _, c := range st.Chans {
		got[c.Name] = string(c.Outcome) + "/" + c.Reason + "/" + c.Detail
	}
	if got["AO1"] != "PASS//" || got["AO2"] != "ERROR/"+ptlib.ReasonDUTCalRange+"/"+string(calstore.RangeAO420) || len(st.Checks) != 1 {
		t.Fatalf("channels %v, checks %d; want AO1 PASS and AO2 calibrated on another range", got, len(st.Checks))
	}
}

func TestReferenceBoardMissingOrUnfitIsSaidNotJudged(t *testing.T) {
	dut, ref := openSimPair(t)
	cases := []struct {
		name string
		ref  *ptboard.Board
		src  ptlib.AnalogSource
		id   string
		why  string
	}{
		{"not connected", nil, identity(true), "aout.out1", ptlib.ReasonNoRef},
		{"not calibrated", ref, func(string) (ptlib.AnalogCal, error) { return ptlib.AnalogCal{}, errors.New("blank") },
			"ain.read2", ptlib.ReasonRefCal},
		{"reference AI on voltage", ref, identity(false), "aout.out1", ptlib.ReasonRefRange},
		// Decision 134: the two ends of a wire must both be current or both voltage.
		{"reference AO on voltage", ref, func(ch string) (ptlib.AnalogCal, error) {
			if ch[:2] == "AO" {
				return ptlib.AnalogCal{K: 1, Unit: "mV", FS: 10000, Range: calstore.RangeVoltage}, nil
			}
			return identity(true)(ch)
		}, "ain.read2", ptlib.ReasonRefRange},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			asked := 0
			e := refAnalogEngine(t, dut, c.ref, c.src, &asked)
			_, st := runOne(t, e, ptlib.Pick{ID: c.id, On: true, Peer: ptitems.PeerRef})
			if st.Outcome != ptreport.OutcomeError || st.Reason != c.why {
				t.Fatalf("%s: %s / %q, want ERROR / %q", c.id, st.Outcome, st.Reason, c.why)
			}
		})
	}
}
