package ptpanel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"PortTool/src/simboard"
	"PortTool/src/stlink"
	"PortTool/src/stlink/stlinktest"
)

// The connection block (PORTTOOL-FLOW.md C.3.6, "连接区"). The clock and the
// reference board run on the simulated board; the ST-Link on the fake CLI.

func call(t *testing.T, srv *httptest.Server, path string, body any) map[string]any {
	t.Helper()
	var resp *http.Response
	var err error
	if body == nil {
		resp, err = http.Get(srv.URL + path)
	} else {
		b, _ := json.Marshal(body)
		resp, err = http.Post(srv.URL+path, "application/json", bytes.NewReader(b))
	}
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func needSim(t *testing.T) {
	t.Helper()
	if _, err := simboard.Find(); err != nil {
		t.Skipf("simulated board not built: %v", err)
	}
}

func clockTime(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	// A poll is skipped while another command is on the line, e.g. the board
	// address lookup right after connecting (boardip.go).
	var c map[string]any
	for end := time.Now().Add(3 * time.Second); c == nil && time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
		c, _ = call(t, srv, "/api/conn", nil)["clock"].(map[string]any)
	}
	if c == nil {
		t.Fatal("no clock while connected")
	}
	if e, _ := c["error"].(string); e != "" {
		t.Fatalf("clock error: %v", c["error"])
	}
	return c["date"].(string) + " " + c["time"].(string)
}

func TestBoardClockMovesOnTheSimulatedBoard(t *testing.T) {
	needSim(t)
	s := New()
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	defer s.Close()

	if c := call(t, srv, "/api/conn", nil)["clock"]; c != nil {
		t.Fatalf("clock before connecting: %v", c)
	}
	if st := call(t, srv, "/api/connect", map[string]any{"port": "sim"}); st["connected"] != true {
		t.Fatalf("connect: %v", st)
	}
	first := clockTime(t, srv)
	time.Sleep(2100 * time.Millisecond)
	second := clockTime(t, srv)
	if first == second {
		t.Fatalf("board time stood still over two seconds: %s", first)
	}
}

func TestStlinkNotConnectedViaFake(t *testing.T) {
	_, restore := stlinktest.Install(t.TempDir())
	defer restore()
	inner := stlink.Run
	stlink.Run = func(cli string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "--list" {
			return "===== STLink Interface =====\nNo ST-LINK detected!\n", nil
		}
		return inner(cli, args...)
	}

	s := New()
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	st := call(t, srv, "/api/conn", map[string]any{"stlink": true})["stlink"].(map[string]any)
	if st["checked"] != true || st["cli"] != true || len(st["probes"].([]any)) != 0 || st["error"] != "" {
		t.Fatalf("want checked, CLI present, no probe: %v", st)
	}
}

func TestReferenceBoardConnectAndDisconnect(t *testing.T) {
	needSim(t)
	s := New()
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	defer s.Close()

	if r := call(t, srv, "/api/ref", nil); r["connected"] != false {
		t.Fatalf("connected before asked: %v", r)
	}
	r := call(t, srv, "/api/ref", map[string]any{"port": "sim"})
	if r["connected"] != true || r["firmware"] == "" || s.refBoard() == nil {
		t.Fatalf("connect: %v", r)
	}
	// Independent of the control port: connecting that leaves the ref alone.
	call(t, srv, "/api/connect", map[string]any{"port": "sim"})
	call(t, srv, "/api/disconnect", map[string]any{})
	if s.refBoard() == nil {
		t.Fatal("dropping the control port dropped the reference board")
	}
	r = call(t, srv, "/api/ref", map[string]any{"port": ""})
	if r["connected"] != false || s.refBoard() != nil {
		t.Fatalf("disconnect: %v", r)
	}
}
