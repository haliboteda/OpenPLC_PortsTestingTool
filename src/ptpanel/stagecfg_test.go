package ptpanel

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"PortTool/src/calstore"
	"PortTool/src/ptboard"
	"PortTool/src/ptitems"
	"PortTool/src/ptlib"
	"PortTool/src/ptproto"
	"PortTool/src/ptreport"
	"PortTool/src/ptstages"
)

// No board and no simulated board: the item runner and the timed part's clock
// are fakes. The real runner driving a board is covered by its own tests and T4-02.

func cfgGoldenCaps(t *testing.T) ptproto.Caps {
	t.Helper()
	raw, err := os.ReadFile("../../TestCase/host/porttool_caps/caps_golden.txt")
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.Split(strings.Split(string(raw), ">>> ")[1], "\n")[1:] {
		if l = strings.TrimRight(l, "\r"); l != "" && !strings.HasPrefix(l, "TEST ") {
			lines = append(lines, l)
		}
	}
	c, err := ptproto.ParseCaps(lines)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func useTempStageCfg(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "porttool_stagecfg.json")
	StageCfgFile = p
	t.Cleanup(func() { StageCfgFile = "" })
	return p
}

func postCfg(t *testing.T, h http.Handler, body any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/stagecfg", bytes.NewReader(b)))
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	return out
}

func getCfg(t *testing.T, h http.Handler) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/stagecfg", nil))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

func TestStageCfgSurvivesARestart(t *testing.T) {
	useTempStageCfg(t)
	off := false
	cfg := stageCfg{Duration: "2", PortsOff: map[string]bool{"knx": true},
		Items: map[string]itemCfg{"temp.read": {On: &off}, "aout.out1": {Peer: ptitems.PeerHuman, Points: []float64{4, 12, 20}},
			"rs485.peer": {Params: map[string]string{"baud": "9600"}}}}
	if out := postCfg(t, New().Handler(), map[string]any{"stage": "aging", "cfg": cfg}); out["error"] != nil {
		t.Fatalf("refused: %v", out["error"])
	}
	got := getCfg(t, New().Handler()) // a fresh server reads the file
	b, _ := json.Marshal(got["aging"])
	var back stageCfg
	_ = json.Unmarshal(b, &back)
	if back.Duration != "2" || !back.PortsOff["knx"] || back.Items["temp.read"].On == nil ||
		back.Items["rs485.peer"].Params["baud"] != "9600" || back.Items["aout.out1"].Peer != ptitems.PeerHuman ||
		len(back.Items["aout.out1"].Points) != 3 {
		t.Fatalf("not remembered: %s", b)
	}
	if f, _ := got["func"].(map[string]any); f["duration"] != "once" {
		t.Errorf("untouched functional config lost its default: %v", got["func"])
	}
}

func TestStageCfgRefusesBadConfigurations(t *testing.T) {
	path := useTempStageCfg(t)
	// A once-only item and a repeatable one of the same group run one after
	// the other: both ticked by default, and saving both is fine (decision 128).
	var f stageCfgs
	for _, id := range []string{"usb.info", "usb.xfer"} {
		it, _ := catalogueItem(id)
		if !f.Func.on(ptitems.Func, it) {
			t.Errorf("%s not ticked by default", id)
		}
	}
	on := true
	out := postCfg(t, New().Handler(), map[string]any{"stage": "func",
		"cfg": stageCfg{Items: map[string]itemCfg{"usb.info": {On: &on}, "usb.xfer": {On: &on}}}})
	if out["error"] != nil {
		t.Fatalf("USB enumeration and transfer together refused: %v", out["error"])
	}
	_ = os.Remove(path)
	for _, bad := range []stageCfg{
		{Duration: "7"},
		{Items: map[string]itemCfg{"can.ext": {}}},                                       // R&D only
		{Items: map[string]itemCfg{"din.level": {Peer: ptitems.PeerPC}}},                 // not a peer it has
		{Items: map[string]itemCfg{"din.level": {Params: map[string]string{"mv": "1"}}}}, // not its parameter
		{Items: map[string]itemCfg{"aout.out1": {Points: []float64{1, 2}}}},              // a walk takes 3-5 points
		{Items: map[string]itemCfg{"temp.read": {Points: []float64{1, 2, 3}}}},           // not an AI / AO item
	} {
		if out := postCfg(t, New().Handler(), map[string]any{"stage": "func", "cfg": bad}); out["error"] == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestParamsAreCheckedAgainstTheBoardsLimits(t *testing.T) {
	useTempStageCfg(t)
	s := New()
	s.caps, s.board = cfgGoldenCaps(t), new(ptboard.Board)
	out := postCfg(t, s.Handler(), map[string]any{"stage": "func",
		"cfg": stageCfg{Items: map[string]itemCfg{"rs485.peer": {Params: map[string]string{"baud": "1234"}}}}})
	if e, _ := json.Marshal(out["error"]); !strings.Contains(string(e), "go.stagecfg.param_limit") {
		t.Fatalf("baud=1234 is not one of the board's rates, got %s", e)
	}
}

// The reference board set to unused: no self-loop (decision 135); its AI / AO
// / DI / DO items turn to a person, which configWhy refuses, CAN to the PC.
func TestRefUnusedSubstitution(t *testing.T) {
	picks := stageCfg{}.picks(ptitems.Func, cfgGoldenCaps(t), true, false)
	want := map[string]ptitems.Peer{
		"din.level": ptitems.PeerHuman, "dout.load": ptitems.PeerHuman,
		"aout.out1": ptitems.PeerHuman, "aout.out2": ptitems.PeerHuman,
		"ain.read2": ptitems.PeerHuman, "can.peer": ptitems.PeerPC,
	}
	for _, p := range picks {
		w, ok := want[p.Item.ID]
		if !ok {
			if p.Peer == ptitems.PeerRef {
				t.Errorf("%s still needs the reference board", p.Item.ID)
			}
			continue
		}
		delete(want, p.Item.ID)
		if p.Peer != w || p.Note == "" {
			t.Errorf("%s: peer %s note %q, want %s with a note", p.Item.ID, p.Peer, p.Note, w)
		}
	}
	for id := range want {
		t.Errorf("%s missing from the functional picks", id)
	}
	for _, p := range (stageCfg{}).picks(ptitems.Func, cfgGoldenCaps(t), false, true) {
		if p.Item.ID == "din.level" && p.Peer != ptitems.PeerRef {
			t.Error("substituted although the reference board is in use")
		}
	}
	// Not connected either: still the reference board, never a person; the
	// step is refused instead (decision 129).
	for _, p := range (stageCfg{}).picks(ptitems.Func, cfgGoldenCaps(t), false, false) {
		if p.Item.ID == "din.level" && p.Peer != ptitems.PeerRef {
			t.Errorf("din.level with no reference board connected: peer %s, want ref", p.Peer)
		}
	}
	useTempStageCfg(t)
	tr := true
	out := postCfg(t, New().Handler(), map[string]any{"ref_unused": tr})
	if sub, _ := out["subst"].(map[string]any); sub["din.level"] == nil {
		t.Errorf("the page is not told what the card should say: %v", out["subst"])
	}
}

func fakePick(id string, once bool, peer ptitems.Peer) ItemPick {
	return ItemPick{Item: ptitems.Item{ID: id, Once: once}, Peer: peer}
}

// Step 2 with no duration: one by one, and the first failure ends it.
func TestFunctionalStopsAtFirstFailure(t *testing.T) {
	var ran []string
	run := func(ctx context.Context, picks []ItemPick, timed bool) ptreport.StepResult {
		if timed || len(picks) != 1 {
			t.Fatalf("no duration means one pick at a time, got timed=%v n=%d", timed, len(picks))
		}
		ran = append(ran, picks[0].Item.ID)
		if picks[0].Item.ID == "b" {
			return ptreport.StepResult{Outcome: ptreport.OutcomeFail, Reason: "out of range"}
		}
		return ptreport.StepResult{Outcome: ptreport.OutcomePass}
	}
	res := runConfig(context.Background(), run,
		[]ItemPick{fakePick("a", false, ""), fakePick("b", false, ""), fakePick("c", false, "")}, 0)
	if res.Outcome != ptreport.OutcomeFail || strings.Join(ran, ",") != "a,b" || !strings.HasPrefix(res.Reason, "b") {
		t.Fatalf("outcome %s reason %q ran %v", res.Outcome, res.Reason, ran)
	}
}

// Step 4 with 4 hours: once-only and person items first, then the rest
// together until the fake clock says the time is up - not before. Relays and
// the AO load join the timed part (decisions 123, 114); other person items do not.
func TestAgingRunsForTheConfiguredDuration(t *testing.T) {
	fire := make(chan time.Time)
	var asked time.Duration
	stageAfter = func(d time.Duration) <-chan time.Time { asked = d; return fire }
	t.Cleanup(func() { stageAfter = time.After })

	var mu sync.Mutex
	var order []string
	run := func(ctx context.Context, picks []ItemPick, timed bool) ptreport.StepResult {
		mu.Lock()
		for _, p := range picks {
			order = append(order, map[bool]string{true: "T:", false: "1:"}[timed]+p.Item.ID)
		}
		mu.Unlock()
		if timed {
			select {
			case <-stageTimeUp(ctx):
			case <-ctx.Done():
			}
		}
		return ptreport.StepResult{Outcome: ptreport.OutcomePass}
	}
	picks := []ItemPick{fakePick("eth.xfer", false, ""), fakePick("eth.link", true, ""),
		fakePick("relay.cycle", false, ptitems.PeerHuman), fakePick("temp.read", false, ""),
		{Item: ptitems.Item{ID: "aout.load", Load: true}, Peer: ptitems.PeerHuman}, fakePick("dout.load", false, ptitems.PeerHuman)}
	done := make(chan ptreport.StepResult)
	go func() { done <- runConfig(context.Background(), run, picks, 4*time.Hour) }()

	select {
	case r := <-done:
		t.Fatalf("ended before the clock ran out: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}
	fire <- time.Time{}
	res := <-done
	if res.Outcome != ptreport.OutcomePass || asked != 4*time.Hour {
		t.Fatalf("outcome %s, waited for %v", res.Outcome, asked)
	}
	if got := strings.Join(order, " "); got != "1:eth.link 1:dout.load T:eth.xfer T:relay.cycle T:temp.read T:aout.load" {
		t.Errorf("order %s", got)
	}

	// A timed run that ends on its own did not run for the configured time.
	early := func(ctx context.Context, picks []ItemPick, timed bool) ptreport.StepResult {
		return ptreport.StepResult{Outcome: ptreport.OutcomePass}
	}
	if r := runConfig(context.Background(), early, picks[:1], time.Hour); r.Outcome != ptreport.OutcomeError {
		t.Errorf("early end recorded %s", r.Outcome)
	}

	// The operator's stop is a stop, even though the runner says nothing failed.
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if r := runConfig(ctx, run, picks[:1], time.Hour); r.Outcome != ptreport.OutcomeStopped {
		t.Errorf("stop recorded %s", r.Outcome)
	}
}

func TestConfigStepFromTheSavedConfig(t *testing.T) {
	useTempStageCfg(t)
	s := New()
	if s.Stages[ptstages.Functional] == nil || s.Stages[ptstages.Aging] == nil {
		t.Fatal("steps 2 and 4 are not registered")
	}
	s.RunItems = nil
	if r := s.Stages[ptstages.Functional](context.Background()); r.Outcome != ptreport.OutcomeError || !strings.Contains(r.Reason, "runner") {
		t.Errorf("no item runner should be an error, got %s %q", r.Outcome, r.Reason)
	}

	var got []string
	s.RunItems = func(ctx context.Context, picks []ItemPick, timed bool) ptreport.StepResult {
		for _, p := range picks {
			got = append(got, p.Item.ID)
		}
		return ptreport.StepResult{Outcome: ptreport.OutcomePass}
	}
	s.caps, s.board = cfgGoldenCaps(t), new(ptboard.Board)
	cfg := stageCfg{PortsOff: map[string]bool{"eth": true, "usb": true, "sd": true, "sdram": true, "din": true,
		"ain": true, "aout": true, "rs232": true, "rs485": true, "can": true, "dout": true, "relay": true},
		Items: map[string]itemCfg{}}
	if out := postCfg(t, s.Handler(), map[string]any{"stage": "func", "cfg": cfg}); out["error"] != nil {
		t.Fatal(out["error"])
	}
	if r := s.Stages[ptstages.Functional](context.Background()); r.Outcome != ptreport.OutcomePass || strings.Join(got, ",") != "temp.read" {
		t.Fatalf("outcome %s ran %v", r.Outcome, got)
	}

	// Each sub-test offers only its own times; forever never (decision 125);
	// the functional test runs once only (decision 139).
	for _, c := range []struct {
		stage, dur string
		ok         bool
	}{{"aging", "forever", false}, {"aging", "once", false}, {"aging", "8", true}, {"func", "once", true},
		{"func", "10m", false}, {"func", "1", false}, {"func", "4", false}, {"user", "30m", true}, {"user", "3", false}} {
		out := postCfg(t, s.Handler(), map[string]any{"stage": c.stage, "cfg": stageCfg{Duration: c.dur}})
		if (out["error"] == nil) != c.ok {
			t.Errorf("%s %q: error %v, want accepted=%v", c.stage, c.dur, out["error"], c.ok)
		}
	}
}

// A duration saved before decision 125 that the picker no longer offers reads as the default.
func TestRetiredDurationReadsAsTheDefault(t *testing.T) {
	path := useTempStageCfg(t)
	if err := os.WriteFile(path, []byte(`{"aging":{"duration":"3"},"func":{"duration":"forever"},"user":{"duration":"30m"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	f := loadStageCfgs()
	if f.Aging.Duration != "4" || f.Func.Duration != "once" || f.User.Duration != "30m" {
		t.Errorf("got aging %q func %q user %q", f.Aging.Duration, f.Func.Duration, f.User.Duration)
	}
	// A functional time saved before decision 139 reads as single.
	if err := os.WriteFile(path, []byte(`{"func":{"duration":"30m"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if f := loadStageCfgs(); f.Func.Duration != "once" {
		t.Errorf("functional 30m read as %q, want once", f.Func.Duration)
	}
}

// Decision 139: one set of AI / AO ranges for all three configurations; a file
// without it reads as the decision-134 default, and a changed channel's test
// points go back to the new range's defaults in every configuration.
func TestSharedRanges(t *testing.T) {
	path := useTempStageCfg(t)
	if err := os.WriteFile(path, []byte(`{"ranges":{"AI1":"i","AO2":"bogus"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	f := loadStageCfgs()
	want := calstore.DefaultRanges()
	want["AI1"] = calstore.RangeCurrent
	for n, r := range want {
		if f.Ranges[n] != r {
			t.Errorf("%s loaded as %q, want %q", n, f.Ranges[n], r)
		}
	}
	_ = os.Remove(path)
	h := New().Handler()
	pts := map[string]itemCfg{"ain.read2": {Points: []float64{4, 12, 18}}, "ain.read1": {Points: []float64{1000, 5000, 9000}}}
	for _, st := range []string{"func", "aging"} {
		if out := postCfg(t, h, map[string]any{"stage": st, "cfg": stageCfg{Items: pts}}); out["error"] != nil {
			t.Fatalf("%s: %v", st, out["error"])
		}
	}
	out := postCfg(t, h, map[string]any{"ranges": map[string]string{"AI2": "v"}})
	if out["error"] != nil {
		t.Fatal(out["error"])
	}
	if r, _ := out["ranges"].(map[string]any); r["AI2"] != "v" || r["AI1"] != "v" {
		t.Errorf("ranges after the change: %v", out["ranges"])
	}
	if ch, _ := out["channels"].(map[string]any); ch["AI2"].(map[string]any)["unit"] != "mV" {
		t.Errorf("AI2's unit does not follow its range: %v", ch["AI2"])
	}
	f = loadStageCfgs()
	for _, c := range []stageCfg{f.Func, f.Aging} {
		if len(c.Items["ain.read2"].Points) != 0 || len(c.Items["ain.read1"].Points) != 3 {
			t.Errorf("points after AI2 changed range: %+v", c.Items)
		}
	}
	if out := postCfg(t, h, map[string]any{"ranges": map[string]string{"AI1": "i420"}}); out["error"] == nil {
		t.Error("AI1 accepted a range it cannot be soldered for")
	}
}

// A full-test step reports its share done: one per single pick, one for the timed part.
func TestRunConfigReportsProgress(t *testing.T) {
	fire := make(chan time.Time)
	stageAfter = func(time.Duration) <-chan time.Time { return fire }
	t.Cleanup(func() { stageAfter = time.After })
	var mu sync.Mutex
	var got []float64
	ctx := context.WithValue(context.Background(), progressKey{}, func(f float64) { mu.Lock(); got = append(got, f); mu.Unlock() })
	run := func(ctx context.Context, picks []ItemPick, timed bool) ptreport.StepResult {
		if timed {
			<-stageTimeUp(ctx)
		}
		return ptreport.StepResult{Outcome: ptreport.OutcomePass}
	}
	picks := []ItemPick{fakePick("eth.link", true, ""), fakePick("sdram.probe", true, ""), fakePick("temp.read", false, "")}
	go func() { time.Sleep(1300 * time.Millisecond); fire <- time.Time{} }()
	if r := runConfig(ctx, run, picks, time.Hour); r.Outcome != ptreport.OutcomePass {
		t.Fatalf("outcome %s", r.Outcome)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) < 3 || got[0] != 1.0/3 || got[1] != 2.0/3 || got[2] < 2.0/3 || got[2] > 1 {
		t.Errorf("progress %v", got)
	}
}

// In a sub-test with a duration the relays run by that duration: only the
// count is configured, each cycle is duration ÷ count, half closed and half
// open; one after another they share it (decision 123).
func TestTimedSubTestRunsRelaysByItsDuration(t *testing.T) {
	useTempStageCfg(t)
	s := New()
	var got map[string]string
	s.RunItems = func(ctx context.Context, picks []ItemPick, timed bool) ptreport.StepResult {
		for _, p := range picks {
			if p.Item.ID == "relay.cycle" {
				got = p.Params
			}
		}
		if timed {
			<-stageTimeUp(ctx)
		}
		return ptreport.StepResult{Outcome: ptreport.OutcomePass}
	}
	fire := make(chan time.Time, 1)
	fire <- time.Time{}
	stageAfter = func(time.Duration) <-chan time.Time { return fire }
	t.Cleanup(func() { stageAfter = time.After })
	s.caps, s.board = cfgGoldenCaps(t), new(ptboard.Board)
	off := map[string]bool{}
	for _, p := range ptitems.Ports {
		off[p.Port] = p.Port != "relay"
	}
	on := true
	cfg := stageCfg{Duration: "2", PortsOff: off, Items: map[string]itemCfg{"relay.cycle": {On: &on,
		Params: map[string]string{"ch": "1,2", "count": "1:2,2:4", "close": "1:5000", "open": "1:5000", "by": "gap", "order": "par"}}}}
	if out := postCfg(t, s.Handler(), map[string]any{"stage": "aging", "cfg": cfg}); out["error"] != nil {
		t.Fatal(out["error"])
	}
	if r := s.Stages[ptstages.Aging](context.Background()); r.Outcome != ptreport.OutcomePass {
		t.Fatalf("outcome %s %q", r.Outcome, r.Reason)
	}
	// 2 h: relay 1 two cycles of 1 h, relay 2 four of 30 min, each half closed.
	if got["close"] != "1:1800000,2:900000" || got["open"] != got["close"] || got["by"] != "time" {
		t.Errorf("parallel params %v", got)
	}
	ser := map[string]string{"ch": "1,2", "count": "1:2,2:4", "order": "ser"}
	if p := ptlib.RelayForDuration(ser, 2*time.Hour); p["close"] != "1:900000,2:450000" {
		t.Errorf("serial shares the duration: %v", p)
	}
}
