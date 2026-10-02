package testcase

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"PortTool/internal/calstore"
	"PortTool/internal/ptplan"
)

// The shipped station-6 plans carry the accuracy CAL-02 settled on, for all
// four channels, so changing the figures is changing a file (decision 30).
func TestShippedStationPlansStateTheAccuracyLimits(t *testing.T) {
	want := map[string]float64{"AI1": 0.1, "AI2": 0.1, "AO1": 0.3, "AO2": 0.3}
	for _, name := range []string{"station6-poweron.json", "station6-poweron-relaxed.json"} {
		plan, err := ptplan.Load(filepath.Join("..", "..", "plans", name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for ch, pct := range want {
			lim, ok := plan.Calibration.Limit(ch)
			if !ok || lim.MaxResidualPctFS != pct {
				t.Errorf("%s: %s limit = %+v, want ±%v %%FS", name, ch, lim, pct)
			}
		}
	}
}

func TestCalibrationBlockRefusesWrongUnitsAndRepeats(t *testing.T) {
	base := `{"schema":1,"name":"t","limit_version":"v","steps":[{"id":"id","type":"PtRaw","command":"pt.id"}],"calibration":%s}`
	for what, block := range map[string]string{
		"AI1 in mA":       `{"channels":[{"channel":"AI1","unit":"mA","full_scale":20,"max_residual_pct_fs":0.1}]}`,
		"a channel twice": `{"channels":[{"channel":"AO1","unit":"mA","full_scale":20,"max_residual_pct_fs":0.3},{"channel":"AO1","unit":"mA","full_scale":20,"max_residual_pct_fs":0.3}]}`,
		"unknown channel": `{"channels":[{"channel":"AO3","unit":"mA","full_scale":20,"max_residual_pct_fs":0.3}]}`,
		"no full scale":   `{"channels":[{"channel":"AO1","unit":"mA","full_scale":0,"max_residual_pct_fs":0.3}]}`,
	} {
		if _, err := ptplan.Parse([]byte(strings.Replace(base, "%s", block, 1))); err == nil {
			t.Errorf("%s: accepted", what)
		}
	}
}

// A walk that names its channel is judged by the plan's limit and archived by
// the board's UID; nothing is sent to the board except asking that UID.
func TestFitIsJudgedAndArchivedByBoardUID(t *testing.T) {
	srv, dir := planPanel(t, plainBoard)
	src, err := os.ReadFile(filepath.Join("..", "..", "plans", "station6-poweron.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "station6-poweron.json"), src, 0o644); err != nil {
		t.Fatal(err)
	}
	calstore.Dir = t.TempDir()
	t.Cleanup(func() { calstore.Dir = "" })

	postJSON(t, srv, "/api/connect", map[string]any{"port": "fake"})

	walk := func(offsets []float64) map[string]any {
		pts := []map[string]any{}
		for i, want := range []float64{0, 5, 10, 15, 20} {
			pts = append(pts, map[string]any{"want": want, "got": want*1.002 + offsets[i]})
		}
		return postJSON(t, srv, "/api/fit", map[string]any{"points": pts, "channel": "AO1", "unit": "mA"})
	}

	good := walk([]float64{0.010, 0.012, 0.008, 0.011, 0.009})
	judge, _ := good["judge"].(map[string]any)
	if judge["pass"] != true {
		t.Fatalf("a residual of a few µA against ±0.3 %%FS must pass: %v (store_error %v)", good["judge"], good["store_error"])
	}
	stored, _ := good["stored"].(map[string]any)
	archive, _ := stored["archive"].(string)
	if !strings.Contains(archive, "003400413135511439303538") {
		t.Fatalf("archive %q is not filed under the board's UID", archive)
	}
	if _, err := os.Stat(archive); err != nil {
		t.Fatal(err)
	}
	if stored["image"] != nil {
		t.Fatal("one channel must not produce the sector-15 image")
	}

	bad := walk([]float64{0, 0.2, 0, -0.2, 0})
	judge, _ = bad["judge"].(map[string]any)
	if judge["pass"] != false {
		t.Fatalf("a 0.2 mA residual is 1 %%FS and must fail: %v", bad["judge"])
	}
	stored, _ = bad["stored"].(map[string]any)
	if stored["kept_archive"] == nil {
		t.Fatal("re-measuring must keep the previous archive")
	}
}
