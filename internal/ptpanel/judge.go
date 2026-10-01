package ptpanel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"PortTool/internal/ptcal"
	"PortTool/internal/ptcheck"
	"PortTool/internal/ptplan"
	"PortTool/internal/ptproto"
	"PortTool/internal/ptseq"
)

// Judging a port's readings on the panel, using the criteria that already
// exist in a plan file.
//
// *** The point of doing it this way is that there is no second copy of a
// *** criterion. *** The panel showing a green tick and a production station
// showing a pass have to mean the same thing, and the only way to be sure of
// that is for both to read the same file and run the same evaluator
// (internal/ptcheck). A table of criteria in the panel's JavaScript would look
// simpler and would drift the first time a limit changed.
//
// Which plan supplies them is one name, so a bench with different wiring can
// point the panel at its own file without touching code.
const defaultCriteriaPlan = "station6-poweron.json"

// judgePlan is the plan the panel judges by, chosen from the page. Empty means the default.
//
// Guarded because the panel can now change it while requests are in flight:
// somebody switching to the relaxed limits does so from the browser, and the
// HTTP handlers reading it run on their own goroutines.
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

// setCriteriaPlan points the panel at another plan's limits.
//
// The name is checked against the plans directory before it is kept, so a bad
// one is refused here rather than silently leaving every port unjudged - which
// is what a plan that fails to load looks like from the screen.
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
// that plan declares.
//
// It goes into the state the page renders, because a verdict means nothing
// without it: the same board is a pass under one limit set and a fail under
// another, and somebody reading a green tick has to be able to see which one
// produced it. The report already carries plan and limit_version for the same
// reason; this is the screen's half of it.
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

// criteriaFor returns the checks the plan states for one board port, and the
// step they came from.
//
// A port with no step in the plan has no criteria, and that is reported as
// such rather than as a pass: "nothing said about this port" and "this port is
// fine" are different answers, and conflating them is how a station passes a
// port nobody ever wrote a limit for.
// A step disabled for production still supplies criteria here. The two
// questions are different: `enabled` says whether a station runs the step, and
// the checks say what passing means. The burn-in is the case - it has no place
// in a 30-second station, but somebody at a bench starts it by hand and needs
// the same verdict a rack run would give.
//
// When `target` is given it is matched exactly, and the port is only used for
// sessions. Matching a run reply by its port instead was a real bug, found by
// the browser test on 2026-09-08: sdram has three targets, the lookup returned
// the first step for the port (sdram.probe's), and judging sdram.crc's
// reply by it reported "no field size" on a chip that had just passed
// everything. One port, several targets, several sets of criteria.
// criteriaFor finds the step whose criteria apply to this reply.
//
// *** A target may appear more than once. *** One plan runs sd.integrity twice
// - once for a large single round, once for 64 small ones - because those
// prove different things about a card. They carry different criteria, so the
// arguments the run was made with are what picks between them. With no
// arguments to go on, the first step for that target wins, which is what the
// panel shows by default.
func criteriaFor(port, target string, args map[string]string) ([]ptcheck.Check, string, bool) {
	var firstChecks []ptcheck.Check
	var firstID string
	haveFirst := false
	path, err := safePlanPath(criteriaPlanName())
	if err != nil {
		return nil, "", false
	}
	plan, err := ptplan.Load(path)
	if err != nil {
		return nil, "", false
	}
	for _, st := range plan.Steps {
		if target != "" {
			if st.Type == ptplan.TypePtRun && st.Target == target {
				if !haveFirst {
					firstChecks, firstID, haveFirst = st.Checks, st.ID, true
				}
				if stepArgsMatch(st, args) {
					return st.Checks, st.ID, true
				}
			}
			continue
		}
		if st.Type == ptplan.TypePtSession && st.Port == port {
			return st.Checks, st.ID, true
		}
	}
	if haveFirst {
		return firstChecks, firstID, true
	}
	return nil, "", false
}

// stepArgsMatch says whether a step was written for the arguments this run
// used. Absent on either side counts as matching: most targets take none, and
// a page that sent nothing should get the plan's own step rather than nothing.
func stepArgsMatch(st ptplan.Step, args map[string]string) bool {
	if len(args) == 0 {
		return len(st.Params) == 0
	}
	for k, v := range args {
		if fmt.Sprint(st.Params[k]) != v {
			return false
		}
	}
	return len(st.Params) == len(args)
}

// handlePortPlan tells the page how the plan starts this port, before it is
// started.
//
// *** This is what makes a panel verdict and a production verdict the same
// *** claim. *** The criteria were written for particular parameters - can's
// are written for mode=extloop, and judging a mode=normal session by them
// would fail a healthy board. So the page starts the session the way the plan
// does rather than with the port's defaults.
func (s *Server) handlePortPlan(w http.ResponseWriter, r *http.Request) {
	port := r.URL.Query().Get("port")
	if port == "" {
		writeErr(w, 400, "没说要哪个端口。")
		return
	}

	path, err := safePlanPath(criteriaPlanName())
	if err != nil {
		writeJSON(w, 200, map[string]any{"known": false})
		return
	}
	plan, err := ptplan.Load(path)
	if err != nil {
		writeJSON(w, 200, map[string]any{"known": false})
		return
	}

	// *** The pt.run targets carry parameters too, and the page has to send
	// *** them. *** The plan's criteria were written for a particular window -
	// sdram.crc over 64 KiB, sd.integrity over so many bytes - and a page that
	// ran the bare target would get the firmware's default instead and judge
	// it by limits meant for something else. The CLI has always sent them
	// (ptseq.doRun uses step.ParamArgs); the page did not, which made the two
	// reach different verdicts from one plan step - exactly what the comment
	// above this file's criteriaFor says must not happen. Found 2026-09-13,
	// when sdram.crc became the first run target whose criteria depend on its
	// arguments.
	runs := map[string]map[string]string{}
	for _, st := range plan.Steps {
		if st.Type != ptplan.TypePtRun || st.Target == "" {
			continue
		}
		if len(st.Params) == 0 {
			continue
		}
		args := map[string]string{}
		for k, v := range st.Params {
			args[k] = v
		}
		runs[st.Target] = args
	}

	for _, st := range plan.Steps {
		if st.Type != ptplan.TypePtSession || st.Port != port {
			continue
		}
		params := map[string]string{}
		for k, v := range st.Params {
			params[k] = v
		}
		frames := st.Frames
		if frames <= 0 {
			frames = 3
		}
		// What kind of far end this step declares. The page needs it to connect
		// one by itself: a TCP peer it can open, a serial one it cannot - that
		// needs somebody to plug an adapter in.
		peer := ""
		if st.Peer != nil {
			switch {
			case st.Peer.TCP != "":
				peer = "tcp"
			case st.Peer.Serial:
				// A person picks the adapter in the page; the plan never names it.
				peer = "com"
			case st.Peer.USB:
				peer = "usb"
			}
		}
		writeJSON(w, 200, map[string]any{
			"known":  true,
			"step":   st.ID,
			"plan":   criteriaPlanName(),
			"params": params,
			"frames": frames,
			"runs":   runs,
			"peer":   peer,
		})
		return
	}
	// A port with no session step still has run targets that take arguments,
	// so the answer carries them rather than a bare "unknown".
	writeJSON(w, 200, map[string]any{"known": false, "runs": runs})
}

// handleJudge evaluates one port's latest reading against the plan's
// criteria. The page sends the raw lines it already has - the frame, or a
// pt.run reply - and they are read here with the same lookups a plan run uses
// (decision 77), so the page parses nothing itself. No traffic to the board.
func (s *Server) handleJudge(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Port   string   `json:"port"`
		Target string   `json:"target"` // for a pt.run reply
		Line   string   `json:"line"`   // a sample frame, as the board sent it
		Lines  []string `json:"lines"`  // a pt.run reply, every line of it
		// What the page actually sent, when a target can be run more than one
		// way. sd.integrity with passes=1 and with passes=64 are two steps in
		// the plan with two sets of criteria, and judging one by the other
		// fails a healthy card.
		Args map[string]string `json:"args"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Port == "" {
		writeErr(w, 400, "没说要判哪个端口。")
		return
	}

	checks, stepID, found := criteriaFor(body.Port, body.Target, body.Args)
	if !found || len(checks) == 0 {
		// Not an error: most ports have no criteria yet, and the page says so
		// rather than showing a tick nobody earned.
		writeJSON(w, 200, map[string]any{
			"port":  body.Port,
			"known": false,
			"why": "方案 " + criteriaPlanName() + " 里没有 " +
				what(body.Port, body.Target) + " 的判据 —— " +
				"所以只能给你原始读数，过没过要你自己看",
		})
		return
	}

	var look ptcheck.Lookup
	if body.Target != "" {
		look = ptseq.ReplyLookup(body.Lines)
	} else {
		kind, frameBody := ptproto.Classify(body.Line)
		f, ok := ptproto.ParseFrame(frameBody)
		if kind != ptproto.LineFrame || !ok {
			writeErr(w, 400, "要判的不是一帧采样数据。")
			return
		}
		look = ptseq.FrameLookup(f)
	}

	results, all := ptcheck.EvalAll(checks, look)
	out := make([]map[string]any, 0, len(results))
	for _, res := range results {
		out = append(out, map[string]any{
			// The check itself, not only a sentence about it: the panel is
			// read in Chinese (user 2026-09-11) and composes its own wording
			// from these. Describe() and Why stay English and stay here -
			// they are what the CLI and the CSV report say, and those are
			// English by convention.
			"check":   res.Check,
			"missing": res.Missing,
			"what":    res.Check.Describe(),
			"got":     res.Got,
			"pass":    res.Pass,
			"why":     res.Why,
		})
	}
	writeJSON(w, 200, map[string]any{
		"port":    body.Port,
		"known":   true,
		"pass":    all,
		"step":    stepID,
		"plan":    criteriaPlanName(),
		"results": out,
	})
}

func what(port, target string) string {
	if target != "" {
		return target
	}
	return port
}

// handleCriteria reports or changes which plan supplies the panel's limits.
func (s *Server) handleCriteria(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var body struct {
			Plan string `json:"plan"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "读不懂这个请求。")
			return
		}
		if err := setCriteriaPlan(body.Plan); err != nil {
			writeErr(w, 400, "换不了判据方案："+err.Error())
			return
		}
	}
	name, limits, problem := criteriaSource()
	writeJSON(w, 200, map[string]any{
		"plan": name, "limitVersion": limits, "problem": problem,
	})
}

// handleFit turns the points a calibration walk collected into a straight
// line. The arithmetic is in internal/ptcal for the reason everything else
// here is shared: a production run will fit the same points unattended, and
// the two must not be able to disagree about what they mean.
//
// *** It returns a fit, not a calibrated board. *** Where a coefficient would
// be stored so it survives the tooling image being replaced is still open
// (ISS-C1), so this is shown on the page and nothing is written to the board
// or to disk. Computing is unblocked; storing is not.
func (s *Server) handleFit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Points []struct {
			Want float64  `json:"want"`
			Got  *float64 `json:"got"`
		} `json:"points"`
		GainTol   float64 `json:"gain_tol"`
		OffsetTol float64 `json:"offset_tol"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "没读懂要拟合的那些点。")
		return
	}

	pts := make([]ptcal.Point, 0, len(body.Points))
	for _, p := range body.Points {
		// A skipped reading arrives as null, and dropping it here is the
		// difference between "fitted on the points that exist" and a fit that
		// is silently NaN.
		if p.Got == nil {
			continue
		}
		pts = append(pts, ptcal.Point{Want: p.Want, Got: *p.Got})
	}

	fit, err := ptcal.LinearFit(pts)
	if err != nil {
		writeJSON(w, 200, map[string]any{"known": false, "why": err.Error()})
		return
	}

	gainTol, offsetTol := body.GainTol, body.OffsetTol
	if gainTol <= 0 {
		gainTol = 0.01
	}
	writeJSON(w, 200, map[string]any{
		"known": true,
		"fit":   fit,
		"ideal": fit.Ideal(gainTol, offsetTol),
	})
}
