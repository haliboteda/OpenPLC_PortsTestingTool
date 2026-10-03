package testcase

// The plan page's HTTP surface, driven end to end against the scripted board.
//
// The executor is covered above. What is only decidable here is what the panel
// wraps around it: a plan is validated before it reaches the disk, a name that
// came from a browser cannot climb out of the plans folder, and the panel's own
// echo responder stands down for the length of a run - without which every
// frame would be answered twice and the board would count the second reply as
// stale, which reads on the line as a miss on a link that is working.

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"PortTool/internal/portmap"
	"PortTool/internal/ptpanel"
	"PortTool/internal/ptplan"
)

func planPanel(t *testing.T, handler func(cmd string, nth int) ([]string, []string)) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	was := ptpanel.PlanDir
	ptpanel.PlanDir = dir
	t.Cleanup(func() { ptpanel.PlanDir = was })

	// Never the bench's real porttool_ports.json.
	portmap.File = filepath.Join(t.TempDir(), "porttool_ports.json")
	t.Cleanup(func() { portmap.File = "" })

	fake := newScriptBoard(t, handler)
	p := ptpanel.New()
	p.Open = func(name string, baud int) (io.ReadWriteCloser, error) { return fake, nil }
	srv := httptest.NewServer(p.Handler())
	t.Cleanup(func() { srv.Close(); p.Close() })
	return srv, dir
}

func plainBoard(cmd string, nth int) ([]string, []string) {
	lines, _ := standardReplies(cmd)
	return lines, nil
}

func postJSON(t *testing.T, srv *httptest.Server, path string, body any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := srv.Client().Post(srv.URL+path, "application/json", strings.NewReader(string(b)))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("POST %s returned unreadable JSON: %v", path, err)
	}
	return out
}

func getJSON(t *testing.T, srv *httptest.Server, path string) map[string]any {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("GET %s returned unreadable JSON: %v", path, err)
	}
	return out
}

// samplePlan is the smallest plan that still exercises both a reply-judging and
// a frame-judging step.
func samplePlan(name string) map[string]any {
	return map[string]any{
		"schema": 1, "name": name, "limit_version": "2026-09-08-t",
		"steps": []any{
			map[string]any{
				"id": "id", "type": "PtRaw", "command": "pt.id",
				"checks": []any{map[string]any{"field": "_text", "op": "contains", "value": "uid"}},
			},
			map[string]any{
				"id": "din", "type": "PtSession", "port": "din",
				"params": map[string]any{"ch": "1", "period": 200},
				"frames": 3, "timeout_ms": 3000,
				// Not miss: din is loop=ctrl, so its counter proves the control
				// port, not the port under test. The bitfield is the reading.
				"checks": []any{map[string]any{"field": "v", "op": "eq", "value": "0x01"}},
			},
		},
	}
}

// The routes below are only reachable through the page, and both pages live in
// one file, so an edit to the manual panel can carry the plan pane away with it
// and nothing else would notice.
func TestPlanPaneIsStillOnThePage(t *testing.T) {
	srv, _ := planPanel(t, plainBoard)
	resp, err := srv.Client().Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{`data-tab="plan"`, `id="planlist"`, `id="planbody"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the served page has no %s", want)
		}
	}
}

func TestPlanPageWritesOnlyPlansThatLoad(t *testing.T) {
	srv, dir := planPanel(t, plainBoard)

	// A plan with no limit_version: a verdict nothing can be traced back to.
	bad := samplePlan("untraceable")
	delete(bad, "limit_version")
	if r := postJSON(t, srv, "/api/plan", map[string]any{"name": "bad", "plan": bad}); r["error"] == nil {
		t.Fatalf("a plan with no limit_version was accepted: %v", r)
	}
	if _, err := os.Stat(filepath.Join(dir, "bad.json")); !os.IsNotExist(err) {
		t.Fatal("a plan that does not validate still reached the disk")
	}

	if r := postJSON(t, srv, "/api/plan", map[string]any{"name": "good", "plan": samplePlan("good")}); r["error"] != nil {
		t.Fatalf("saving a valid plan: %v", r["error"])
	}

	// What the page writes has to be readable by the same loader the CLI uses,
	// or a plan edited in the panel would only run in the panel.
	if _, err := ptplan.Load(filepath.Join(dir, "good.json")); err != nil {
		t.Fatalf("the panel wrote a file its own loader rejects: %v", err)
	}

	list := getJSON(t, srv, "/api/plans")
	names, _ := list["plans"].([]any)
	if len(names) != 1 || names[0] != "good.json" {
		t.Fatalf("/api/plans = %v, want only the plan that validated", names)
	}

	back := getJSON(t, srv, "/api/plan?name=good")
	plan, _ := back["plan"].(map[string]any)
	if plan == nil || plan["name"] != "good" || len(plan["steps"].([]any)) != 2 {
		t.Fatalf("the plan did not come back as it was saved: %v", back)
	}
}

// A save may move how a step measures. It may not move what counts as a pass.
//
// Relaxing a limit ships a board that failed, and the report's traceability is
// the plan name plus its limit_version - so neither can be reachable from a
// request. The page draws them read-only, but a page is only what a browser
// chose to send; this is the half that does not depend on the browser.
// DECISIONS.md 30, restated in 34.
func TestSavingAPlanCannotMoveItsLimits(t *testing.T) {
	srv, dir := planPanel(t, plainBoard)

	if r := postJSON(t, srv, "/api/plan", map[string]any{"name": "keep", "plan": samplePlan("keep")}); r["error"] != nil {
		t.Fatalf("saving the plan to start from: %v", r["error"])
	}
	before, err := ptplan.Load(filepath.Join(dir, "keep.json"))
	if err != nil {
		t.Fatal(err)
	}

	// One widened limit, one forged limit_version, and one legitimate edit in
	// the same request. Without the legitimate one, a save that refused
	// everything would pass this test.
	attack := samplePlan("keep")
	steps := attack["steps"].([]any)
	step := steps[1].(map[string]any)
	step["checks"] = []any{map[string]any{"field": "v", "op": "contains", "value": "0x"}}
	step["timeout_ms"] = 9999
	attack["limit_version"] = "forged"

	if r := postJSON(t, srv, "/api/plan", map[string]any{"name": "keep", "plan": attack}); r["error"] != nil {
		t.Fatalf("the save was refused outright, so the parameter edit was lost too: %v", r["error"])
	}

	after, err := ptplan.Load(filepath.Join(dir, "keep.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := after.Steps[1].Checks, before.Steps[1].Checks; !reflect.DeepEqual(got, want) {
		t.Errorf("a widened limit reached the file: %+v, want %+v", got, want)
	}
	if after.LimitVersion != before.LimitVersion {
		t.Errorf("limit_version = %q, want the file's own %q", after.LimitVersion, before.LimitVersion)
	}
	if after.Steps[1].TimeoutMS != 9999 {
		t.Errorf("timeout_ms = %d, want the edit to have gone through", after.Steps[1].TimeoutMS)
	}
}

// A step the file has never heard of has no limits on disk to keep, and making
// some up would be saving a step that passes on anything.
func TestSavingAPlanRefusesAStepTheFileDoesNotHave(t *testing.T) {
	srv, dir := planPanel(t, plainBoard)

	if r := postJSON(t, srv, "/api/plan", map[string]any{"name": "keep", "plan": samplePlan("keep")}); r["error"] != nil {
		t.Fatalf("saving the plan to start from: %v", r["error"])
	}

	smuggled := samplePlan("keep")
	smuggled["steps"] = append(smuggled["steps"].([]any), map[string]any{
		"id": "smuggled", "type": "PtRaw", "command": "pt.id",
		"checks": []any{map[string]any{"field": "_text", "op": "contains", "value": ""}},
	})
	if r := postJSON(t, srv, "/api/plan", map[string]any{"name": "keep", "plan": smuggled}); r["error"] == nil {
		t.Fatal("a step that is not in the file was accepted, limits and all")
	}

	after, err := ptplan.Load(filepath.Join(dir, "keep.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Steps) != 2 {
		t.Fatalf("the file has %d steps, want the 2 it had", len(after.Steps))
	}
}

func TestPlanNameCannotLeaveThePlansFolder(t *testing.T) {
	srv, _ := planPanel(t, plainBoard)
	for _, name := range []string{"", "..", "../evil", `..\evil`, "sub/evil"} {
		r := getJSON(t, srv, "/api/plan?name="+url.QueryEscape(name))
		if r["error"] == nil {
			t.Errorf("name %q was accepted as a plan file", name)
		}
	}
}

func TestPlanCheckNeedsABoardToSayAnything(t *testing.T) {
	srv, _ := planPanel(t, plainBoard)

	// Unconnected, the page must say why it is silent rather than show a clean
	// bill of health: no findings and no board look identical otherwise.
	r := postJSON(t, srv, "/api/plan/check", map[string]any{"plan": samplePlan("p")})
	if note, _ := r["note"].(string); note == "" {
		t.Fatalf("checking with no board said nothing about the missing board: %v", r)
	}

	if st := postJSON(t, srv, "/api/connect", map[string]any{"port": "COM_TEST"}); st["connected"] != true {
		t.Fatalf("connect did not take: %v", st)
	}

	// din and rs485 are what capsReply reports; a plan naming anything else has
	// to be caught here rather than at the bench.
	p := samplePlan("p")
	p["steps"].([]any)[1].(map[string]any)["port"] = "nosuch"
	r = postJSON(t, srv, "/api/plan/check", map[string]any{"plan": p})
	findings, _ := r["findings"].([]any)
	if len(findings) == 0 || !strings.Contains(findings[0].(string), "nosuch") {
		t.Fatalf("the board's caps did not catch an unknown port: %v", r)
	}

	r = postJSON(t, srv, "/api/plan/check", map[string]any{"plan": samplePlan("p")})
	if clean, _ := r["findings"].([]any); len(clean) != 0 {
		t.Fatalf("a plan the board can run was reported as a problem: %v", clean)
	}
}

func TestPlanRunAnswersEachFrameExactlyOnce(t *testing.T) {
	var echoed []string
	srv, _ := planPanel(t, func(cmd string, nth int) ([]string, []string) {
		switch {
		case strings.HasPrefix(cmd, "pt.start din"):
			return []string{"OK din started"}, []string{
				"!din t=1 seq=1 rx=0 miss=0 v=0x01",
				"!din t=2 seq=2 rx=1 miss=0 v=0x01",
				"!din t=3 seq=3 rx=2 miss=0 v=0x01",
			}
		case strings.HasPrefix(cmd, "pt.echo "):
			echoed = append(echoed, cmd)
			return []string{"OK " + strings.TrimPrefix(cmd, "pt.echo ") + " noted"}, nil
		}
		return plainBoard(cmd, nth)
	})

	if r := postJSON(t, srv, "/api/plan/run", map[string]any{"plan": samplePlan("p")}); r["error"] == nil {
		t.Fatal("a run with no board connected should have been refused")
	}

	postJSON(t, srv, "/api/connect", map[string]any{"port": "COM_TEST"})
	r := postJSON(t, srv, "/api/plan/run", map[string]any{"plan": samplePlan("p"), "sn": "SN-1"})
	if r["error"] != nil {
		t.Fatalf("run: %v", r["error"])
	}

	rep, _ := r["report"].(map[string]any)
	steps, _ := rep["steps"].([]any)
	if len(steps) != 2 {
		t.Fatalf("report has %d steps, want 2: %v", len(steps), rep)
	}
	for _, s := range steps {
		m := s.(map[string]any)
		if m["outcome"] != "PASS" {
			t.Errorf("step %v is %v: %v", m["id"], m["outcome"], m["reason"])
		}
	}
	if rep["sn"] != "SN-1" {
		t.Errorf("the serial number did not reach the report: %v", rep["sn"])
	}

	// The point of the whole exercise: the executor answered, the panel's
	// responder did not, so there is one reply per frame and not two.
	if want := []string{"pt.echo din 1", "pt.echo din 2", "pt.echo din 3"}; !equalStrings(echoed, want) {
		t.Fatalf("echoes = %v, want %v", echoed, want)
	}

	// And the responder is back on afterwards, or the next manual session on
	// this connection would report misses nobody caused.
	if st := getJSON(t, srv, "/api/state"); st["autoEcho"] != true {
		t.Error("auto echo was left switched off after the run")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A plan the firmware cannot run as written is refused on the panel exactly as
// the CLI refuses it (decision 77): a report from it would be a verdict on the
// plan, not on the board.
func TestPlanRunRefusesAPlanThatDoesNotFitTheFirmware(t *testing.T) {
	var started bool
	srv, _ := planPanel(t, func(cmd string, nth int) ([]string, []string) {
		if strings.HasPrefix(cmd, "pt.start") {
			started = true
		}
		return plainBoard(cmd, nth)
	})
	postJSON(t, srv, "/api/connect", map[string]any{"port": "COM_TEST"})

	p := samplePlan("p")
	p["steps"].([]any)[1].(map[string]any)["port"] = "nosuch"
	r := postJSON(t, srv, "/api/plan/run", map[string]any{"plan": p})
	// The refusal is a dictionary key; the findings themselves stay as the plan checker words them.
	e, _ := r["error"].(map[string]any)
	args, _ := e["args"].(map[string]any)
	msg, _ := args["findings"].(string)
	if e["$t"] != "go.plan.mismatch" || !strings.Contains(msg, "nosuch") {
		t.Fatalf("an unknown port was not refused by name: %v", r)
	}
	if r["report"] != nil || started {
		t.Fatalf("the plan ran anyway (report %v, started %v)", r["report"] != nil, started)
	}
}
