package testcase

// The indicator item (PORTTOOL-FLOW.md C.3.6 "指示灯"): the six pulses of
// pt.run led.blink are over before the question appears, so the PC keeps the
// lamp blinking with pt.led while the person is asked, and leaves it off after.
//
// Bench: a scripted fake board. It cannot show whether LED3 really lights.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"PortTool/src/ptboard"
	"PortTool/src/ptitems"
	"PortTool/src/ptlib"
	"PortTool/src/ptreport"
)

func TestIndicatorKeepsBlinkingWhileThePersonIsAsked(t *testing.T) {
	fake := newScriptBoard(t, func(cmd string, nth int) ([]string, []string) {
		if lines, ok := standardReplies(cmd); ok {
			return lines, nil
		}
		switch {
		case cmd == "pt.run led.blink":
			return []string{"OK led.blink pin=PE2 pulses=6 half_ms=250 observed=unknown"}, nil
		case strings.HasPrefix(cmd, "pt.led fault="):
			return []string{"OK led " + strings.TrimPrefix(cmd, "pt.led ")}, nil
		}
		return []string{"ERR unknown " + cmd}, nil
	})
	b := ptboard.New(fake, 0)
	t.Cleanup(func() { b.Close() })

	var mu sync.Mutex
	var during []string
	e := doutEngine(t, b, nil, "led.blink", func(ptlib.Question) ptlib.Answer {
		time.Sleep(1200 * time.Millisecond) // long enough for the lamp to turn on and off
		mu.Lock()
		during = append([]string(nil), fake.commands()...)
		mu.Unlock()
		return ptlib.Answer{Yes: true}
	})
	rep, err := e.Run(context.Background(), []ptlib.Pick{{ID: "led.blink", On: true, Peer: ptitems.PeerHuman}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if o := outcomes(rep)["led.blink"]; o != ptreport.OutcomePass {
		t.Fatalf("led.blink answered yes is %s, want PASS", o)
	}
	mu.Lock()
	defer mu.Unlock()
	on, off := 0, 0
	for _, c := range during {
		switch c {
		case "pt.led fault=1":
			on++
		case "pt.led fault=0":
			off++
		}
	}
	if on < 2 || off < 1 {
		t.Fatalf("while asked the lamp was driven %v: want it switched on and off repeatedly", during)
	}
	all := fake.commands()
	if all[len(all)-1] != "pt.led fault=0" {
		t.Errorf("after the answer the last command was %q, want the lamp left off", all[len(all)-1])
	}
}
