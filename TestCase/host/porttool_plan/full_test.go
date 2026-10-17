package testcase

// The full fixture test through the panel's HTTP surface, against the scripted
// board: one record per run, progress and continuing from the step that did
// not pass, stop and the safety card. PORTTOOL-FLOW.md C.3.3-C.3.5.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"strconv"
	"testing"
	"time"

	"PortTool/src/calarea"
	"PortTool/src/calstore"
	"PortTool/src/portmap"
	"PortTool/src/ptpanel"
	"PortTool/src/ptplan"
	"PortTool/src/ptrecord"
	"PortTool/src/ptreport"
	"PortTool/src/ptstages"
)

const fakeUID = "003400413135511439303538"

// fullPanel is a panel on the scripted board with the station plan in place,
// its safety questions replaced by questions, and records in a temp folder.
func fullPanel(t *testing.T, questions []string, stages map[ptstages.Step]ptstages.Func) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	was := ptpanel.PlanDir
	ptpanel.PlanDir = dir
	t.Cleanup(func() { ptpanel.PlanDir = was })
	portmap.File = filepath.Join(t.TempDir(), "porttool_ports.json")
	t.Cleanup(func() { portmap.File = "" })
	records := t.TempDir()
	ptrecord.Dir = records
	t.Cleanup(func() { ptrecord.Dir = "" })
	isolateDelivery(t)

	plan, err := ptplan.Load(filepath.Join("..", "..", "..", "src", "plans", "station6-poweron.json"))
	if err != nil {
		t.Fatal(err)
	}
	plan.Safety = &ptplan.Safety{Questions: questions}
	data, _ := json.MarshalIndent(plan, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "station6-poweron.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	fake := newScriptBoard(t, plainBoard)
	p := ptpanel.New()
	p.Open = func(name string, baud int) (io.ReadWriteCloser, error) {
		if name == refPort {
			return newScriptBoard(t, plainBoard), nil
		}
		return fake, nil
	}
	for st, f := range stages {
		p.Stages[st] = f
	}
	srv := httptest.NewServer(p.Handler())
	t.Cleanup(func() { srv.Close(); p.Close() })
	connectRef(t, srv)
	return srv, records
}

// refPort is the fake reference board: the configurations' AI / AO / DI / DO
// run against it, so a step needing it is refused without it (decision 129).
const refPort = "fakeref"

func connectRef(t *testing.T, srv *httptest.Server) {
	t.Helper()
	if j := postJSON(t, srv, "/api/ref", map[string]any{"port": refPort}); j["connected"] != true {
		t.Fatalf("connect the reference board: %v", j)
	}
}

// waitFull polls /api/full until cond holds.
func waitFull(t *testing.T, srv *httptest.Server, what string, cond func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j := getJSON(t, srv, "/api/full")
		if cond(j) {
			return j
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
	return nil
}

func waitingSafety(j map[string]any) bool { return j["safety"] != nil }
func finished(j map[string]any) bool      { return j["running"] == false && j["last"] != nil }

func onlyReport(t *testing.T, records string) (ptreport.Report, string) {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(records, fakeUID, "2*.json"))
	if len(files) != 1 {
		t.Fatalf("want one record, have %v", files)
	}
	return readReport(t, files[0]), files[0]
}

func readReport(t *testing.T, path string) ptreport.Report {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var r ptreport.Report
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func outcomesOf(r ptreport.Report) string {
	var out []string
	for _, s := range r.Steps {
		out = append(out, s.Type+":"+string(s.Outcome))
	}
	return strings.Join(out, " ")
}

func fixedStage(o ptreport.Outcome) ptstages.Func {
	return func(ctx context.Context) ptreport.StepResult { return ptreport.StepResult{Outcome: o} }
}

func TestSafetyCardAnswersReachTheRecord(t *testing.T) {
	srv, records := fullPanel(t, []string{"Insulation OK?", "Earth bond OK?"}, nil)
	postJSON(t, srv, "/api/connect", map[string]any{"port": "fake"})

	steps := getJSON(t, srv, "/api/full")["steps"].([]any)
	for _, s := range steps {
		row := s.(map[string]any)
		if (row["step"].(float64) != 6) != (row["available"] == true) {
			t.Fatalf("steps 2, 4 (from their configurations) and 5 run in the panel today: %v", row)
		}
		if row["available"] == false && row["why"] == nil {
			t.Fatalf("a step that cannot be picked must say why: %v", row)
		}
	}
	if r := postJSON(t, srv, "/api/full/start", map[string]any{"steps": []int{5, 6}}); r["error"] == nil {
		t.Fatal("a step that cannot run was accepted")
	}

	if r := postJSON(t, srv, "/api/full/start", map[string]any{"steps": []int{5}}); r["started"] != true {
		t.Fatalf("start: %v", r)
	}
	j := waitFull(t, srv, "the safety card", waitingSafety)
	qs := j["safety"].(map[string]any)["questions"].([]any)
	if len(qs) != 2 || qs[0] != "Insulation OK?" {
		t.Fatalf("questions come from the plan: %v", qs)
	}

	yes, no := true, false
	answer := func(pass *bool, first *bool) map[string]any {
		return postJSON(t, srv, "/api/full/safety", map[string]any{
			"sn": " SN/0042 ",
			"answers": []any{
				map[string]any{"yes": first},
				map[string]any{"yes": &no, "note": "bond 0.2 ohm, re-checked"},
			},
			"pass": pass,
		})
	}
	if r := answer(nil, &yes); r["error"] == nil {
		t.Fatal("submitted without a verdict")
	}
	if r := answer(&yes, nil); r["error"] == nil {
		t.Fatal("submitted with a question unanswered")
	}
	if r := answer(&no, &yes); r["ok"] != true {
		t.Fatalf("submit: %v", r)
	}
	j = waitFull(t, srv, "the run to finish", finished)

	rep, path := onlyReport(t, records)
	if !strings.HasSuffix(path, "-SN_0042.json") || rep.SN != "SN/0042" {
		t.Fatalf("SN in name and report: %s, %q", path, rep.SN)
	}
	if rep.BoardUID != fakeUID || rep.Plan == "" || rep.LimitVersion == "" {
		t.Fatalf("header: %+v", rep)
	}
	if got := outcomesOf(rep); got != "5:FAIL" {
		t.Fatal(got)
	}
	s := rep.Steps[0]
	x := s.Attempts[0].Extra
	if s.ID != "safety" || x["q1"] != "yes" || x["q1.text"] != "Insulation OK?" || x["q2"] != "no" ||
		x["q2.note"] != "bond 0.2 ohm, re-checked" || x["q1.note"] != "" {
		t.Fatalf("answers in extra: %v", x)
	}
	last := j["last"].(map[string]any)
	if last["record"] != path || last["passed"] != false {
		t.Fatalf("last: %v", last)
	}
}

func TestFullTestContinuesFromTheStepThatDidNotPass(t *testing.T) {
	var step6 atomic.Value
	step6.Store(ptreport.OutcomeFail)
	srv, records := fullPanel(t, nil, map[ptstages.Step]ptstages.Func{
		ptstages.Functional: fixedStage(ptreport.OutcomePass),
		ptstages.UserLevel: func(ctx context.Context) ptreport.StepResult {
			return ptreport.StepResult{Outcome: step6.Load().(ptreport.Outcome)}
		},
	})
	postJSON(t, srv, "/api/connect", map[string]any{"port": "fake"})
	pick := []int{2, 5, 6}
	pass := true

	run := func(resume string) {
		t.Helper()
		if r := postJSON(t, srv, "/api/full/start", map[string]any{"steps": pick, "resume": resume}); r["started"] != true {
			t.Fatalf("start %q: %v", resume, r)
		}
		waitFull(t, srv, "the safety card", waitingSafety)
		postJSON(t, srv, "/api/full/safety", map[string]any{"answers": []any{}, "pass": &pass})
		waitFull(t, srv, "the run to finish", finished)
	}
	run("")
	first, firstPath := onlyReport(t, records)
	if got := outcomesOf(first); got != "2:PASS 5:PASS 6:FAIL" {
		t.Fatal(got)
	}

	ask := postJSON(t, srv, "/api/full/start", map[string]any{"steps": pick})
	q, _ := ask["ask"].(map[string]any)
	if q == nil || q["from"] != float64(6) || q["uid"] != fakeUID {
		t.Fatalf("the second start must ask to continue from step 6: %v", ask)
	}

	time.Sleep(1100 * time.Millisecond) // records are named by the second they started
	step6.Store(ptreport.OutcomePass)
	if r := postJSON(t, srv, "/api/full/start", map[string]any{"steps": pick, "resume": "continue"}); r["started"] != true {
		t.Fatalf("continue: %v", r)
	}
	waitFull(t, srv, "the run to finish", finished)
	files, _ := filepath.Glob(filepath.Join(records, fakeUID, "2*.json"))
	if len(files) != 2 {
		t.Fatalf("want two records, have %v", files)
	}
	second := readReport(t, files[1])
	if files[1] == firstPath {
		second = readReport(t, files[0])
	}
	if got := outcomesOf(second); got != "2:SKIPPED 5:SKIPPED 6:PASS" {
		t.Fatal(got)
	}
	if !strings.Contains(second.Steps[0].Reason, filepath.Base(firstPath)) {
		t.Fatalf("the skipped step names the report it passed in: %q", second.Steps[0].Reason)
	}
	if _, err := os.Stat(filepath.Join(records, fakeUID, "progress.json")); !os.IsNotExist(err) {
		t.Fatal("progress kept after every picked step passed")
	}
	if r := postJSON(t, srv, "/api/full/start", map[string]any{"steps": pick}); r["ask"] != nil {
		t.Fatalf("a board that passed everything is asked to continue: %v", r)
	}
	postJSON(t, srv, "/api/full/stop", map[string]any{})
	waitFull(t, srv, "the third run to stop", finished)
}

func TestStopRecordsStoppedAndSkipsTheRest(t *testing.T) {
	srv, records := fullPanel(t, nil, map[ptstages.Step]ptstages.Func{
		ptstages.UserLevel: fixedStage(ptreport.OutcomePass),
	})
	postJSON(t, srv, "/api/connect", map[string]any{"port": "fake"})
	postJSON(t, srv, "/api/full/start", map[string]any{"steps": []int{5, 6}})
	waitFull(t, srv, "the safety card", waitingSafety)
	postJSON(t, srv, "/api/full/stop", map[string]any{})
	waitFull(t, srv, "the run to finish", finished)

	rep, _ := onlyReport(t, records)
	if got := outcomesOf(rep); got != "5:STOPPED 6:SKIPPED" {
		t.Fatal(got)
	}
	if !strings.Contains(rep.Steps[0].Reason, "stopped by operator") {
		t.Fatalf("reason: %q", rep.Steps[0].Reason)
	}
	p, _ := ptrecord.LoadProgress(fakeUID)
	if len(p.Passed) != 0 {
		t.Fatalf("a stopped step is not a passed one: %v", p)
	}
}

func TestSimulatedBoardGetsNoRecord(t *testing.T) {
	srv, records := fullPanel(t, nil, nil)
	postJSON(t, srv, "/api/connect", map[string]any{"port": "sim"})
	pass := true
	postJSON(t, srv, "/api/full/start", map[string]any{"steps": []int{5}})
	waitFull(t, srv, "the safety card", waitingSafety)
	postJSON(t, srv, "/api/full/safety", map[string]any{"answers": []any{}, "pass": &pass})
	j := waitFull(t, srv, "the run to finish", finished)
	last := j["last"].(map[string]any)
	if last["record"] != nil || last["record_why"] == nil || last["passed"] != true {
		t.Fatalf("last: %v", last)
	}
	if entries, _ := os.ReadDir(records); len(entries) != 0 {
		t.Fatalf("records written for the simulated board: %v", entries)
	}
}

func TestSafetyQuestionsAreValidated(t *testing.T) {
	base, err := os.ReadFile(filepath.Join("..", "..", "..", "src", "plans", "station6-poweron.json"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := ptplan.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	if p.Safety == nil || len(p.SafetyQuestions()) != 0 {
		t.Fatalf("the shipped plan carries an empty questionnaire: %+v", p.Safety)
	}
	for what, qs := range map[string][]string{
		"an empty question":   {"Insulation OK?", "  "},
		"a repeated question": {"Insulation OK?", "Insulation OK?"},
	} {
		p.Safety = &ptplan.Safety{Questions: qs}
		if err := p.Validate(); err == nil {
			t.Errorf("%s: accepted", what)
		}
	}
}

// Step 6 that could not factory reset leaves the red banner's flag (decision 96).
func TestRootLeftOnTheBoardIsFlagged(t *testing.T) {
	srv, _ := fullPanel(t, nil, map[ptstages.Step]ptstages.Func{
		ptstages.UserLevel: func(ctx context.Context) ptreport.StepResult {
			return ptreport.StepResult{Outcome: ptreport.OutcomeError, Reason: ptstages.RootLeft + ": no ST-Link"}
		},
	})
	postJSON(t, srv, "/api/connect", map[string]any{"port": "fake"})
	postJSON(t, srv, "/api/full/start", map[string]any{"steps": []int{6}})
	j := waitFull(t, srv, "the run to finish", finished)
	if last := j["last"].(map[string]any); last["root_left"] != true {
		t.Fatalf("last: %v", last)
	}
}

// isolateDelivery points the delivery folder (stage6/, fixture image) and the
// saved configurations at empty temp places, so the bench's own files never
// decide a test.
func isolateDelivery(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	ptpanel.DeliveryDir = dir
	ptpanel.StageCfgFile = filepath.Join(t.TempDir(), "porttool_stagecfg.json")
	t.Cleanup(func() { ptpanel.DeliveryDir, ptpanel.StageCfgFile = "", "" })
	return dir
}

// analogCaps is capsReply plus the two analog ports, so the configurations
// pick AI / AO items.
func analogCaps() []string {
	body := append(capsReply()[1:],
		// As caps_golden.txt (T4-01) has them.
		"OK port=ain board=upper kind=session blk=D term=D12,D13 channels=2 loop=ctrl params=ch,period running=0",
		"OK vals=ain ch=1,2 period=500",
		"OK limits=ain ch:1..2 period:50..",
		"OK port=aout board=upper kind=session blk=D term=D14,D15 channels=2 loop=ctrl params=ch,mv,period running=0",
		"OK vals=aout ch=1,2 mv=1:0,2:0 period=1000",
		"OK limits=aout ch:1..2 mv:0..2500 period:50..",
	)
	return append([]string{fmt.Sprintf("OK porttool=0.9.0 ports=5 lines=%d", len(body))}, body...)
}

// analogBoard is plainBoard with the analog ports and pt.cal answering area.
func analogBoard(area []byte) func(cmd string, nth int) ([]string, []string) {
	return func(cmd string, nth int) ([]string, []string) {
		switch cmd {
		case "pt.caps":
			return analogCaps(), nil
		case "pt.cal":
			return []string{"OK cal=" + strings.ToUpper(hex.EncodeToString(area))}, nil
		}
		return plainBoard(cmd, nth)
	}
}

func onBoardCal(t *testing.T) []byte {
	return calArea(t, fakeUID, [calarea.Channels]calarea.Channel{{Gain: 1}, {Gain: 1}, {Gain: 1}, {Gain: 1}})
}

// setRefRanges saves the reference board's ranges on its settings page, its
// wiring as it is (decision 135).
func setRefRanges(t *testing.T, srv *httptest.Server, ranges map[string]any) map[string]any {
	t.Helper()
	st := getJSON(t, srv, "/api/refboard")
	j := postJSON(t, srv, "/api/refboard", map[string]any{"wiring": st["wiring"], "ranges": ranges})
	if j["error"] != nil {
		t.Fatalf("reference settings: %v", j)
	}
	return j
}

// flowPanel is a panel on analogBoard(area), connected, with stages faked and
// the server handed back for RunItems.
func flowPanel(t *testing.T, area []byte, stages map[ptstages.Step]ptstages.Func) (*httptest.Server, *ptpanel.Server, string) {
	t.Helper()
	was := ptpanel.PlanDir
	ptpanel.PlanDir = t.TempDir()
	t.Cleanup(func() { ptpanel.PlanDir = was })
	portmap.File = filepath.Join(t.TempDir(), "porttool_ports.json")
	t.Cleanup(func() { portmap.File = "" })
	ptrecord.Dir = t.TempDir()
	t.Cleanup(func() { ptrecord.Dir = "" })
	calstore.Dir = t.TempDir()
	t.Cleanup(func() { calstore.Dir = "" })
	dir := isolateDelivery(t)

	p := ptpanel.New()
	p.Open = func(name string, baud int) (io.ReadWriteCloser, error) {
		return newScriptBoard(t, analogBoard(area)), nil
	}
	for st, f := range stages {
		p.Stages[st] = f
	}
	srv := httptest.NewServer(p.Handler())
	t.Cleanup(func() { srv.Close(); p.Close() })
	postJSON(t, srv, "/api/connect", map[string]any{"port": "fake"})
	connectRef(t, srv)
	// The fakes play AO as current only, so AI1 and AO1 (voltage by default) go
	// to current: on the reference board's registration, and on the configurations'
	// shared ranges the full test tests the board under test on (decision 139).
	setRefRanges(t, srv, map[string]any{"AI1": "i", "AO1": "i20"})
	if j := postJSON(t, srv, "/api/stagecfg", map[string]any{"ranges": map[string]string{"AI1": "i", "AO1": "i20"}}); j["error"] != nil {
		t.Fatalf("shared ranges: %v", j)
	}
	return srv, p, dir
}

func strList(v any) string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return strings.Join(out, ",")
}

// Decision 97: the picked steps' AI / AO need the board's own calibration.
func TestFullTestRefusesChannelsWithoutCalibration(t *testing.T) {
	var ran atomic.Int32
	counting := func(ctx context.Context) ptreport.StepResult {
		ran.Add(1)
		return ptreport.StepResult{Outcome: ptreport.OutcomePass}
	}
	stages := map[ptstages.Step]ptstages.Func{ptstages.Functional: counting, ptstages.Aging: counting}
	srv, _, _ := flowPanel(t, blankArea(), stages)

	r := postJSON(t, srv, "/api/full/start", map[string]any{"steps": []int{2}})
	if r["started"] == true || r["error"] == nil {
		t.Fatalf("started without calibration: %v", r)
	}
	// Same-number default wiring: AI1 read is on by default too (decision 134).
	if got := strList(r["uncalibrated"]); got != "AI1,AI2,AO1,AO2" || r["cal_port"] != "ain" {
		t.Fatalf("the refusal lists every channel tested and where to calibrate: %v", r)
	}

	// Only what the configuration tests is asked for.
	cfg := map[string]any{"ports_off": map[string]any{"ain": true},
		"items": map[string]any{"aout.load": map[string]any{"params": map[string]any{"out1": "20", "out2": "20"}}}}
	if j := postJSON(t, srv, "/api/stagecfg", map[string]any{"stage": "aging", "cfg": cfg}); j["error"] != nil {
		t.Fatalf("save aging config: %v", j)
	}
	r = postJSON(t, srv, "/api/full/start", map[string]any{"steps": []int{4}})
	if got := strList(r["uncalibrated"]); got != "AO1,AO2" || r["cal_port"] != "aout" {
		t.Fatalf("aging without AI needs only AO: %v", r)
	}
	if ran.Load() != 0 {
		t.Fatal("a refused start ran a step")
	}

	// Step 5 tests no AI / AO: no gate.
	if r := postJSON(t, srv, "/api/full/start", map[string]any{"steps": []int{5}}); r["started"] != true {
		t.Fatalf("step 5 alone needs no calibration: %v", r)
	}
	postJSON(t, srv, "/api/full/stop", map[string]any{})
	waitFull(t, srv, "the run to finish", finished)
}

func TestFullTestStartsWithCalibrationOnBoard(t *testing.T) {
	stages := map[ptstages.Step]ptstages.Func{ptstages.Functional: fixedStage(ptreport.OutcomePass)}
	srv, _, _ := flowPanel(t, onBoardCal(t), stages)
	if r := postJSON(t, srv, "/api/full/start", map[string]any{"steps": []int{2}}); r["started"] != true {
		t.Fatalf("a calibrated board was refused: %v", r)
	}
	waitFull(t, srv, "the run to finish", finished)
}

// Decision 129: a configuration's target is filled in before the run; a ticked
// AO load with no current keeps its step from being picked.
func TestConfigStepNeedsItsTargets(t *testing.T) {
	stages := map[ptstages.Step]ptstages.Func{ptstages.Aging: fixedStage(ptreport.OutcomePass)}
	srv, _, _ := flowPanel(t, onBoardCal(t), stages)
	step4 := func() map[string]any {
		for _, s := range getJSON(t, srv, "/api/full")["steps"].([]any) {
			if row := s.(map[string]any); row["step"] == float64(4) {
				return row
			}
		}
		t.Fatal("no step 4 row")
		return nil
	}
	row := step4()
	why, _ := row["why"].(map[string]any)
	if row["available"] != false || why == nil || why["$t"] != "go.full.no_target" {
		t.Fatalf("aging with the AO load current blank must be refused: %v", row)
	}
	if r := postJSON(t, srv, "/api/full/start", map[string]any{"steps": []int{4}}); r["started"] == true {
		t.Fatal("started aging with a blank target")
	}
	load := func(params map[string]any) map[string]any {
		return map[string]any{"items": map[string]any{"aout.load": map[string]any{"params": params}}}
	}
	// Each AO has its own target (STG-38): one of the two is not enough.
	if j := postJSON(t, srv, "/api/stagecfg", map[string]any{"stage": "aging", "cfg": load(map[string]any{"out1": "20"})}); j["error"] != nil {
		t.Fatalf("save aging config: %v", j)
	}
	if row := step4(); row["available"] != false {
		t.Fatalf("aging with AO2's target blank must be refused: %v", row)
	}
	// Outside every range of AO2 is never stored (C.3.6 「填数超量程」).
	if j := postJSON(t, srv, "/api/stagecfg", map[string]any{"stage": "aging", "cfg": load(map[string]any{"out1": "20", "out2": "20000"})}); j["error"] == nil {
		t.Fatalf("20000 on AO2 stored: %v", j)
	}
	// A page opened before STG-38 still sends the shared "ma": it fills both.
	if j := postJSON(t, srv, "/api/stagecfg", map[string]any{"stage": "aging", "cfg": load(map[string]any{"ma": "20"})}); j["error"] != nil {
		t.Fatalf("save aging config with the old ma: %v", j)
	}
	if row := step4(); row["available"] != true {
		t.Fatalf("both targets filled in from ma, aging still refused: %v", row)
	}
	if j := postJSON(t, srv, "/api/stagecfg", map[string]any{"stage": "aging", "cfg": load(map[string]any{"out1": "20", "out2": "5000"})}); j["error"] != nil {
		t.Fatalf("save aging config: %v", j)
	}
	if row := step4(); row["available"] != true {
		t.Fatalf("both targets filled in, aging still refused: %v", row)
	}
}

// Step 6 is tickable only with the delivery's three files and a real board.
func TestStep6NeedsTheDeliveryFiles(t *testing.T) {
	srv, _, dir := flowPanel(t, onBoardCal(t), nil)
	step6 := func() map[string]any {
		for _, s := range getJSON(t, srv, "/api/full")["steps"].([]any) {
			if row := s.(map[string]any); row["step"] == float64(6) {
				return row
			}
		}
		t.Fatal("no step 6 row")
		return nil
	}
	row := step6()
	why, _ := row["why"].(map[string]any)
	if row["available"] != false || why == nil || why["$t"] != "go.full.user_files" ||
		!strings.Contains(fmt.Sprint(why["args"]), ptstages.DemoName) {
		t.Fatalf("missing files must disable step 6 and name them: %v", row)
	}
	if r := postJSON(t, srv, "/api/full/start", map[string]any{"steps": []int{6}}); r["started"] == true {
		t.Fatal("started step 6 without its files")
	}

	files, _ := ptstages.LocateUserLevelFiles(dir)
	if err := os.MkdirAll(filepath.Dir(files.Bootloader), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{files.Bootloader, files.IAPTool, files.Demo} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if row := step6(); row["available"] != true {
		t.Fatalf("all three files there, step 6 still disabled: %v", row)
	}

	postJSON(t, srv, "/api/connect", map[string]any{"port": "sim"})
	row = step6()
	why, _ = row["why"].(map[string]any)
	if row["available"] != false || why == nil || why["$t"] != "go.full.user_sim" {
		t.Fatalf("the simulated board cannot run step 6: %v", row)
	}
}

// Every step of a run has one number, the same on the page, in the log and in
// the saved report (decision 128): a sub-test that ends badly with no numbered
// step to blame gets a whole-step entry with its reason; safety is a step.
func TestStepNumbersMatchBetweenPageLogAndReport(t *testing.T) {
	srv, records := fullPanel(t, nil, map[ptstages.Step]ptstages.Func{
		ptstages.Aging: func(ctx context.Context) ptreport.StepResult {
			return ptreport.StepResult{Outcome: ptreport.OutcomeTimeout, Reason: "probe silent"}
		},
	})
	postJSON(t, srv, "/api/connect", map[string]any{"port": "fake"})
	if r := postJSON(t, srv, "/api/full/start", map[string]any{"steps": []int{4}}); r["started"] != true {
		t.Fatalf("start: %v", r)
	}
	j := waitFull(t, srv, "the run to finish", finished)
	rep, _ := onlyReport(t, records)
	items := rep.Steps[0].Items
	if len(items) != 1 || items[0].No != 1 || items[0].Outcome != ptreport.OutcomeTimeout ||
		!strings.Contains(items[0].Where, "#1 ") || !strings.Contains(items[0].Value, "probe silent") {
		t.Fatalf("report items: %+v", items)
	}
	run := j["run"].(map[string]any)
	steps := run["steps"].([]any)
	st := steps[0].(map[string]any)
	if len(steps) != 1 || st["no"].(float64) != 1 || st["state"] != "TIMEOUT" || st["kind"] != "whole" {
		t.Fatalf("page steps: %v", steps)
	}
	// The log carries the same number on the verdict line, a timeout level
	// apart from a failure, and a closing line with the report path.
	var end, done map[string]any
	for _, l := range run["log"].([]any) {
		row := l.(map[string]any)
		switch row["kind"] {
		case "end":
			end = row
		case "done":
			done = row
		}
	}
	if end == nil || end["no"].(float64) != 1 || end["lv"] != "timeout" {
		t.Fatalf("verdict line: %v", end)
	}
	if done == nil || done["msg"].(map[string]any)["$t"] != "run.log.done" {
		t.Fatalf("closing line: %v", done)
	}
	if li := j["last"].(map[string]any)["items"].([]any); len(li) != 1 || li[0].(map[string]any)["no"].(float64) != 1 {
		t.Fatalf("last.items: %v", li)
	}
	// Only the lines after a sequence number come back.
	seq := uint64(end["seq"].(float64))
	again := getJSON(t, srv, fmt.Sprintf("/api/full?log=%d&run=%.0f", seq, run["id"].(float64)))["run"].(map[string]any)["log"].([]any)
	for _, l := range again {
		if uint64(l.(map[string]any)["seq"].(float64)) <= seq {
			t.Fatalf("line at or before %d came back: %v", seq, l)
		}
	}
	// A page that last saw another run gets every line of this one.
	if all := getJSON(t, srv, fmt.Sprintf("/api/full?log=%d&run=1", seq))["run"].(map[string]any)["log"].([]any); len(all) != len(run["log"].([]any)) {
		t.Fatalf("another run's seq cut this run's lines: %d of %d", len(all), len(run["log"].([]any)))
	}

	// A new run numbers from 1 again; the safety questionnaire is one step.
	if r := postJSON(t, srv, "/api/full/start", map[string]any{"steps": []int{5}}); r["started"] != true {
		t.Fatalf("start: %v", r)
	}
	waitFull(t, srv, "the safety card", waitingSafety)
	yes := true
	postJSON(t, srv, "/api/full/safety", map[string]any{"sn": "SN1", "answers": []any{}, "pass": &yes})
	j = waitFull(t, srv, "the second run to finish", finished)
	files, _ := filepath.Glob(filepath.Join(records, fakeUID, "*-SN1.json"))
	if len(files) != 1 {
		t.Fatalf("second record: %v", files)
	}
	items = readReport(t, files[0]).Steps[0].Items
	if len(items) != 1 || items[0].No != 1 || items[0].Outcome != ptreport.OutcomePass {
		t.Fatalf("safety step in the report: %+v", items)
	}
	st = j["run"].(map[string]any)["steps"].([]any)[0].(map[string]any)
	if st["kind"] != "safety" || st["no"].(float64) != 1 {
		t.Fatalf("safety step on the page: %v", st)
	}
}

// Decision 134: a reference-board wire whose two ends are on different ranges
// cannot test, the card says so and the step does not start.
func TestRangeMismatchRefusesTheStep(t *testing.T) {
	stages := map[ptstages.Step]ptstages.Func{ptstages.Functional: fixedStage(ptreport.OutcomePass)}
	srv, _, _ := flowPanel(t, onBoardCal(t), stages)
	step2 := func() map[string]any {
		for _, s := range getJSON(t, srv, "/api/full")["steps"].([]any) {
			if row := s.(map[string]any); row["step"] == float64(2) {
				return row
			}
		}
		t.Fatal("no step 2 row")
		return nil
	}
	if row := step2(); row["available"] != true {
		t.Fatalf("matching ranges refused: %v", row)
	}
	// The reference AI1 reading AO1 under test goes to voltage; AO1 stays current.
	ref := getJSON(t, srv, "/api/refboard")
	chans, _ := ref["ranges"].(map[string]any)
	ai1, _ := chans["AI1"].(map[string]any)
	if ai1["range"] != "i" || chans["AO1"] == nil || ref["wiring"] == nil || ref["impact"] == nil {
		t.Fatalf("the reference board's settings are shown: %v", ref)
	}
	setRefRanges(t, srv, map[string]any{"AI1": "v", "AO1": "i20"})
	// The reference AI1 is voltage now; AO1 under test stays current on the shared ranges.
	row := step2()
	why, _ := row["why"].(map[string]any)
	if row["available"] != false || why == nil || why["$t"] != "go.full.ref_cannot" {
		t.Fatalf("mismatched ranges must refuse the step: %v", row)
	}
	mm, _ := getJSON(t, srv, "/api/stagecfg")["mismatch"].(map[string]any)
	if mm["aout.out1"] == nil {
		t.Fatalf("the configuration card is told which item: %v", mm)
	}
	if r := postJSON(t, srv, "/api/full/start", map[string]any{"steps": []int{2}}); r["started"] == true {
		t.Fatal("started with mismatched ranges")
	}
	// Decision 139: the board under test's side is the configurations' range, not
	// its R&D registration. AO1 under test to voltage matches again.
	postJSON(t, srv, "/api/stagecfg", map[string]any{"ranges": map[string]string{"AO1": "v"}})
	if row := step2(); row["available"] != true {
		t.Fatalf("the shared AO1 range matches the reference AI1 again: %v", row)
	}
	// STG-40 row 11: AO1 calibrated on 0-20 mA, tested on voltage, refuses the
	// step too; the card's mark is the same reason.
	if _, err := calstore.Store(fakeUID, "AO1", calstore.Channel{Range: calstore.RangeAO020, Unit: "mA", Gain: 1},
		calstore.Meta{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	row = step2()
	why, _ = row["why"].(map[string]any)
	args, _ := why["args"].(map[string]any)
	if inner, _ := args["why"].(map[string]any); row["available"] != false || inner["$t"] != "rng.kb_other_range" {
		t.Fatalf("a calibration on another range must refuse the step: %v", row)
	}
	mm, _ = getJSON(t, srv, "/api/stagecfg")["mismatch"].(map[string]any)
	if fmt.Sprint(mm["aout.out1"]) != fmt.Sprint(row["why"]) {
		t.Fatalf("the card's mark and the refusal are one sentence: %v / %v", mm["aout.out1"], row["why"])
	}
	if r := postJSON(t, srv, "/api/full/start", map[string]any{"steps": []int{2}}); r["started"] == true {
		t.Fatal("started with AO1 calibrated on another range")
	}
}

// Decision 129 「多路」: a multi-channel item runs while one of its channels can
// be tested (the others record not wired); none testable refuses the step.
func TestPartlyWiredDIWalkStillRuns(t *testing.T) {
	stages := map[ptstages.Step]ptstages.Func{ptstages.Functional: fixedStage(ptreport.OutcomePass)}
	srv, _, _ := flowPanel(t, onBoardCal(t), stages)
	step2 := func() map[string]any {
		for _, s := range getJSON(t, srv, "/api/full")["steps"].([]any) {
			if row := s.(map[string]any); row["step"] == float64(2) {
				return row
			}
		}
		t.Fatal("no step 2 row")
		return nil
	}
	st := getJSON(t, srv, "/api/refboard")
	w := st["wiring"].(map[string]any)
	di := w["di"].(map[string]any)
	di["1"] = map[string]any{"via": 0, "to": 0}
	if j := postJSON(t, srv, "/api/refboard", map[string]any{"wiring": w, "ranges": map[string]any{"AI1": "i"}}); j["error"] != nil {
		t.Fatalf("save: %v", j)
	}
	if row := step2(); row["available"] != true {
		t.Fatalf("DI1 not wired, DI2-8 wired: the step must still run: %v", row)
	}
	for n := 1; n <= 8; n++ {
		di[strconv.Itoa(n)] = map[string]any{"via": 0, "to": 0}
	}
	postJSON(t, srv, "/api/refboard", map[string]any{"wiring": w, "ranges": map[string]any{"AI1": "i"}})
	row := step2()
	why, _ := row["why"].(map[string]any)
	if row["available"] != false || why == nil || why["$t"] != "go.full.ref_cannot" {
		t.Fatalf("no DI wired: the step must be refused: %v", row)
	}
}
