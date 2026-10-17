// Package ptrecord keeps what a full fixture test leaves per board: one report
// per run under records/<UID>/, and progress.json saying which steps that board
// has passed, so the next run can continue from the step that did not.
// Layout and rules: $PROD/docs/modules/M4/PORTTOOL-FLOW.md, C.3.3.
package ptrecord

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"PortTool/src/calstore"
	"PortTool/src/ptreport"
)

// Dir overrides where records live. Tests set it; a bench never does.
var Dir string

const (
	progressName = "progress.json"
	stampLayout  = "20060102-150405"
)

func root() string {
	if Dir != "" {
		return Dir
	}
	// Beside the executable, like calibration/ and runlogs/.
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "records")
	}
	return "records"
}

// BoardDir is where one board's records and progress live.
func BoardDir(uid string) string { return filepath.Join(root(), uid) }

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// FileName is <start>[-<SN>].json. The SN is kept verbatim in the report; only
// the file name loses characters a file system may refuse.
func FileName(started time.Time, sn string) string {
	name := started.Format(stampLayout)
	if sn != "" {
		name += "-" + unsafeName.ReplaceAllString(sn, "_")
	}
	return name + ".json"
}

// Write files a finished run under its board's UID and returns the path.
// It never overwrites: a record is evidence.
func Write(r ptreport.Report) (string, error) {
	if _, err := calstore.ParseUID(r.BoardUID); err != nil {
		return "", err
	}
	dir := BoardDir(r.BoardUID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, FileName(r.StartedAt, r.SN))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	if err := r.WriteJSON(f); err != nil {
		f.Close()
		return "", err
	}
	return path, f.Close()
}

// Passed is one step a board has passed, and the report that shows it.
type Passed struct {
	Step   int    `json:"step"`
	Report string `json:"report"`
}

// Progress is progress.json.
type Progress struct {
	UID    string   `json:"uid"`
	Passed []Passed `json:"passed"`
}

// Has reports whether the board has passed step, and in which report.
func (p Progress) Has(step int) (string, bool) {
	for _, x := range p.Passed {
		if x.Step == step {
			return x.Report, true
		}
	}
	return "", false
}

// LoadProgress reads a board's progress; none on disk is an empty one.
func LoadProgress(uid string) (Progress, error) {
	p := Progress{UID: uid}
	data, err := os.ReadFile(filepath.Join(BoardDir(uid), progressName))
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, fmt.Errorf("reading %s: %w", progressName, err)
	}
	return p, nil
}

// ClearProgress forgets which steps a board has passed.
func ClearProgress(uid string) error {
	err := os.Remove(filepath.Join(BoardDir(uid), progressName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Update merges the steps this run passed into the board's progress. When every
// picked step has now passed, the progress is removed: the next run starts over.
func Update(uid string, picked []int, r ptreport.Report, reportFile string) error {
	p, err := LoadProgress(uid)
	if err != nil {
		return err
	}
	for _, s := range r.Steps {
		if !s.Outcome.IsPass() {
			continue
		}
		var step int
		if _, err := fmt.Sscan(s.Type, &step); err != nil {
			continue
		}
		kept := p.Passed[:0]
		for _, x := range p.Passed {
			if x.Step != step {
				kept = append(kept, x)
			}
		}
		p.Passed = append(kept, Passed{Step: step, Report: reportFile})
	}
	all := true
	for _, s := range picked {
		if _, ok := p.Has(s); !ok {
			all = false
		}
	}
	if all || len(p.Passed) == 0 {
		return ClearProgress(uid)
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(BoardDir(uid), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(BoardDir(uid), progressName), append(data, '\n'), 0o644)
}

// ResumeAt is the index into picked of the first step the board has not
// passed. Zero means there is nothing to continue from and no question to ask.
func ResumeAt(picked []int, p Progress) int {
	for i, s := range picked {
		if _, ok := p.Has(s); !ok {
			return i
		}
	}
	return 0
}
