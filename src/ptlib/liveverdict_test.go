package ptlib

import (
	"testing"

	"PortTool/src/ptreport"
)

// A live card is judged by the reference values typed on it (decision 128).
func TestLiveVerdict(t *testing.T) {
	yes, no := true, false
	for _, c := range []struct {
		name string
		st   LiveState
		want ptreport.Outcome
	}{
		{"nothing typed", LiveState{Kind: "din"}, ptreport.OutcomeStopped},
		{"all match", LiveState{Kind: "din", Ref: []LiveRefRow{{Ch: 1, Pass: &yes}, {Ch: 2, Pass: &yes}}}, ptreport.OutcomePass},
		{"one off", LiveState{Kind: "ain", Ref: []LiveRefRow{{Ch: 1, Pass: &yes}, {Ch: 2, Pass: &no}}}, ptreport.OutcomeFail},
		{"quad missed steps", LiveState{Kind: "quad", Err: 3, Ref: []LiveRefRow{{Ch: 1, Pass: &yes}}}, ptreport.OutcomeFail},
		{"typed, no board value yet", LiveState{Kind: "dout", Ref: []LiveRefRow{{Ch: 1}}}, ptreport.OutcomeStopped},
		// Decision 129: a row left blank is not measured to the end.
		{"one of two blank", LiveState{Kind: "ain", Analog: make([]LiveAnalog, 2), Ref: []LiveRefRow{{Ch: 1, Pass: &yes}}}, ptreport.OutcomeStopped},
		{"one of four groups typed", LiveState{Kind: "quad", Groups: make([]LiveQuad, 4), Ref: []LiveRefRow{{Ch: 1, Pass: &yes}}}, ptreport.OutcomeStopped},
		{"blank rows, one off", LiveState{Kind: "dout", Bits: make([]int, 8), Ref: []LiveRefRow{{Ch: 1, Pass: &no}}}, ptreport.OutcomeFail},
		{"every row typed", LiveState{Kind: "dout", Bits: make([]int, 2), Ref: []LiveRefRow{{Ch: 1, Pass: &yes}, {Ch: 2, Pass: &yes}}}, ptreport.OutcomePass},
	} {
		if got := liveVerdict(c.st); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}
