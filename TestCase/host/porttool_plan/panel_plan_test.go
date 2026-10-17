package testcase

// What the panel still does with plan files, driven against the scripted board:
// list them for the header's criteria selector, and resolve the test-item
// catalogue's criteria against the chosen one. Editing and running a plan is
// the CLI's (decision 102).

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"PortTool/src/portmap"
	"PortTool/src/ptpanel"
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

// No plan tab (decision 102); the criteria selector stays.
func TestPageHasCriteriaSelectorAndNoPlanTab(t *testing.T) {
	srv, _ := planPanel(t, plainBoard)
	resp, err := srv.Client().Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `id="critplan"`) {
		t.Error("the served page has no criteria selector")
	}
	for _, gone := range []string{`data-tab="plan"`, `id="planbody"`} {
		if strings.Contains(string(body), gone) {
			t.Errorf("the served page still has %s", gone)
		}
	}
	for _, path := range []string{"/api/plan?name=x", "/api/plan/run", "/api/plan/check"} {
		r, err := srv.Client().Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.Header.Get("Content-Type") == "application/json; charset=utf-8" {
			t.Errorf("%s still answers", path)
		}
	}
}

// A name from the browser picks a file in the plans folder and nothing else.
func TestCriteriaPlanNameCannotLeaveThePlansFolder(t *testing.T) {
	srv, _ := planPanel(t, plainBoard)
	for _, name := range []string{"..", "../evil", `..\evil`, "sub/evil"} {
		r := postJSON(t, srv, "/api/criteria", map[string]any{"plan": name})
		if r["error"] == nil {
			t.Errorf("name %q was accepted as a plan file", name)
		}
	}
}

// /api/items is the catalogue cut down to the board's ports, each criterion
// resolved against the plan the panel judges by.
func TestItemsFollowCapsAndCriteriaPlan(t *testing.T) {
	srv, dir := planPanel(t, plainBoard)
	plan := samplePlan("crit")
	plan["steps"] = append(plan["steps"].([]any), map[string]any{
		"id": "digital-in", "type": "PtSession", "port": "din", "frames": 3,
		"checks": []any{map[string]any{"field": "v", "op": "eq", "value": "0xFF"}},
	})
	b, _ := json.Marshal(plan)
	if err := os.WriteFile(filepath.Join(dir, "crit.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if r := postJSON(t, srv, "/api/criteria", map[string]any{"plan": "crit.json"}); r["error"] != nil {
		t.Fatalf("switching criteria: %v", r["error"])
	}
	t.Cleanup(func() { postJSON(t, srv, "/api/criteria", map[string]any{"plan": ""}) })

	if r := getJSON(t, srv, "/api/items"); r["connected"] != false {
		t.Fatalf("unconnected /api/items = %v", r)
	}
	postJSON(t, srv, "/api/connect", map[string]any{"port": "fake"})
	r := getJSON(t, srv, "/api/items")
	ports := map[string]bool{}
	for _, p := range r["ports"].([]any) {
		ports[p.(map[string]any)["port"].(string)] = true
	}
	if !ports["din"] || !ports["rs485"] || !ports["sdram"] || len(ports) != 3 {
		t.Fatalf("ports = %v, want exactly the board's din, rs485, sdram", ports)
	}
	var level map[string]any
	for _, x := range r["items"].([]any) {
		it := x.(map[string]any)
		if !ports[it["port"].(string)] {
			t.Errorf("item %v is for a port the board did not report", it["id"])
		}
		if it["id"] == "din.level" {
			level = it
		}
	}
	if level == nil {
		t.Fatal("no din.level item")
	}
	crit := level["criteria"].([]any)[0].(map[string]any)
	if crit["step"] != "digital-in" || crit["missing"] != false || len(crit["checks"].([]any)) != 1 {
		t.Fatalf("din.level criteria = %v, want the plan's digital-in check", crit)
	}
}
