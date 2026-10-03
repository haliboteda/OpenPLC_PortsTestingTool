package ptpanel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"PortTool/internal/simboard"
)

// exitEnv makes this test binary stand in for a simulated board that dies at
// once: started as the "simulator", it exits with this code before answering.
const exitEnv = "PTPANEL_TEST_SIM_EXIT"

func TestMain(m *testing.M) {
	if os.Getenv(exitEnv) != "" {
		os.Exit(3)
	}
	os.Exit(m.Run())
}

// A simulated board that exits must be reported as that, with its exit code -
// not as firmware built without PORTTOOL_ENABLE, which is advice for a board.
func TestSimulatorThatExitsIsNamedWithItsCode(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PORTTOOL_SIM", exe)
	t.Setenv(exitEnv, "1")

	s := &Server{autoEcho: true, Open: simboard.OpenPort}
	defer s.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/connect", strings.NewReader(`{"port":"sim"}`))
	s.handleConnect(rec, req)

	var state map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatalf("connect answered %d %q", rec.Code, rec.Body.String())
	}
	e, _ := state["capsError"].(map[string]any)
	args, _ := e["args"].(map[string]any)
	if e["$t"] != "go.caps.sim_exited" || args["code"] != float64(3) {
		t.Fatalf("capsError = %v, want it to say the simulator exited with code 3", state["capsError"])
	}
}
