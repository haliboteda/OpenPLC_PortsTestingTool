package ptpanel

// AI / AO items: range registration per board, the calibration walk's fit and
// its explicit 存到本地, and the board's own k, b (pt.cal) that ptlib judges
// the read / output items with. PORTTOOL-FLOW.md C.3.2, C.3.6.

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"PortTool/src/calarea"
	"PortTool/src/calstore"
	"PortTool/src/ptboard"
	"PortTool/src/ptcal"
	"PortTool/src/ptlib"
	"PortTool/src/ptplan"
	"PortTool/src/ptproto"
)

// analogCal is a channel's conversion on range rg with the board's k, b; the
// formula is the range table's (calstore.SpecOf, decision 136).
func analogCal(channel string, rg calstore.Range, k, b float64) ptlib.AnalogCal {
	lo, fs := rg.Scale()
	out := ptlib.AnalogCal{K: k, B: b, Unit: rg.Unit(), Lo: lo, Min: rg.Min(), FS: fs, Range: rg}
	if strings.HasPrefix(channel, "AI") {
		out.Nominal = rg.AINominal
	}
	return out
}

func (s *Server) analogRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/analog", s.handleAnalog)
	mux.HandleFunc("/api/analog/ranges", s.handleRanges)
	mux.HandleFunc("/api/analog/fit", s.handleAnalogFit)
	mux.HandleFunc("/api/cal/store", s.handleCalStore)
}

// Board calibration states, as the card and the full-run gate tell them apart.
const (
	calOK      = "ok"
	calBlank   = "blank"   // never written
	calCorrupt = "corrupt" // bad CRC or unknown version
	calOther   = "other"   // written for another board's UID
	calUnread  = "unread"  // the board did not answer pt.cal
)

type kb struct {
	K float64 `json:"k"`
	B float64 `json:"b"`
}

type boardCal struct {
	State string        `json:"state"`
	Ch    map[string]kb `json:"ch"`
	Why   *msg          `json:"why,omitempty"`
}

// readBoardCal asks the board for its calibration area now (pt.cal) and
// decodes it with ptproto.ParseCal; only coefficients written for this UID
// count, as the app treats them (SECTOR-15.md).
func (s *Server) readBoardCal(uid string) boardCal {
	s.mu.Lock()
	b := s.board
	s.mu.Unlock()
	return readCalOf(b, uid)
}

// readCalOf is readBoardCal for any connected board, the reference one too.
func readCalOf(b *ptboard.Board, uid string) boardCal {
	out := boardCal{Ch: map[string]kb{}}
	if b == nil {
		out.State, out.Why = calUnread, ref(m("go.uid.no_board"))
		return out
	}
	lines, err := b.Send("pt.cal", ptboard.ExpectFor("pt.cal"), ptboard.TimeoutFor("pt.cal"))
	if err != nil {
		out.State, out.Why = calUnread, ref(m("go.analog.cal_unread", "detail", err.Error()))
		return out
	}
	for _, l := range lines {
		kind, body := ptproto.Classify(l)
		if kind != ptproto.LineOK || !strings.HasPrefix(body, "cal=") {
			continue
		}
		cal, err := ptproto.ParseCal(body)
		switch {
		case errors.Is(err, calarea.ErrCorrupt):
			out.State = calCorrupt
		case err != nil:
			out.State, out.Why = calUnread, ref(m("go.analog.cal_unread", "detail", err.Error()))
		case !cal.Present:
			out.State = calBlank
		default:
			words, perr := calstore.ParseUID(uid)
			if perr != nil || words != cal.UID {
				out.State = calOther
				break
			}
			out.State = calOK
			for i, n := range calarea.Names {
				out.Ch[n] = kb{K: float64(cal.Ch[i].Gain), B: float64(cal.Ch[i].Offset)}
			}
		}
		return out
	}
	out.State, out.Why = calUnread, ref(m("go.analog.cal_unread", "detail", strings.Join(lines, " | ")))
	return out
}

// CalibratedChannels says which of AI1, AI2, AO1, AO2 the connected board
// carries its own calibration for, asking it now (pt.id, pt.cal). The full
// fixture test's gate calls it (decision 97). The area is written four
// channels at a time, so the answer is all four or none. An error means the
// board could not be asked.
func (s *Server) CalibratedChannels() ([]string, error) {
	uid, _, err := s.askUID()
	if err != nil {
		return nil, err
	}
	cal := s.readBoardCal(uid)
	if cal.State == calUnread {
		return nil, msgError{*cal.Why}
	}
	var have []string
	for _, n := range calarea.Names {
		if _, ok := cal.Ch[n]; ok {
			have = append(have, n)
		}
	}
	return have, nil
}

type chanInfo struct {
	Range   calstore.Range   `json:"range"`
	Unit    string           `json:"unit"`
	Lo      float64          `json:"lo"`
	Min     float64          `json:"min"` // the lowest settable value (calstore Range.Min)
	FS      float64          `json:"fs"`
	Options []calstore.Range `json:"options"`
	Specs   []calstore.Spec  `json:"specs"` // each option's jumpers and formula (decision 136)
}

// handleAnalog is what an AI / AO card shows besides its own walk: the
// board's registered ranges, the local archive, the board's own k, b, and
// why 存到本地 / 烧进板子 cannot be used right now.
func (s *Server) handleAnalog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.analogState())
}

func chanInfos(ranges calstore.Ranges) map[string]chanInfo {
	chans := map[string]chanInfo{}
	for _, n := range calarea.Names {
		rg := ranges[n]
		lo, fs := rg.Scale()
		chans[n] = chanInfo{Range: rg, Unit: rg.Unit(), Lo: lo, Min: rg.Min(), FS: fs, Options: calstore.RangeOptions(n), Specs: calstore.SpecsFor(n)}
	}
	return chans
}

func (s *Server) analogState() map[string]any {
	out := map[string]any{}
	uid, sim, err := s.askUID()
	ranges, registered := calstore.DefaultRanges(), false
	if err != nil {
		out["uid_error"] = msgOf(err)
	} else {
		out["uid"], out["sim"] = uid, sim
		rg, reg, rerr := calstore.LoadRanges(uid)
		ranges, registered = rg, reg
		if rerr != nil {
			out["ranges_error"] = m("go.detail", "detail", rerr.Error())
		}
	}
	out["channels"], out["registered"] = chanInfos(ranges), registered

	local, image := map[string]calstore.Local{}, false
	if err == nil && !sim {
		local, image, _ = calstore.Archived(uid)
	}
	out["local"], out["image"] = local, image
	if err == nil {
		out["board"] = s.readBoardCal(uid)
	}

	// Why each button is off; the page adds "no fit yet" itself.
	switch {
	case err != nil:
		out["store_why"] = msgOf(err)
		out["burn_why"] = msgOf(err)
	case sim:
		out["store_why"] = m("go.uid.sim")
		out["burn_why"] = m("go.analog.burn_sim")
	default:
		var missing []string
		for _, n := range calarea.Names {
			if _, ok := local[n]; !ok {
				missing = append(missing, n)
			}
		}
		if len(missing) > 0 || !image {
			out["burn_why"] = m("go.analog.burn_missing", "list", strings.Join(missing, ", "))
		}
	}
	return out
}

// handleRanges registers what the connected board's channels are soldered
// for (decisions 98, 102). The simulated board keeps one too.
func (s *Server) handleRanges(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ranges calstore.Ranges `json:"ranges"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, m("go.judge.bad_request"))
		return
	}
	uid, _, err := s.askUID()
	if err != nil {
		writeJSON(w, 200, map[string]any{"error": msgOf(err)})
		return
	}
	path, err := calstore.SaveRanges(uid, body.Ranges, time.Now())
	if err != nil {
		writeJSON(w, 200, map[string]any{"error": m("go.detail", "detail", err.Error())})
		return
	}
	s.say(m("go.analog.ranges_saved", "uid", uid, "path", path))
	writeJSON(w, 200, s.analogState())
}

// anaPoint is one point of an AI / AO walk, in the registered range's unit.
type anaPoint struct {
	Set  float64  `json:"set"`  // the target: what the source was set to (AI) or the board told to give (AO)
	Raw  *float64 `json:"raw"`  // AI: the board's averaged raw reading
	Want *float64 `json:"want"` // AO: what the board says it puts out (the !aout frame), uncalibrated
	Got  *float64 `json:"got"`  // the instrument; null when skipped
}

type anaWalk struct {
	Channel string     `json:"channel"`
	Points  []anaPoint `json:"points"`
}

func (w anaWalk) isAI() bool { return strings.HasPrefix(w.Channel, "AI") }

// rangeOf is the channel's registered range; without a board UID, the default.
func (s *Server) rangeOf(channel string) calstore.Range {
	if uid, _, err := s.askUID(); err == nil {
		if rg, _, err := calstore.LoadRanges(uid); err == nil {
			return rg[channel]
		}
	}
	return calstore.DefaultRanges()[channel]
}

// fitPoints are the walk's points in the calibration area's direction:
// measured ~= k * nominal + b.
func (w anaWalk) fitPoints(rg calstore.Range) []ptcal.Point {
	var pts []ptcal.Point
	for _, p := range w.Points {
		if p.Got == nil {
			continue
		}
		switch {
		case w.isAI() && p.Raw != nil:
			pts = append(pts, ptcal.Point{Want: rg.AINominal(*p.Raw), Got: *p.Got})
		case !w.isAI() && p.Want != nil:
			pts = append(pts, ptcal.Point{Want: *p.Want, Got: *p.Got})
		}
	}
	return pts
}

func decodeWalk(w http.ResponseWriter, r *http.Request) (anaWalk, bool) {
	var body anaWalk
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, m("go.fit.bad_request"))
		return body, false
	}
	if _, ok := calarea.Index(body.Channel); !ok {
		writeErr(w, 400, m("go.fit.bad_request"))
		return body, false
	}
	return body, true
}

type walkFit struct {
	fit    ptcal.Fit
	pts    []ptcal.Point
	rg     calstore.Range
	fs     float64
	pctFS  float64
	fitErr error
	// gainOff: the reading did not follow the input, most likely the AI
	// jumpers are not soldered; such a fit is never stored (decision 106).
	gainOff bool
}

// A floating AI pin does not follow the source, so its gain lands far from 1;
// a good board is within a few %.
const gainMin, gainMax = 0.5, 1.5

func (s *Server) fitWalk(body anaWalk) walkFit {
	rg := s.rangeOf(body.Channel)
	_, fs := rg.Scale()
	wf := walkFit{rg: rg, fs: fs, pts: body.fitPoints(rg)}
	wf.fit, wf.fitErr = ptcal.LinearFit(wf.pts)
	if wf.fitErr == nil {
		wf.pctFS = math.Abs(wf.fit.MaxResidual) / fs * 100
		wf.gainOff = wf.fit.Gain < gainMin || wf.fit.Gain > gainMax
	}
	return wf
}

// handleAnalogFit fits a calibration walk without storing it. The residual
// is shown against the range's full scale, never judged (decision 99).
func (s *Server) handleAnalogFit(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeWalk(w, r)
	if !ok {
		return
	}
	wf := s.fitWalk(body)
	if wf.fitErr != nil {
		writeJSON(w, 200, map[string]any{"known": false, "why": m("go.analog.fit_failed", "detail", wf.fitErr.Error())})
		return
	}
	pts := make([]map[string]float64, 0, len(wf.pts))
	for _, p := range wf.pts {
		pts = append(pts, map[string]float64{"want": p.Want, "got": p.Got})
	}
	out := map[string]any{
		"known": true, "fit": wf.fit, "points": pts,
		"range": wf.rg, "unit": wf.rg.Unit(), "full_scale": wf.fs, "residual_pct_fs": wf.pctFS,
	}
	if wf.gainOff {
		out["refuse"] = m("ana.gain_off", "k", fmt.Sprintf("%.3f", wf.fit.Gain))
	}
	writeJSON(w, 200, out)
}

// handleCalStore is 存到本地: the walk is refitted here, so what is archived
// is what this server computes, then filed by the board's UID with its range
// (decision 97). Refused on the simulated board and without a usable fit.
func (s *Server) handleCalStore(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeWalk(w, r)
	if !ok {
		return
	}
	uid, err := s.boardUID()
	if err != nil {
		writeJSON(w, 200, map[string]any{"error": msgOf(err)})
		return
	}
	wf := s.fitWalk(body)
	if wf.gainOff {
		writeJSON(w, 200, map[string]any{"error": m("ana.gain_off", "k", fmt.Sprintf("%.3f", wf.fit.Gain))})
		return
	}
	if wf.fitErr != nil {
		writeJSON(w, 200, map[string]any{"error": m("go.analog.fit_failed", "detail", wf.fitErr.Error())})
		return
	}
	plan, limitVersion := criteriaPlanName(), ""
	if path, err := safePlanPath(plan); err == nil {
		if p, err := ptplan.Load(path); err == nil {
			limitVersion = p.LimitVersion
		}
	}
	fs, pct := wf.fs, wf.pctFS
	ch := calstore.Channel{
		Range: wf.rg, Unit: wf.rg.Unit(), Gain: wf.fit.Gain, Offset: wf.fit.Offset,
		MaxResidual: wf.fit.MaxResidual, MaxResidualAt: wf.fit.MaxResidualAt, RMSResidual: wf.fit.RMSResidual,
		FullScale: &fs, ResidualPctFS: &pct,
	}
	for _, p := range wf.pts {
		ch.Points = append(ch.Points, calstore.Point{Want: p.Want, Got: p.Got})
	}
	res, err := calstore.Store(uid, body.Channel, ch, calstore.Meta{PortTool: s.Version, Plan: plan, LimitVersion: limitVersion}, time.Now())
	if err != nil {
		writeJSON(w, 200, map[string]any{"error": m("go.detail", "detail", err.Error())})
		return
	}
	s.say(m("go.analog.stored", "channel", body.Channel, "archive", res.Archive))
	writeJSON(w, 200, map[string]any{"stored": res, "state": s.analogState()})
}

// AnalogSource is what ptlib's AI / AO walk needs: the connected board's own
// k, b read now over pt.cal, and the channel's registered range (C.3.2).
func (s *Server) AnalogSource() ptlib.AnalogSource { return s.analogSourceOn(nil) }

// dutRange is a channel's range: from ranges when given (the fixture tests'
// shared set, decision 139), else the board's registration.
func dutRange(uid, channel string, ranges calstore.Ranges) calstore.Range {
	if ranges != nil {
		return ranges[channel]
	}
	if r, _, err := calstore.LoadRanges(uid); err == nil {
		return r[channel]
	}
	return calstore.DefaultRanges()[channel]
}

// analogSourceOn is AnalogSource on the given ranges; nil = registered.
func (s *Server) analogSourceOn(ranges calstore.Ranges) ptlib.AnalogSource {
	return func(channel string) (ptlib.AnalogCal, error) {
		uid, _, err := s.askUID()
		if err != nil {
			return ptlib.AnalogCal{}, err
		}
		rg := dutRange(uid, channel, ranges)
		cal := s.readBoardCal(uid)
		c, have := cal.Ch[channel]
		if !have {
			why := m("go.analog.not_calibrated", "channel", channel)
			if cal.Why != nil {
				why = *cal.Why
			}
			return ptlib.AnalogCal{}, msgError{why}
		}
		out := analogCal(channel, rg, c.K, c.B)
		out.Calibrated = s.dutCalRanges()[channel]
		return out, nil
	}
}

// RefAnalogSource is AnalogSource for the reference board: its own k, b over
// pt.cal and the ranges registered for its UID (STG-31, decision 97).
func (s *Server) RefAnalogSource() ptlib.AnalogSource {
	return func(channel string) (ptlib.AnalogCal, error) {
		b := s.refBoard()
		if b == nil {
			return ptlib.AnalogCal{}, msgError{m("go.uid.no_board")}
		}
		uid, err := uidOf(b)
		if err != nil {
			return ptlib.AnalogCal{}, err
		}
		rg := calstore.DefaultRanges()[channel]
		if ranges, _, err := calstore.LoadRanges(uid); err == nil {
			rg = ranges[channel]
		}
		cal := readCalOf(b, uid)
		c, have := cal.Ch[channel]
		if !have {
			why := m("go.analog.ref_not_calibrated", "channel", channel)
			if cal.Why != nil {
				why = *cal.Why
			}
			return ptlib.AnalogCal{}, msgError{why}
		}
		return analogCal(channel, rg, c.K, c.B), nil
	}
}

// AnalogNominal is a channel's registered range with k = 1, b = 0: what a
// live card shows on a board that carries no calibration (decision 108).
func (s *Server) AnalogNominal() ptlib.AnalogSource { return s.analogNominalOn(nil) }

// analogNominalOn is AnalogNominal on the given ranges; nil = registered.
func (s *Server) analogNominalOn(ranges calstore.Ranges) ptlib.AnalogSource {
	return func(channel string) (ptlib.AnalogCal, error) {
		uid, _, err := s.askUID()
		if err != nil {
			return ptlib.AnalogCal{}, err
		}
		return analogCal(channel, dutRange(uid, channel, ranges), 1, 0), nil
	}
}
