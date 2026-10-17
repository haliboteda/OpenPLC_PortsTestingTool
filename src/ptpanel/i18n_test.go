package ptpanel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// What goes into files is the dictionary's English, nested reasons included.
func TestMsgEnglishResolvesNestedMessages(t *testing.T) {
	got := m("go.hold.finished", "why", m("go.reason.time_up")).English()
	want := "[continuous] Ended: time is up. All ports stopped, outputs released."
	if got != want {
		t.Fatalf("English() = %q, want %q", got, want)
	}
}

// The page reads {"$t": key, "args": ...}; no message is "".
func TestMsgJSONShape(t *testing.T) {
	b, _ := json.Marshal(map[string]any{"a": m("go.cmd.empty"), "b": msg{}})
	if string(b) != `{"a":{"$t":"go.cmd.empty","args":null},"b":""}` {
		t.Fatalf("JSON = %s", b)
	}
}

// A missing or unreadable settings file, or an unknown language, means Chinese.
func TestSettingsFallBackToChinese(t *testing.T) {
	dir := t.TempDir()
	SettingsFile = filepath.Join(dir, "porttool_settings.json")
	t.Cleanup(func() { SettingsFile = "" })

	if got := loadSettings().Lang; got != "zh" {
		t.Fatalf("no file: lang = %q, want zh", got)
	}
	_ = os.WriteFile(SettingsFile, []byte(`{"lang":"fr"}`), 0o644)
	if got := loadSettings().Lang; got != "zh" {
		t.Fatalf("unknown language: lang = %q, want zh", got)
	}
	if err := saveSettings(panelSettings{Lang: "en"}); err != nil {
		t.Fatal(err)
	}
	if got := loadSettings().Lang; got != "en" {
		t.Fatalf("after saving en: lang = %q", got)
	}
}
