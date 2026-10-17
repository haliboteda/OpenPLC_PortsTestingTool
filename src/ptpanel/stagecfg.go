package ptpanel

// The three configurations - functional (2), aging (4), user-level (6) - and
// the executor that runs steps 2 and 4 of a full test from them.
// $PROD/docs/modules/M4/PORTTOOL-FLOW.md C.3.6 "三种配置"; decisions 97, 102;
// STG-04, STG-26, STG-27.

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"PortTool/src/calstore"
	"PortTool/src/ptitems"
	"PortTool/src/ptlib"
	"PortTool/src/ptproto"
	"PortTool/src/refboard"
	"PortTool/src/ptreport"
	"PortTool/src/ptstages"
)

// StageCfgFile overrides where the configurations are kept; tests set it.
var StageCfgFile string

var stageCfgMu sync.Mutex

// itemCfg is what differs from the catalogue's default for one item.
type itemCfg struct {
	On     *bool             `json:"on,omitempty"`
	Peer   ptitems.Peer      `json:"peer,omitempty"`
	Params map[string]string `json:"params,omitempty"`
	// Points are an AI / AO read or output item's test points; not a
	// firmware parameter, so not in Params (decision 97, C.3.2).
	Points []float64 `json:"points,omitempty"`
}

// stageCfg is one configuration. Only changes are recorded, so an item added
// to the catalogue later still gets its default.
type stageCfg struct {
	Duration string             `json:"duration,omitempty"`
	PortsOff map[string]bool    `json:"ports_off,omitempty"`
	Items    map[string]itemCfg `json:"items,omitempty"`
}

// stageCfgs is porttool_stagecfg.json.
type stageCfgs struct {
	Func  stageCfg `json:"func"`
	Aging stageCfg `json:"aging"`
	User  stageCfg `json:"user"`
	// RefUnused is the connection zone's "reference board: unused" (STG-27).
	RefUnused bool `json:"ref_unused"`
	// Ranges are the AI / AO ranges all three test the board under test on,
	// one set shared by them (decision 139); missing channels read as the default.
	Ranges calstore.Ranges `json:"ranges,omitempty"`
}

// cfgStages are the configurable test types, and their default durations (the prototype's).
var cfgStages = map[ptitems.Stage]string{ptitems.Func: "once", ptitems.Aging: "4", ptitems.User: "once"}

// durations are what R&D's timed group offers; forever is -1.
var durations = map[string]time.Duration{
	"once": 0, "1": time.Hour, "2": 2 * time.Hour, "3": 3 * time.Hour, "4": 4 * time.Hour, "forever": -1,
}

// stageDurations are what each sub-test's time picker offers, hours as bare
// numbers (decision 125; PORTTOOL-FLOW.md C.3.6 「三种配置」). The functional
// test runs once only (decision 139).
var stageDurations = map[ptitems.Stage][]string{
	ptitems.Func:  {"once"},
	ptitems.Aging: {"1", "2", "4", "8"},
	ptitems.User:  {"once", "10m", "30m", "1"},
}

var cfgDurations = map[string]time.Duration{
	"once": 0, "10m": 10 * time.Minute, "30m": 30 * time.Minute,
	"1": time.Hour, "2": 2 * time.Hour, "4": 4 * time.Hour, "8": 8 * time.Hour,
}

func durationOffered(st ptitems.Stage, d string) bool {
	for _, x := range stageDurations[st] {
		if x == d {
			return true
		}
	}
	return false
}

func (f *stageCfgs) of(st ptitems.Stage) *stageCfg {
	switch st {
	case ptitems.Func:
		return &f.Func
	case ptitems.Aging:
		return &f.Aging
	case ptitems.User:
		return &f.User
	}
	return nil
}

func stageCfgPath() string {
	if StageCfgFile != "" {
		return StageCfgFile
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "porttool_stagecfg.json")
	}
	return "porttool_stagecfg.json"
}

// loadStageCfgs reads the file; missing or unreadable means every default.
func loadStageCfgs() stageCfgs {
	stageCfgMu.Lock()
	defer stageCfgMu.Unlock()
	var f stageCfgs
	if b, err := os.ReadFile(stageCfgPath()); err != nil || json.Unmarshal(b, &f) != nil {
		f = stageCfgs{}
	}
	f.Ranges = cfgRanges(f.Ranges)
	for st, d := range cfgStages {
		c := f.of(st)
		// A duration the picker no longer offers (3 h, forever before decision 125) reads as the default.
		if !durationOffered(st, c.Duration) {
			c.Duration = d
		}
		// A file saved before an item or a parameter was retired still loads
		// (ptitems.RetiredItem, ptitems.Retired).
		for id, ic := range c.Items {
			if ptitems.RetiredItem(id) {
				delete(c.Items, id)
				continue
			}
			ptitems.DropRetired(id, ic.Params)
		}
	}
	return f
}

// cfgRanges is a saved range set with the decision-134 default for every
// channel missing or not one it can be soldered for.
func cfgRanges(saved calstore.Ranges) calstore.Ranges {
	r := calstore.DefaultRanges()
	for n, v := range saved {
		if _, ok := r[n]; ok && v.ValidFor(n) {
			r[n] = v
		}
	}
	return r
}

// setRanges changes the shared ranges; a channel that changed range loses its
// test points in every configuration, so they come back as the new range's
// defaults (decision 139).
func (f *stageCfgs) setRanges(set calstore.Ranges) *msg {
	for n, v := range set {
		if !v.ValidFor(n) {
			return ref(m("go.stagecfg.bad_range", "channel", n, "range", string(v)))
		}
	}
	for n, v := range set {
		if f.Ranges[n] == v {
			continue
		}
		f.Ranges[n] = v
		for _, st := range []ptitems.Stage{ptitems.Func, ptitems.Aging, ptitems.User} {
			c := f.of(st)
			for id, ic := range c.Items {
				if it, ok := catalogueItem(id); ok && ptlib.IsAnalogReading(it) && ptlib.AnalogChannel(it) == n {
					ic.Points = nil
					c.Items[id] = ic
				}
			}
		}
	}
	return nil
}

func saveStageCfgs(f stageCfgs) error {
	stageCfgMu.Lock()
	defer stageCfgMu.Unlock()
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(stageCfgPath(), append(b, '\n'), 0o644)
}

// on says whether an item is ticked: as recorded, else ticked, except that of
// items excluding each other only the catalogue's first starts ticked (same
// rule as cfgOn in web/js/stagecfg.js).
func (c stageCfg) on(st ptitems.Stage, it ptitems.Item) bool {
	if ic, ok := c.Items[it.ID]; ok && ic.On != nil {
		return *ic.On
	}
	if defaultOff(it) {
		return false // ticked, it would refuse the step (decisions 129, 135)
	}
	if it.Exclusive == "" {
		return true
	}
	for _, o := range ptitems.Catalogue {
		if (o.ID == it.ID || ptitems.Excludes(o, it)) && o.In(st) {
			return o.ID == it.ID
		}
	}
	return true
}

// peer is the saved choice, else the connection-dependent default decided when
// the configuration runs (ptitems.DefaultPeer, decision 112). With the
// reference board set to unused the first peer stays, so picks substitutes it
// (decision 135).
func (c stageCfg) peer(it ptitems.Item, refUnused, refConnected bool) ptitems.Peer {
	if fixedRef(it) {
		for _, p := range it.Peers {
			if p == ptitems.PeerRef {
				return p
			}
		}
		return it.Peers[0] // no automatic route: configWhy refuses it
	}
	if p := c.Items[it.ID].Peer; p != "" {
		return p
	}
	if refUnused {
		return it.Peers[0]
	}
	return ptitems.DefaultPeer(it, refConnected)
}

// fixedRef says an item runs against the reference board in every
// configuration, never a person: the fixture tests are unattended (decisions 129, 130).
func fixedRef(it ptitems.Item) bool {
	switch it.Port {
	case "ain", "aout", "din", "dout", "relay":
		return true
	}
	return false
}

// defaultOff is an item a configuration starts unticked: one the reference
// board cannot test, or an analog channel the default wiring leaves without a
// reference channel (refboard.Default, decisions 134, 135). /api/items hands it to
// the page's cfgOn.
func defaultOff(it ptitems.Item) bool {
	if noAutoRoute(it) {
		return true
	}
	// Opt-in: it adds time to every board (decision 139).
	if it.ID == "sdram.hold" {
		return true
	}
	if fixedRef(it) && it.Channel > 0 && (it.Port == "ain" || it.Port == "aout") && !ptlib.IsCalibration(it) {
		return refboard.Default().RefFor(ptlib.AnalogChannel(it)) == ""
	}
	return false
}

// noAutoRoute is a fixedRef item that offers no reference-board peer.
func noAutoRoute(it ptitems.Item) bool {
	if !fixedRef(it) {
		return false
	}
	for _, p := range it.Peers {
		if p == ptitems.PeerRef {
			return false
		}
	}
	return true
}

func catalogueItem(id string) (ptitems.Item, bool) {
	for _, it := range ptitems.Catalogue {
		if it.ID == id {
			return it, true
		}
	}
	return ptitems.Item{}, false
}

// check refuses what a configuration cannot hold. Values are checked against
// the board's limits when a board is connected (hasCaps).
func (c stageCfg) check(st ptitems.Stage, caps ptproto.Caps, hasCaps bool) *msg {
	if !durationOffered(st, c.Duration) {
		return ref(m("go.stagecfg.bad_duration", "duration", c.Duration))
	}
	for p := range c.PortsOff {
		known := false
		for _, pi := range ptitems.Ports {
			known = known || pi.Port == p
		}
		if !known {
			return ref(m("go.stagecfg.unknown_port", "port", p))
		}
	}
	for id, ic := range c.Items {
		it, ok := catalogueItem(id)
		if !ok || !it.In(st) {
			return ref(m("go.stagecfg.unknown_item", "item", id))
		}
		if ic.Peer != "" {
			allowed := false
			for _, p := range it.Peers {
				allowed = allowed || p == ic.Peer
			}
			if !allowed {
				return ref(m("go.stagecfg.bad_peer", "item", m(it.Name), "peer", string(ic.Peer)))
			}
		}
		if len(ic.Points) > 0 && (!ptlib.IsAnalogReading(it) || !ptlib.PointsOK(ic.Points)) {
			return ref(m("lib.err.points", "item", m(it.Name)))
		}
		// A point outside every range of the channel is never stored (C.3.6 「填数超量程」).
		if len(ic.Points) > 0 {
			lo, fs := channelBounds(ptlib.AnalogChannel(it))
			for _, x := range ic.Points {
				if x < lo || x > fs {
					return ref(m("go.memo.out_of_range", "value", x, "lo", lo, "hi", fs))
				}
			}
		}
		for k, v := range ic.Params {
			editable := false
			for _, p := range it.Params {
				editable = editable || p == k
			}
			if !editable || v == "" || strings.ContainsAny(v, " \t=") || !ptlib.HostParamOK(k, v) {
				return ref(m("go.stagecfg.bad_param", "item", m(it.Name), "param", k, "value", v))
			}
			if bad := loadTargetBad(k, v); bad != nil {
				return bad
			}
			if hasCaps {
				if p, ok := caps.Port(it.Port); ok {
					if l, ok := p.Limits[k]; ok {
						if fine, _ := l.Accepts(v); !fine {
							return ref(m("go.stagecfg.param_limit", "item", m(it.Name), "param", k, "value", v, "spec", l.Spec))
						}
					}
				}
			}
		}
	}
	// One session cannot run in two modes at once (decision 102); once-only
	// and repeatable items of one group may both be ticked (decision 128).
	var on []ptitems.Item
	for _, it := range ptitems.Catalogue {
		if it.Exclusive == "" || !it.In(st) || !c.on(st, it) {
			continue
		}
		for _, o := range on {
			if ptitems.Excludes(it, o) {
				return ref(m("go.stagecfg.exclusive", "port", it.Port, "a", m(o.Name), "b", m(it.Name)))
			}
		}
		on = append(on, it)
	}
	return nil
}

// ItemPick is one item as a configuration runs it. Peer is after the
// substitution for an unused reference board; Note says why it differs from
// the configured one.
type ItemPick struct {
	Item   ptitems.Item
	Peer   ptitems.Peer
	Params map[string]string
	Points []float64 // AI / AO test points; empty = the defaults
	Note   string
}

// ItemRunner is what the step executors need from the item runner (lane L2's
// "run these items"). timed=false: the one pick runs once. timed=true: the
// picks run together until stageTimeUp(ctx) closes, ctx is done or one fails;
// returning because the time is up is not a failure. Contract in
// PORTTOOL-FLOW.md C.3.6.
type ItemRunner func(ctx context.Context, picks []ItemPick, timed bool) ptreport.StepResult

// substText is the note for a fallback peer; literal keys (C.4.1).
// A person stands for "cannot run": there is no self-loop (decision 135).
var substText = map[ptitems.Peer]string{ptitems.PeerHuman: "go.stagecfg.subst.none", ptitems.PeerPC: "go.stagecfg.subst.pc"}

// substitute is an item's peer with the reference board unused: a person,
// else the PC. There is no self-loop (decision 135); an AI / AO / DI / DO /
// relay item thus turns to a person and configWhy refuses it.
func substitute(it ptitems.Item) (ptitems.Peer, msg) {
	for _, want := range []ptitems.Peer{ptitems.PeerHuman, ptitems.PeerPC} {
		for _, p := range it.Peers {
			if p == want {
				return want, m(substText[want])
			}
		}
	}
	return ptitems.PeerRef, msg{}
}

// picks is the configuration as this board runs it, in catalogue order.
func (c stageCfg) picks(st ptitems.Stage, caps ptproto.Caps, refUnused, refConnected bool) []ItemPick {
	_, items := ptitems.ForCaps(caps)
	var out []ItemPick
	for _, it := range items {
		if !it.In(st) || c.PortsOff[it.Port] || !c.on(st, it) {
			continue
		}
		p := ItemPick{Item: it, Peer: c.peer(it, refUnused, refConnected), Params: c.Items[it.ID].Params, Points: c.Items[it.ID].Points}
		if p.Peer == ptitems.PeerRef && refUnused {
			var note msg
			p.Peer, note = substitute(it)
			p.Note = note.English()
		}
		out = append(out, p)
	}
	return out
}

func (s *Server) stageCfgJSON() map[string]any {
	f := loadStageCfgs()
	out := map[string]any{"func": f.Func, "aging": f.Aging, "user": f.User, "ref_unused": f.RefUnused,
		"ranges": f.Ranges, "channels": chanInfos(f.Ranges)}
	// What the reference board as registered cannot test, per item (decisions 134, 135).
	if !f.RefUnused && s.refBoard() != nil {
		mm := map[string]any{}
		s.mu.Lock()
		caps := s.caps
		s.mu.Unlock()
		for _, st := range []ptitems.Stage{ptitems.Func, ptitems.Aging, ptitems.User} {
			for _, p := range f.of(st).picks(st, caps, false, true) {
				if p.Peer != ptitems.PeerRef {
					continue
				}
				if why := s.refCannot(p); why != nil {
					mm[p.Item.ID] = *why
				}
			}
		}
		out["mismatch"] = mm
	}
	if f.RefUnused {
		subst := map[string]any{}
		for _, it := range ptitems.Catalogue {
			for _, p := range it.Peers {
				if p == ptitems.PeerRef {
					peer, note := substitute(it)
					subst[it.ID] = map[string]any{"peer": peer, "note": note}
				}
			}
		}
		out["subst"] = subst
	}
	return out
}

// handleStageCfg reads and saves the configurations. Saving sends nothing to
// the board: only Start starts a test (decision 97).
func (s *Server) handleStageCfg(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var body struct {
			Stage     ptitems.Stage `json:"stage"`
			Cfg       *stageCfg       `json:"cfg"`
			RefUnused *bool           `json:"ref_unused"`
			Ranges    calstore.Ranges `json:"ranges"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, m("go.stagecfg.bad_request"))
			return
		}
		f := loadStageCfgs()
		if body.Cfg != nil {
			if _, ok := cfgStages[body.Stage]; !ok {
				writeErr(w, 400, m("go.stagecfg.bad_stage", "stage", string(body.Stage)))
				return
			}
			s.mu.Lock()
			caps, hasCaps := s.caps, s.board != nil
			s.mu.Unlock()
			c := *body.Cfg
			if c.Duration == "" {
				c.Duration = cfgStages[body.Stage]
			}
			for id, ic := range c.Items {
				if ptitems.RetiredItem(id) {
					delete(c.Items, id) // a page opened before the retirement
					continue
				}
				ptitems.DropRetired(id, ic.Params) // a page opened before the retirement
			}
			if bad := c.check(body.Stage, caps, hasCaps); bad != nil {
				out := s.stageCfgJSON()
				out["error"] = *bad
				writeJSON(w, 200, out)
				return
			}
			*f.of(body.Stage) = c
		}
		if body.RefUnused != nil {
			f.RefUnused = *body.RefUnused
		}
		if bad := f.setRanges(body.Ranges); bad != nil {
			out := s.stageCfgJSON()
			out["error"] = *bad
			writeJSON(w, 200, out)
			return
		}
		if err := saveStageCfgs(f); err != nil {
			out := s.stageCfgJSON()
			out["error"] = m("go.stagecfg.not_saved", "detail", err)
			writeJSON(w, 200, out)
			return
		}
	}
	writeJSON(w, 200, s.stageCfgJSON())
}

// stageAfter is the clock the timed part waits on; tests replace it.
var stageAfter = time.After

type timeUpKey struct{}

// stageTimeUp is the full test's clock as the item runner sees it: closed when
// the configured time is up; nil outside a timed part. It ends the timed part
// without cancelling ctx, so relays still short of their count run on
// (decision 123).
func stageTimeUp(ctx context.Context) <-chan struct{} {
	c, _ := ctx.Value(timeUpKey{}).(<-chan struct{})
	return c
}

// configStep is a full-test step run from a saved configuration: steps 2 and 4,
// and the test part of step 6.
func (s *Server) configStep(st ptitems.Stage) ptstages.Func {
	return func(ctx context.Context) ptreport.StepResult {
		fail := func(reason string) ptreport.StepResult {
			return ptreport.StepResult{Outcome: ptreport.OutcomeError, Reason: reason}
		}
		if s.RunItems == nil {
			return fail("the item runner is not part of this build")
		}
		f := loadStageCfgs()
		c := *f.of(st)
		d := cfgDurations[c.Duration]
		s.mu.Lock()
		b, caps := s.board, s.caps
		s.mu.Unlock()
		if b == nil {
			return fail("no board connected")
		}
		// Step 6 asks afresh: the board runs the demo by now, not the fixture
		// firmware the panel connected to (PORTTOOL-FLOW.md C.3.6).
		if st == ptitems.User {
			c, err := b.Caps()
			if err != nil {
				return fail("pt.caps: " + err.Error())
			}
			caps = c
			s.mu.Lock()
			s.caps = c
			s.mu.Unlock()
		}
		picks := c.picks(st, caps, f.RefUnused, s.refBoard() != nil)
		if len(picks) == 0 {
			return fail("the " + string(st) + " configuration picks no item this board has")
		}
		// With a duration the relays run by it, only their counts are configured (decision 123).
		for i, p := range picks {
			if d > 0 && ptlib.RunsToCount(p.Item) {
				picks[i].Params = ptlib.RelayForDuration(p.Params, d)
			}
		}
		return runConfig(ctx, s.RunItems, picks, d)
	}
}

type progressKey struct{}

// stepProgress reports how much of a full-test step is done, 0..1; a no-op
// outside a full test (PORTTOOL-FLOW.md C.3.3 「进度」).
func stepProgress(ctx context.Context, frac float64) {
	if f, ok := ctx.Value(progressKey{}).(func(float64)); ok {
		f(frac)
	}
}

// runConfig runs picks as STG-26 says: with no duration every pick runs once,
// one by one; with one, once-only and person-answered picks run one by one
// first, then the rest run together for d. The first non-pass ends the step.
func runConfig(ctx context.Context, run ItemRunner, picks []ItemPick, d time.Duration) ptreport.StepResult {
	res := ptreport.StepResult{Outcome: ptreport.OutcomePass}
	var single, timed []ItemPick
	for _, p := range picks {
		// Relays join the timed part and are judged at its end (decisions 123, 130);
		// the AO load holds its current to the end, nobody asked (decision 114).
		if d > 0 && !p.Item.Once && (p.Peer != ptitems.PeerHuman || ptlib.RunsToCount(p.Item) || p.Item.Load) {
			timed = append(timed, p)
		} else {
			single = append(single, p)
		}
	}
	// Each single pick is one share of the progress, the timed part one more.
	shares := float64(len(single))
	if len(timed) > 0 {
		shares++
	}
	for i, p := range single {
		if ctx.Err() != nil {
			res.Outcome = ptreport.OutcomeStopped
			return res
		}
		got := run(ctx, []ItemPick{p}, false)
		res.Attempts = append(res.Attempts, got.Attempts...)
		if !got.Outcome.IsPass() {
			res.Outcome, res.Reason = got.Outcome, itemReason(p.Item.ID, got.Reason)
			return res
		}
		stepProgress(ctx, float64(i+1)/shares)
	}
	if len(timed) == 0 {
		return res
	}

	up := make(chan struct{})
	tctx := context.WithValue(ctx, timeUpKey{}, (<-chan struct{})(up))
	done, clockGone := make(chan struct{}), make(chan struct{})
	reached := false
	started := time.Now()
	go func() {
		defer close(clockGone)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		end := stageAfter(d)
		for {
			select {
			case <-end:
				reached = true
				close(up)
				return
			case <-done:
				return
			case <-tick.C:
				stepProgress(ctx, (float64(len(single))+min(1, float64(time.Since(started))/float64(d)))/shares)
			}
		}
	}()
	got := run(tctx, timed, true)
	close(done)
	<-clockGone
	res.Attempts = append(res.Attempts, got.Attempts...)
	switch {
	case ctx.Err() != nil:
		res.Outcome, res.Reason = ptreport.OutcomeStopped, got.Reason
	case !got.Outcome.IsPass():
		res.Outcome, res.Reason = got.Outcome, got.Reason
	case !reached:
		// A timed run that ends on its own has not run for the configured time.
		res.Outcome = ptreport.OutcomeError
		res.Reason = "timed items ended after " + time.Since(started).Round(time.Second).String() + " of " + d.String()
	}
	return res
}

func itemReason(id, reason string) string {
	if reason == "" {
		return id
	}
	return id + ": " + reason
}
