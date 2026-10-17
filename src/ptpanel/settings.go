package ptpanel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

// The panel's own preferences, beside the executable in porttool_settings.json.
// Kept apart from porttool_ports.json, which says only which COM is which
// (LANG-02). A missing or unreadable file means Chinese.

// SettingsFile overrides where the preferences are kept; tests set it.
var SettingsFile string

var settingsMu sync.Mutex

type panelSettings struct {
	Lang string `json:"lang"`
}

var langs = map[string]string{"zh": "zh-CN", "en": "en", "de": "de"}

func settingsPath() string {
	if SettingsFile != "" {
		return SettingsFile
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "porttool_settings.json")
	}
	return "porttool_settings.json"
}

func loadSettings() panelSettings {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	var st panelSettings
	b, err := os.ReadFile(settingsPath())
	if err != nil || json.Unmarshal(b, &st) != nil {
		st = panelSettings{}
	}
	if _, ok := langs[st.Lang]; !ok {
		st.Lang = "zh"
	}
	return st
}

func saveSettings(st panelSettings) error {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath(), append(b, '\n'), 0o644)
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var body panelSettings
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, m("go.settings.bad_request"))
			return
		}
		if _, ok := langs[body.Lang]; !ok {
			writeErr(w, 400, m("go.settings.bad_lang", "lang", body.Lang))
			return
		}
		if err := saveSettings(body); err != nil {
			// 200: the choice still applies to this reload, it just will not
			// be remembered next time; the page says so in words.
			writeJSON(w, 200, map[string]any{"error": m("go.settings.not_saved", "detail", err)})
			return
		}
	}
	writeJSON(w, 200, loadSettings())
}

// handleIndex serves the page with the dictionary and the chosen language
// written into it, so every table on the page is worded before it is read.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	page, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	dictionary()
	st := loadSettings()
	words, _ := json.Marshal(dict)
	lang, _ := json.Marshal(st.Lang)
	de, _ := json.Marshal(deOK)
	page = bytes.Replace(page, []byte(`<html lang="zh-CN">`), []byte(`<html lang="`+langs[st.Lang]+`">`), 1)
	page = bytes.Replace(page, []byte("/*STRINGS*/{}"), words, 1)
	page = bytes.Replace(page, []byte(`/*LANG*/"zh"`), lang, 1)
	page = bytes.Replace(page, []byte("/*DE_REVIEWED*/false"), de, 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(page)
}
