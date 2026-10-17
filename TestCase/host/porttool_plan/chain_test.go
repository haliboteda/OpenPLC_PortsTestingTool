package testcase

// The fixture tests' DO -> relay -> reference DI chain (decision 133,
// PORTTOOL-FLOW.md C.3.6 「DO、继电器一条链」), and R&D refusing the
// reference board.
//
// Bench: two simulated boards wired by simboard (reference DIn reads 1 while DOn
// and RYn under test are both on, n = 1..6; DI7 / DI8 follow DO7 / DO8), two left
// unwired, or two scripted fakes with DO1 / DO2 swapped. None sees a real
// contact, its bounce or a load.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"PortTool/src/ptboard"

	"PortTool/src/ptlib"
	"PortTool/src/ptitems"
	"PortTool/src/ptreport"
	"PortTool/src/refboard"
)

func chainState(e *ptlib.Engine, id string) ptlib.State {
	for _, s := range e.States() {
		if s.ID == id {
			return s
		}
	}
	return ptlib.State{}
}

func TestDOChainWalksAllEightOutputsOnWiredBoards(t *testing.T) {
	dut, ref := openSimPair(t)
	e := doutEngine(t, dut, ref, "dout.load", func(ptlib.Question) ptlib.Answer { return ptlib.Answer{} })
	p := ptlib.Pick{ID: "dout.load", On: true, Peer: ptitems.PeerRef, Params: map[string]string{"ch": "1,2,3,4,5,6,7,8"}}
	rep, err := e.Run(context.Background(), []ptlib.Pick{p}, 0)
	if err != nil {
		t.Fatal(err)
	}
	st := chainState(e, "dout.load")
	if o := outcomes(rep)["dout.load"]; o != ptreport.OutcomePass || st.Reason != "" {
		t.Fatalf("dout.load on the chain: %s / %q, want PASS; %+v", o, st.Reason, st)
	}
	for _, c := range st.Chans {
		if c.Outcome != ptreport.OutcomePass {
			t.Errorf("%s: %s, want each DO passed on its own", c.Name, c.Outcome)
		}
	}
	if len(st.Chans) != 8 {
		t.Errorf("channels: %+v, want DO1-8 each with its own result", st.Chans)
	}
	if len(st.Checks) != 16 {
		t.Errorf("checks: %d, want each DI on its own beat and all eight low", len(st.Checks))
	}
}

func TestDOChainFailsWhenTheReferenceDIReadsNothing(t *testing.T) {
	dut, _ := openSimRaw(t)
	ref, _ := openSimRaw(t) // not linked: no wire reaches its DI
	e := doutEngine(t, dut, ref, "dout.load", func(ptlib.Question) ptlib.Answer { return ptlib.Answer{} })
	p := ptlib.Pick{ID: "dout.load", On: true, Peer: ptitems.PeerRef, Params: map[string]string{"ch": "1,2"}}
	rep, err := e.Run(context.Background(), []ptlib.Pick{p}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if o, st := outcomes(rep)["dout.load"], chainState(e, "dout.load"); o != ptreport.OutcomeFail || st.Reason != ptlib.ReasonCheckChain {
		t.Fatalf("got %s / %q, want FAIL / %s", o, st.Reason, ptlib.ReasonCheckChain)
	}
}

func relayChainPick(count string) ptlib.Pick {
	return ptlib.Pick{ID: "relay.cycle", On: true, Peer: ptitems.PeerRef,
		Params: map[string]string{"ch": "1,2", "close": "1000", "open": "1000", "count": count, "by": "gap"}}
}

func TestRelayChainCountsEachRelaysCyclesOnTheReferenceDI(t *testing.T) {
	dut, ref := openSimPair(t)
	e := doutEngine(t, dut, ref, "relay.cycle", func(q ptlib.Question) ptlib.Answer {
		t.Errorf("a person was asked on the chain: %+v", q)
		return ptlib.Answer{}
	})
	rep, err := e.Run(context.Background(), []ptlib.Pick{relayChainPick("2")}, 0)
	if err != nil {
		t.Fatal(err)
	}
	st := chainState(e, "relay.cycle")
	if o := outcomes(rep)["relay.cycle"]; o != ptreport.OutcomePass {
		t.Fatalf("relay.cycle on the chain: %s / %q %s", o, st.Reason, st.Detail)
	}
	for _, c := range st.Relay.Chans {
		if c.Seen != 2 {
			t.Errorf("relay %d: the reference DI counted %d, want 2", c.Ch, c.Seen)
		}
	}
	if len(st.Chans) != 2 || st.Chans[0].Name != "RY1" || st.Chans[1].Outcome != ptreport.OutcomePass {
		t.Errorf("channels: %+v, want RY1 and RY2 each passed", st.Chans)
	}
}

func TestRelayChainFailsWhenTheReferenceDICountsNothing(t *testing.T) {
	dut, _ := openSimRaw(t)
	ref, _ := openSimRaw(t)
	e := doutEngine(t, dut, ref, "relay.cycle", func(ptlib.Question) ptlib.Answer { return ptlib.Answer{} })
	rep, err := e.Run(context.Background(), []ptlib.Pick{relayChainPick("1")}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if o, st := outcomes(rep)["relay.cycle"], chainState(e, "relay.cycle"); o != ptreport.OutcomeFail || st.Reason != ptlib.ReasonCheckChain {
		t.Fatalf("got %s / %q, want FAIL / %s", o, st.Reason, ptlib.ReasonCheckChain)
	}
}

// R&D is done by hand: the reference board is refused and never the default.
func TestRDRefusesTheReferenceBoardAndDefaultsToAPerson(t *testing.T) {
	b, fake := doutBoard(t)
	e := doutEngine(t, b, nil, "dout.load", func(ptlib.Question) ptlib.Answer { return ptlib.Answer{} })
	e.RD, e.RefConnected = true, true
	_, err := e.Run(context.Background(), []ptlib.Pick{{ID: "dout.load", On: true, Peer: ptitems.PeerRef}}, 0)
	var pe *ptlib.PickError
	if !errors.As(err, &pe) || pe.Code != ptlib.ReasonBadPeer {
		t.Fatalf("R&D with the reference board: %v, want a %s refusal", err, ptlib.ReasonBadPeer)
	}
	if len(fake.commands()) != 0 {
		t.Errorf("the board was touched: %v", fake.commands())
	}
	if _, err := e.Run(context.Background(), []ptlib.Pick{{ID: "dout.load", On: true, Params: map[string]string{"ch": "1"}}}, 0); err != nil {
		t.Fatal(err)
	}
	if chainState(e, "dout.load").Drop == nil {
		t.Errorf("no peer named: %+v, want a person's DO walk", chainState(e, "dout.load"))
	}
}

// With a duration the DO chain runs first and alone; the relays then share
// the chain for the timed part (decision 133, an exception to decision 102).
func TestTimedRunTestsTheDOChainBeforeTheRelays(t *testing.T) {
	dut, ref := openSimPair(t)
	e := doutEngine(t, dut, ref, "dout.load", func(ptlib.Question) ptlib.Answer { return ptlib.Answer{} })
	e.Items = append(e.Items, catalogueItem(t, "relay.cycle"))
	picks := []ptlib.Pick{
		{ID: "dout.load", On: true, Peer: ptitems.PeerRef, Params: map[string]string{"ch": "1,2"}},
		relayChainPick("1"),
	}
	rep, err := e.Run(context.Background(), picks, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	got := outcomes(rep)
	if got["dout.load"] != ptreport.OutcomePass || got["relay.cycle"] != ptreport.OutcomePass {
		t.Fatalf("outcomes %v, want both PASS; %+v", got, e.States())
	}
	var do, relay ptreport.StepResult
	for _, s := range rep.Steps {
		switch s.ID {
		case "dout.load":
			do = s
		case "relay.cycle":
			relay = s
		}
	}
	if end := do.StartedAt.Add(time.Duration(do.DurationMS) * time.Millisecond); relay.StartedAt.Before(end) {
		t.Errorf("the relays started at %s, before the DO chain ended at %s", relay.StartedAt, end)
	}
}

// swappedBench is a board under test and a reference board whose DI1 and DI2
// are wired to DO2 and DO1: the cable swapped. Only DO and its reading are
// played; relays always pass.
func swappedBench(t *testing.T) (*ptboard.Board, *ptboard.Board) {
	t.Helper()
	var mu sync.Mutex
	do := map[int]bool{}
	var dutFake *scriptBoard
	later := func(line string) { // after the reply, as a board's next frame
		go func() { time.Sleep(50 * time.Millisecond); dutFake.emit(line) }()
	}
	dutFake = newScriptBoard(t, func(cmd string, nth int) ([]string, []string) {
		if lines, ok := standardReplies(cmd); ok {
			return lines, nil
		}
		switch {
		case strings.HasPrefix(cmd, "pt.hold"):
			return []string{"OK hold"}, nil
		case strings.HasPrefix(cmd, "pt.start relay"):
			later("!relay t=1 seq=1 rx=0 miss=0 mode=hold ch1=1 ch2=1")
			return []string{"OK relay started"}, nil
		case strings.HasPrefix(cmd, "pt.start dout"), strings.HasPrefix(cmd, "pt.set dout"):
			mu.Lock()
			for _, f := range strings.Fields(cmd) {
				if v, ok := strings.CutPrefix(f, "duty="); ok {
					for _, p := range strings.Split(v, ",") {
						c, d, _ := strings.Cut(p, ":")
						n, _ := strconv.Atoi(c)
						do[n] = d != "0"
					}
				}
			}
			mu.Unlock()
			later("!dout t=1 seq=1 rx=0 miss=0 mode=hold res=8 tick=256000")
			return []string{"OK dout"}, nil
		}
		return []string{"ERR unknown " + cmd}, nil
	})
	dut := ptboard.New(dutFake, 0)
	t.Cleanup(func() { dut.Close() })

	stop := make(chan struct{})
	var refFake *scriptBoard
	refFake = newScriptBoard(t, func(cmd string, nth int) ([]string, []string) {
		if strings.HasPrefix(cmd, "pt.start din") {
			go func() {
				for tick := 1; ; tick++ {
					select {
					case <-stop:
						return
					case <-time.After(20 * time.Millisecond):
					}
					mu.Lock()
					wire := map[int]int{1: 2, 2: 1} // DI1 <- DO2, DI2 <- DO1
					line := fmt.Sprintf("!din t=%d seq=1 rx=0 miss=0 v=0x00 mode=level", tick*50)
					for di := 1; di <= 2; di++ {
						lv := 0
						if do[wire[di]] {
							lv = 1
						}
						line += fmt.Sprintf(" ch%d=%d", di, lv)
					}
					mu.Unlock()
					refFake.emit(line)
				}
			}()
			return []string{"OK din started"}, nil
		}
		if cmd == "pt.stop din" {
			close(stop)
		}
		if lines, ok := standardReplies(cmd); ok {
			return lines, nil
		}
		return []string{"ERR unknown " + cmd}, nil
	})
	ref := ptboard.New(refFake, 0)
	t.Cleanup(func() { ref.Close() })
	return dut, ref
}

// The DO walk drives one output at a time, so a swapped DO1 / DO2 pair fails.
func TestDOChainCatchesSwappedWires(t *testing.T) {
	dut, ref := swappedBench(t)
	e := doutEngine(t, dut, ref, "dout.load", func(ptlib.Question) ptlib.Answer { return ptlib.Answer{} })
	p := ptlib.Pick{ID: "dout.load", On: true, Peer: ptitems.PeerRef, Params: map[string]string{"ch": "1,2"}}
	rep, err := e.Run(context.Background(), []ptlib.Pick{p}, 0)
	if err != nil {
		t.Fatal(err)
	}
	st := chainState(e, "dout.load")
	if o := outcomes(rep)["dout.load"]; o != ptreport.OutcomeFail || st.Reason != ptlib.ReasonCheckChain {
		t.Fatalf("swapped DO1 / DO2: %s / %q %s, want FAIL / %s", o, st.Reason, st.Detail, ptlib.ReasonCheckChain)
	}
	if len(st.Checks) != 4 {
		t.Errorf("the card lists %d failed checks, want DI1 and DI2 wrong in both beats: %+v", len(st.Checks), st.Checks)
	}
	if len(st.Chans) != 2 || st.Chans[0].Outcome != ptreport.OutcomeFail || st.Chans[1].Outcome != ptreport.OutcomeFail {
		t.Errorf("channels: %+v, want DO1 and DO2 each failed", st.Chans)
	}
}

// Decision 135: the walk reads the reference DI the registration names, so a
// cable crossed on purpose and registered as such passes.
func TestDOChainFollowsTheRegisteredWiring(t *testing.T) {
	dut, ref := swappedBench(t)
	e := doutEngine(t, dut, ref, "dout.load", func(ptlib.Question) ptlib.Answer { return ptlib.Answer{} })
	w := refboard.Default()
	w.DO[1] = refboard.Link{Via: 1, To: 2}
	w.DO[2] = refboard.Link{Via: 2, To: 1}
	e.Wiring = func() refboard.Wiring { return w }
	p := ptlib.Pick{ID: "dout.load", On: true, Peer: ptitems.PeerRef, Params: map[string]string{"ch": "1,2"}}
	rep, err := e.Run(context.Background(), []ptlib.Pick{p}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if o, st := outcomes(rep)["dout.load"], chainState(e, "dout.load"); o != ptreport.OutcomePass {
		t.Fatalf("crossed and registered crossed: %s / %q %+v", o, st.Reason, st.Checks)
	}
}

// A relay on no DO cannot be counted: it says so on its own channel, the
// others are judged (decisions 129, 135).
func TestRelayChainSaysWhichRelayIsOnNoDO(t *testing.T) {
	dut, ref := openSimPair(t)
	e := doutEngine(t, dut, ref, "relay.cycle", func(ptlib.Question) ptlib.Answer { return ptlib.Answer{} })
	w := refboard.Default()
	w.DO[2] = refboard.Link{Via: 0, To: 2}
	e.Wiring = func() refboard.Wiring { return w }
	rep, err := e.Run(context.Background(), []ptlib.Pick{relayChainPick("1")}, 0)
	if err != nil {
		t.Fatal(err)
	}
	st := chainState(e, "relay.cycle")
	got := map[string]string{}
	for _, c := range st.Chans {
		got[c.Name] = string(c.Outcome) + "/" + c.Reason
	}
	if got["RY1"] != "PASS/" || got["RY2"] != "ERROR/"+ptlib.ReasonRefUnwired || outcomes(rep)["relay.cycle"] != ptreport.OutcomeError {
		t.Fatalf("channels %v, item %s; want RY1 PASS and RY2 not wired", got, outcomes(rep)["relay.cycle"])
	}
}
