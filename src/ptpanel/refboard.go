package ptpanel

// The reference board's settings page (decision 135, PORTTOOL-FLOW.md C.3.6
// 「标准机设置页」): its ranges, how it is wired to the board under test, the
// parameters the panel runs it with, and what that leaves the fixture tests
// able to test. refImpact is the one place that says whether a channel can be
// tested against the reference board; the full-test gate uses it too.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"PortTool/src/calstore"
	"PortTool/src/ptboard"
	"PortTool/src/ptitems"
	"PortTool/src/ptlib"
	"PortTool/src/ptproto"
	"PortTool/src/refboard"
	"PortTool/src/simboard"
)

func (s *Server) refRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/refboard", s.handleRefBoard)
	mux.HandleFunc("/api/refboard/check", s.handleRefBoardCheck)
	mux.HandleFunc("/api/refboard/status", s.handleRefBoardStatus)
}

// refRanges is the reference board's UID and the ranges registered for it.
func (s *Server) refRanges() (string, calstore.Ranges, bool, error) {
	b := s.refBoard()
	if b == nil {
		return "", nil, false, msgError{m("lib.reason.no_ref")}
	}
	uid, err := uidOf(b)
	if err != nil {
		return "", nil, false, err
	}
	r, reg, err := calstore.LoadRanges(uid)
	return uid, r, reg, err
}

// refWiring is the connected reference board's registration, the default
// when it has none or no reference board is connected.
func (s *Server) refWiring() refboard.Wiring {
	uid, _, _, err := s.refRanges()
	if err != nil {
		return refboard.Default()
	}
	w, _, err := refboard.Load(calstore.BoardDir(uid))
	if err != nil {
		return refboard.Default()
	}
	return w
}

// dutRanges is the ranges the fixture tests test the board under test on:
// the configurations' shared set, not its R&D registration (decision 139).
func (s *Server) dutRanges() calstore.Ranges {
	return loadStageCfgs().Ranges
}

// rangeNameKey is each range's name on the page (analog.js anaRangeName).
var rangeNameKey = map[calstore.Range]string{
	calstore.RangeVoltage: "ana.range.v", calstore.RangeCurrent: "ana.range.i",
	calstore.RangeAO020: "ana.range.i20", calstore.RangeAO420: "ana.range.i420",
}

// dutCalRangeWhy is the configuration card's 「校准时是……」 mark, word for word
// (rangeinfo.js rngSummary): the card and the gate say one sentence.
func dutCalRangeWhy(calibrated calstore.Range) msg {
	return m("rng.kb_other_range", "r", m(rangeNameKey[calibrated]))
}

// dutCalRanges is the range each channel of the board under test was
// calibrated on, from its local archive; nil on the simulated board, which
// keeps none (C.3.2), or when the board cannot be asked.
func (s *Server) dutCalRanges() calstore.Ranges {
	s.mu.Lock()
	uid, sim := s.uid, simboard.IsSim(s.portNam)
	s.mu.Unlock()
	if sim {
		return nil
	}
	if uid == "" {
		u, _, err := s.askUID()
		if err != nil {
			return nil
		}
		uid = u
	}
	loc, _, err := calstore.Archived(uid)
	if err != nil || len(loc) == 0 {
		return nil
	}
	out := calstore.Ranges{}
	def := calstore.DefaultRanges()
	for ch, l := range loc {
		// An archive without a range is on the product default (calstore.Channel).
		if out[ch] = l.Range; out[ch] == "" {
			out[ch] = def[ch]
		}
	}
	return out
}

// impactRow is one channel of one fixture-test item against the reference board.
type impactRow struct {
	Item string `json:"item"`
	Name msg    `json:"name"`
	Ch   string `json:"ch"`  // the board under test's channel: AI1, DO3, RY2
	Ref  string `json:"ref"` // the reference channel serving it, "" when none
	OK   bool   `json:"ok"`
	Why  *msg   `json:"why,omitempty"`
}

// refCalInfo is what refImpact knows of the reference board's calibration:
// which channels carry k, b for its UID, and the range each was archived with.
type refCalInfo struct {
	have  map[string]bool
	burnt map[string]calstore.Range
	known bool // false: not read (the gate does not ask the board)
}

// refImpact says, channel by channel, what the fixture tests can test with
// this registration and these ranges. A nil dut skips the voltage / current
// match: the settings page leaves it to whoever wires (decision 140). dutCal
// is the range each channel of the board under test was calibrated on (its
// archive); a channel calibrated on another range than dut cannot be tested
// (STG-40 row 11). Nil skips it.
func refImpact(w refboard.Wiring, dut, refR calstore.Ranges, cal refCalInfo, dutCal calstore.Ranges) []impactRow {
	var out []impactRow
	item := func(id string) ptitems.Item { it, _ := catalogueItem(id); return it }
	add := func(id, ch, refCh string, why *msg) {
		out = append(out, impactRow{Item: id, Name: m(item(id).Name), Ch: ch, Ref: refCh, OK: why == nil, Why: why})
	}
	analog := func(id, dutCh string) {
		r := w.RefFor(dutCh)
		switch {
		case r == "":
			add(id, dutCh, "", ref(m("refboard.why.unwired", "ch", dutCh)))
		case cal.known && !cal.have[r]:
			add(id, dutCh, r, ref(m("refboard.why.no_cal", "ref", r)))
		// The exact range: 0-20 mA and 4-20 mA differ (PORTTOOL-FLOW.md C.3.2 「档位与换算」).
		case cal.known && cal.burnt[r] != "" && cal.burnt[r] != refR[r]:
			add(id, dutCh, r, ref(m("refboard.why.cal_range", "ref", r)))
		case dut != nil && dutCal[dutCh] != "" && dutCal[dutCh] != dut[dutCh]:
			add(id, dutCh, r, ref(dutCalRangeWhy(dutCal[dutCh])))
		case dut != nil && refR[r].Current() != dut[dutCh].Current():
			add(id, dutCh, r, ref(m("go.full.range_mismatch", "item", m(item(id).Name), "dut", dutCh, "ref", r)))
		default:
			add(id, dutCh, r, nil)
		}
	}
	analog("ain.read1", "AI1")
	analog("ain.read2", "AI2")
	analog("aout.out1", "AO1")
	analog("aout.out2", "AO2")
	analog("aout.load", "AO1")
	analog("aout.load", "AO2")
	for n := 1; n <= 8; n++ {
		l := w.DILink(n)
		ch := "DI" + strconv.Itoa(n)
		if l.To == 0 {
			add("din.level", ch, "", ref(m("refboard.why.unwired", "ch", ch)))
		} else {
			add("din.level", ch, "DO"+strconv.Itoa(l.To), nil)
		}
	}
	for n := 1; n <= 8; n++ {
		l := w.DOLink(n)
		ch := "DO" + strconv.Itoa(n)
		if l.To == 0 {
			add("dout.load", ch, "", ref(m("refboard.why.unwired", "ch", ch)))
		} else {
			add("dout.load", ch, "DI"+strconv.Itoa(l.To), nil)
		}
	}
	for k := 1; k <= refboard.Relays; k++ {
		ch := "RY" + strconv.Itoa(k)
		do := w.DOOfRelay(k)
		switch {
		case do == 0:
			add("relay.cycle", ch, "", ref(m("refboard.why.relay_unchained", "relay", ch)))
		case w.DOLink(do).To == 0:
			add("relay.cycle", ch, "", ref(m("refboard.why.unwired", "ch", "DO"+strconv.Itoa(do))))
		default:
			add("relay.cycle", ch, "DI"+strconv.Itoa(w.DOLink(do).To), nil)
		}
	}
	return out
}

// pickChannels is the channels a configured pick tests, as refImpact names them.
func pickChannels(p ItemPick) []string {
	switch p.Item.ID {
	case "aout.load":
		return []string{"AO1", "AO2"}
	case "din.level":
		return []string{"DI1", "DI2", "DI3", "DI4", "DI5", "DI6", "DI7", "DI8"}
	}
	var out []string
	for _, c := range ptlib.Channels(p.Item, p.Peer, p.Params) {
		out = append(out, ptlib.ChanName(p.Item.Port, c))
	}
	if len(out) == 0 && p.Item.Channel > 0 {
		out = append(out, ptlib.AnalogChannel(p.Item))
	}
	return out
}

// pickBaud is the CAN exchange's bit rate a pick runs with: its own, else the
// criteria plan's.
func pickBaud(p ItemPick, caps ptproto.Caps) string {
	if b := p.Params["baud"]; b != "" {
		return b
	}
	plan, _ := libPlan()
	return ptlib.Defaults(p.Item, plan, caps)["baud"]
}

// canRow is the CAN exchange of one configuration against the reference
// board's registered rate: both ends must run the same (decision 135).
func canRow(w refboard.Wiring, step int, baud string) impactRow {
	r := impactRow{Item: "can.peer", Name: m("refboard.can_row", "sub", stepName(step)), Ch: "CAN", Ref: "CAN", OK: true}
	if refB := strconv.Itoa(w.Params.CANBaud); baud != refB {
		r.OK, r.Why = false, ref(m("refboard.why.can_baud", "dut", baud, "ref", refB))
	}
	return r
}

// canRows are canRow for every configuration that runs the CAN exchange
// against the reference board.
func (s *Server) canRows(w refboard.Wiring) []impactRow {
	f := loadStageCfgs()
	s.mu.Lock()
	caps := s.caps
	s.mu.Unlock()
	var out []impactRow
	for _, sp := range []struct {
		st   ptitems.Stage
		step int
	}{{ptitems.Func, 2}, {ptitems.Aging, 4}, {ptitems.User, 6}} {
		for _, p := range f.of(sp.st).picks(sp.st, caps, f.RefUnused, true) {
			if p.Item.ID == "can.peer" && p.Peer == ptitems.PeerRef {
				out = append(out, canRow(w, sp.step, pickBaud(p, caps)))
			}
		}
	}
	return out
}

// refCannot is why a configured pick cannot run against the reference board
// as registered, nil when it can. The calibration is not asked here: the run
// says so on its own. A multi-channel item runs while one of its ticked
// channels is testable; the run records the others as not wired or ranges
// mismatched (decisions 129, 134).
func (s *Server) refCannot(p ItemPick) *msg {
	_, refR, _, err := s.refRanges()
	if err != nil {
		return nil // no reference board: configWhy says so on its own
	}
	w := s.refWiring()
	if p.Item.ID == "can.peer" {
		s.mu.Lock()
		caps := s.caps
		s.mu.Unlock()
		if r := canRow(w, 0, pickBaud(p, caps)); !r.OK {
			return ref(m("go.full.ref_cannot", "item", m(p.Item.Name), "ch", r.Ch, "why", *r.Why))
		}
		return nil
	}
	rows := refImpact(w, s.dutRanges(), refR, refCalInfo{}, s.dutCalRanges())
	want := map[string]bool{}
	for _, c := range pickChannels(p) {
		want[c] = true
	}
	var first *impactRow
	testable := 0
	for i, r := range rows {
		if r.Item != p.Item.ID || !want[r.Ch] {
			continue
		}
		if r.OK {
			testable++
		} else if first == nil {
			first = &rows[i]
		}
	}
	if first == nil || (len(want) > 1 && testable > 0) {
		return nil
	}
	return ref(m("go.full.ref_cannot", "item", m(p.Item.Name), "ch", first.Ch, "why", *first.Why))
}

// handleRefBoardStatus is what the settings page refreshes every second: the
// reference board's DI levels and temperatures, each from a short session.
// While any test runs it does not touch the board and the page says 「测试中」
// (decision 135).
func (s *Server) handleRefBoardStatus(w http.ResponseWriter, r *http.Request) {
	b := s.refBoard()
	if b == nil {
		writeJSON(w, 200, map[string]any{"error": m("lib.reason.no_ref")})
		return
	}
	if !s.peekMu.TryLock() {
		writeJSON(w, 200, map[string]any{"busy": true})
		return
	}
	defer s.peekMu.Unlock()
	s.mu.Lock()
	timed := s.run != nil
	s.mu.Unlock()
	s.full.mu.Lock()
	full := s.full.running
	s.full.mu.Unlock()
	if timed || full || s.libBusy() {
		writeJSON(w, 200, map[string]any{"busy": true})
		return
	}
	out := map[string]any{}
	if f, err := peekPort(b, "din", "pt.start din ch=1,2,3,4,5,6,7,8 period=50"); err == nil {
		bits := make([]int, 8)
		if v, err := strconv.ParseUint(frameField(f, "v"), 0, 8); err == nil {
			for i := range bits {
				bits[i] = int(v>>i) & 1
			}
		}
		out["din"] = bits
	} else {
		out["error"] = msgOf(err)
	}
	// The control line answers when any of these does.
	out["ctrl"] = out["error"] == nil
	if lines, err := b.Send("pt.run eth.link", ptboard.ExpectOne, ptboard.DefaultTimeout); err == nil {
		eth := map[string]string{}
		for _, l := range lines {
			for _, p := range ptproto.Fields(l) {
				if p.Key == "link" || p.Key == "speed" || p.Key == "fd" {
					eth[p.Key] = p.Value
				}
			}
		}
		out["eth"] = eth
	}
	// mode=bus sends nothing of its own: it only reports the bus state.
	can := fmt.Sprintf("pt.start can mode=bus baud=%d period=200", s.refWiring().Params.CANBaud)
	if f, err := peekPort(b, "can", can); err == nil {
		out["can"] = map[string]string{"tec": frameField(f, "tec"), "rec": frameField(f, "rec"),
			"lec": frameField(f, "lec"), "bus": frameField(f, "bus")}
	}
	if f, err := peekPort(b, "temp", "pt.start temp ch=1,2 period=200"); err == nil {
		var t []string
		for _, ch := range []string{"ch1", "ch2"} {
			parts := strings.Split(frameField(f, ch), "/")
			if dc, err := strconv.Atoi(parts[len(parts)-1]); err == nil && len(parts) == 2 {
				t = append(t, strconv.FormatFloat(float64(dc)/10, 'f', 1, 64))
			} else {
				t = append(t, "")
			}
		}
		out["temp"] = t
	}
	writeJSON(w, 200, out)
}

func frameField(f ptproto.Frame, k string) string {
	v, _ := ptproto.Get(f.Fields, k)
	return v
}

// refCalOf reads the reference board's calibration and archive for the page.
func (s *Server) refCalOf(uid string) (boardCal, refCalInfo) {
	bc := readCalOf(s.refBoard(), uid)
	info := refCalInfo{have: map[string]bool{}, burnt: map[string]calstore.Range{}, known: true}
	for ch := range bc.Ch {
		info.have[ch] = true
	}
	if local, _, err := calstore.Archived(uid); err == nil {
		for ch, l := range local {
			info.burnt[ch] = l.Range
		}
	}
	return bc, info
}

func (s *Server) refBoardState() map[string]any {
	uid, refR, _, err := s.refRanges()
	if err != nil {
		return map[string]any{"error": msgOf(err)}
	}
	w, saved, err := refboard.Load(calstore.BoardDir(uid))
	out := map[string]any{"uid": uid, "saved": saved, "wiring": w, "ranges": chanInfos(refR),
		"defaults": refboard.DefaultParams()}
	if err != nil {
		out["load_error"] = m("go.detail", "detail", err.Error())
	}
	s.conn.mu.Lock()
	out["port"], out["firmware"] = s.conn.ref.port, s.conn.ref.firmware
	s.conn.mu.Unlock()
	bc, info := s.refCalOf(uid)
	out["cal"], out["burnt"] = bc, info.burnt
	out["impact"] = append(refImpact(w, nil, refR, info, nil), s.canRows(w)...)
	out["ports"] = s.refPortValues()
	return out
}

// refPortValues is what the reference board reports now for the ports the
// page shows (pt.caps vals=).
func (s *Server) refPortValues() map[string]map[string]string {
	out := map[string]map[string]string{}
	b := s.refBoard()
	if b == nil {
		return out
	}
	c, err := b.Caps()
	if err != nil {
		return out
	}
	for _, port := range []string{"rs232", "eth", "can", "rs485", "din", "ain"} {
		if p, ok := c.Port(port); ok {
			out[port] = p.Values
		}
	}
	return out
}

type refBoardBody struct {
	Wiring refboard.Wiring  `json:"wiring"`
	Ranges calstore.Ranges `json:"ranges"`
}

// problemsOf words a registration's problems for the page.
func problemsOf(ps []refboard.Problem) []msg {
	out := make([]msg, 0, len(ps))
	for _, p := range ps {
		kv := []any{}
		for k, v := range p.Args {
			kv = append(kv, k, v)
		}
		out = append(out, m(p.Key, kv...))
	}
	return out
}

func checkRefRanges(r calstore.Ranges) error {
	for ch, v := range r {
		if !v.ValidFor(ch) {
			return fmt.Errorf("channel %s cannot be soldered for range %q", ch, v)
		}
	}
	return nil
}

// handleRefBoardCheck works out problems and impact of unsaved settings, so
// the page shows them as they are typed (decision 135).
func (s *Server) handleRefBoardCheck(w http.ResponseWriter, r *http.Request) {
	var body refBoardBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, m("go.judge.bad_request"))
		return
	}
	uid, refR, _, err := s.refRanges()
	if err != nil {
		writeJSON(w, 200, map[string]any{"error": msgOf(err)})
		return
	}
	for ch, v := range body.Ranges {
		if v.ValidFor(ch) {
			refR[ch] = v
		}
	}
	_, info := s.refCalOf(uid)
	writeJSON(w, 200, map[string]any{"problems": problemsOf(body.Wiring.Check()),
		"impact": append(refImpact(body.Wiring, nil, refR, info, nil), s.canRows(body.Wiring)...)})
}

// handleRefBoard reads (GET) or saves (POST) the reference board's settings:
// ranges into its ranges.json, wiring and parameters into refboard.json, both
// by its UID; nothing is saved when either is refused.
func (s *Server) handleRefBoard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 200, s.refBoardState())
		return
	}
	var body refBoardBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, m("go.judge.bad_request"))
		return
	}
	uid, _, _, err := s.refRanges()
	if err != nil {
		writeJSON(w, 200, map[string]any{"error": msgOf(err)})
		return
	}
	if ps := body.Wiring.Check(); len(ps) > 0 {
		out := s.refBoardState()
		out["error"], out["problems"] = m("refboard.refused"), problemsOf(ps)
		writeJSON(w, 200, out)
		return
	}
	if err := checkRefRanges(body.Ranges); err != nil {
		writeJSON(w, 200, map[string]any{"error": m("go.detail", "detail", err.Error())})
		return
	}
	now := time.Now()
	if _, err := calstore.SaveRanges(uid, body.Ranges, now); err != nil {
		writeJSON(w, 200, map[string]any{"error": m("go.detail", "detail", err.Error())})
		return
	}
	path, _, err := refboard.Save(calstore.BoardDir(uid), uid, body.Wiring, now)
	if err != nil {
		writeJSON(w, 200, map[string]any{"error": m("go.detail", "detail", err.Error())})
		return
	}
	s.say(m("refboard.saved", "uid", uid, "path", strings.TrimSpace(path)))
	writeJSON(w, 200, s.refBoardState())
}
