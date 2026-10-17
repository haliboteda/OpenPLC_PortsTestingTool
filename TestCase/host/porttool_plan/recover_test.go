package testcase

// The recover items (decision 150, PORTTOOL-FLOW.md C.3.6 「断开恢复」) and
// the failure codes of the item runner's report (C.3.6 「失败码」).
//
// Bench: the simulated board, cut by sim.cut instead of a person; a scripted
// board closed and replaced for the RS232 adapter being pulled. Neither pulls
// a real cable.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"PortTool/src/ptboard"
	"PortTool/src/ptcheck"
	"PortTool/src/ptitems"
	"PortTool/src/ptlib"
	"PortTool/src/ptplan"
	"PortTool/src/ptreport"
	"PortTool/src/ptseq"
	"PortTool/src/simboard"
)

// askLog records the questions and leaves each up until the run takes it down,
// as a wait question with nobody pressing stop.
type askLog struct {
	mu    sync.Mutex
	walks []string
	auto  bool
	on    func(q ptlib.Question)
}

func (a *askLog) ask(ctx context.Context, q ptlib.Question) (ptlib.Answer, error) {
	a.mu.Lock()
	a.walks = append(a.walks, q.Walk)
	a.auto = a.auto || q.Auto
	on := a.on
	a.mu.Unlock()
	if on != nil {
		on(q)
	}
	<-ctx.Done()
	return ptlib.Answer{}, ctx.Err()
}

func (a *askLog) got() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.walks...)
}

func recoverItem(t *testing.T, port string) ptitems.Item {
	t.Helper()
	for _, it := range ptitems.Catalogue {
		if it.Recover && it.Port == port {
			return it
		}
	}
	t.Fatalf("no recover item for %s", port)
	return ptitems.Item{}
}

func valuesOf(st ptlib.State) map[string]string {
	out := map[string]string{}
	for _, kv := range st.Values {
		out[kv[0]] = kv[1]
	}
	return out
}

var recoverPorts = []string{"eth", "usb", "rs232", "rs485", "can", "knx"}

// shippedPlan is one of the plans shipped with PortTool, the six recover
// steps dropped when strip is set.
func shippedPlan(t *testing.T, name string, strip bool) ptplan.Plan {
	t.Helper()
	plan, err := ptplan.Load(filepath.Join("..", "..", "..", "src", "plans", name))
	if err != nil {
		t.Fatal(err)
	}
	if strip {
		var keep []ptplan.Step
		for _, st := range plan.Steps {
			if !strings.HasSuffix(st.ID, "-recover") {
				keep = append(keep, st)
			}
		}
		plan.Steps = keep
	}
	return plan
}

// simRecover runs one port's recover item on the simulated board, which cuts
// the port itself, judged by plan; it checks the walk and returns the report
// and the card's last state.
func simRecover(t *testing.T, port string, plan ptplan.Plan) (ptreport.Report, ptlib.State) {
	t.Helper()
	b, rw := openSimRaw(t)
	caps, err := b.Caps()
	if err != nil {
		t.Fatal(err)
	}
	asks := &askLog{}
	var last ptlib.State
	var mu sync.Mutex
	e := &ptlib.Engine{
		Board: b, Caps: caps, Plan: plan, Items: ptlib.ForPlan([]ptitems.Item{recoverItem(t, port)}, plan), RD: true,
		Runner: func() *ptseq.Runner {
			return &ptseq.Runner{Board: b, OpenPeer: refusePeer, FindCDC: noCDC, Sleep: func(time.Duration) {}}
		},
		Ask: asks.ask,
		Cut: func(p string, ms int) error { return simboard.Cut(rw, p, ms) },
		Update: func(st ptlib.State) {
			mu.Lock()
			last = st
			mu.Unlock()
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	rep, err := e.Run(ctx, []ptlib.Pick{{ID: port + ".recover", On: true}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	st := last
	mu.Unlock()
	v := valuesOf(st)
	t.Logf("%s.recover %s %v", port, rep.Steps[0].Outcome, v)
	if w := asks.got(); len(w) != 2 || w[0] != "recover_pull" || w[1] != "recover_plug" || !asks.auto {
		t.Errorf("questions %v (auto %v), want pull then plug, both saying the board cuts itself", w, asks.auto)
	}
	if v["recover_ms"] == "" || v["down_ms"] == "" || v["lost"] == "" || v["reset"] != "0" {
		t.Errorf("values %v, want recover_ms, down_ms, lost and reset 0", v)
	}
	if port == "rs232" && v["cause"] != "PIN" {
		t.Errorf("RS232 reads the reset cause after it is back: %v", v)
	}
	return rep, st
}

// A plan without the port's recover step leaves the item recorded only, with
// no failure code and no part in the port verdict (PORTTOOL-FLOW.md C.3.6 「断开恢复」).
func TestRecoverItemsWithoutCriteriaRecord(t *testing.T) {
	plan := shippedPlan(t, "station6-poweron.json", true)
	for _, port := range recoverPorts {
		t.Run(port, func(t *testing.T) {
			t.Parallel()
			rep, st := simRecover(t, port, plan)
			if got := rep.Steps[0].Outcome; got != ptlib.Recorded {
				t.Fatalf("%s.recover: %s %s %s, want RECORDED", port, got, st.Reason, st.Detail)
			}
			if rep.Steps[0].FailCode != "" || len(rep.FailCodes) != 0 || len(st.Checks) != 0 {
				t.Errorf("a recorded item has no failure code or checks: %q %v %v", rep.Steps[0].FailCode, rep.FailCodes, st.Checks)
			}
			it := ptlib.ForPlan([]ptitems.Item{recoverItem(t, port)}, plan)[0]
			if !ptlib.RecordOnly(it) || it.Pending != ptitems.RecoverPending {
				t.Errorf("without its step the item is record-only %v, pending %q", ptlib.RecordOnly(it), it.Pending)
			}
			if v := ptlib.PortVerdicts([]ptitems.Item{it}, []ptlib.State{st})[port]; v != "" {
				t.Errorf("port verdict %q, want none from a recorded recover item", v)
			}
		})
	}
}

// Both shipped criteria plans judge every port's recover item by the same
// provisional limits, and the simulated board passes them (decision 150).
func TestRecoverItemsJudgedByTheShippedPlans(t *testing.T) {
	for _, name := range []string{"station6-poweron.json", "station6-poweron-relaxed.json"} {
		plan := shippedPlan(t, name, false)
		for _, port := range recoverPorts {
			t.Run(name+"/"+port, func(t *testing.T) {
				t.Parallel()
				rep, st := simRecover(t, port, plan)
				if got := rep.Steps[0].Outcome; got != ptreport.OutcomePass {
					t.Fatalf("%s.recover: %s %s %s, want PASS; checks %v", port, got, st.Reason, st.Detail, st.Checks)
				}
				var got []string
				for _, c := range st.Checks {
					got = append(got, c.Check.Describe())
				}
				if strings.Join(got, "; ") != "recover_ms <= 10000 ms; reset eq 0" {
					t.Errorf("checks %v, want recover_ms <= 10000 ms and reset eq 0", got)
				}
				if rep.Steps[0].FailCode != "" {
					t.Errorf("a pass has no failure code: %q", rep.Steps[0].FailCode)
				}
				it := ptlib.ForPlan([]ptitems.Item{recoverItem(t, port)}, plan)[0]
				if v := ptlib.PortVerdicts([]ptitems.Item{it}, []ptlib.State{st})[port]; v != ptlib.PortPass {
					t.Errorf("port verdict %q, want pass: a judged recover item counts", v)
				}
			})
		}
	}
}

// A recover item over its limit fails, coded by the first failed check.
func TestRecoverItemFailsItsLimit(t *testing.T) {
	plan := shippedPlan(t, "station6-poweron.json", false)
	for i, st := range plan.Steps {
		if st.ID == "eth-recover" {
			limit := -1.0
			plan.Steps[i].Checks = append([]ptcheck.Check(nil), st.Checks...)
			plan.Steps[i].Checks[0].Max = &limit
		}
	}
	rep, st := simRecover(t, "eth", plan)
	if got := rep.Steps[0].Outcome; got != ptreport.OutcomeFail {
		t.Fatalf("eth.recover: %s, want FAIL; checks %v", got, st.Checks)
	}
	if rep.Steps[0].FailCode != "eth.recover.recover_ms-range" || strings.Join(rep.FailCodes, " ") != "eth.recover.recover_ms-range" {
		t.Errorf("fail_code %q, fail_codes %v, want eth.recover.recover_ms-range", rep.Steps[0].FailCode, rep.FailCodes)
	}
	it := ptlib.ForPlan([]ptitems.Item{recoverItem(t, "eth")}, plan)[0]
	if v := ptlib.PortVerdicts([]ptitems.Item{it}, []ptlib.State{st})["eth"]; v != ptlib.PortFail {
		t.Errorf("port verdict %q, want fail", v)
	}
}

// Pulling the RS232 adapter closes the control port: the run opens it again
// under its name, follows the frames on the new connection and reads the reset
// cause from there.
func TestRecoverRS232ReopensTheControlPort(t *testing.T) {
	stream := func(s *scriptBoard, from int, frame func(i int) string) chan struct{} {
		stop := make(chan struct{})
		go func() {
			for i := from; ; i++ {
				select {
				case <-stop:
					return
				case <-time.After(80 * time.Millisecond):
				}
				s.emit(frame(i))
			}
		}()
		return stop
	}
	var stopFirst chan struct{}
	first := newScriptBoard(t, nil)
	first.handler = func(cmd string, _ int) ([]string, []string) {
		if lines, ok := standardReplies(cmd); ok {
			return lines, nil
		}
		if strings.HasPrefix(cmd, "pt.start rs232") {
			stopFirst = stream(first, 1, func(i int) string {
				return fmt.Sprintf("!rs232 t=%d seq=%d rx=%d miss=0", 1000+500*i, i+1, i)
			})
			return []string{"OK started rs232"}, nil
		}
		return []string{"ERR unknown " + cmd}, nil
	}
	b1 := ptboard.New(first, 0)
	t.Cleanup(func() { b1.Close() })

	second := newScriptBoard(t, func(cmd string, _ int) ([]string, []string) {
		if lines, ok := standardReplies(cmd); ok {
			return lines, nil
		}
		if cmd == "pt.run reset.cause" {
			return []string{"OK reset.cause cause=PIN rsr=0x04000000"}, nil
		}
		return []string{"ERR unknown " + cmd}, nil
	})
	// The board kept running: its seq stood still while nothing came back.
	stopSecond := make(chan struct{})
	defer func() { close(stopSecond) }()
	streamSecond := func() {
		stopSecond = stream(second, 40, func(i int) string {
			if i < 43 {
				return fmt.Sprintf("!rs232 t=%d seq=5 rx=4 miss=%d", 1000+500*i, i-30)
			}
			return fmt.Sprintf("!rs232 t=%d seq=%d rx=%d miss=0", 1000+500*i, i-37, i-38)
		})
	}
	b2 := ptboard.New(second, 0)
	t.Cleanup(func() { b2.Close() })

	asks := &askLog{}
	asks.on = func(q ptlib.Question) {
		if q.Walk == "recover_pull" { // the person pulls the adapter
			close(stopFirst)
			_ = first.Close()
		}
	}
	tries := 0
	var e *ptlib.Engine
	var last ptlib.State
	var mu sync.Mutex
	e = &ptlib.Engine{
		Board: b1, Plan: ptplan.Plan{Name: "t"}, Items: []ptitems.Item{recoverItem(t, "rs232")}, RD: true,
		Runner: func() *ptseq.Runner { return &ptseq.Runner{Board: b1, Sleep: func(time.Duration) {}} },
		Ask:    asks.ask,
		Reconnect: func(ctx context.Context) (*ptboard.Board, error) {
			tries++
			if tries == 1 {
				return nil, errors.New("not plugged back yet")
			}
			e.Runner = func() *ptseq.Runner { return &ptseq.Runner{Board: b2, Sleep: func(time.Duration) {}} }
			streamSecond()
			return b2, nil
		},
		Update: func(st ptlib.State) {
			mu.Lock()
			last = st
			mu.Unlock()
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rep, err := e.Run(ctx, []ptlib.Pick{{ID: "rs232.recover", On: true}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	st := last
	mu.Unlock()
	v := valuesOf(st)
	if got := rep.Steps[0].Outcome; got != ptlib.Recorded {
		t.Fatalf("rs232.recover: %s %s %s, want RECORDED; values %v", got, st.Reason, st.Detail, v)
	}
	if tries < 2 || e.Board != b2 {
		t.Errorf("reopened %d times, board swapped %v: want a retry and the new connection", tries, e.Board == b2)
	}
	if w := asks.got(); len(w) != 2 || w[0] != "recover_pull" || w[1] != "recover_plug" {
		t.Errorf("questions %v, want pull then plug", w)
	}
	if v["cause"] != "PIN" || v["reset"] != "0" || v["lost"] != "12" || v["recover_ms"] == "" {
		t.Errorf("values %v, want the reset cause PIN, reset 0, frames lost and a recovery time", v)
	}
	if !second.sawCommand("pt.stop rs232") {
		t.Errorf("the session was not stopped on the new connection: %v", second.commands())
	}
}

// The item runner's report spells a code for each item that did not pass and
// sums them up on top (decision 150).
func TestItemRunnerReportCarriesFailCodes(t *testing.T) {
	b, _ := libBoard(t, "!din t=100 v=0x00", "!din t=200 v=0x00", "!din t=300 v=0x00")
	e, _ := libEngine(t, b, ptlib.Answer{Yes: false})
	rep, err := e.Run(context.Background(), []ptlib.Pick{pick("din.level", true), pick("led.blink", true), pick("sd.probe", true)}, 0)
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]string{}
	for _, s := range rep.Steps {
		codes[s.ID] = s.FailCode
	}
	want := map[string]string{"din.level": "din.level.v-eq", "led.blink": "led.blink.operator_no", "sd.probe": "", "temp.read": ""}
	for id, c := range want {
		if codes[id] != c {
			t.Errorf("%s: fail_code %q, want %q", id, codes[id], c)
		}
	}
	if strings.Join(rep.FailCodes, " ") != "din.level.v-eq led.blink.operator_no" {
		t.Errorf("fail_codes %v, want the two codes in catalogue order", rep.FailCodes)
	}
	_ = ptreport.OutcomeFail
}
