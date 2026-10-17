package testcase

// The item runner behind the panel's top-right Start (src/ptlib):
// ticked items become plan steps, unticked ones are SKIPPED, and with a
// duration the once-only items run once first while the rest run together
// until time is up or the first failure. PORTTOOL-FLOW.md C.3.6 "测试项怎么跑".
//
// Bench: a scripted fake board and a fake deadman; no panel, no real clock
// beyond a few hundred milliseconds.

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"PortTool/src/ptboard"
	"PortTool/src/ptitems"
	"PortTool/src/ptlib"
	"PortTool/src/ptplan"
	"PortTool/src/ptproto"
	"PortTool/src/ptreport"
	"PortTool/src/ptseq"
)

const libPlanJSON = `{"schema":1,"name":"lib","limit_version":"t","steps":[
 {"id":"din-all","type":"PtSession","port":"din","frames":2,"timeout_ms":2000,"params":{"period":"100"},
  "checks":[{"field":"v","op":"eq","value":"0xFF"}]},
 {"id":"temp","type":"PtSession","port":"temp","frames":1,"timeout_ms":2000,
  "checks":[{"field":"ok","op":"eq","value":"1"},
   {"field":"ch1[1]","op":"range","min":0,"max":600},{"field":"ch2[1]","op":"range","min":0,"max":600}]},
 {"id":"sd-card","type":"PtRun","target":"sd.probe","timeout_ms":2000,
  "checks":[{"field":"detected","op":"eq","value":"1"}]},
 {"id":"sdram-stress","type":"PtRun","target":"sdram.sweep","timeout_ms":2000,
  "checks":[{"field":"mismatches","op":"eq","value":"0"}]}]}`

func libItem(port, id string, crit ...string) ptitems.Item {
	return ptitems.Item{ID: port + "." + id, Port: port, Peers: []ptitems.Peer{ptitems.PeerNone},
		Stages: []ptitems.Stage{ptitems.RD}, Criteria: crit}
}

func libItems() []ptitems.Item {
	din := libItem("din", "level", "din-all")
	din.Targets = []ptitems.Target{{Session: "din"}}
	din.Params, din.Exclusive = []string{"period"}, "din.mode"
	quad := libItem("din", "quad")
	quad.Targets, quad.Exclusive = []ptitems.Target{{Session: "din", Set: map[string]string{"mode": "quad"}}}, "din.mode"
	temp := libItem("temp", "read", "temp")
	sd := libItem("sd", "probe", "sd-card")
	sd.Once = true
	sweep := libItem("sdram", "sweep", "sdram-stress")
	led := libItem("led", "blink")
	led.Targets, led.Peers, led.Human = []ptitems.Target{{Run: "led.blink"}}, []ptitems.Peer{ptitems.PeerHuman}, ptitems.AnswerYesNo
	knx := libItem("knx", "bus")
	knx.Targets = []ptitems.Target{{Run: "knx.pullup"}}
	return []ptitems.Item{din, quad, temp, sd, sweep, led, knx}
}

// libBoard answers the commands these items send. dinFrames is what din
// pushes after pt.start.
func libBoard(t *testing.T, dinFrames ...string) (*ptboard.Board, *scriptBoard) {
	t.Helper()
	if dinFrames == nil {
		dinFrames = []string{"!din t=100 v=0xFF", "!din t=200 v=0xFF", "!din t=300 v=0xFF"}
	}
	fake := newScriptBoard(t, func(cmd string, nth int) ([]string, []string) {
		if lines, ok := standardReplies(cmd); ok {
			return lines, nil
		}
		switch {
		case strings.HasPrefix(cmd, "pt.start din"):
			return []string{"OK din started"}, dinFrames
		case cmd == "pt.start temp":
			return []string{"OK temp started"}, []string{"!temp t=100 ok=1 ch1=738/238 ch2=751/251", "!temp t=200 ok=1 ch1=738/238 ch2=751/251"}
		case cmd == "pt.run sd.probe":
			return []string{"OK sd.probe detected=1"}, nil
		case cmd == "pt.run sdram.sweep":
			time.Sleep(20 * time.Millisecond)
			return []string{"OK sdram.sweep mismatches=0"}, nil
		case cmd == "pt.run led.blink":
			return []string{"OK led.blink pulses=4"}, nil
		case cmd == "pt.run knx.pullup":
			return []string{"OK knx.pullup lost_on=0 lost_off=3"}, nil
		}
		return []string{"ERR unknown " + cmd}, nil
	})
	b := ptboard.New(fake, 0)
	t.Cleanup(func() { b.Close() })
	return b, fake
}

// fakeHold stands in for the panel's continuous run: it records what it was
// asked and releases the board the way hold.go does.
type fakeHold struct {
	b       *ptboard.Board
	mu      sync.Mutex
	started [][]string
	faults  []string
	stops   int
	ended   chan struct{}
	once    sync.Once
}

func (h *fakeHold) Start(ports []string, _ time.Duration) (<-chan struct{}, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.started = append(h.started, ports)
	h.ended = make(chan struct{})
	return h.ended, nil
}
func (h *fakeHold) Why() string { return "" }
func (h *fakeHold) release() {
	_, _ = h.b.Send("pt.stop all", ptboard.ExpectOne, time.Second)
	h.once.Do(func() { close(h.ended) })
}
func (h *fakeHold) Fault(why string) {
	h.mu.Lock()
	h.faults = append(h.faults, why)
	h.mu.Unlock()
	h.release()
}
func (h *fakeHold) Stop() {
	h.mu.Lock()
	h.stops++
	h.mu.Unlock()
	h.release()
}

func libEngine(t *testing.T, b *ptboard.Board, answer ptlib.Answer) (*ptlib.Engine, *fakeHold) {
	t.Helper()
	plan, err := ptplan.Parse([]byte(libPlanJSON))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	h := &fakeHold{b: b}
	return &ptlib.Engine{
		Board: b, Plan: plan, Items: libItems(), Hold: h,
		Runner: func() *ptseq.Runner {
			return &ptseq.Runner{Board: b, OpenPeer: refusePeer, FindCDC: noCDC, Sleep: func(time.Duration) {}}
		},
		Ask: func(ctx context.Context, q ptlib.Question) (ptlib.Answer, error) { return answer, nil },
	}, h
}

func pick(id string, on bool) ptlib.Pick { return ptlib.Pick{ID: id, On: on} }

func outcomes(rep ptreport.Report) map[string]ptreport.Outcome {
	out := map[string]ptreport.Outcome{}
	for _, s := range rep.Steps {
		out[s.ID] = s.Outcome
	}
	return out
}

func count(cmds []string, want string) int {
	n := 0
	for _, c := range cmds {
		if c == want {
			n++
		}
	}
	return n
}

func TestTickedItemsGiveOneResultEachAndUntickedAreSkipped(t *testing.T) {
	b, _ := libBoard(t)
	e, _ := libEngine(t, b, ptlib.Answer{})
	rep, err := e.Run(context.Background(), []ptlib.Pick{
		pick("din.level", true), pick("temp.read", true), pick("sd.probe", true), pick("sdram.sweep", false),
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := outcomes(rep)
	for _, id := range []string{"din.level", "temp.read", "sd.probe"} {
		if got[id] != ptreport.OutcomePass {
			t.Errorf("%s: %s, want PASS (%v)", id, got[id], rep.Steps)
		}
	}
	if got["sdram.sweep"] != ptreport.OutcomeSkipped {
		t.Errorf("unticked sdram.sweep: %s, want SKIPPED", got["sdram.sweep"])
	}
	// Items outside the picks are not in this run at all.
	if _, ok := got["led.blink"]; !ok {
		t.Errorf("an item in scope but not picked must still be reported: %v", got)
	}
}

func TestExclusiveItemsCannotBothBeTicked(t *testing.T) {
	b, _ := libBoard(t)
	e, _ := libEngine(t, b, ptlib.Answer{})
	_, err := e.Run(context.Background(), []ptlib.Pick{pick("din.level", true), pick("din.quad", true)}, 0)
	if pe, ok := err.(*ptlib.PickError); !ok || pe.Code != "exclusive" {
		t.Fatalf("want an exclusive PickError, got %v", err)
	}
}

func TestEditableParamGoesToTheBoardAndFixedOneIsRefused(t *testing.T) {
	b, fake := libBoard(t)
	e, _ := libEngine(t, b, ptlib.Answer{})
	p := pick("din.level", true)
	p.Params = map[string]string{"period": "250"}
	if _, err := e.Run(context.Background(), []ptlib.Pick{p}, 0); err != nil {
		t.Fatal(err)
	}
	if !fake.sawCommand("pt.start din period=250") {
		t.Errorf("the card's period did not replace the plan's: %v", fake.commands())
	}
	p.Params = map[string]string{"mode": "quad"}
	if _, err := e.Run(context.Background(), []ptlib.Pick{p}, 0); err == nil {
		t.Errorf("a parameter the catalogue does not list as editable was accepted")
	}
}

func TestPersonsNoFailsTheItem(t *testing.T) {
	b, _ := libBoard(t)
	e, _ := libEngine(t, b, ptlib.Answer{Yes: false})
	rep, err := e.Run(context.Background(), []ptlib.Pick{pick("led.blink", true)}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(rep)["led.blink"]; got != ptreport.OutcomeFail {
		t.Fatalf("led.blink with the answer no: %s, want FAIL", got)
	}
}

func TestOnceRunsOnceOthersRepeatAndSessionsRunTogether(t *testing.T) {
	b, fake := libBoard(t)
	e, h := libEngine(t, b, ptlib.Answer{})
	rep, err := e.Run(context.Background(), []ptlib.Pick{
		pick("sd.probe", true), pick("sdram.sweep", true), pick("din.level", true), pick("temp.read", true),
	}, 600*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	got := outcomes(rep)
	for _, id := range []string{"sd.probe", "sdram.sweep", "din.level", "temp.read"} {
		if got[id] != ptreport.OutcomePass {
			t.Errorf("%s: %s, want PASS", id, got[id])
		}
	}
	cmds := fake.commands()
	if n := count(cmds, "pt.run sd.probe"); n != 1 {
		t.Errorf("once-only sd.probe ran %d times, want 1", n)
	}
	if n := count(cmds, "pt.run sdram.sweep"); n < 2 {
		t.Errorf("sdram.sweep ran %d times within the duration, want it repeated", n)
	}
	// Both sessions start before anything is stopped: concurrent, not in turn.
	firstStop := len(cmds)
	for i, c := range cmds {
		if strings.HasPrefix(c, "pt.stop") {
			firstStop = i
			break
		}
	}
	for _, want := range []string{"pt.start din period=100", "pt.start temp"} {
		i := indexOf(cmds, want)
		if i < 0 || i > firstStop {
			t.Errorf("%q at %d, first stop at %d: %v", want, i, firstStop, cmds)
		}
	}
	if len(h.started) != 1 || strings.Join(h.started[0], ",") != "din,temp" || h.stops != 1 {
		t.Errorf("deadman: started %v, stops %d; want one hold over din,temp ended normally", h.started, h.stops)
	}
}

func TestFirstFailureStopsTheTimedRun(t *testing.T) {
	b, _ := libBoard(t, "!din t=100 v=0xFF", "!din t=200 v=0xFF", "!din t=300 v=0x00")
	e, h := libEngine(t, b, ptlib.Answer{})
	start := time.Now()
	rep, err := e.Run(context.Background(), []ptlib.Pick{
		pick("sd.probe", true), pick("din.level", true), pick("temp.read", true),
	}, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the run went on for %s after din failed", took)
	}
	got := outcomes(rep)
	if got["din.level"] != ptreport.OutcomeFail || got["temp.read"] != ptreport.OutcomeStopped ||
		got["sd.probe"] != ptreport.OutcomePass {
		t.Errorf("outcomes %v; want din FAIL, temp STOPPED, sd.probe PASS", got)
	}
	if len(h.faults) != 1 || !strings.Contains(h.faults[0], "din.level") {
		t.Errorf("fault lamp: %v, want one naming din.level", h.faults)
	}
}

func TestKNXDiagnosticsDoNotDecideThePort(t *testing.T) {
	items := append(libItems(), libItem("knx", "loop", "knx"))
	v := ptlib.PortVerdicts(items, []ptlib.State{
		{ID: "knx.bus", Port: "knx", Outcome: ptlib.Recorded},
		{ID: "din.level", Port: "din", Outcome: ptreport.OutcomePass},
		{ID: "din.quad", Port: "din", Outcome: ptlib.Recorded},
		{ID: "temp.read", Port: "temp", Outcome: ptreport.OutcomeFail},
	})
	if _, ok := v["knx"]; ok {
		t.Errorf("knx has only a record-only diagnostic and must have no verdict: %v", v)
	}
	if v["din"] != ptlib.PortUnjudged || v["temp"] != ptlib.PortFail {
		t.Errorf("verdicts %v; want din unjudged (one item has no criteria), temp fail", v)
	}
	v = ptlib.PortVerdicts(items, []ptlib.State{
		{ID: "knx.bus", Port: "knx", Outcome: ptlib.Recorded},
		{ID: "knx.loop", Port: "knx", Outcome: ptreport.OutcomePass},
	})
	if v["knx"] != ptlib.PortPass {
		t.Errorf("knx: %q; the judged loop passed and the diagnostic only recorded, want pass", v["knx"])
	}
	frames := libItem("knx", "frames")
	v = ptlib.PortVerdicts(append(items, frames), []ptlib.State{
		{ID: "knx.frames", Port: "knx", Outcome: ptreport.OutcomeStopped},
		{ID: "knx.loop", Port: "knx", Outcome: ptreport.OutcomeSkipped},
	})
	if _, ok := v["knx"]; ok {
		t.Errorf("a frames session stopped by hand is still only a diagnostic, want no knx verdict: %v", v)
	}
}

func indexOf(v []string, want string) int {
	for i, s := range v {
		if s == want {
			return i
		}
	}
	return -1
}

// libCaps is capsReply plus the indicator LED, so the panel has an item that
// asks the person.
func libCaps() []string {
	body := append(capsReply()[1:], "OK port=led board=bridge kind=run blk=- term=PE2 channels=1 loop=none runs=led.blink")
	return append([]string{"OK porttool=0.9.0 ports=4 lines=" + strconv.Itoa(len(body))}, body...)
}

// waitLib polls /api/lib until cond holds.
func waitLib(t *testing.T, srv *httptest.Server, what string, cond func(map[string]any) bool) map[string]any {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if j := getJSON(t, srv, "/api/lib"); cond(j) {
			return j
		}
	}
	t.Fatalf("timed out waiting for %s: %v", what, getJSON(t, srv, "/api/lib"))
	return nil
}

func TestPanelStartRunsTickedItemsAndAsksThePerson(t *testing.T) {
	srv, dir := planPanel(t, func(cmd string, nth int) ([]string, []string) {
		switch cmd {
		case "pt.caps":
			return libCaps(), nil
		case "pt.run sdram.probe":
			return []string{"OK sdram.probe ready=1"}, nil
		case "pt.run led.blink":
			return []string{"OK led.blink pulses=4"}, nil
		}
		lines, _ := standardReplies(cmd)
		return lines, nil
	})
	plan := `{"schema":1,"name":"bench","limit_version":"t","steps":[
	 {"id":"sdram-buses","type":"PtRun","target":"sdram.probe","checks":[{"field":"ready","op":"eq","value":"1"}]},
	 {"id":"sdram-stress","type":"PtRun","target":"sdram.sweep","checks":[{"field":"mismatches","op":"eq","value":"0"}]},
	 {"id":"indicator-drive","type":"PtRun","target":"led.blink","checks":[{"field":"pulses","op":"eq","value":"4"}]}]}`
	if err := os.WriteFile(filepath.Join(dir, "station6-poweron.json"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	postJSON(t, srv, "/api/connect", map[string]any{"port": "fake"})

	start := postJSON(t, srv, "/api/lib/start", map[string]any{"stage": "rd", "duration": "once", "items": []any{
		map[string]any{"id": "sdram.probe", "on": true},
		map[string]any{"id": "sdram.sweep", "on": false},
		map[string]any{"id": "led.blink", "on": true},
	}})
	if start["started"] != true {
		t.Fatalf("start refused: %v", start)
	}
	ask := waitLib(t, srv, "the question", func(j map[string]any) bool { return j["ask"] != nil })["ask"].(map[string]any)
	if ask["item"] != "led.blink" || ask["kind"] != "yes_no" {
		t.Fatalf("asked %v, want led.blink yes/no", ask)
	}
	if r := postJSON(t, srv, "/api/ask", map[string]any{"id": ask["id"], "value": "3"}); r["error"] == nil {
		t.Errorf("a yes/no question took a number: %v", r)
	}
	postJSON(t, srv, "/api/ask", map[string]any{"id": ask["id"], "yes": true})
	j := waitLib(t, srv, "the end of the run", func(j map[string]any) bool { return j["running"] == false })

	st := j["states"].(map[string]any)
	want := map[string]string{"sdram.probe": "PASS", "sdram.sweep": "SKIPPED", "led.blink": "PASS"}
	for id, o := range want {
		if got := st[id].(map[string]any)["outcome"]; got != o {
			t.Errorf("%s: %v, want %s", id, got, o)
		}
	}
	if j["ports"].(map[string]any)["sdram"] != "pass" {
		t.Errorf("port verdicts %v, want sdram pass", j["ports"])
	}
}

func TestPanelStartRefusesTwoOfOneExclusiveGroup(t *testing.T) {
	srv, _ := planPanel(t, plainBoard)
	postJSON(t, srv, "/api/connect", map[string]any{"port": "fake"})
	r := postJSON(t, srv, "/api/lib/start", map[string]any{"stage": "rd", "duration": "once", "items": []any{
		map[string]any{"id": "din.level", "on": true}, map[string]any{"id": "din.quad", "on": true},
	}})
	if r["error"] == nil || getJSON(t, srv, "/api/lib")["running"] == true {
		t.Fatalf("two items of one exclusive group started: %v", r)
	}
}

// knx.frames stays open its WatchS, lists the event frames on the card and
// keeps the counters from the periodic frame (decision 105).
func TestWatchedSessionListsItsEventsForItsWholeWindow(t *testing.T) {
	fake := newScriptBoard(t, func(cmd string, nth int) ([]string, []string) {
		if lines, ok := standardReplies(cmd); ok {
			return lines, nil
		}
		if strings.HasPrefix(cmd, "pt.start knx") {
			return []string{"OK knx started"}, []string{
				// Events first: on the simulated board the periodic frame comes late.
				"!knx.tx t=150 n=9 dst=31/7/255 val=1",
				"!knx.rx t=160 n=9 crc=raw dst=31/7/255 val=1",
				"!knx t=1100 mode=frames crc_raw=1 crc_bad=0",
			}
		}
		return []string{"ERR unknown " + cmd}, nil
	})
	b := ptboard.New(fake, 0)
	t.Cleanup(func() { b.Close() })
	e, _ := libEngine(t, b, ptlib.Answer{})
	frames := libItem("knx", "frames")
	frames.Targets = []ptitems.Target{{Session: "knx", Set: map[string]string{"mode": "frames"}}}
	frames.WatchS = 1
	e.Items = []ptitems.Item{frames}

	began := time.Now()
	rep, err := e.Run(context.Background(), []ptlib.Pick{pick("knx.frames", true)}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took < time.Second {
		t.Errorf("the session closed after %s, before its 1 s window", took)
	}
	if got := outcomes(rep)["knx.frames"]; got != ptlib.Recorded {
		t.Errorf("knx.frames: %s, want RECORDED", got)
	}
	st := e.States()[0]
	if len(st.Events) != 2 || !strings.HasPrefix(st.Events[0], "!knx.tx") || !strings.HasPrefix(st.Events[1], "!knx.rx") {
		t.Errorf("events on the card: %q, want the tx then the rx frame", st.Events)
	}
	vals := map[string]string{}
	for _, kv := range st.Values {
		vals[kv[0]] = kv[1]
	}
	if vals["crc_raw"] != "1" {
		t.Errorf("values: %v, want the last periodic frame's crc_raw=1", st.Values)
	}
}

// relayEngine runs relay.cycle against a fake board that answers each
// pt.start relay with the frames frames(cmd) gives (decision 123).
func relayEngine(t *testing.T, frames func(cmd string) []string) (*ptlib.Engine, *scriptBoard) {
	t.Helper()
	fake := newScriptBoard(t, func(cmd string, nth int) ([]string, []string) {
		if lines, ok := standardReplies(cmd); ok {
			return lines, nil
		}
		if strings.HasPrefix(cmd, "pt.start relay") {
			return []string{"OK started relay"}, frames(cmd)
		}
		return []string{"ERR unknown " + cmd}, nil
	})
	b := ptboard.New(fake, 0)
	t.Cleanup(func() { b.Close() })
	e, _ := libEngine(t, b, ptlib.Answer{})
	e.Ask = countsAsSet
	for _, it := range ptitems.Catalogue {
		if it.ID == "relay.cycle" {
			e.Items = []ptitems.Item{it}
		}
	}
	return e, fake
}

// countsAsSet is a person who saw each relay's load switch exactly its count (decision 130).
func countsAsSet(ctx context.Context, q ptlib.Question) (ptlib.Answer, error) {
	return ptlib.Answer{Value: strconv.FormatFloat(q.Set, 'f', -1, 64)}, nil
}

// Decision 130: a relay is judged by the cycles its load fed back, not the
// board's n. The reference board's count is chain_test.go's.
func TestRelayIsJudgedByTheLoadsCycles(t *testing.T) {
	frames := func(string) []string {
		return []string{"!relay t=1 seq=1 rx=0 miss=0 mode=cycle ch1=0 n1=2 done=1"}
	}
	p := pick("relay.cycle", true)
	p.Params = map[string]string{"ch": "1", "close": "1000", "open": "1000", "count": "2", "by": "gap"}
	e, _ := relayEngine(t, frames)
	var qs []ptlib.Question
	e.Ask = func(ctx context.Context, q ptlib.Question) (ptlib.Answer, error) {
		qs = append(qs, q)
		return ptlib.Answer{Value: "1"}, nil
	}
	rep, err := e.Run(context.Background(), []ptlib.Pick{p}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(rep)["relay.cycle"]; got != ptreport.OutcomeFail || len(qs) != 1 || qs[0].Walk != "relay" || qs[0].Set != 2 {
		t.Fatalf("the load switched once of 2: %s, asked %+v", got, qs)
	}
}

func relayExtra(rep ptreport.Report) map[string]string {
	for _, s := range rep.Steps {
		if s.ID == "relay.cycle" && len(s.Attempts) > 0 {
			return s.Attempts[0].Extra
		}
	}
	return nil
}

// Parallel: one mode=cycle session with every ticked relay, values that agree
// sent once; the board's per-relay counts reach the card and the report.
func TestRelayParallelIsOneSessionAndReportsEachCount(t *testing.T) {
	e, fake := relayEngine(t, func(string) []string {
		return []string{
			"!relay t=1 seq=1 rx=0 miss=0 mode=cycle ch1=0 n1=0 ch2=0 n2=0 done=0",
			"!relay t=2 seq=2 rx=1 miss=0 mode=cycle ch1=1 n1=0 ch2=1 n2=0 done=0",
			"!relay t=3 seq=3 rx=2 miss=0 mode=cycle ch1=0 n1=1 ch2=0 n2=1 done=0",
			"!relay t=4 seq=4 rx=3 miss=0 mode=cycle ch1=1 n1=1 ch2=0 n2=1 done=0",
			"!relay t=5 seq=5 rx=4 miss=0 mode=cycle ch1=0 n1=2 ch2=0 n2=1 done=1",
		}
	})
	p := pick("relay.cycle", true)
	p.Params = map[string]string{"ch": "1,2", "close": "1:1000,2:1000", "open": "1:1500,2:2000",
		"count": "1:2,2:1", "order": "par", "by": "gap"}
	rep, err := e.Run(context.Background(), []ptlib.Pick{p}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := "pt.start relay ch=1,2 mode=cycle close=1000 open=1:1500,2:2000 count=1:2,2:1"; !fake.sawCommand(want) {
		t.Errorf("start lines: %v, want %q", fake.commands(), want)
	}
	if got := outcomes(rep)["relay.cycle"]; got != ptreport.OutcomePass {
		t.Errorf("every load switched its count: %s, want PASS", got)
	}
	st := e.States()[0].Relay
	if st == nil || len(st.Chans) != 2 || st.Chans[0].N != 2 || st.Chans[1].N != 1 ||
		st.Chans[0].Phase != "done" || st.Chans[1].Phase != "done" {
		t.Errorf("card state: %+v, want relay 1 at 2 and relay 2 at 1, both done", st)
	}
	x := relayExtra(rep)
	if x["relay1_done"] != "2" || x["relay2_done"] != "1" || x["relay2_open_ms"] != "2000" || x["order"] != "par" {
		t.Errorf("report: %v, want each relay's values and reached count", x)
	}
	if !fake.sawCommand("pt.stop relay") {
		t.Errorf("session not stopped: %v", fake.commands())
	}
}

// Serial: one session per relay in relay order, the next after done=1.
func TestRelaySerialRunsOneRelayAfterTheOther(t *testing.T) {
	e, fake := relayEngine(t, func(cmd string) []string {
		ch := strings.TrimPrefix(strings.Fields(cmd)[2], "ch=")
		return []string{
			"!relay t=1 seq=1 rx=0 miss=0 mode=cycle ch" + ch + "=0 n" + ch + "=0 done=0",
			"!relay t=2 seq=2 rx=1 miss=0 mode=cycle ch" + ch + "=0 n" + ch + "=1 done=1",
		}
	})
	p := pick("relay.cycle", true)
	p.Params = map[string]string{"ch": "3,1", "close": "1000", "open": "1000", "count": "1", "order": "ser"}
	rep, err := e.Run(context.Background(), []ptlib.Pick{p}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var starts []string
	for _, c := range fake.commands() {
		if strings.HasPrefix(c, "pt.start relay") || c == "pt.stop relay" {
			starts = append(starts, c)
		}
	}
	want := []string{
		"pt.start relay ch=1 mode=cycle close=1000 open=1000 count=1", "pt.stop relay",
		"pt.start relay ch=3 mode=cycle close=1000 open=1000 count=1", "pt.stop relay",
	}
	if strings.Join(starts, "|") != strings.Join(want, "|") {
		t.Errorf("sessions: %v, want %v", starts, want)
	}
	if got := outcomes(rep)["relay.cycle"]; got != ptreport.OutcomePass {
		t.Errorf("every load switched its count: %s, want PASS", got)
	}
}

// Stopped early: the counts reached so far are what the report keeps.
func TestRelayStoppedEarlyKeepsTheCountsReached(t *testing.T) {
	e, _ := relayEngine(t, func(string) []string {
		return []string{"!relay t=1 seq=1 rx=0 miss=0 mode=cycle ch1=1 n1=3 done=0"}
	})
	p := pick("relay.cycle", true)
	p.Params = map[string]string{"ch": "1", "close": "1000", "open": "1000", "count": "500"}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			if st := e.States(); len(st) > 0 && st[0].Relay != nil && st[0].Relay.Chans[0].N == 3 {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	rep, err := e.Run(ctx, []ptlib.Pick{p}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(rep)["relay.cycle"]; got != ptreport.OutcomeStopped {
		t.Errorf("stopped early: %s, want STOPPED", got)
	}
	if x := relayExtra(rep); x["relay1_done"] != "3" || x["relay1_count"] != "500" {
		t.Errorf("report: %v, want 3 of 500", x)
	}
}

// Every ticked relay needs all three values; the floor comes from pt.caps.
func TestRelayRefusesMissingOrTooFastValues(t *testing.T) {
	e, fake := relayEngine(t, func(string) []string { return nil })
	caps, err := ptproto.ParseCaps([]string{
		"OK porttool=0.17.0 ports=1 lines=3",
		"OK port=relay board=lower kind=session blk=B term=B01-B12 channels=6 loop=ctrl params=ch,mode,on,close,open,count running=0",
		"OK vals=relay ch=1,2,3,4,5,6 mode=hold on=1:0,2:0,3:0,4:0,5:0,6:0 close=2000 open=2000 count=10",
		"OK limits=relay ch:1..6 mode:hold|cycle on:0..1 close:1000.. open:1000.. count:1..1000000",
	})
	if err != nil {
		t.Fatal(err)
	}
	e.Caps = caps
	for _, params := range []map[string]string{
		{"ch": "1,2", "close": "1000", "open": "1000", "count": "1:5"}, // relay 2 has no count
		{"ch": "", "close": "1000", "open": "1000", "count": "5"},      // nothing ticked
		{"ch": "1", "close": "999", "open": "1000", "count": "5"},      // below the floor
		{"ch": "1", "close": "1000", "open": "1000", "count": "5", "order": "both"},
	} {
		p := pick("relay.cycle", true)
		p.Params = params
		if _, err := e.Run(context.Background(), []ptlib.Pick{p}, 0); err == nil {
			t.Errorf("%v was accepted", params)
		}
	}
	for _, c := range fake.commands() {
		if strings.HasPrefix(c, "pt.start relay") {
			t.Errorf("a refused pick reached the board: %s", c)
		}
	}
}

// The board takes a PC line of up to 256 bytes (PORTTOOL-FLOW.md A.3): six
// different 7-digit values in parallel fit; values that make the line longer
// are refused before anything is sent (decision 123).
func TestRelayLineFitsTheBoardOrIsRefused(t *testing.T) {
	e, fake := relayEngine(t, func(cmd string) []string {
		return []string{"!relay t=1 seq=1 rx=0 miss=0 mode=cycle ch1=0 n1=1 ch2=0 n2=1 ch3=0 n3=1 ch4=0 n4=1 ch5=0 n5=1 ch6=0 n6=1 done=1"}
	})
	fits := map[string]string{"ch": "1,2,3,4,5,6", "order": "par",
		"close": "1:1000001,2:1000002,3:1000003,4:1000004,5:1000005,6:1000006",
		"open":  "1:2000001,2:2000002,3:2000003,4:2000004,5:2000005,6:2000006",
		"count": "1:1000000,2:999999,3:999998,4:999997,5:999996,6:999995"}
	p := pick("relay.cycle", true)
	p.Params = fits
	if _, err := e.Run(context.Background(), []ptlib.Pick{p}, 0); err != nil {
		t.Fatalf("six different 7-digit values refused: %v", err)
	}
	var sent string
	for _, c := range fake.commands() {
		if strings.HasPrefix(c, "pt.start relay") {
			sent = c
		}
	}
	if len(sent) <= 192 || len(sent) > 256 {
		t.Errorf("start line is %d bytes, want over 192 and within 256: %s", len(sent), sent)
	}

	long := map[string]string{}
	for k, v := range fits {
		long[k] = v
	}
	long["close"] = "1:4000000001,2:4000000002,3:4000000003,4:4000000004,5:4000000005,6:4000000006"
	long["open"] = "1:4000000011,2:4000000012,3:4000000013,4:4000000014,5:4000000015,6:4000000016"
	p.Params = long
	before := len(fake.commands())
	_, err := e.Run(context.Background(), []ptlib.Pick{p}, 0)
	var pe *ptlib.PickError
	if !errors.As(err, &pe) || pe.Code != "line_too_long" {
		t.Errorf("a line over 256 bytes: %v, want line_too_long", err)
	}
	for _, c := range fake.commands()[before:] {
		if strings.HasPrefix(c, "pt.start relay") {
			t.Errorf("an over-long line reached the board: %d bytes", len(c))
		}
	}
	long["order"] = "ser" // one relay per line fits again
	p.Params = long
	if _, err := e.Run(context.Background(), []ptlib.Pick{p}, 0); err != nil {
		t.Errorf("the same values run one by one were refused: %v", err)
	}
}

// With a duration the relay starts together with the timed items instead of
// first on its own, runs to its count, and is asked about once the whole run
// is over; when it outlasts the duration the run waits for it rather than
// cutting it short (decision 123, PORTTOOL-FLOW.md C.3.6 「选了时长」).
func TestTimedRelayRunsAlongsideAndTheRunWaitsForItsCount(t *testing.T) {
	var relayStart, relayDone time.Time
	var mu sync.Mutex
	var fake *scriptBoard
	fake = newScriptBoard(t, func(cmd string, nth int) ([]string, []string) {
		if lines, ok := standardReplies(cmd); ok {
			return lines, nil
		}
		switch {
		case cmd == "pt.start temp":
			return []string{"OK temp started"}, []string{"!temp t=100 ok=1 ch1=738/238 ch2=751/251", "!temp t=200 ok=1 ch1=738/238 ch2=751/251"}
		case strings.HasPrefix(cmd, "pt.start relay"):
			mu.Lock()
			relayStart = time.Now()
			mu.Unlock()
			// The count is reached well after the 300 ms duration.
			go func() {
				time.Sleep(900 * time.Millisecond)
				mu.Lock()
				relayDone = time.Now()
				mu.Unlock()
				fake.emit("!relay t=3 seq=3 rx=2 miss=0 mode=cycle ch1=0 n1=2 done=1")
			}()
			return []string{"OK started relay"}, []string{
				"!relay t=1 seq=1 rx=0 miss=0 mode=cycle ch1=1 n1=0 done=0",
				"!relay t=2 seq=2 rx=1 miss=0 mode=cycle ch1=0 n1=1 done=0",
			}
		}
		return []string{"ERR unknown " + cmd}, nil
	})
	b := ptboard.New(fake, 0)
	t.Cleanup(func() { b.Close() })
	e, h := libEngine(t, b, ptlib.Answer{Yes: true})
	var asked time.Time
	e.Ask = func(ctx context.Context, q ptlib.Question) (ptlib.Answer, error) {
		if q.Item == "relay.cycle" {
			mu.Lock()
			asked = time.Now()
			mu.Unlock()
		}
		return countsAsSet(ctx, q)
	}
	temp := libItem("temp", "read", "temp")
	e.Items = []ptitems.Item{temp}
	for _, it := range ptitems.Catalogue {
		if it.ID == "relay.cycle" {
			e.Items = append(e.Items, it)
		}
	}
	rp := pick("relay.cycle", true)
	rp.Params = map[string]string{"ch": "1", "close": "1000", "open": "1000", "count": "2",
		"by": "time", "minutes": "0.1", "ratio": "30"} // ratio: an old saved card, ignored

	began := time.Now()
	rep, err := e.Run(context.Background(), []ptlib.Pick{pick("temp.read", true), rp}, 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	took := time.Since(began)
	got := outcomes(rep)
	if got["temp.read"] != ptreport.OutcomePass || got["relay.cycle"] != ptreport.OutcomePass {
		t.Fatalf("outcomes %v; want temp PASS and relay PASS (its load switched its count)", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if took < 900*time.Millisecond || relayDone.IsZero() {
		t.Errorf("the run ended after %s, before the relay reached its count", took)
	}
	cmds := fake.commands()
	// Started together: both sessions are on before anything is stopped.
	firstStop := len(cmds)
	for i, c := range cmds {
		if strings.HasPrefix(c, "pt.stop") {
			firstStop = i
			break
		}
	}
	ti, ri := indexOf(cmds, "pt.start temp"), indexOf(cmds, "pt.start relay ch=1 mode=cycle close=1000 open=1000 count=2")
	if ti < 0 || ri < 0 || ti > firstStop || ri > firstStop {
		t.Errorf("temp at %d, relay at %d, first stop at %d: %v", ti, ri, firstStop, cmds)
	}
	if relayStart.Sub(began) > 200*time.Millisecond {
		t.Errorf("the relay started %s into the run, not with the timed items", relayStart.Sub(began))
	}
	if asked.Before(relayDone) {
		t.Errorf("asked about the relay before it reached its count")
	}
	// The temperature session ran on until the relay was done, not just for the duration.
	if i := indexOf(cmds, "pt.stop all"); i < 0 || h.stops != 1 {
		t.Errorf("deadman: stops %d, commands %v; want one normal end", h.stops, cmds)
	}
	if x := relayExtra(rep); x["relay1_done"] != "2" || x["relay1_count"] != "2" {
		t.Errorf("report: %v, want 2 of 2", x)
	}
}

// A failure elsewhere stops the relay too; it keeps the count it reached and
// is not asked about (decision 123: the first failure stops everything).
func TestTimedRelayStopsAtTheFirstFailure(t *testing.T) {
	var fake *scriptBoard
	fake = newScriptBoard(t, func(cmd string, nth int) ([]string, []string) {
		if lines, ok := standardReplies(cmd); ok {
			return lines, nil
		}
		switch {
		case cmd == "pt.start temp":
			go func() { // fails once the relay has counted one cycle
				time.Sleep(300 * time.Millisecond)
				fake.emit("!temp t=300 ok=0")
			}()
			return []string{"OK temp started"}, []string{"!temp t=100 ok=1 ch1=738/238 ch2=751/251", "!temp t=200 ok=1 ch1=738/238 ch2=751/251"}
		case strings.HasPrefix(cmd, "pt.start relay"):
			return []string{"OK started relay"}, []string{"!relay t=1 seq=1 rx=0 miss=0 mode=cycle ch1=1 n1=1 done=0"}
		}
		return []string{"ERR unknown " + cmd}, nil
	})
	b := ptboard.New(fake, 0)
	t.Cleanup(func() { b.Close() })
	e, h := libEngine(t, b, ptlib.Answer{Yes: true})
	asked := 0
	e.Ask = func(ctx context.Context, q ptlib.Question) (ptlib.Answer, error) {
		asked++
		return ptlib.Answer{Yes: true}, nil
	}
	e.Items = []ptitems.Item{libItem("temp", "read", "temp")}
	for _, it := range ptitems.Catalogue {
		if it.ID == "relay.cycle" {
			e.Items = append(e.Items, it)
		}
	}
	rp := pick("relay.cycle", true)
	rp.Params = map[string]string{"ch": "1", "close": "1000", "open": "1000", "count": "50"}
	rep, err := e.Run(context.Background(), []ptlib.Pick{pick("temp.read", true), rp}, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	got := outcomes(rep)
	if got["temp.read"] != ptreport.OutcomeFail || got["relay.cycle"] != ptreport.OutcomeStopped || asked != 0 {
		t.Errorf("outcomes %v, asked %d; want temp FAIL, relay STOPPED, nobody asked", got, asked)
	}
	if x := relayExtra(rep); x["relay1_done"] != "1" || x["relay1_count"] != "50" {
		t.Errorf("report: %v, want 1 of 50", x)
	}
	if len(h.faults) != 1 {
		t.Errorf("fault lamp: %v", h.faults)
	}
}
