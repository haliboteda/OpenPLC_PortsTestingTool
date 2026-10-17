package ptpanel

// What the page remembers per test type besides the three configurations:
// R&D's ticks, peers, parameters, durations and typed card cells, and the
// full test's ticked steps. PORTTOOL-FLOW.md C.3.6 「页面记住什么」「填数超量程」.
//
// Kept as flat cells so every value can be checked on its own: a value that
// fails its check is refused and never written, the last good one stays.

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"PortTool/src/calstore"
	"PortTool/src/ptitems"
	"PortTool/src/ptlib"
	"PortTool/src/ptproto"
)

// PageMemoFile overrides where the memo is kept; tests set it.
var PageMemoFile string

var pageMemoMu sync.Mutex

// pageMemo is porttool_pagememo.json: section ("rd", "full") -> cell -> value.
type pageMemo map[string]map[string]string

var memoSections = map[string]bool{"rd": true, "full": true}

func pageMemoPath() string {
	if PageMemoFile != "" {
		return PageMemoFile
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "porttool_pagememo.json")
	}
	return "porttool_pagememo.json"
}

func loadPageMemo() pageMemo {
	f := pageMemo{}
	if b, err := os.ReadFile(pageMemoPath()); err == nil {
		_ = json.Unmarshal(b, &f)
	}
	// A memory saved before a parameter was retired still loads (ptitems.Retired);
	// a renamed one's value fills its new cells that are not set.
	for _, cells := range f {
		for k, v := range cells {
			if !retiredCell(k) {
				continue
			}
			if p := strings.Split(k, "|"); len(p) == 3 && p[0] == "par" {
				for _, n := range ptitems.RenamedTo(p[1], p[2]) {
					if _, set := cells["par|"+p[1]+"|"+n]; !set {
						cells["par|"+p[1]+"|"+n] = v
					}
				}
			}
			delete(cells, k)
		}
	}
	return f
}

// retiredCell says a cell holds a parameter its item no longer takes, or a
// setting the page no longer has.
func retiredCell(k string) bool {
	p := strings.Split(k, "|")
	// "src": the R&D cards' data source, gone with the reference board in R&D (decision 133).
	// "snap|<port>": a last round saved before it was kept per board; whose board is unknown (STG-39).
	// Any cell of a retired item (decision 141).
	return (len(p) == 3 && p[0] == "par" && ptitems.Retired(p[1], p[2])) || (len(p) == 2 && (p[0] == "src" || p[0] == "snap")) ||
		(len(p) >= 2 && ptitems.RetiredItem(p[1]))
}

// uidCell is a board UID as pt.id reports it: hex, bounded.
func uidCell(u string) bool {
	if u == "" || len(u) > 64 {
		return false
	}
	for _, r := range u {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

func (s *Server) handlePageMemo(w http.ResponseWriter, r *http.Request) {
	pageMemoMu.Lock()
	defer pageMemoMu.Unlock()
	if r.Method != http.MethodPost {
		writeJSON(w, 200, loadPageMemo())
		return
	}
	var body struct {
		Section string            `json:"section"`
		Cells   map[string]string `json:"cells"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !memoSections[body.Section] {
		writeErr(w, 400, m("go.memo.bad_request"))
		return
	}
	s.mu.Lock()
	caps, hasCaps := s.caps, s.board != nil
	s.mu.Unlock()
	// Good cells are kept; a bad one is dropped and named, so one wrong value
	// never takes the rest of the page's memory with it.
	good := map[string]string{}
	var first *msg
	var refused []string
	for k, v := range body.Cells {
		if retiredCell(k) {
			continue // a page opened before the retirement; dropped, not refused
		}
		if bad := memoCellBad(body.Section, k, v, body.Cells, caps, hasCaps); bad != nil {
			if first == nil {
				first = bad
			}
			refused = append(refused, k)
			continue
		}
		good[k] = v
	}
	f := loadPageMemo()
	if old, ok := f[body.Section]; ok {
		for _, k := range refused {
			if v, had := old[k]; had && memoCellBad(body.Section, k, v, old, caps, hasCaps) == nil {
				good[k] = v // the last good value stays
			}
		}
	}
	f[body.Section] = good
	b, err := json.MarshalIndent(f, "", "  ")
	if err == nil {
		err = os.WriteFile(pageMemoPath(), append(b, '\n'), 0o644)
	}
	if err != nil {
		writeErr(w, 500, m("go.memo.write_failed", "detail", err.Error()))
		return
	}
	if first != nil {
		writeJSON(w, 400, map[string]any{"error": *first, "refused": refused})
		return
	}
	writeJSON(w, 200, f)
}

// memoCellBad checks one cell; nil is fine. all is the whole section, for
// cells checked against another (PWM duty and frequency against the bits).
func memoCellBad(section, key, v string, all map[string]string, caps ptproto.Caps, hasCaps bool) *msg {
	parts := strings.Split(key, "|")
	bad := func(lo, hi any) *msg { return ref(m("go.memo.out_of_range", "value", v, "lo", lo, "hi", hi)) }
	if section == "full" {
		if len(parts) == 2 && parts[0] == "step" && (v == "0" || v == "1") {
			return nil
		}
		return ref(m("go.memo.unknown", "cell", key))
	}
	item := func(id string) (ptitems.Item, bool) {
		it, ok := catalogueItem(id)
		return it, ok && it.In(ptitems.RD)
	}
	switch {
	case parts[0] == "off" && len(parts) == 2:
		if _, ok := item(parts[1]); ok && (v == "0" || v == "1") {
			return nil
		}
	case parts[0] == "peer" && len(parts) == 2:
		it, ok := item(parts[1])
		if !ok {
			break
		}
		for _, p := range it.RDPeers() {
			if string(p) == v {
				return nil
			}
		}
		return ref(m("go.stagecfg.bad_peer", "item", m(it.Name), "peer", v))
	case parts[0] == "par" && len(parts) == 3:
		it, ok := item(parts[1])
		if !ok {
			break
		}
		k := parts[2]
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
		return nil
	case parts[0] == "dur" && len(parts) == 2:
		if _, ok := durations[v]; ok {
			return nil
		}
		return ref(m("go.stagecfg.bad_duration", "duration", v))
	case (parts[0] == "pts" || parts[0] == "tgt") && len(parts) == 2:
		it, ok := item(parts[1])
		if !ok || (it.Port != "ain" && it.Port != "aout") {
			break
		}
		lo, fs := channelBounds(ptlib.AnalogChannel(it))
		nums := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' })
		if len(nums) == 0 || len(nums) > 6 || (parts[0] == "tgt" && len(nums) != 1) {
			return ref(m("go.memo.points", "value", v))
		}
		for _, n := range nums {
			x, err := strconv.ParseFloat(n, 64)
			if err != nil || x < lo || x > fs {
				return bad(lo, fs)
			}
		}
		return nil
	case (parts[0] == "met" || parts[0] == "act") && len(parts) >= 2:
		// A meter reading: any real number near the channel's span.
		it, ok := item(parts[1])
		if !ok {
			break
		}
		_, fs := channelBounds(ptlib.AnalogChannel(it))
		if x, err := strconv.ParseFloat(v, 64); err != nil || x < 0 || x > fs*1.2 {
			return bad(0, fs*1.2)
		}
		return nil
	case parts[0] == "lvl" && len(parts) == 2:
		if v == "0" || v == "1" {
			return nil
		}
	case parts[0] == "drop" && len(parts) == 2:
		if x, err := strconv.ParseFloat(v, 64); err != nil || x < 0 || x > 30 {
			return bad(0, 30)
		}
		return nil
	case parts[0] == "quad" && len(parts) == 2:
		if x, err := strconv.Atoi(v); err != nil || x < 0 || x > 1000000 {
			return bad(0, 1000000)
		}
		return nil
	case parts[0] == "blk" && len(parts) == 2: // DO toggles a person counted (decision 137)
		if x, err := strconv.Atoi(v); err != nil || x < 0 || x > 1000 {
			return bad(0, 1000)
		}
		return nil
	case (parts[0] == "pwmok" && len(parts) == 2) || (parts[0] == "chk" && len(parts) == 3 && rdTables[parts[1]]):
		if v == "0" || v == "1" {
			return nil
		}
	case parts[0] == "snap" && len(parts) == 3:
		// A port page's last round per board (decision 137, STG-39): kept as the page wrote it, bounded.
		if uidCell(parts[1]) && knownPort(parts[2]) && len(v) <= 64<<10 && json.Valid([]byte(v)) {
			return nil
		}
	case key == "pwmres":
		if v == "8" || v == "10" || v == "12" || v == "16" {
			return nil
		}
	case parts[0] == "pwm" && len(parts) == 3:
		res, err := strconv.Atoi(all["pwmres"])
		if err != nil {
			res = 8
		}
		x, err := strconv.Atoi(v)
		switch parts[2] {
		case "on":
			if v == "0" || v == "1" {
				return nil
			}
		case "duty":
			hi := 1<<uint(res) - 1
			if err != nil || x < 0 || x > hi {
				return bad(0, hi)
			}
			return nil
		case "freq":
			hi := 256000 >> uint(res)
			if err != nil || x < 1 || x > hi {
				return bad(1, hi)
			}
			return nil
		}
	}
	return ref(m("go.memo.unknown", "cell", fmt.Sprintf("%s=%s", key, v)))
}

// rdTables are the R&D card tables a row tick belongs to (rdvisit.js).
var rdTables = map[string]bool{"ain": true, "aout": true, "level": true, "quad": true, "drop": true, "blink": true}

func knownPort(port string) bool {
	for _, p := range ptitems.Ports {
		if p.Port == port {
			return true
		}
	}
	return false
}

// channelBounds is the widest span any of the channel's ranges allows; the
// page checks the registered range itself, which is narrower.
// loadTargetBad checks an AO load target against every range of its own AO
// (STG-38, C.3.6 「填数超量程」); nil for any other parameter.
func loadTargetBad(k, v string) *msg {
	ch := ptitems.LoadChannel(k)
	if ch == "" {
		return nil
	}
	lo, fs := channelBounds(ch)
	if x, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && (x < lo || x > fs) {
		return ref(m("go.memo.out_of_range", "value", v, "lo", lo, "hi", fs))
	}
	return nil
}

func channelBounds(ch string) (lo, fs float64) {
	lo, fs = math.Inf(1), math.Inf(-1)
	for _, r := range calstore.RangeOptions(ch) {
		l, f := r.Scale()
		lo, fs = math.Min(lo, l), math.Max(fs, f)
	}
	if math.IsInf(lo, 0) {
		return 0, 0
	}
	return lo, fs
}
