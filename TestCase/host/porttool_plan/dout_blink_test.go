package testcase

// DO toggle count and PWM (STG-33, PORTTOOL-FLOW.md C.3.6 "DO 翻转计数 / DO 方波"):
// dout.blink is counted by a person, judged within one of the six toggles
// (decision 133: R&D is done by hand); dout.square asks the person while the
// PWM is still on.
//
// Bench: a scripted fake board. It sees no real 24 V edges and no real scope.

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"PortTool/src/ptboard"
	"PortTool/src/ptitems"
	"PortTool/src/ptlib"
	"PortTool/src/ptplan"
	"PortTool/src/ptreport"
	"PortTool/src/ptseq"
)

func doutEngine(t *testing.T, dut, ref *ptboard.Board, id string, ask func(ptlib.Question) ptlib.Answer) *ptlib.Engine {
	t.Helper()
	plan, err := ptplan.Load(filepath.Join("..", "..", "..", "src", "plans", "station6-poweron.json"))
	if err != nil {
		t.Fatal(err)
	}
	return &ptlib.Engine{
		Board: dut, Plan: plan, Items: []ptitems.Item{catalogueItem(t, id)}, Hold: &fakeHold{b: dut},
		Runner: func() *ptseq.Runner {
			return &ptseq.Runner{Board: dut, Ref: ref, OpenPeer: refusePeer, FindCDC: noCDC}
		},
		Ask: func(ctx context.Context, q ptlib.Question) (ptlib.Answer, error) { return ask(q), nil },
	}
}

// doutBoard answers a dout session with a frame per toggle.
func doutBoard(t *testing.T) (*ptboard.Board, *scriptBoard) {
	t.Helper()
	fake := newScriptBoard(t, func(cmd string, nth int) ([]string, []string) {
		if lines, ok := standardReplies(cmd); ok {
			return lines, nil
		}
		switch {
		case strings.HasPrefix(cmd, "pt.start dout"):
			var frames []string
			for i := 1; i <= 8; i++ {
				frames = append(frames, "!dout t=100 seq=1 rx=0 miss=0 mode=blink res=8 tick=256000 ch1=255")
			}
			return []string{"OK dout started"}, frames
		case strings.HasPrefix(cmd, "pt.stop"):
			return []string{"OK stopped"}, nil
		}
		return []string{"ERR unknown " + cmd}, nil
	})
	b := ptboard.New(fake, 0)
	t.Cleanup(func() { b.Close() })
	return b, fake
}

func TestDoutBlinkCountedByAPersonIsJudgedWithinOne(t *testing.T) {
	for _, c := range []struct {
		typed string
		want  ptreport.Outcome
	}{{"6", ptreport.OutcomePass}, {"5", ptreport.OutcomePass}, {"3", ptreport.OutcomeFail}} {
		t.Run(c.typed, func(t *testing.T) {
			b, _ := doutBoard(t)
			e := doutEngine(t, b, nil, "dout.blink", func(ptlib.Question) ptlib.Answer { return ptlib.Answer{Value: c.typed} })
			rep, err := e.Run(context.Background(), []ptlib.Pick{{ID: "dout.blink", On: true, Peer: ptitems.PeerHuman}}, 0)
			if err != nil {
				t.Fatal(err)
			}
			if o := outcomes(rep)["dout.blink"]; o != c.want {
				t.Fatalf("typed %s: %s, want %s", c.typed, o, c.want)
			}
		})
	}
}

func TestDoutSquareAsksWhileThePWMIsOn(t *testing.T) {
	b, fake := doutBoard(t)
	var mu sync.Mutex
	var seen []string
	e := doutEngine(t, b, nil, "dout.square", func(q ptlib.Question) ptlib.Answer {
		mu.Lock()
		seen = append([]string(nil), fake.commands()...)
		mu.Unlock()
		return ptlib.Answer{Yes: true}
	})
	rep, err := e.Run(context.Background(), []ptlib.Pick{{ID: "dout.square", On: true, Peer: ptitems.PeerHuman}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if o := outcomes(rep)["dout.square"]; o != ptreport.OutcomePass {
		t.Fatalf("dout.square answered yes is %s, want PASS", o)
	}
	mu.Lock()
	defer mu.Unlock()
	started, stopped := false, false
	for _, c := range seen {
		started = started || strings.HasPrefix(c, "pt.start dout")
		stopped = stopped || strings.HasPrefix(c, "pt.stop dout")
	}
	if !started || stopped {
		t.Fatalf("when asked, commands were %v: want the PWM started and not yet stopped", seen)
	}
	if !fake.sawCommand("pt.stop dout") {
		t.Error("the PWM was not stopped after the answer")
	}
}
