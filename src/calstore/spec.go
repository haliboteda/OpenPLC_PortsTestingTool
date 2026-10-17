package calstore

// The range table (decision 136): each range's jumpers, nominal formula and
// full scale, the one copy the panel shows and the executor converts with.
// Jumpers and component values follow HARDWARE-FACTS.md 「模拟输入前端」 and
// 「模拟输出 AOUT1 / AOUT2」; change them there first.

import "math"

// Component values in the nominal formulas.
const (
	VrefMV  = 2500.0 // VREFBUF: ADC and DAC full scale
	ADCMax  = 65535.0
	DACMax  = 4095.0
	divTop  = 68.0   // kΩ, upper leg of the AI voltage divider
	divLow  = 22.6   // kΩ, lower leg
	limR    = 374.0  // Ω, AI current-range series resistor; not in the formula
	shuntR  = 124.0  // Ω, AI current sense
	setR    = 1024.0 // Ω, XTR111 R_SET
	xtrGain = 10.0   // XTR111: I = 10 × VIN / R_SET
	biasR   = 40.2   // kΩ, VREF -> VIN with the 4-20 mA jumper closed
	inR     = 10.0   // kΩ, DAC -> VIN
)

// AINominal is an AI raw count on this range in its unit, before k, b.
func (r Range) AINominal(raw float64) float64 {
	pin := raw * VrefMV / ADCMax
	if r == RangeVoltage {
		return pin * (divTop + divLow) / divLow
	}
	return pin / shuntR
}

// AODACmV is the DAC millivolts that put out nominal v (the range's unit).
func (r Range) AODACmV(v float64) float64 {
	switch r {
	case RangeVoltage:
		return v // the terminal is the DAC voltage on the current circuit
	case RangeAO420:
		vin := v * setR / xtrGain
		return (vin*(biasR+inR) - inR*VrefMV) / biasR
	}
	return v * setR / xtrGain
}

// AONominal is the inverse of AODACmV.
func (r Range) AONominal(mv float64) float64 {
	switch r {
	case RangeVoltage:
		return mv
	case RangeAO420:
		vin := (biasR*mv + inR*VrefMV) / (biasR + inR)
		return xtrGain * vin / setR
	}
	return xtrGain * mv / setR
}

// Min is the lowest value a point or target on this range may be: its low
// end, or what an AO puts out at code 0 when that is higher (a 4-20 mA AO
// gives about 4.86 mA), to 0.01 as the page shows it (decision 136).
func (r Range) Min() float64 {
	lo, _ := r.Scale()
	return math.Max(lo, math.Round(r.AONominal(0)*100)/100)
}

// Line is one line as the page shows it: a strings.json key and its args, in
// the panel's message shape, so the page words it in its language.
type Line struct {
	Key  string         `json:"$t"`
	Args map[string]any `json:"args,omitempty"`
}

// Jumper is one solder jumper and its state on a range.
type Jumper struct {
	Name   string `json:"name"`
	State  Line   `json:"state"`
	Closed bool   `json:"closed"`
}

// Spec is one channel on one range. The formula is linear: AI nominal =
// raw × Gain + Offset; AO DAC mV = nominal × Gain + Offset.
type Spec struct {
	Range   Range    `json:"id"`
	Default bool     `json:"default"`
	Unit    string   `json:"unit"`
	Lo      float64  `json:"lo"`
	Min     float64  `json:"min"`
	FS      float64  `json:"fs"`
	Gain    float64  `json:"gain"`
	Offset  float64  `json:"offset"`
	Jumpers []Jumper `json:"jumpers"`
	Formula []Line   `json:"formula"`
	Note    Line     `json:"note"`
	Warn    *Line    `json:"warn,omitempty"`
}

// parts are each channel's jumpers and resistors (HARDWARE-FACTS.md).
var parts = map[string]struct {
	jpA, jpB string // AI: terminal side, MCU side; AO: voltage select, 4-20 mA bias
	r1, r2   string // AI: divider top, bottom; AO: R_SET, DAC -> VIN
	r3, r4   string // AI: series, shunt; AO: VREF -> VIN
}{
	"AI1": {jpA: "JP9", jpB: "JP5", r1: "R19", r2: "R20", r3: "R23", r4: "R24"},
	"AI2": {jpA: "JP8", jpB: "JP6", r1: "R21", r2: "R22", r3: "R25", r4: "R26"},
	"AO1": {jpA: "JP1", jpB: "JP3", r1: "R9", r2: "R13", r3: "R11"},
	"AO2": {jpA: "JP2", jpB: "JP4", r1: "R10", r2: "R16", r3: "R14"},
}

func ln(key string, kv ...any) Line {
	l := Line{Key: key}
	if len(kv) > 0 {
		l.Args = map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			l.Args[kv[i].(string)] = kv[i+1]
		}
	}
	return l
}

func jp(name string, closed bool, key string, kv ...any) Jumper {
	return Jumper{Name: name, Closed: closed, State: ln(key, kv...)}
}

// SpecOf is channel ("AI1".."AO2") on range r; false when r is not one of its ranges.
func SpecOf(channel string, r Range) (Spec, bool) {
	p, ok := parts[channel]
	if !ok || !r.ValidFor(channel) {
		return Spec{}, false
	}
	lo, fs := r.Scale()
	s := Spec{Range: r, Default: DefaultRanges()[channel] == r, Unit: r.Unit(), Lo: lo, Min: lo, FS: fs}
	if channel[:2] == "AI" {
		s.Offset = r.AINominal(0)
		s.Gain = r.AINominal(1) - s.Offset
		s.Formula = []Line{ln("rng.f.pin", "vref", VrefMV, "max", ADCMax)}
		if r == RangeVoltage {
			s.Jumpers = []Jumper{jp(p.jpA, true, "rng.jp.pins", "pins", "2–3"), jp(p.jpB, true, "rng.jp.pins", "pins", "1–2")}
			s.Formula = append(s.Formula, ln("rng.f.ai_v", "sum", divTop+divLow, "lo", divLow))
			s.Note = ln("rng.n.ai_v", "top", p.r1, "ktop", divTop, "bot", p.r2, "kbot", divLow)
		} else {
			s.Jumpers = []Jumper{jp(p.jpA, true, "rng.jp.pins", "pins", "1–2"), jp(p.jpB, true, "rng.jp.pins", "pins", "2–3")}
			s.Formula = append(s.Formula, ln("rng.f.ai_i", "r", shuntR))
			s.Note = ln("rng.n.ai_i", "shunt", p.r4, "r", shuntR, "lim", p.r3, "rl", limR)
		}
		return s, true
	}
	s.Offset = r.AODACmV(0)
	s.Gain = r.AODACmV(1) - s.Offset
	s.Min = r.Min()
	s.Formula = []Line{ln("rng.f.dac", "vref", VrefMV, "max", DACMax)}
	switch r {
	case RangeVoltage:
		s.Jumpers = []Jumper{jp(p.jpA, true, "rng.jp.v_closed"), jp(p.jpB, false, "rng.jp.open")}
		s.Formula = append(s.Formula, ln("rng.f.ao_v"))
		s.Note = ln("rng.n.ao_v")
		s.Warn = &Line{Key: "rng.w.ao_v"}
	case RangeAO420:
		s.Jumpers = []Jumper{jp(p.jpA, false, "rng.jp.i_open"), jp(p.jpB, true, "rng.jp.bias")}
		s.Formula = append(s.Formula, ln("rng.f.ao_vin420", "rb", biasR, "rin", inR, "vref", VrefMV, "sum", biasR+inR),
			ln("rng.f.ao_i420", "g", xtrGain, "r", setR))
		s.Note = ln("rng.n.ao_i420", "rb", p.r3, "krb", biasR, "rin", p.r2, "krin", inR)
		w := ln("rng.w.ao_i420", "ma", r.Min())
		s.Warn = &w
	default:
		s.Jumpers = []Jumper{jp(p.jpA, false, "rng.jp.i_open"), jp(p.jpB, false, "rng.jp.open")}
		s.Formula = append(s.Formula, ln("rng.f.ao_i", "g", xtrGain, "r", setR))
		s.Note = ln("rng.n.ao_i", "rset", p.r1, "r", setR, "rin", p.r2, "krin", inR)
	}
	return s, true
}

// SpecsFor is every range a channel can be soldered for, in RangeOptions order.
func SpecsFor(channel string) []Spec {
	var out []Spec
	for _, r := range RangeOptions(channel) {
		if s, ok := SpecOf(channel, r); ok {
			out = append(out, s)
		}
	}
	return out
}
