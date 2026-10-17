package ptlib

import (
	"PortTool/src/ptitems"
	"PortTool/src/ptreport"
)

// Port verdicts on the port list.
const (
	PortPass     = "pass"
	PortFail     = "fail"
	PortUnjudged = "unjudged"
)

// PortVerdicts sums item outcomes per port: any failure fails the port, all
// passes pass it, anything not judged leaves it unjudged. KNX's record-only
// diagnostics do not count (decision 103, STG-20). Ports with nothing run are
// absent.
func PortVerdicts(items []ptitems.Item, states []State) map[string]string {
	byID := map[string]ptitems.Item{}
	for _, it := range items {
		byID[it.ID] = it
	}
	out := map[string]string{}
	for _, s := range states {
		it, ok := byID[s.ID]
		if !ok || s.Outcome == "" || s.Outcome == ptreport.OutcomeSkipped {
			continue
		}
		// Whatever their outcome: a watched knx.frames is normally stopped by hand.
		if RecordOnly(it) {
			continue
		}
		// A multi-channel item has no result of its own: its channels count (decision 129).
		o := s.Outcome
		if len(s.Chans) > 0 {
			o = worst(s.Chans)
		}
		v := PortUnjudged
		switch o {
		case ptreport.OutcomePass:
			v = PortPass
		case ptreport.OutcomeFail, ptreport.OutcomeTimeout:
			v = PortFail
		}
		switch cur := out[s.Port]; {
		case cur == "", cur == PortPass:
			out[s.Port] = v
		case cur == PortUnjudged && v == PortFail:
			out[s.Port] = v
		}
	}
	return out
}
