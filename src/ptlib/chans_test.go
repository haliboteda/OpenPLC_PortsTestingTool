package ptlib

import (
	"testing"

	"PortTool/src/ptcheck"
	"PortTool/src/ptitems"
	"PortTool/src/ptreport"
)

func chanEngine(id string) (*Engine, *job) {
	var it ptitems.Item
	for _, c := range ptitems.Catalogue {
		if c.ID == id {
			it = c
		}
	}
	e := &Engine{states: map[string]*State{id: {ID: id}}, results: map[string]*ptreport.StepResult{id: {ID: id}}}
	return e, &job{item: it, peer: ptitems.PeerHuman}
}

func chanOutcomes(e *Engine, id string) map[string]ptreport.Outcome {
	out := map[string]ptreport.Outcome{}
	for _, c := range e.states[id].Chans {
		out[c.Name] = c.Outcome
	}
	return out
}

// Decision 129 「多路」: temperature 1 and 2 each their own result; a check on
// no channel (the reference voltage) fails both.
func TestTemperatureChannelsFromTheChecks(t *testing.T) {
	e, j := chanEngine("temp.read")
	j.peer = ptitems.PeerNone
	pass := []ptcheck.Result{{Check: ptcheck.Check{Field: "vdda"}, Pass: true},
		{Check: ptcheck.Check{Field: "ch1[1]"}, Pass: true}, {Check: ptcheck.Check{Field: "ch2[1]"}, Pass: false}}
	e.checkChans(j, Channels(j.item, j.peer, nil), pass)
	if got := chanOutcomes(e, "temp.read"); got["T1"] != ptreport.OutcomePass || got["T2"] != ptreport.OutcomeFail {
		t.Fatalf("channels %v, want T1 PASS, T2 FAIL", got)
	}
	e, j = chanEngine("temp.read")
	j.peer = ptitems.PeerNone
	e.checkChans(j, []int{1, 2}, []ptcheck.Result{{Check: ptcheck.Check{Field: "vdda"}, Pass: false},
		{Check: ptcheck.Check{Field: "ch1[1]"}, Pass: true}, {Check: ptcheck.Check{Field: "ch2[1]"}, Pass: true}})
	if got := chanOutcomes(e, "temp.read"); got["T1"] != ptreport.OutcomeFail || got["T2"] != ptreport.OutcomeFail {
		t.Fatalf("a bad reference voltage: %v, want both FAIL", got)
	}
}

// A live card: matching rows pass, a wrong one fails, a blank one is stopped.
func TestLiveCardChannels(t *testing.T) {
	e, j := chanEngine("aout.manual")
	yes, no := true, false
	e.liveChans(j, LiveState{Kind: "aout", Ref: []LiveRefRow{{Ch: 1, Pass: &no}}})
	if got := chanOutcomes(e, "aout.manual"); got["AO1"] != ptreport.OutcomeFail || got["AO2"] != ptreport.OutcomeStopped {
		t.Fatalf("channels %v, want AO1 FAIL, AO2 STOPPED", got)
	}
	e, j = chanEngine("din.quad")
	e.liveChans(j, LiveState{Kind: "quad", Ref: []LiveRefRow{{Ch: 1, Pass: &yes}, {Ch: 2, Pass: &yes}}})
	if got := chanOutcomes(e, "din.quad"); got["DI1/DI2"] != ptreport.OutcomePass || got["DI5/DI6"] != ptreport.OutcomeStopped {
		t.Fatalf("quad groups %v", got)
	}
}

// A channel nobody judged does not make the item worse; all recorded stays recorded.
func TestWorstSkipsRecordedChannels(t *testing.T) {
	rs := []ptreport.ChannelResult{{Outcome: ptreport.OutcomePass}, {Outcome: Recorded}}
	if o := worst(rs); o != ptreport.OutcomePass {
		t.Errorf("pass + recorded: %s", o)
	}
	if o := worst([]ptreport.ChannelResult{{Outcome: Recorded}}); o != Recorded {
		t.Errorf("recorded only: %s", o)
	}
	if o := worst([]ptreport.ChannelResult{{Outcome: ptreport.OutcomeFail}, {Outcome: ptreport.OutcomeStopped}}); o != ptreport.OutcomeFail {
		t.Errorf("a failure is not hidden by a stop: %s", o)
	}
}
