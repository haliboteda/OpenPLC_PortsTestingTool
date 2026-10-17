package ptpanel

import (
	"testing"

	"PortTool/src/calstore"
	"PortTool/src/ptlib"
	"PortTool/src/refboard"
)

func impactOf(rows []impactRow, item, ch string) impactRow {
	for _, r := range rows {
		if r.Item == item && r.Ch == ch {
			return r
		}
	}
	return impactRow{}
}

// Decision 135: what the fixture tests can test follows the registration,
// channel by channel, with the reason when they cannot.
func TestRefImpactFollowsTheWiring(t *testing.T) {
	w := refboard.Default()
	w.Analog["AO1"], w.Analog["AO2"] = "AI2", "" // AI1 under test left without a reference AO
	dut := calstore.Ranges{"AI1": calstore.RangeVoltage, "AI2": calstore.RangeCurrent, "AO1": calstore.RangeAO020, "AO2": calstore.RangeAO020}
	ref := calstore.Ranges{"AI1": calstore.RangeCurrent, "AI2": calstore.RangeCurrent, "AO1": calstore.RangeAO020, "AO2": calstore.RangeVoltage}
	rows := refImpact(w, dut, ref, refCalInfo{}, nil)
	if r := impactOf(rows, "ain.read2", "AI2"); !r.OK || r.Ref != "AO1" {
		t.Errorf("AI2 <- AO1 on current both ends: %+v", r)
	}
	if r := impactOf(rows, "ain.read1", "AI1"); r.OK || r.Why == nil || r.Why.Key != "refboard.why.unwired" {
		t.Errorf("AI1 has no reference AO: %+v", r)
	}
	if r := impactOf(rows, "relay.cycle", "RY3"); !r.OK || r.Ref != "DI3" {
		t.Errorf("RY3 on DO3 read on DI3: %+v", r)
	}

	w.Analog["AO2"] = "AI1" // the reference AO2 is on voltage, AI1 under test too
	w.DO[3] = refboard.Link{Via: 0, To: 3}
	w.DO[5] = refboard.Link{Via: 5, To: 0}
	rows = refImpact(w, dut, ref, refCalInfo{}, nil)
	if r := impactOf(rows, "ain.read1", "AI1"); !r.OK || r.Ref != "AO2" {
		t.Errorf("AI1 <- AO2 on voltage both ends: %+v", r)
	}
	if r := impactOf(rows, "relay.cycle", "RY3"); r.OK || r.Why.Key != "refboard.why.relay_unchained" {
		t.Errorf("RY3 on no DO: %+v", r)
	}
	if r := impactOf(rows, "dout.load", "DO5"); r.OK || r.Why.Key != "refboard.why.unwired" {
		t.Errorf("DO5 wired to nothing: %+v", r)
	}
	if r := impactOf(rows, "relay.cycle", "RY5"); r.OK {
		t.Errorf("RY5 on DO5, which reaches no DI: %+v", r)
	}

	ref["AI1"] = calstore.RangeVoltage // reads AO1 under test, which is current
	rows = refImpact(w, dut, ref, refCalInfo{known: true, have: map[string]bool{"AI1": true, "AO1": true}}, nil)
	if r := impactOf(rows, "aout.out1", "AO1"); r.OK || r.Why.Key != "go.full.range_mismatch" {
		t.Errorf("AO1 current read on a voltage AI: %+v", r)
	}
	if r := impactOf(rows, "aout.out2", "AO2"); r.OK || r.Why.Key != "refboard.why.no_cal" {
		t.Errorf("the reference AI2 has no calibration: %+v", r)
	}
}

// STG-40 row 11: a channel of the board under test calibrated on another
// range than the configurations' cannot be tested, in the card's own words.
func TestRefImpactRefusesAnotherCalibrationRange(t *testing.T) {
	w := refboard.Default()
	dut := calstore.Ranges{"AI1": calstore.RangeVoltage, "AI2": calstore.RangeCurrent, "AO1": calstore.RangeAO020, "AO2": calstore.RangeAO020}
	ref := calstore.Ranges{"AI1": calstore.RangeCurrent, "AI2": calstore.RangeCurrent, "AO1": calstore.RangeVoltage, "AO2": calstore.RangeCurrent}
	cal := calstore.Ranges{"AI1": calstore.RangeVoltage, "AO1": calstore.RangeAO420}
	rows := refImpact(w, dut, ref, refCalInfo{}, cal)
	if r := impactOf(rows, "ain.read1", "AI1"); !r.OK {
		t.Errorf("AI1 calibrated on the range it is tested on: %+v", r)
	}
	r := impactOf(rows, "aout.out1", "AO1")
	if r.OK || r.Why == nil || r.Why.Key != "rng.kb_other_range" {
		t.Fatalf("AO1 calibrated on 4-20 mA, tested on 0-20 mA: %+v", r)
	}
	if n, _ := r.Why.Args["r"].(msg); n.Key != "ana.range.i420" {
		t.Errorf("the reason names the range it was calibrated on: %+v", r.Why.Args)
	}
	if r := impactOf(rows, "aout.out2", "AO2"); !r.OK {
		t.Errorf("AO2 has no archive entry: not this check's to refuse: %+v", r)
	}
	// The settings page (dut nil) does not ask.
	if r := impactOf(refImpact(w, nil, ref, refCalInfo{}, cal), "aout.out1", "AO1"); !r.OK {
		t.Errorf("the settings page leaves the board under test's ranges out: %+v", r)
	}
}

// STG-40 补充: a run's channel calibrated on another range is worded as the
// configuration card's 「校准时是……」 mark.
func TestChannelCalibratedOnAnotherRangeIsTheCardsSentence(t *testing.T) {
	code := ptlib.ReasonDUTCalRange
	why := reasonMsg(reasonText[code], code, string(calstore.RangeAO420))
	if n, _ := why.Args["r"].(msg); why.Key != "rng.kb_other_range" || n.Key != "ana.range.i420" {
		t.Fatalf("%+v", why)
	}
}

// Decision 135: the CAN exchange cannot be tested when its rate is not the
// reference board's registered one.
func TestCANRowComparesTheTwoRates(t *testing.T) {
	w := refboard.Default()
	if r := canRow(w, 2, "500000"); !r.OK {
		t.Errorf("both 500000: %+v", r)
	}
	if r := canRow(w, 2, "250000"); r.OK || r.Why == nil || r.Why.Key != "refboard.why.can_baud" {
		t.Errorf("250000 against 500000: %+v", r)
	}
}
