package ptpanel

// The plan files beside the exe. The panel only lists them, for the header's
// "criteria from which plan" selector; editing and running a plan is the CLI's
// (decision 102). Limits change by picking another named plan, never on screen
// (DECISIONS.md 30).

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PlanDir is where the panel looks for plan files. Set before Serve; empty
// means the plans directory beside the executable, then ./src/plans.
var PlanDir string

func (s *Server) planRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/plans", s.handlePlanList)
}

func planDir() string {
	if PlanDir != "" {
		return PlanDir
	}
	if exe, err := os.Executable(); err == nil {
		beside := filepath.Join(filepath.Dir(exe), "plans")
		if st, err := os.Stat(beside); err == nil && st.IsDir() {
			return beside
		}
	}
	return filepath.Join("src", "plans")
}

// safePlanPath keeps a request inside the plans directory: a path from the
// browser is still input, even on 127.0.0.1.
func safePlanPath(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("no plan named")
	}
	if strings.ContainsAny(name, `/\`) || name == ".." {
		return "", fmt.Errorf("a plan name is a file in the plans folder, not a path")
	}
	if !strings.HasSuffix(name, ".json") {
		name += ".json"
	}
	return filepath.Join(planDir(), name), nil
}

func (s *Server) handlePlanList(w http.ResponseWriter, r *http.Request) {
	dir := planDir()
	entries, err := os.ReadDir(dir)
	out := map[string]any{"dir": dir}
	if err != nil {
		out["error"] = fmt.Sprintf("cannot read %s: %v", dir, err)
		writeJSON(w, http.StatusOK, out)
		return
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	out["plans"] = names
	writeJSON(w, http.StatusOK, out)
}
