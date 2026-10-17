package ptpanel

import (
	"strings"
	"testing"

	"PortTool/src/ptitems"
	"PortTool/src/ptlib"
	"PortTool/src/ptreport"
	"PortTool/src/ptstages"
)

// A person's DI walk is one numbered step per channel; an item that runs once
// is one step; a skipped item gets none (decision 128).
func TestJournalNumbersChannelsAndItems(t *testing.T) {
	din, _ := catalogueItem("din.level")
	// usb.info has no channels; temp.read is one step per channel since decision 129 (next test).
	info, _ := catalogueItem("usb.info")
	j := newJournal(4, nil)
	st := ptstages.Functional
	j.stageStart(st)

	walk := ItemPick{Item: din, Peer: ptitems.PeerHuman}
	j.observe(st, walk, ptlib.State{ID: din.ID, Running: true})
	j.observe(st, walk, ptlib.State{ID: din.ID, Running: true, DinWalk: &ptlib.DinWalkResult{}})
	j.observe(st, walk, ptlib.State{ID: din.ID, Running: true, DinWalk: &ptlib.DinWalkResult{On: 1}})
	j.observe(st, walk, ptlib.State{ID: din.ID, Running: true, DinWalk: &ptlib.DinWalkResult{On: 2,
		Rows: []ptlib.DinRow{{Ch: 1, Seen: "0x1", Pass: true}}}})
	j.observe(st, walk, ptlib.State{ID: din.ID, Running: false, Outcome: ptreport.OutcomeFail, DinWalk: &ptlib.DinWalkResult{
		Rows: []ptlib.DinRow{{Ch: 1, Seen: "0x1", Pass: true}, {Ch: 2, Seen: "0x0", Why: ptlib.DinNone}}}})

	once := ItemPick{Item: info, Peer: info.Peers[0]}
	j.observe(st, once, ptlib.State{ID: info.ID, Running: true})
	j.observe(st, once, ptlib.State{ID: info.ID, Outcome: ptreport.OutcomePass})

	sdram, _ := catalogueItem("sdram.sweep")
	j.observe(st, ItemPick{Item: sdram}, ptlib.State{ID: sdram.ID, Outcome: ptreport.OutcomeSkipped})

	res := ptreport.StepResult{Outcome: ptreport.OutcomeFail}
	items := j.stageEnd(st, &res)
	want := []struct {
		ch int
		o  ptreport.Outcome
	}{{1, ptreport.OutcomePass}, {2, ptreport.OutcomeFail}, {0, ptreport.OutcomePass}}
	if len(items) != len(want) {
		t.Fatalf("steps: %+v", items)
	}
	for i, w := range want {
		if items[i].No != i+1 || items[i].Channel != w.ch || items[i].Outcome != w.o {
			t.Fatalf("step %d: %+v, want ch %d %s", i+1, items[i], w.ch, w.o)
		}
	}
	if w := items[1].Where; !strings.HasPrefix(w, "#2 ") || !strings.HasSuffix(w, " > DI2") {
		t.Fatalf("where: %q", items[1].Where)
	}
	if pass, fail, _, left := j.tally(); pass != 2 || fail != 1 || left != 1 {
		t.Fatalf("tally %d %d %d", pass, fail, left)
	}
}

// Decision 129 「多路」: a multi-channel item is a numbered step per channel,
// each ending on its own result; RY2 stopped does not hide RY1's failure.
func TestJournalGivesEachChannelItsOwnStep(t *testing.T) {
	relay, _ := catalogueItem("relay.cycle")
	j := newJournal(2, nil)
	st := ptstages.Aging
	j.stageStart(st)
	p := ItemPick{Item: relay, Peer: ptitems.PeerRef, Params: map[string]string{"ch": "1,2"}}
	j.observe(st, p, ptlib.State{ID: relay.ID, Running: true})
	j.observe(st, p, ptlib.State{ID: relay.ID, Outcome: ptreport.OutcomeFail, Chans: []ptreport.ChannelResult{
		{Ch: 1, Name: "RY1", Outcome: ptreport.OutcomeFail, Reason: ptlib.ReasonCheckChain},
		{Ch: 2, Name: "RY2", Outcome: ptreport.OutcomeStopped, Reason: ptlib.ReasonStopped}}})
	res := ptreport.StepResult{Outcome: ptreport.OutcomeFail}
	items := j.stageEnd(st, &res)
	if len(items) != 2 || items[0].Outcome != ptreport.OutcomeFail || items[1].Outcome != ptreport.OutcomeStopped ||
		!strings.HasSuffix(items[0].Where, " > RY1") || items[1].Channel != 2 {
		t.Fatalf("steps: %+v, want RY1 FAIL then RY2 STOPPED", items)
	}
}
