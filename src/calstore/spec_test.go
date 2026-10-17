package calstore

import (
	"math"
	"testing"
)

// Decision 136: each range has its own formula, the AO ones invert, and the
// table the page shows carries the same numbers the executor converts with.
func TestRangeFormulas(t *testing.T) {
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-6 }
	pin := 1000.0 * ADCMax / VrefMV // raw count for 1000 mV on the pin
	if v := RangeVoltage.AINominal(pin); !near(v, 1000*90.6/22.6) {
		t.Errorf("AI voltage: %v", v)
	}
	if v := RangeCurrent.AINominal(pin); !near(v, 1000/124.0) {
		t.Errorf("AI current: %v", v)
	}
	if mv := RangeAO020.AODACmV(20); !near(mv, 2048) {
		t.Errorf("AO 0-20 mA at 20 mA: %v mV, want 2048", mv)
	}
	if mv := RangeVoltage.AODACmV(1500); !near(mv, 1500) {
		t.Errorf("AO voltage: %v", mv)
	}
	if ma := RangeAO420.AONominal(0); math.Abs(ma-4.86) > 0.01 {
		t.Errorf("AO 4-20 mA at code 0: %v mA, want about 4.86", ma)
	}
	for _, r := range []Range{RangeVoltage, RangeAO020, RangeAO420} {
		for _, v := range []float64{5, 12, 20} {
			if back := r.AONominal(r.AODACmV(v)); !near(back, v) {
				t.Errorf("%s: %v -> %v", r, v, back)
			}
		}
	}
	for _, ch := range []string{"AI1", "AI2", "AO1", "AO2"} {
		specs := SpecsFor(ch)
		if len(specs) != len(RangeOptions(ch)) {
			t.Fatalf("%s: %d specs for %d ranges", ch, len(specs), len(RangeOptions(ch)))
		}
		def := 0
		for _, s := range specs {
			if s.Default {
				def++
			}
			if len(s.Jumpers) != 2 || len(s.Formula) < 2 || s.Note.Key == "" || s.FS <= 0 {
				t.Errorf("%s %s: incomplete spec %+v", ch, s.Range, s)
			}
			x := 7.0
			if ch[:2] == "AI" && !near(x*s.Gain+s.Offset, s.Range.AINominal(x)) {
				t.Errorf("%s %s: page formula differs from AINominal", ch, s.Range)
			}
			if ch[:2] == "AO" && !near(x*s.Gain+s.Offset, s.Range.AODACmV(x)) {
				t.Errorf("%s %s: page formula differs from AODACmV", ch, s.Range)
			}
		}
		if def != 1 {
			t.Errorf("%s: %d default ranges", ch, def)
		}
	}
	if s, _ := SpecOf("AO1", RangeVoltage); s.Warn == nil || s.Jumpers[0].Name != "JP1" || !s.Jumpers[0].Closed {
		t.Errorf("AO1 voltage: %+v", s)
	}
	if s, _ := SpecOf("AO2", RangeAO420); s.Warn == nil || s.Jumpers[1].Name != "JP4" || !s.Jumpers[1].Closed {
		t.Errorf("AO2 4-20 mA: %+v", s)
	}
	if s, _ := SpecOf("AI2", RangeCurrent); s.Jumpers[0].Name != "JP8" || s.Jumpers[1].Name != "JP6" {
		t.Errorf("AI2 current: %+v", s)
	}
	// The lowest settable value: the range's low end, or a 4-20 mA AO's floor at code 0.
	if RangeAO420.Min() != 4.86 || RangeAO020.Min() != 0 || RangeVoltage.Min() != 0 {
		t.Errorf("minimums: %v %v %v", RangeAO420.Min(), RangeAO020.Min(), RangeVoltage.Min())
	}
	if s, _ := SpecOf("AO1", RangeAO420); s.Min != 4.86 {
		t.Errorf("AO1 4-20 mA spec minimum %v", s.Min)
	}
	if _, ok := SpecOf("AI1", RangeAO420); ok {
		t.Error("AI has no 4-20 mA range")
	}
}
