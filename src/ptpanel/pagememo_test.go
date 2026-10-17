package ptpanel

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func memoServer(t *testing.T) {
	PageMemoFile = filepath.Join(t.TempDir(), "porttool_pagememo.json")
	t.Cleanup(func() { PageMemoFile = "" })
}

func postMemo(t *testing.T, section string, cells map[string]string) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"section": section, "cells": cells})
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/memo", bytes.NewReader(b)))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func getMemo(t *testing.T) map[string]map[string]string {
	t.Helper()
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/memo", nil))
	var out map[string]map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// R&D's choices come back from a fresh server, each test type in its own section.
func TestPageMemoSurvivesARestartPerTestType(t *testing.T) {
	memoServer(t)
	rd := map[string]string{"dur|rd": "2", "peer|dout.load": "human", "pts|ain.cal1": "0,2500,5000,7500,10000",
		"tgt|aout.out1": "15", "drop|3": "0.4", "quad|1": "400", "pwmres": "8", "pwm|1|duty": "128", "pwm|1|freq": "1000"}
	if code, out := postMemo(t, "rd", rd); code != 200 {
		t.Fatalf("good cells refused: %d %v", code, out)
	}
	if code, out := postMemo(t, "full", map[string]string{"step|2": "1"}); code != 200 {
		t.Fatalf("full refused: %d %v", code, out)
	}
	got := getMemo(t)
	for k, v := range rd {
		if got["rd"][k] != v {
			t.Errorf("rd %s = %q, want %q", k, got["rd"][k], v)
		}
	}
	if got["full"]["step|2"] != "1" || got["rd"]["step|2"] != "" {
		t.Errorf("sections mixed: %v", got)
	}
	// R&D has no reference board (decision 133).
	if code, _ := postMemo(t, "rd", map[string]string{"peer|dout.load": "ref"}); code != 400 {
		t.Errorf("an R&D peer of the reference board was kept: %d", code)
	}
}

// A value out of range is refused and never written; the last good one stays
// (C.3.6 「填数超量程」, user 2026-10-13: 「非法数值提示，并无法记录」).
func TestPageMemoRefusesAnOutOfRangeValueAndKeepsTheLastGoodOne(t *testing.T) {
	memoServer(t)
	if code, _ := postMemo(t, "rd", map[string]string{"pts|ain.cal1": "0,2500,5000", "dur|rd": "1"}); code != 200 {
		t.Fatal("good points refused")
	}
	for cell, v := range map[string]string{
		"pts|ain.cal1": "0,2500,200000", // the user's screenshot: far above any AI range
		"tgt|aout.out1": "-3",
		"pwm|1|duty":    "300", // 8 bits: 0..255
		"dur|rd":        "9",
		"drop|1":        "abc",
		"nonsense":      "1",
	} {
		// The page always sends its whole state: the good cells ride along.
		cells := map[string]string{"pts|ain.cal1": "0,2500,5000", "pwmres": "8"}
		cells[cell] = v
		code, out := postMemo(t, "rd", cells)
		if code != 400 || out["error"] == nil {
			t.Errorf("%s=%s accepted: %d %v", cell, v, code, out)
		}
	}
	got := getMemo(t)["rd"]
	if got["pts|ain.cal1"] != "0,2500,5000" {
		t.Errorf("the last good points were lost or overwritten: %q", got["pts|ain.cal1"])
	}
	if got["tgt|aout.out1"] != "" || got["pwm|1|duty"] != "" || got["nonsense"] != "" {
		t.Errorf("a refused value was written: %v", got)
	}
}

// The configurations refuse a test point outside every range of the channel too.
func TestStageCfgRefusesAPointOutsideTheChannel(t *testing.T) {
	p := filepath.Join(t.TempDir(), "porttool_stagecfg.json")
	StageCfgFile = p
	t.Cleanup(func() { StageCfgFile = "" })
	cfg := map[string]any{"duration": "once", "items": map[string]any{"ain.read1": map[string]any{"points": []float64{1000, 5000, 200000}}}}
	out := postCfg(t, New().Handler(), map[string]any{"stage": "func", "cfg": cfg})
	if out["error"] == nil {
		t.Fatalf("200000 mV accepted: %v", out)
	}
}

// Files saved while 按时长 still had a ratio load without it, and a page opened
// before then is not refused for sending it (decision 123).
func TestRetiredRelayRatioIsDroppedNotRefused(t *testing.T) {
	memoServer(t)
	old := `{"rd":{"par|relay.cycle|ratio":"30","par|relay.cycle|count":"1:5"}}`
	if err := os.WriteFile(PageMemoFile, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	got := getMemo(t)
	if _, kept := got["rd"]["par|relay.cycle|ratio"]; kept || got["rd"]["par|relay.cycle|count"] != "1:5" {
		t.Errorf("memo after load: %v", got["rd"])
	}
	if code, out := postMemo(t, "rd", map[string]string{"par|relay.cycle|ratio": "30", "par|relay.cycle|count": "1:6"}); code != 200 {
		t.Errorf("a cell with the old ratio was refused: %d %v", code, out)
	}

	cfg := useTempStageCfg(t)
	saved := `{"user":{"duration":"once","items":{"relay.cycle":{"params":{"ch":"1","count":"1:5","ratio":"1:30"}}}}}`
	if err := os.WriteFile(cfg, []byte(saved), 0o644); err != nil {
		t.Fatal(err)
	}
	p := loadStageCfgs().User.Items["relay.cycle"].Params
	if _, kept := p["ratio"]; kept || p["count"] != "1:5" {
		t.Errorf("configuration after load: %v", p)
	}
	s := New()
	c := stageCfg{Duration: "once", Items: map[string]itemCfg{"relay.cycle": {Params: map[string]string{"ch": "1", "ratio": "1:30"}}}}
	if out := postCfg(t, s.Handler(), map[string]any{"stage": "user", "cfg": c}); out["error"] != nil {
		t.Errorf("a configuration with the old ratio was refused: %v", out["error"])
	}
}

// The AO load's shared "ma" from before STG-38 fills both channels' targets on
// load; each target is checked against its own AO's ranges.
func TestAOLoadSharedMAFillsBothTargets(t *testing.T) {
	memoServer(t)
	old := `{"rd":{"par|aout.load|ma":"20","par|aout.load|out2":"5"}}`
	if err := os.WriteFile(PageMemoFile, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	got := getMemo(t)["rd"]
	if _, kept := got["par|aout.load|ma"]; kept || got["par|aout.load|out1"] != "20" || got["par|aout.load|out2"] != "5" {
		t.Errorf("memo after load: %v", got)
	}
	if code, out := postMemo(t, "rd", map[string]string{"par|aout.load|out1": "20", "par|aout.load|out2": "20000"}); code != 400 || out["error"] == nil {
		t.Errorf("20000 on AO2 accepted: %d %v", code, out)
	}

	cfg := useTempStageCfg(t)
	saved := `{"aging":{"duration":"4","items":{"aout.load":{"params":{"ma":"20"}}}}}`
	if err := os.WriteFile(cfg, []byte(saved), 0o644); err != nil {
		t.Fatal(err)
	}
	p := loadStageCfgs().Aging.Items["aout.load"].Params
	if _, kept := p["ma"]; kept || p["out1"] != "20" || p["out2"] != "20" {
		t.Errorf("configuration after load: %v", p)
	}
}

// A memory or configuration saved while the window CRC was an item loads
// without it, and a page opened before then is not refused for it (decision 141).
func TestRetiredSDRAMCRCIsDroppedNotRefused(t *testing.T) {
	memoServer(t)
	old := `{"rd":{"off|sdram.crc":"1","off|sdram.hold":"1"}}`
	if err := os.WriteFile(PageMemoFile, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	got := getMemo(t)["rd"]
	if _, kept := got["off|sdram.crc"]; kept || got["off|sdram.hold"] != "1" {
		t.Errorf("memo after load: %v", got)
	}
	if code, out := postMemo(t, "rd", map[string]string{"off|sdram.crc": "0", "off|sdram.hold": "0"}); code != 200 || out["error"] != nil {
		t.Errorf("a cell of the retired item was refused: %d %v", code, out)
	}

	cfg := useTempStageCfg(t)
	saved := `{"func":{"duration":"once","items":{"sdram.crc":{"on":true},"sdram.hold":{"on":true}}}}`
	if err := os.WriteFile(cfg, []byte(saved), 0o644); err != nil {
		t.Fatal(err)
	}
	items := loadStageCfgs().Func.Items
	if _, kept := items["sdram.crc"]; kept || items["sdram.hold"].On == nil {
		t.Errorf("configuration after load: %v", items)
	}
	s := New()
	on := true
	c := stageCfg{Duration: "once", Items: map[string]itemCfg{"sdram.crc": {On: &on}}}
	if out := postCfg(t, s.Handler(), map[string]any{"stage": "func", "cfg": c}); out["error"] != nil {
		t.Errorf("a configuration naming the retired item was refused: %v", out["error"])
	}
}

// A port page's last round is kept per board UID; an old snap|<port> is dropped,
// not refused (STG-39).
func TestPageMemoKeepsEachBoardsSnapshotApart(t *testing.T) {
	memoServer(t)
	a, b := "003400413135511439303538", "0011223344556677AABBCCDD"
	cells := map[string]string{
		"snap|" + a + "|din": `{"at":"2026-10-16T08:00:00Z","res":[],"log":["a"]}`,
		"snap|" + b + "|din": `{"at":"2026-10-16T09:00:00Z","res":[],"log":["b"]}`,
		"snap|din":           `{"at":"2026-10-15T08:00:00Z","res":[],"log":["old"]}`,
	}
	if code, out := postMemo(t, "rd", cells); code != 200 {
		t.Fatalf("snapshots refused: %d %v", code, out)
	}
	got := getMemo(t)["rd"]
	if got["snap|"+a+"|din"] != cells["snap|"+a+"|din"] || got["snap|"+b+"|din"] != cells["snap|"+b+"|din"] {
		t.Errorf("one board's snapshot took the other's place: %v", got)
	}
	if _, kept := got["snap|din"]; kept {
		t.Error("a snapshot without a board UID was kept")
	}
	if code, _ := postMemo(t, "rd", map[string]string{"snap|not-a-uid|din": `{}`}); code != 400 {
		t.Errorf("a snapshot under a malformed UID was kept: %d", code)
	}
}
