package ptpanel

import (
	"encoding/json"
	"net/http"
	"sync"

	"PortTool/src/ptboard"
	"PortTool/src/ptplan"
	"PortTool/src/ptproto"
	"PortTool/src/simboard"
)

// The panel judges by the criteria in a plan file and ptcheck, the same as a
// production station: there is no second copy of a criterion (PORTTOOL-FLOW.md
// 「判定和序列全在上位机」, decision 77).
const defaultCriteriaPlan = "station6-poweron.json"

// judgePlan is the plan the panel judges by, chosen from the page; empty means
// the default. Guarded: handlers read it while the page changes it.
var (
	judgeMu   sync.RWMutex
	judgePlan string
)

func criteriaPlanName() string {
	judgeMu.RLock()
	defer judgeMu.RUnlock()
	if judgePlan != "" {
		return judgePlan
	}
	return defaultCriteriaPlan
}

// setCriteriaPlan points the panel at another plan's limits. A plan that does
// not load is refused here rather than leaving every port silently unjudged.
func setCriteriaPlan(name string) error {
	if name == "" {
		judgeMu.Lock()
		judgePlan = ""
		judgeMu.Unlock()
		return nil
	}
	path, err := safePlanPath(name)
	if err != nil {
		return err
	}
	if _, err := ptplan.Load(path); err != nil {
		return err
	}
	judgeMu.Lock()
	judgePlan = name
	judgeMu.Unlock()
	return nil
}

// criteriaSource says which plan the panel is judging by and which limit set
// it declares; the screen's half of the report's plan and limit_version.
func criteriaSource() (name, limits, problem string) {
	name = criteriaPlanName()
	path, err := safePlanPath(name)
	if err != nil {
		return name, "", err.Error()
	}
	plan, err := ptplan.Load(path)
	if err != nil {
		return name, "", err.Error()
	}
	return name, plan.LimitVersion, ""
}

// handleCriteria reports or changes which plan supplies the panel's limits.
func (s *Server) handleCriteria(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var body struct {
			Plan string `json:"plan"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, m("go.judge.bad_request"))
			return
		}
		s.full.mu.Lock()
		full := s.full.running
		s.full.mu.Unlock()
		if s.libBusy() || full {
			writeErr(w, 409, m("lib.err.busy"))
			return
		}
		if err := setCriteriaPlan(body.Plan); err != nil {
			writeErr(w, 400, m("go.judge.plan_switch_failed", "detail", err))
			return
		}
		// Results on the cards were judged by the old limits (decision 30, PORTTOOL-FLOW.md C.3.6).
		s.clearLib()
	}
	name, limits, problem := criteriaSource()
	writeJSON(w, 200, map[string]any{
		"plan": name, "limitVersion": limits, "problem": problem,
	})
}

// boardUID asks the connected board for its UID now, rather than trusting one
// remembered from earlier: a board swapped between walks must not inherit
// the previous board's archive. The simulated board is refused: its readings
// are invented, and filed under a UID they would pass for a board's.
func (s *Server) boardUID() (string, error) {
	uid, sim, err := s.askUID()
	if err != nil {
		return "", err
	}
	if sim {
		return "", msgError{m("go.uid.sim")}
	}
	return uid, nil
}

// askUID sends pt.id, the simulated board included: a range registration is
// a soldering fact, not a reading, so the simulator may keep one (C.3.2).
func (s *Server) askUID() (uid string, sim bool, err error) {
	s.mu.Lock()
	b, name := s.board, s.portNam
	s.mu.Unlock()
	if b == nil {
		return "", false, msgError{m("go.uid.no_board")}
	}
	uid, err = uidOf(b)
	return uid, err == nil && simboard.IsSim(name), err
}

// handleUID is the connected board's UID, asked once per connection: the R&D
// port pages keep their last round per board (snap|<UID>|<port>, STG-39).
func (s *Server) handleUID(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	b, uid := s.board, s.uid
	s.mu.Unlock()
	if b == nil || uid != "" {
		writeJSON(w, 200, map[string]any{"uid": uid})
		return
	}
	uid, _, err := s.askUID()
	if err != nil {
		writeJSON(w, 200, map[string]any{"uid": "", "error": msgOf(err)})
		return
	}
	s.mu.Lock()
	if s.board == b {
		s.uid = uid
	}
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"uid": uid})
}

// uidOf asks one connected board for its UID (pt.id).
func uidOf(b *ptboard.Board) (string, error) {
	lines, err := b.Send("pt.id", ptboard.ExpectFor("pt.id"), ptboard.TimeoutFor("pt.id"))
	if err != nil {
		return "", msgError{m("go.uid.no_answer", "detail", err)}
	}
	for _, l := range lines {
		if uid, ok := ptproto.Get(ptproto.Fields(l), "uid"); ok {
			return uid, nil
		}
	}
	return "", msgError{m("go.uid.missing")}
}
