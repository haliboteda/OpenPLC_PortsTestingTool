package testcase

// Live cards (decision 108, PORTTOOL-FLOW.md C.3.6 "实时卡片"): with a person
// standing in for the reference board, the session stays open, the card
// follows every frame, DO / AO are set from the card, and the item is
// judged by the typed reference values when the person ends it, STOPPED
// when none was typed.
//
// Bench: one simulated board; the person's signals are sim.din / sim.quad /
// sim.ain. It does not see real 24 V inputs, a real DAC or a meter.

import (
	"PortTool/src/calstore"
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"PortTool/src/ptboard"
	"PortTool/src/ptitems"
	"PortTool/src/ptlib"
	"PortTool/src/ptplan"
	"PortTool/src/ptproto"
	"PortTool/src/ptreport"
	"PortTool/src/ptseq"
	"PortTool/src/simboard"
)

func openSim(t *testing.T) *ptboard.Board {
	t.Helper()
	a, err := simboard.OpenPort("sim", 0)
	if errors.Is(err, simboard.ErrNotBuilt) {
		t.Skip("simulated board not built: tools/build.sh sim")
	}
	if err != nil {
		t.Fatal(err)
	}
	b := ptboard.New(a, 0)
	t.Cleanup(func() { b.Close() })
	return b
}

func catItem(t *testing.T, id string) ptitems.Item {
	t.Helper()
	for _, it := range ptitems.Catalogue {
		if it.ID == id {
			return it
		}
	}
	t.Fatalf("%s is not in the catalogue", id)
	return ptitems.Item{}
}

// liveRun starts one live item and returns its engine and the run's result.
func liveRun(t *testing.T, b *ptboard.Board, id string, setup func(e *ptlib.Engine)) (*ptlib.Engine, context.CancelFunc, <-chan ptreport.Report) {
	t.Helper()
	e := &ptlib.Engine{
		Board: b, Plan: ptplan.Plan{}, Items: []ptitems.Item{catItem(t, id)}, Hold: &fakeHold{b: b},
		Runner: func() *ptseq.Runner { return &ptseq.Runner{Board: b, OpenPeer: refusePeer, FindCDC: noCDC} },
		Ask:    func(ctx context.Context, q ptlib.Question) (ptlib.Answer, error) { return ptlib.Answer{}, nil },
	}
	if setup != nil {
		setup(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan ptreport.Report, 1)
	go func() {
		rep, err := e.Run(ctx, []ptlib.Pick{{ID: id, On: true, Peer: ptitems.PeerHuman}}, 0)
		if err != nil {
			t.Error(err)
		}
		done <- rep
	}()
	return e, cancel, done
}

func stateOf(e *ptlib.Engine, id string) ptlib.State {
	for _, s := range e.States() {
		if s.ID == id {
			return s
		}
	}
	return ptlib.State{}
}

// waitLive waits until the card's live state satisfies ok.
func waitLive(t *testing.T, e *ptlib.Engine, id, what string, ok func(*ptlib.LiveState) bool) *ptlib.LiveState {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if lv := stateOf(e, id).Live; lv != nil && ok(lv) {
			return lv
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s: %s never showed; last %+v", id, what, stateOf(e, id).Live)
	return nil
}

func sim(b *ptboard.Board, line string) {
	_, _ = b.Send(line, ptboard.ExpectMany, 300*time.Millisecond) // sim.* answer SIM, not OK
}

func bitsEq(got []int, want int) bool {
	if len(got) != 8 {
		return false
	}
	for i, v := range got {
		if v != (want>>i)&1 {
			return false
		}
	}
	return true
}

func waitDone(t *testing.T, done <-chan ptreport.Report) ptreport.Report {
	t.Helper()
	select {
	case rep := <-done:
		return rep
	case <-time.After(8 * time.Second):
		t.Fatal("the live item did not end")
	}
	return ptreport.Report{}
}

// DI level with a person is walked input by input and judged by the board, in
// R&D too (decision 128).
func TestDILevelWithAPersonIsWalkedAndJudged(t *testing.T) {
	b := openSim(t)
	plan, err := ptplan.Load(filepath.Join("..", "..", "..", "src", "plans", "station6-poweron.json"))
	if err != nil {
		t.Fatal(err)
	}
	sim(b, "sim.din 0x00")
	var walked []string
	e := &ptlib.Engine{
		Board: b, Plan: plan, Items: []ptitems.Item{catItem(t, "din.level")}, Hold: &fakeHold{b: b},
		Runner: func() *ptseq.Runner { return &ptseq.Runner{Board: b, OpenPeer: refusePeer, FindCDC: noCDC} },
		Ask: func(ctx context.Context, q ptlib.Question) (ptlib.Answer, error) {
			// The person puts 24 V on the input asked for; the board ends the question.
			walked = append(walked, q.Channel)
			n, _ := strconv.Atoi(strings.TrimPrefix(q.Channel, "DI"))
			sim(b, fmt.Sprintf("sim.din 0x%02X", 1<<(n-1)))
			<-ctx.Done()
			sim(b, "sim.din 0x00")
			return ptlib.Answer{}, ctx.Err()
		},
	}
	rep, err := e.Run(context.Background(), []ptlib.Pick{{ID: "din.level", On: true, Peer: ptitems.PeerHuman,
		Params: map[string]string{"ch": "1,2,3"}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if o := outcomes(rep)["din.level"]; o != ptreport.OutcomePass || strings.Join(walked, ",") != "DI1,DI2,DI3" {
		t.Fatalf("din.level with a person: %s, walked %v", o, walked)
	}
	if v := ptlib.PortVerdicts([]ptitems.Item{catItem(t, "din.level")}, e.States())["din"]; v != ptlib.PortPass {
		t.Errorf("the walk decides the port: %s", v)
	}
}

func TestLiveQuadCardCountsByHandAndResets(t *testing.T) {
	b := openSim(t)
	e, cancel, done := liveRun(t, b, "din.quad", nil)
	waitLive(t, e, "din.quad", "the count card", func(lv *ptlib.LiveState) bool { return lv.Kind == "quad" && len(lv.Groups) == 4 })
	sim(b, "sim.quad 40")
	// Not the exact 40: a live card judges nothing, it only has to follow.
	waitLive(t, e, "din.quad", "every group counting forward alike", func(lv *ptlib.LiveState) bool {
		for _, g := range lv.Groups {
			if g.Count < 30 || g.Count != lv.Groups[0].Count || g.Dir != 1 {
				return false
			}
		}
		return lv.Err == 0
	})
	if err := e.Live("din.quad", ptlib.LiveReq{Op: ptlib.LiveReset}); err != nil {
		t.Fatal(err)
	}
	waitLive(t, e, "din.quad", "every group back to 0", func(lv *ptlib.LiveState) bool {
		for _, g := range lv.Groups {
			if g.Count != 0 {
				return false
			}
		}
		return true
	})
	cancel()
	if o := outcomes(waitDone(t, done))["din.quad"]; o != ptreport.OutcomeStopped {
		t.Fatalf("stopping the run leaves din.quad %s, want STOPPED", o)
	}
}

func TestLiveDOCardSwitchesOneOutputAndAllGoOffAtTheEnd(t *testing.T) {
	b := openSim(t)
	e, _, done := liveRun(t, b, "dout.manual", nil)
	waitLive(t, e, "dout.manual", "eight outputs off", func(lv *ptlib.LiveState) bool { return lv.Kind == "dout" && bitsEq(lv.Bits, 0) })
	if err := e.Live("dout.manual", ptlib.LiveReq{Op: ptlib.LiveDO, Ch: 3, On: true}); err != nil {
		t.Fatal(err)
	}
	waitLive(t, e, "dout.manual", "DO3 on", func(lv *ptlib.LiveState) bool { return bitsEq(lv.Bits, 0x04) })
	if err := e.Live("dout.manual", ptlib.LiveReq{Op: ptlib.LiveDO, Ch: 9, On: true}); err == nil {
		t.Error("DO9 was accepted")
	}
	if err := e.Live("dout.manual", ptlib.LiveReq{Op: ptlib.LiveEnd}); err != nil {
		t.Fatal(err)
	}
	rep := waitDone(t, done)
	sent := strings.Join(rep.Steps[0].Attempts[0].Sent, "\n")
	for _, want := range []string{"pt.start dout mode=hold ch=1,2,3,4,5,6,7,8 res=8 duty=0", "pt.set dout duty=1:0,2:0,3:255,4:0", "pt.stop dout"} {
		if !strings.Contains(sent, want) {
			t.Errorf("sent %q, want %q among it", sent, want)
		}
	}
}

func TestLiveAOCardDrivesTheTypedCurrentThroughTheBoardsKB(t *testing.T) {
	b := openSim(t)
	cal := ptlib.AnalogCal{K: 2, B: 0.5, Unit: "mA", FS: 20, Range: calstore.RangeAO020}
	e, _, done := liveRun(t, b, "aout.manual", func(e *ptlib.Engine) {
		e.Analog = func(string) (ptlib.AnalogCal, error) { return cal, nil }
	})
	waitLive(t, e, "aout.manual", "two AO rows", func(lv *ptlib.LiveState) bool { return lv.Kind == "aout" && len(lv.Analog) == 2 })
	if err := e.Live("aout.manual", ptlib.LiveReq{Op: ptlib.LiveAO, Ch: 1, MA: 10}); err != nil {
		t.Fatal(err)
	}
	lv := waitLive(t, e, "aout.manual", "AO1 at 10 mA", func(lv *ptlib.LiveState) bool {
		a := lv.Analog[0]
		return a.Set != nil && *a.Set == 10 && a.Value != nil && math.Abs(*a.Value-10) < 0.1
	})
	if !lv.Analog[0].Calibrated {
		t.Error("AO1 used the board's k, b but is not shown as calibrated")
	}
	if err := e.Live("aout.manual", ptlib.LiveReq{Op: ptlib.LiveAO, Ch: 1, MA: 25}); err == nil {
		t.Error("25 mA was accepted")
	}
	if err := e.Live("aout.manual", ptlib.LiveReq{Op: ptlib.LiveEnd}); err != nil {
		t.Fatal(err)
	}
	sent := strings.Join(waitDone(t, done).Steps[0].Attempts[0].Sent, "\n")
	// (10 - 0.5) / 2 * 102.4 = 486.4 mV at the DAC (decision 106's arithmetic).
	for _, want := range []string{"pt.set aout mv=1:486", "pt.set aout mv=1:0,2:0", "pt.stop aout"} {
		if !strings.Contains(sent, want) {
			t.Errorf("sent %q, want %q among it", sent, want)
		}
	}
}

// Decision 134: a voltage AO has no nominal stage, so without the board's k, b
// it outputs nothing and says why; with them it drives mV 1:1 at the DAC.
func TestLiveAOVoltageNeedsTheBoardsCalibration(t *testing.T) {
	b := openSim(t)
	volt := ptlib.AnalogCal{K: 1, Unit: "mV", FS: 10000, Range: calstore.RangeVoltage}
	calibrated := false
	e, _, done := liveRun(t, b, "aout.manual", func(e *ptlib.Engine) {
		e.Analog = func(string) (ptlib.AnalogCal, error) {
			if calibrated {
				return ptlib.AnalogCal{K: 2, B: 100, Unit: "mV", FS: 10000, Range: calstore.RangeVoltage}, nil
			}
			return ptlib.AnalogCal{}, errors.New("blank")
		}
		e.AnalogNominal = func(string) (ptlib.AnalogCal, error) { return volt, nil }
	})
	waitLive(t, e, "aout.manual", "two AO rows", func(lv *ptlib.LiveState) bool { return lv.Kind == "aout" && len(lv.Analog) == 2 })
	if err := e.Live("aout.manual", ptlib.LiveReq{Op: ptlib.LiveAO, Ch: 1, MA: 5000}); !errors.Is(err, ptlib.ErrAOVoltUncal) {
		t.Errorf("an uncalibrated voltage AO: %v, want %v", err, ptlib.ErrAOVoltUncal)
	}
	if err := e.Live("aout.manual", ptlib.LiveReq{Op: ptlib.LiveEnd}); err != nil {
		t.Fatal(err)
	}
	if sent := strings.Join(waitDone(t, done).Steps[0].Attempts[0].Sent, " | "); strings.Contains(sent, "pt.set aout mv=1:5") {
		t.Errorf("an uncalibrated voltage AO was driven: %s", sent)
	}
}

func TestLiveAICardShowsNominalValuesWhenTheBoardHasNoCalibration(t *testing.T) {
	b := openSim(t)
	nominal := func(raw float64) float64 { return raw * 2500 / 65535 * 90.6 / 22.6 }
	e, _, done := liveRun(t, b, "ain.live", func(e *ptlib.Engine) {
		e.Analog = func(ch string) (ptlib.AnalogCal, error) {
			return ptlib.AnalogCal{}, fmt.Errorf("%s not calibrated", ch)
		}
		e.AnalogNominal = func(string) (ptlib.AnalogCal, error) {
			return ptlib.AnalogCal{K: 1, Unit: "mV", FS: 10000, Range: calstore.RangeVoltage, Nominal: nominal}, nil
		}
	})
	sim(b, "sim.ain 1 1000")
	lv := waitLive(t, e, "ain.live", "AI1 at 1000 mV on the pin", func(lv *ptlib.LiveState) bool {
		return lv.Kind == "ain" && len(lv.Analog) == 2 && lv.Analog[0].PinMV != nil && *lv.Analog[0].PinMV == 1000
	})
	a := lv.Analog[0]
	if a.Calibrated || a.Unit != "mV" || a.Value == nil || math.Abs(*a.Value-1000*90.6/22.6) > 5 {
		t.Errorf("AI1 %+v (value %v), want the nominal terminal mV and not calibrated", a, a.Value)
	}
	if err := e.Live("ain.live", ptlib.LiveReq{Op: ptlib.LiveEnd}); err != nil {
		t.Fatal(err)
	}
	if o := outcomes(waitDone(t, done))["ain.live"]; o != ptreport.OutcomeStopped {
		t.Fatalf("ain.live is %s, want STOPPED", o)
	}
}

func TestTheUnattendedFullTestSkipsALiveCard(t *testing.T) {
	b := openSim(t)
	e, _, done := liveRun(t, b, "dout.manual", func(e *ptlib.Engine) { e.NoLive = true })
	rep := waitDone(t, done)
	if o := outcomes(rep)["dout.manual"]; o != ptreport.OutcomeSkipped {
		t.Fatalf("dout.manual in the full test is %s, want SKIPPED", o)
	}
	if r := stateOf(e, "dout.manual").Reason; r != ptlib.ReasonLiveFull {
		t.Errorf("reason %q, want %q", r, ptlib.ReasonLiveFull)
	}
}

// DI level with a person in the full test (decision 126): one input at a
// time, the person's 24 V is sim.din here, the board's reading answers. DI2
// comes with DI3 high too (a short), so DI2 fails and the walk goes on; on
// DI3 the wire left on DI1 (passed) for longer than 2 s is waited out.
func TestFullTestWalksDILevelInputByInput(t *testing.T) {
	b := openSim(t)
	plan, err := ptplan.Load(filepath.Join("..", "..", "..", "src", "plans", "station6-poweron.json"))
	if err != nil {
		t.Fatal(err)
	}
	sim(b, "sim.din 0x00")
	applied := map[string]string{"DI1": "0x01", "DI2": "0x06", "DI3": "0x04"}
	var asked []string
	e := &ptlib.Engine{
		Board: b, Plan: plan, Items: []ptitems.Item{catItem(t, "din.level")}, Hold: &fakeHold{b: b}, NoLive: true,
		Runner: func() *ptseq.Runner { return &ptseq.Runner{Board: b, OpenPeer: refusePeer, FindCDC: noCDC} },
		Ask: func(ctx context.Context, q ptlib.Question) (ptlib.Answer, error) {
			if q.Kind != ptitems.AnswerWait || q.Walk != "din" {
				t.Errorf("asked %+v, want a wait question of the DI walk", q)
			}
			asked = append(asked, q.Channel)
			if q.Channel == "DI3" {
				sim(b, "sim.din 0x05")
				time.Sleep(2500 * time.Millisecond)
			}
			sim(b, "sim.din "+applied[q.Channel])
			<-ctx.Done() // the board's reading ends the question
			sim(b, "sim.din 0x00")
			return ptlib.Answer{}, ptlib.ErrStopped
		},
	}
	start := time.Now()
	rep, err := e.Run(context.Background(), []ptlib.Pick{{ID: "din.level", On: true, Peer: ptitems.PeerHuman,
		Params: map[string]string{"ch": "1,2,3"}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if o := outcomes(rep)["din.level"]; o != ptreport.OutcomeFail {
		t.Fatalf("din.level is %s, want FAIL: DI2 came with DI3", o)
	}
	if strings.Join(asked, ",") != "DI1,DI2,DI3" {
		t.Errorf("asked %v, want DI1, DI2, DI3", asked)
	}
	w := stateOf(e, "din.level").DinWalk
	if w == nil || len(w.Rows) != 3 {
		t.Fatalf("card %+v, want three rows", w)
	}
	want := []struct {
		why  string
		seen string
	}{{ptlib.DinOK, "0x01"}, {ptlib.DinWrong, "0x06"}, {ptlib.DinOK, "0x04"}}
	for i, r := range w.Rows {
		if r.Why != want[i].why || r.Seen != want[i].seen || r.Pass != (want[i].why == ptlib.DinOK) {
			t.Errorf("DI%d: %+v, want %s seeing %s", r.Ch, r, want[i].why, want[i].seen)
		}
	}
	if d := time.Since(start); d < 2*time.Second || d > 20*time.Second {
		t.Errorf("walk took %s: DI2 should fail after about 2 s of the wrong reading", d)
	}
	sent := strings.Join(rep.Steps[0].Attempts[0].Sent, "\n")
	for _, s := range []string{"pt.start din ch=1,2,3,4,5,6,7,8 mode=level", "pt.stop din"} {
		if !strings.Contains(sent, s) {
			t.Errorf("sent %q, want %q among it", sent, s)
		}
	}
}

// Stop in the question box stops the walk.
func TestFullTestDIWalkStopsWhenThePersonStops(t *testing.T) {
	b := openSim(t)
	plan, err := ptplan.Load(filepath.Join("..", "..", "..", "src", "plans", "station6-poweron.json"))
	if err != nil {
		t.Fatal(err)
	}
	sim(b, "sim.din 0x00")
	e := &ptlib.Engine{
		Board: b, Plan: plan, Items: []ptitems.Item{catItem(t, "din.level")}, Hold: &fakeHold{b: b}, NoLive: true,
		Runner: func() *ptseq.Runner { return &ptseq.Runner{Board: b, OpenPeer: refusePeer, FindCDC: noCDC} },
		Ask: func(ctx context.Context, q ptlib.Question) (ptlib.Answer, error) {
			return ptlib.Answer{}, ptlib.ErrStopped
		},
	}
	rep, err := e.Run(context.Background(), []ptlib.Pick{{ID: "din.level", On: true, Peer: ptitems.PeerHuman}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if o := outcomes(rep)["din.level"]; o != ptreport.OutcomeStopped {
		t.Fatalf("din.level is %s, want STOPPED", o)
	}
}

// DO under load with a person (STG-32 A): the drop is asked while that one
// channel is on, judged by high-side-drop, then the channel goes off.
func TestDOLoadAsksTheDropWhileThatChannelIsOn(t *testing.T) {
	b := openSim(t)
	plan, err := ptplan.Load(filepath.Join("..", "..", "..", "src", "plans", "station6-poweron.json"))
	if err != nil {
		t.Fatal(err)
	}
	frames, unsub := b.Subscribe(1024)
	defer unsub()
	answers := map[string]string{"DO1": "0.5", "DO2": "1.5"}
	var asked []string
	e := &ptlib.Engine{
		Board: b, Plan: plan, Items: []ptitems.Item{catItem(t, "dout.load")}, Hold: &fakeHold{b: b},
		Runner: func() *ptseq.Runner { return &ptseq.Runner{Board: b, OpenPeer: refusePeer, FindCDC: noCDC} },
		Ask: func(ctx context.Context, q ptlib.Question) (ptlib.Answer, error) {
			asked = append(asked, q.Channel)
			n := strings.TrimPrefix(q.Channel, "DO")
			// The channel asked about must be on at the board right now.
			deadline := time.After(3 * time.Second)
			for {
				select {
				case ev := <-frames:
					if ev.Frame.Port == "dout" {
						if v, _ := ptproto.Get(ev.Frame.Fields, "ch"+n); v == "255" {
							return ptlib.Answer{Value: answers[q.Channel]}, nil
						}
					}
				case <-deadline:
					t.Errorf("asked about %s while it was not on", q.Channel)
					return ptlib.Answer{Value: answers[q.Channel]}, nil
				}
			}
		},
	}
	rep, err := e.Run(context.Background(), []ptlib.Pick{{ID: "dout.load", On: true, Peer: ptitems.PeerHuman,
		Params: map[string]string{"ch": "1,2"}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if o := outcomes(rep)["dout.load"]; o != ptreport.OutcomeFail {
		t.Fatalf("dout.load is %s, want FAIL: DO2 dropped 1.5 V", o)
	}
	if strings.Join(asked, ",") != "DO1,DO2" {
		t.Errorf("asked %v, want DO1 then DO2", asked)
	}
	d := stateOf(e, "dout.load").Drop
	if d == nil || len(d.Rows) != 2 || *d.Rows[0].Pass != true || *d.Rows[1].Pass != false || d.Max != "1" {
		t.Fatalf("card %+v, want DO1 within 1 V and DO2 not", d)
	}
	sent := strings.Join(rep.Steps[0].Attempts[0].Sent, "\n")
	for _, want := range []string{"pt.hold", "pt.start dout mode=hold ch=1 duty=1:255", "pt.start dout mode=hold ch=2 duty=2:255"} {
		if !strings.Contains(sent, want) {
			t.Errorf("sent %q, want %q among it", sent, want)
		}
	}
	if strings.Count(sent, "pt.stop dout") < 2 {
		t.Errorf("each channel is switched off after its reading; sent %q", sent)
	}
}
