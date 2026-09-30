// Package ptreport is what one run of a plan leaves behind.
//
// Three rules shape it, all of them from the production test guide:
//
//   - Every attempt is kept, including the ones a retry replaced. Failure data
//     must not be overwritten.
//   - Raw values are kept, not just verdicts. Limits will change, and a run
//     has to stay re-judgeable against the new ones.
//   - A timeout is a different outcome from a failed limit. A timeout is
//     usually wiring or a worn fixture probe - a false failure - and a failed
//     limit is usually the board. Recorded as one, failure analysis is over.
package ptreport

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"PortTool/internal/ptcheck"
)

// Outcome is what became of one step.
type Outcome string

const (
	OutcomePass Outcome = "PASS"
	// OutcomeFail is a limit that was read and not met.
	OutcomeFail Outcome = "FAIL"
	// OutcomeTimeout is the step not finishing in time - kept apart from FAIL.
	OutcomeTimeout Outcome = "TIMEOUT"
	// OutcomeError is the step not running at all: the port went away, an
	// external tool could not be started.
	OutcomeError Outcome = "ERROR"
	// OutcomeSkipped is a step switched off in the plan, or gated out by its
	// execute_condition. Kept in the report either way - a step that vanished
	// silently is a step nobody knows was not run.
	OutcomeSkipped Outcome = "SKIPPED"
)

// Counts as a pass for the purpose of the next step's condition and the run's
// exit code. A skipped step is not a failure.
func (o Outcome) IsPass() bool { return o == OutcomePass }

// Attempt is one try at a step.
type Attempt struct {
	N          int               `json:"n"`
	StartedAt  time.Time         `json:"started_at"`
	DurationMS int64             `json:"duration_ms"`
	Sent       []string          `json:"sent,omitempty"`
	Raw        []string          `json:"raw,omitempty"`
	Checks     []ptcheck.Result  `json:"checks,omitempty"`
	Extra      map[string]string `json:"extra,omitempty"`
	Err        string            `json:"error,omitempty"`
	Outcome    Outcome           `json:"outcome"`
}

// StepResult is one step's place in the report.
type StepResult struct {
	ID         string    `json:"id"`
	Type       string    `json:"type"`
	Outcome    Outcome   `json:"outcome"`
	Reason     string    `json:"reason,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	DurationMS int64     `json:"duration_ms"`
	Attempts   []Attempt `json:"attempts,omitempty"`
}

// Report is a whole run.
type Report struct {
	Plan         string       `json:"plan"`
	LimitVersion string       `json:"limit_version"`
	ToolVersion  string       `json:"tool_version"`
	Firmware     string       `json:"firmware,omitempty"`
	BoardUID     string       `json:"board_uid,omitempty"`
	SN           string       `json:"sn,omitempty"`
	Port         string       `json:"port,omitempty"`
	StartedAt    time.Time    `json:"started_at"`
	EndedAt      time.Time    `json:"ended_at"`
	Steps        []StepResult `json:"steps"`
}

// Passed reports whether nothing failed. A run with every step skipped did not
// prove anything, so it does not pass.
func (r Report) Passed() bool {
	ran := 0
	for _, s := range r.Steps {
		if s.Outcome == OutcomeSkipped {
			continue
		}
		ran++
		if !s.Outcome.IsPass() {
			return false
		}
	}
	return ran > 0
}

// Tally counts the outcomes, for a one-line summary.
func (r Report) Tally() map[Outcome]int {
	out := map[Outcome]int{}
	for _, s := range r.Steps {
		out[s.Outcome]++
	}
	return out
}

// Summary is the line a production operator reads.
func (r Report) Summary() string {
	t := r.Tally()
	verdict := "FAIL"
	if r.Passed() {
		verdict = "PASS"
	}
	return fmt.Sprintf("%s - %d passed, %d failed, %d timed out, %d errored, %d skipped, %s",
		verdict, t[OutcomePass], t[OutcomeFail], t[OutcomeTimeout], t[OutcomeError], t[OutcomeSkipped],
		r.EndedAt.Sub(r.StartedAt).Round(time.Millisecond))
}

// WriteJSON writes the whole report, for a system to read.
func (r Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteCSV writes one row per check, for a person to read in a spreadsheet.
//
// The limit itself is a column. Without it a report says a board passed but
// not what it passed, and six months later nobody can tell whether the limits
// have moved since.
func (r Report) WriteCSV(w io.Writer) error {
	cw := csv.NewWriter(w)
	header := []string{
		"plan", "limit_version", "sn", "board_uid", "firmware",
		"step", "type", "step_outcome", "attempt", "field", "limit", "got", "unit", "check", "why",
	}
	if err := cw.Write(header); err != nil {
		return err
	}
	for _, s := range r.Steps {
		if len(s.Attempts) == 0 {
			row := r.csvPrefix()
			row = append(row, s.ID, s.Type, string(s.Outcome), "", "", "", "", "", "", s.Reason)
			if err := cw.Write(row); err != nil {
				return err
			}
			continue
		}
		for _, a := range s.Attempts {
			if len(a.Checks) == 0 {
				row := r.csvPrefix()
				row = append(row, s.ID, s.Type, string(s.Outcome), strconv.Itoa(a.N),
					"", "", "", "", string(a.Outcome), firstNonEmpty(a.Err, s.Reason))
				if err := cw.Write(row); err != nil {
					return err
				}
				continue
			}
			for _, c := range a.Checks {
				verdict := "FAIL"
				if c.Pass {
					verdict = "PASS"
				}
				row := r.csvPrefix()
				row = append(row, s.ID, s.Type, string(s.Outcome), strconv.Itoa(a.N),
					c.Check.Field, c.Check.Describe(), c.Got, c.Check.Unit, verdict, c.Why)
				if err := cw.Write(row); err != nil {
					return err
				}
			}
		}
	}
	cw.Flush()
	return cw.Error()
}

func (r Report) csvPrefix() []string {
	return []string{r.Plan, r.LimitVersion, r.SN, r.BoardUID, r.Firmware}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// SetExtra records something about an attempt that is not a reading: the exit
// code of a tool, the operator's answer, where a report was written. Kept out
// of Checks because none of it was judged against a limit.
func (a *Attempt) SetExtra(key, value string) {
	if a.Extra == nil {
		a.Extra = map[string]string{}
	}
	a.Extra[key] = value
}
