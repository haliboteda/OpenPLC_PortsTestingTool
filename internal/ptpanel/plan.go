package ptpanel

// The plan page: load a plan file, edit how its steps measure, check it against
// the board that is connected, and run it.
//
// *** Parameters are editable and limits are not. A parameter says how to
// *** measure - period, byte count, baud - and changing one only changes the
// *** measurement, which is then judged by the same limits as before. A limit
// *** says what counts as a pass, and relaxing one ships a board that failed.
// *** So limits are changed by swapping in a named plan file, never on screen:
// *** DECISIONS.md 30, restated in 34. keepLimits below is where that holds.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"PortTool/internal/portmap"
	"PortTool/internal/ptplan"
	"PortTool/internal/ptseq"
)

// planState is everything the plan page needs that the manual panel does not.
type planState struct {
	mu      sync.Mutex
	running bool
}

// PlanDir is where the panel looks for plan files. Set before Serve; empty
// means the plans directory beside the executable, then ./TestCase/plans.
var PlanDir string

func (s *Server) planRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/plans", s.handlePlanList)
	mux.HandleFunc("/api/plan", s.handlePlanLoadSave)
	mux.HandleFunc("/api/plan/check", s.handlePlanCheck)
	mux.HandleFunc("/api/plan/run", s.handlePlanRun)
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
	return filepath.Join("TestCase", "plans")
}

// safePlanPath keeps a request inside the plans directory. The panel binds to
// 127.0.0.1, but a path from a browser is still input, and ".." in it would
// read or overwrite whatever the operator's account can reach.
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

func (s *Server) handlePlanLoadSave(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		s.savePlan(w, r)
		return
	}
	path, err := safePlanPath(r.URL.Query().Get("name"))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}
	plan, err := ptplan.Load(path)
	if err != nil {
		// The parser's own words: they name the step and the field, which is
		// more use than "could not load".
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"plan": plan, "name": filepath.Base(path)})
}

func (s *Server) savePlan(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string      `json:"name"`
		Plan ptplan.Plan `json:"plan"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}
	path, err := safePlanPath(body.Name)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}
	// What the file says now, for the limits to be taken from and for the log
	// line below to have something to compare against.
	before, hadBefore := ptplan.Plan{}, false
	if _, err := os.Stat(path); err == nil {
		if p, err := ptplan.Load(path); err == nil {
			before, hadBefore = p, true
		}
	}
	// Limits come off the disk, never off the wire. See DECISIONS.md 34.
	if err := keepLimits(&body.Plan, path); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}
	// Validated before it is written, never after: a plan file on disk that
	// does not load is one somebody will try to run on a line.
	if err := body.Plan.Validate(); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}
	data, err := json.MarshalIndent(body.Plan, "", "  ")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}
	s.emit("saved " + filepath.Base(path))
	if hadBefore {
		for _, line := range paramChanges(before, body.Plan) {
			s.emit(filepath.Base(path) + ": " + line)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"saved": filepath.Base(path)})
}

// paramChanges names every parameter this save moved, and what it moved from.
//
// The saved file itself only shows where a parameter ended up. Which readings
// on screen were taken before the change and which after is a question the log
// answers and the file cannot - and it is the question somebody asks when two
// runs of the same plan disagree. DECISIONS.md 34.
func paramChanges(before, after ptplan.Plan) []string {
	was := make(map[string]ptplan.Params, len(before.Steps))
	for _, s := range before.Steps {
		was[s.ID] = s.Params
	}
	var out []string
	for _, s := range after.Steps {
		old, known := was[s.ID]
		if !known {
			continue
		}
		for k, v := range s.Params {
			if o, had := old[k]; !had {
				out = append(out, fmt.Sprintf("%s %s set to %s", s.ID, k, v))
			} else if o != v {
				out = append(out, fmt.Sprintf("%s %s %s -> %s", s.ID, k, o, v))
			}
		}
		for k, o := range old {
			if _, still := s.Params[k]; !still {
				out = append(out, fmt.Sprintf("%s %s removed (was %s)", s.ID, k, o))
			}
		}
	}
	// Map order is random, and a log line that comes out differently every
	// time is one nobody can diff.
	sort.Strings(out)
	return out
}

// keepLimits puts the limits back the way the file on disk has them, so a save
// can only ever have changed how a step measures, never what counts as a pass.
//
// The panel draws the checks read-only, so nothing here should normally have a
// different value to put back. That is exactly why it is done on this side as
// well: a page is what a browser sends, and the one thing that must not be
// takeable from a browser is the number that decides whether a board ships.
//
// limit_version travels with them. It is the report's whole traceability
// claim - "these readings were judged by that limit set" - and a claim that a
// period edit could rewrite is not a claim.
//
// A step the file does not have is refused rather than merged: it would be a
// step whose limits nothing on disk can supply, and inventing empty ones would
// mean saving a step that passes on anything.
//
// A name with no file yet is a new plan, and there is nothing to keep: its
// limits are the ones it is being created with, under its own name and its own
// limit_version. That is the sanctioned way to get different limits (DECISIONS
// 30) - what is barred is quietly moving the ones a named plan already has.
func keepLimits(p *ptplan.Plan, path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	onDisk, err := ptplan.Load(path)
	if err != nil {
		return fmt.Errorf("cannot re-read %s to keep its limits: %v", filepath.Base(path), err)
	}
	byID := make(map[string]ptplan.Step, len(onDisk.Steps))
	for _, s := range onDisk.Steps {
		byID[s.ID] = s
	}
	for i := range p.Steps {
		was, ok := byID[p.Steps[i].ID]
		if !ok {
			return fmt.Errorf(
				"step %q is not in %s, and limits are only ever taken from the file - "+
					"reload the plan and try again",
				p.Steps[i].ID, filepath.Base(path))
		}
		p.Steps[i].Checks = was.Checks
	}
	p.LimitVersion = onDisk.LimitVersion
	return nil
}

// handlePlanCheck reports what only the connected board can settle.
func (s *Server) handlePlanCheck(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Plan ptplan.Plan `json:"plan"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}
	if err := body.Plan.Validate(); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}

	s.mu.Lock()
	connected := s.board != nil
	caps := s.caps
	s.mu.Unlock()

	if !connected {
		writeJSON(w, http.StatusOK, map[string]any{
			"findings": []string{},
			"note":     "connect a board to check ports, parameters and limits",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"findings": body.Plan.CheckAgainstCaps(caps)})
}

func (s *Server) handlePlanRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Plan ptplan.Plan `json:"plan"`
		SN   string      `json:"sn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}
	if err := body.Plan.Validate(); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}

	s.mu.Lock()
	board := s.board
	caps := s.caps
	port := s.portNam
	echoWas := s.autoEcho
	s.mu.Unlock()

	if board == nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": "connect a board first"})
		return
	}
	// Refused like the CLI refuses it (decision 77): a report from a plan the
	// firmware cannot run as written is a verdict on the plan, not the board.
	if findings := body.Plan.CheckAgainstCaps(caps); len(findings) > 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"error":    m("go.plan.mismatch", "findings", strings.Join(findings, "\n- ")),
			"findings": findings,
		})
		return
	}

	// The executor answers the loop counters itself while a plan runs.
	// Leaving the panel's responder on as well would answer each frame twice,
	// and the board would count the second as a stale reply - a miss on a
	// link that is working.
	s.mu.Lock()
	s.autoEcho = false
	s.mu.Unlock()

	s.plan.mu.Lock()
	if s.plan.running {
		s.plan.mu.Unlock()
		s.mu.Lock()
		s.autoEcho = echoWas
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"error": "a plan is already running"})
		return
	}
	s.plan.running = true
	s.plan.mu.Unlock()

	defer func() {
		s.plan.mu.Lock()
		s.plan.running = false
		s.plan.mu.Unlock()
		s.mu.Lock()
		s.autoEcho = echoWas
		s.mu.Unlock()
	}()

	s.emit(fmt.Sprintf("running %s (limits %s)", body.Plan.Name, body.Plan.LimitVersion))

	runner := &ptseq.Runner{
		Board:       board,
		Caps:        &caps,
		Log:         func(line string) { s.emit(line) },
		ToolVersion: s.Version,
		SN:          body.SN,
		PortName:    port,
		BaseDir:     planDir(),
		SerialPeer:  portmap.Peer,
		// A UserConfirm step fails here rather than blocking: the run is one
		// synchronous request, so there is nowhere to put the question.
		Confirm: func(prompt string) (bool, error) {
			return false, fmt.Errorf(
				"this step needs an operator answer, which the panel cannot ask for yet: %s", prompt)
		},
	}

	report, err := runner.Run(body.Plan)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}

	s.emit(report.Summary())
	writeJSON(w, http.StatusOK, map[string]any{"report": report, "summary": report.Summary()})
}
