package testcase

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"PortTool/src/calarea"
	"PortTool/src/calstore"
	"PortTool/src/portmap"
	"PortTool/src/ptpanel"
	"PortTool/src/ptplan"
)

// The shipped station-6 plans list all four calibrated channels with their
// full scale, and nothing else: calibration has no residual limit (decision 99).
func TestShippedStationPlansStateTheCalibratedChannels(t *testing.T) {
	want := map[string]float64{"AI1": 10000, "AI2": 20, "AO1": 20, "AO2": 20}
	for _, name := range []string{"station6-poweron.json", "station6-poweron-relaxed.json"} {
		plan, err := ptplan.Load(filepath.Join("..", "..", "..", "src", "plans", name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for ch, fs := range want {
			c, ok := plan.Calibration.Channel(ch)
			if !ok || c.FullScale != fs {
				t.Errorf("%s: %s = %+v, want full scale %v", name, ch, c, fs)
			}
		}
	}
}

func TestCalibrationBlockRefusesWrongUnitsAndRepeats(t *testing.T) {
	base := `{"schema":1,"name":"t","limit_version":"v","steps":[{"id":"id","type":"PtRaw","command":"pt.id"}],"calibration":%s}`
	for what, block := range map[string]string{
		"a unit no range has": `{"channels":[{"channel":"AI1","unit":"V","full_scale":10}]}`,
		"a channel twice":     `{"channels":[{"channel":"AO1","unit":"mA","full_scale":20},{"channel":"AO1","unit":"mA","full_scale":20}]}`,
		"unknown channel":     `{"channels":[{"channel":"AO3","unit":"mA","full_scale":20}]}`,
		"no full scale":       `{"channels":[{"channel":"AO1","unit":"mA","full_scale":0}]}`,
	} {
		if _, err := ptplan.Parse([]byte(strings.Replace(base, "%s", block, 1))); err == nil {
			t.Errorf("%s: accepted", what)
		}
	}
	// The unit follows the registered range, so AI1 on a current range and AO1
	// on a voltage range are both fine (decision 134).
	ok := `{"channels":[{"channel":"AI1","unit":"mA","full_scale":20},{"channel":"AO1","unit":"mV","full_scale":10000}]}`
	if _, err := ptplan.Parse([]byte(strings.Replace(base, "%s", ok, 1))); err != nil {
		t.Errorf("AI1 in mA, AO1 in mV: %v", err)
	}
}

// ---- AI / AO cards: fit, 存到本地, range registration, pt.cal (C.3.2) ----

const boardUID = "003400413135511439303538"

// calArea is the 56-byte record a board with this UID reports over pt.cal.
func calArea(t *testing.T, uid string, ch [calarea.Channels]calarea.Channel) []byte {
	t.Helper()
	w, err := calstore.ParseUID(uid)
	if err != nil {
		t.Fatal(err)
	}
	return calarea.Encode(w, ch)
}

func blankArea() []byte { return []byte(strings.Repeat("\xff", calarea.Size)) }

// calBoard is plainBoard answering pt.cal with area.
func calBoard(area []byte) func(cmd string, nth int) ([]string, []string) {
	return func(cmd string, nth int) ([]string, []string) {
		if cmd == "pt.cal" {
			return []string{"OK cal=" + strings.ToUpper(hex.EncodeToString(area))}, nil
		}
		return plainBoard(cmd, nth)
	}
}

// calPanel is planPanel that also hands back the server, for the Go function
// the full-run gate calls, and is connected already.
func calPanel(t *testing.T, area []byte) (*httptest.Server, *ptpanel.Server) {
	t.Helper()
	srv, p, _ := calPanelOn(t, calBoard(area))
	return srv, p
}

// calPanelOn is calPanel on a board answering handler, handed back too.
func calPanelOn(t *testing.T, handler func(cmd string, nth int) ([]string, []string)) (*httptest.Server, *ptpanel.Server, *scriptBoard) {
	t.Helper()
	was := ptpanel.PlanDir
	ptpanel.PlanDir = t.TempDir()
	t.Cleanup(func() { ptpanel.PlanDir = was })
	portmap.File = filepath.Join(t.TempDir(), "porttool_ports.json")
	t.Cleanup(func() { portmap.File = "" })
	calstore.Dir = t.TempDir()
	t.Cleanup(func() { calstore.Dir = "" })

	fake := newScriptBoard(t, handler)
	p := ptpanel.New()
	p.Open = func(name string, baud int) (io.ReadWriteCloser, error) { return fake, nil }
	srv := httptest.NewServer(p.Handler())
	t.Cleanup(func() { srv.Close(); p.Close() })
	postJSON(t, srv, "/api/connect", map[string]any{"port": "fake"})
	return srv, p, fake
}

// burnBoard is plainBoard keeping a calibration area the way the fixture
// firmware does: pt.calw writes it, pt.cal reports it (decision 142). refuse,
// when set, is pt.calw's answer instead and nothing is written.
func burnBoard(refuse string) func(cmd string, nth int) ([]string, []string) {
	var mu sync.Mutex
	area := blankArea()
	return func(cmd string, nth int) ([]string, []string) {
		mu.Lock()
		defer mu.Unlock()
		if rec, ok := strings.CutPrefix(cmd, "pt.calw "); ok {
			if refuse != "" {
				return []string{refuse}, nil
			}
			b, err := hex.DecodeString(rec)
			if err != nil || len(b) != calarea.Size {
				return []string{"ERR calw refused: want 112 hex digits"}, nil
			}
			area = b
			return []string{"OK calw words=2 ms=1100"}, nil
		}
		if cmd == "pt.cal" {
			return []string{"OK cal=" + strings.ToUpper(hex.EncodeToString(area))}, nil
		}
		return plainBoard(cmd, nth)
	}
}

func sentCalw(b *scriptBoard) []string {
	var out []string
	for _, c := range b.commands() {
		if strings.HasPrefix(c, "pt.calw") {
			out = append(out, c)
		}
	}
	return out
}

func writeImage(t *testing.T, uid string, img []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(calstore.ImagePath(uid)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(calstore.ImagePath(uid), img, 0o644); err != nil {
		t.Fatal(err)
	}
}

// 烧进板子 sends the board's own archived image as one pt.calw line and only
// reports success once pt.cal gives the same k, b back. PORTTOOL-FLOW.md C.3.2.
func TestBurnSendsTheBoardsOwnImageOverTheControlPort(t *testing.T) {
	srv, _, board := calPanelOn(t, burnBoard(""))

	if r := postJSON(t, srv, "/api/cal/burn", map[string]any{}); r["error"] == nil {
		t.Fatal("burned without an image")
	}
	if c := sentCalw(board); len(c) != 0 {
		t.Fatalf("sent pt.calw without an image: %v", c)
	}

	w, err := calstore.ParseUID(boardUID)
	if err != nil {
		t.Fatal(err)
	}
	ch := [calarea.Channels]calarea.Channel{{Gain: 1.01, Offset: -3}, {Gain: 0.98, Offset: 0.02}, {Gain: 1, Offset: 0.1}, {Gain: 1.02, Offset: -0.05}}
	img := calarea.Image(w, ch)
	writeImage(t, boardUID, img)

	r := postJSON(t, srv, "/api/cal/burn", map[string]any{})
	if r["ok"] != true {
		t.Fatalf("burn: %v", r)
	}
	want := "pt.calw " + strings.ToUpper(hex.EncodeToString(img[:calarea.Size]))
	if c := sentCalw(board); len(c) != 1 || c[0] != want {
		t.Fatalf("pt.calw sent: %v, want one %q", c, want)
	}
	cal, _ := r["board"].(map[string]any)
	ai1, _ := cal["ch"].(map[string]any)["AI1"].(map[string]any)
	if cal["state"] != "ok" || float32(ai1["k"].(float64)) != ch[0].Gain {
		t.Fatalf("read back: %v", r["board"])
	}
	if done, _ := json.Marshal(r["done"]); !strings.Contains(string(done), "AI1 k=1.01") {
		t.Fatalf("the result does not show k, b: %s", done)
	}

	postJSON(t, srv, "/api/connect", map[string]any{"port": "sim"})
	if r := postJSON(t, srv, "/api/cal/burn", map[string]any{}); r["error"] == nil {
		t.Fatal("burned for the simulated board")
	}
}

// The board's own refusal comes through in its words, and an image that is not
// the calibration-area format never reaches the board.
func TestBurnReportsTheBoardsRefusalAndStopsABadImage(t *testing.T) {
	srv, _, board := calPanelOn(t, burnBoard("ERR calw refused: uid is not this board's"))
	w, _ := calstore.ParseUID(boardUID)
	img := calarea.Image(w, [calarea.Channels]calarea.Channel{{Gain: 1}, {Gain: 1}, {Gain: 1}, {Gain: 1}})
	writeImage(t, boardUID, img)

	r := postJSON(t, srv, "/api/cal/burn", map[string]any{})
	if why, _ := json.Marshal(r["error"]); r["error"] == nil || !strings.Contains(string(why), "uid is not this board's") {
		t.Fatalf("refusal: %v", r)
	}

	bad := append([]byte(nil), img...)
	bad[calarea.AreaSize-1] = 0
	writeImage(t, boardUID, bad)
	before := len(sentCalw(board))
	if r := postJSON(t, srv, "/api/cal/burn", map[string]any{}); r["error"] == nil {
		t.Fatal("burned an image with data after its record")
	}
	if len(sentCalw(board)) != before {
		t.Fatal("a bad image reached the board")
	}
}

// aoWalk is what an AO walk collects: the board's nominal and the meter.
func aoWalk(wants, gots []float64) []map[string]any {
	var pts []map[string]any
	for i := range wants {
		pts = append(pts, map[string]any{"set": wants[i], "want": wants[i], "got": gots[i]})
	}
	return pts
}

// aiWalk is what an AI walk collects: the board's raw reading and the source.
func aiWalk(raws, gots []float64) []map[string]any {
	var pts []map[string]any
	for i := range raws {
		pts = append(pts, map[string]any{"set": gots[i], "raw": raws[i], "got": gots[i]})
	}
	return pts
}

func archiveOf(t *testing.T) calstore.Archive {
	t.Helper()
	var arc calstore.Archive
	data, err := os.ReadFile(filepath.Join(calstore.Dir, boardUID, "calibration.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &arc); err != nil {
		t.Fatal(err)
	}
	return arc
}

// A walk whose reading does not follow the source - an AI with its jumpers
// not soldered reads a floating pin - fits a gain far from 1: the fit says so
// and 存到本地 refuses it, while a normal walk is stored (decision 106).
func TestStoreRefusesAGainFarFromOne(t *testing.T) {
	srv, _ := calPanel(t, blankArea())
	floating := aiWalk([]float64{10150, 10250, 10200}, []float64{0, 4000, 8000})
	fit := postJSON(t, srv, "/api/analog/fit", map[string]any{"channel": "AI1", "points": floating})
	if fit["known"] != true || fit["refuse"] == nil {
		t.Fatalf("a floating AI1 must fit and say it will not be stored: %v", fit)
	}
	if r := postJSON(t, srv, "/api/cal/store", map[string]any{"channel": "AI1", "points": floating}); r["error"] == nil || r["stored"] != nil {
		t.Fatalf("a gain far from 1 must not be stored: %v", r)
	}
	if _, err := os.Stat(filepath.Join(calstore.Dir, boardUID, "calibration.json")); err == nil {
		t.Fatal("the refused fit was archived")
	}
	good := aiWalk([]float64{0, 26000, 52000}, []float64{0, 4000, 8000})
	if r := postJSON(t, srv, "/api/cal/store", map[string]any{"channel": "AI1", "points": good}); r["error"] != nil || r["stored"] == nil {
		t.Fatalf("a normal walk must be stored: %v", r)
	}
}

// Fitting stores nothing; 存到本地 stores whatever the residual (decision 99)
// and, with all four channels in, writes the sector-15 image.
func TestStoreIsSeparateFromFitAndIgnoresTheResidual(t *testing.T) {
	srv, _ := calPanel(t, blankArea())
	mA := []float64{0, 10, 20}

	// AO2 is current by default (decision 134), so its walk is in mA.
	fit := postJSON(t, srv, "/api/analog/fit", map[string]any{"channel": "AO2", "points": aoWalk(mA, []float64{0.01, 10.02, 20.0})})
	if fit["known"] != true || fit["unit"] != "mA" {
		t.Fatalf("fit: %v", fit)
	}
	if _, err := os.Stat(filepath.Join(calstore.Dir, boardUID)); err == nil {
		t.Fatal("fitting must not write anything")
	}

	// 0.6 mA off a 20 mA full scale is 3 % FS: still stored.
	bad := postJSON(t, srv, "/api/cal/store", map[string]any{"channel": "AO2",
		"points": aoWalk([]float64{0, 5, 10, 15, 20}, []float64{0, 5.6, 10, 14.4, 20})})
	stored, _ := bad["stored"].(map[string]any)
	if stored == nil || stored["image"] != nil {
		t.Fatalf("a rough fit must be stored, without an image yet: %v", bad)
	}
	if pct := archiveOf(t).Channels["AO2"].ResidualPctFS; pct == nil || *pct < 2 {
		t.Fatalf("the residual is archived against full scale: %v", pct)
	}

	postJSON(t, srv, "/api/cal/store", map[string]any{"channel": "AO1", "points": aoWalk(mA, mA)})
	postJSON(t, srv, "/api/cal/store", map[string]any{"channel": "AI2", "points": aiWalk([]float64{0, 26000, 52000}, []float64{0, 8, 16})})
	last := postJSON(t, srv, "/api/cal/store", map[string]any{"channel": "AI1", "points": aiWalk([]float64{0, 26000, 52000}, []float64{0, 4000, 8000})})
	stored, _ = last["stored"].(map[string]any)
	if stored == nil || stored["image"] == nil {
		t.Fatalf("all four stored must produce the image: %v", last)
	}
	state, _ := last["state"].(map[string]any)
	if why, _ := json.Marshal(state["burn_why"]); strings.Contains(string(why), "burn_missing") {
		t.Fatalf("with all four stored the burn reason is no longer a missing channel: %s", why)
	}
}

// Without a usable fit there is nothing to store.
func TestStoreRefusesWithoutAFit(t *testing.T) {
	srv, _ := calPanel(t, blankArea())
	for what, c := range map[string]struct {
		ch  string
		pts []map[string]any
	}{
		"one point":      {"AO1", aoWalk([]float64{10}, []float64{10})},
		"all skipped":    {"AO1", []map[string]any{{"set": 0, "want": 0, "got": nil}, {"set": 10, "want": 10, "got": nil}}},
		"no spread":      {"AO1", aoWalk([]float64{5, 5, 5}, []float64{5, 5.1, 4.9})},
		"no AI raw read": {"AI2", []map[string]any{{"set": 0, "got": 0}, {"set": 10, "got": 10}}},
	} {
		res := postJSON(t, srv, "/api/cal/store", map[string]any{"channel": c.ch, "points": c.pts})
		if res["error"] == nil || res["stored"] != nil {
			t.Errorf("%s: stored anyway: %v", what, res)
		}
	}
	if _, err := os.Stat(filepath.Join(calstore.Dir, boardUID, "calibration.json")); err == nil {
		t.Fatal("nothing may be archived without a fit")
	}
}

// Registering AI1 as current switches its unit and full scale, and AI1 is
// then stored in mA (decision 102 / STG-24); the registration is per UID.
func TestRangeRegistrationSwitchesUnits(t *testing.T) {
	srv, _ := calPanel(t, blankArea())
	chOf := func(state map[string]any, name string) map[string]any {
		chans, _ := state["channels"].(map[string]any)
		c, _ := chans[name].(map[string]any)
		return c
	}
	st := getJSON(t, srv, "/api/analog")
	if st["registered"] != false || chOf(st, "AI1")["unit"] != "mV" || chOf(st, "AI2")["unit"] != "mA" || chOf(st, "AO1")["range"] != "v" || chOf(st, "AO2")["range"] != "i20" {
		t.Fatalf("an unregistered board must show the default ranges (decision 134): %v", st["channels"])
	}
	st = postJSON(t, srv, "/api/analog/ranges", map[string]any{"ranges": map[string]string{"AI1": "i", "AO2": "i420"}})
	ai1, ao2 := chOf(st, "AI1"), chOf(st, "AO2")
	if st["registered"] != true || ai1["unit"] != "mA" || ai1["fs"] != 20.0 || ao2["lo"] != 4.0 {
		t.Fatalf("registration did not switch units: %v", st["channels"])
	}
	if _, err := os.Stat(filepath.Join(calstore.Dir, boardUID, "ranges.json")); err != nil {
		t.Fatal("the registration is kept beside the board's calibration:", err)
	}
	// AO may be voltage on other products: mV against 10 V (decision 134); AI has no 4-20 range.
	if r := postJSON(t, srv, "/api/analog/ranges", map[string]any{"ranges": map[string]string{"AO1": "v"}}); r["error"] != nil || chOf(r, "AO1")["unit"] != "mV" || chOf(r, "AO1")["fs"] != 10000.0 {
		t.Fatalf("AO1 as a voltage range: %v", r)
	}
	if r := postJSON(t, srv, "/api/analog/ranges", map[string]any{"ranges": map[string]string{"AI1": "i420"}}); r["error"] == nil {
		t.Fatalf("AI cannot be 4-20 mA: %v", r)
	}
	postJSON(t, srv, "/api/analog/ranges", map[string]any{"ranges": map[string]string{"AO1": "i20"}})

	walk := aiWalk([]float64{0, 26000, 52000}, []float64{0, 8, 16})
	fit := postJSON(t, srv, "/api/analog/fit", map[string]any{"channel": "AI1", "points": walk})
	if fit["unit"] != "mA" || fit["full_scale"] != 20.0 {
		t.Fatalf("AI1 in current range must fit in mA against 20 mA: %v", fit)
	}
	if res := postJSON(t, srv, "/api/cal/store", map[string]any{"channel": "AI1", "points": walk}); res["error"] != nil {
		t.Fatalf("AI1 in mA must be accepted once registered as current: %v", res["error"])
	}
	c := archiveOf(t).Channels["AI1"]
	if c.Unit != "mA" || c.Range != calstore.RangeCurrent {
		t.Fatalf("archived AI1 = %+v", c)
	}
	// The nominal followed the range: raw 26000 over the 124 R shunt is ~8 mA.
	if c.Gain < 0.95 || c.Gain > 1.05 {
		t.Fatalf("AI1 current nominal was not computed through the shunt: gain %v", c.Gain)
	}

	again, _, err := calstore.LoadRanges(boardUID)
	if err != nil || again["AI1"] != calstore.RangeCurrent || again["AI2"] != calstore.RangeCurrent {
		t.Fatalf("registration not persisted: %v %v", again, err)
	}
}
