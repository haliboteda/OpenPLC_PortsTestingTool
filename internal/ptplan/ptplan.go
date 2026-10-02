// Package ptplan is the plan file: an ordered list of steps, each carrying its
// own limits.
//
// A limit is a step's parameter, not an entry in a separate table. There is no
// table to keep in step with the sequence, and no way for a limit to exist
// that no step uses - see DECISIONS.md 24.
//
// Step types are the three generic pt.* ones, and the port is a parameter
// rather than part of the type. Adding a port to the firmware then costs one
// more step in a plan file and no change to this program, which is the whole
// point of pt.caps - see DECISIONS.md 25.
package ptplan

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"PortTool/internal/calarea"
	"PortTool/internal/ptcheck"
	"PortTool/internal/ptproto"
)

// Schema is the plan format this build understands.
const Schema = 1

// Type is what a step does.
type Type string

const (
	// TypePtSession starts a session, collects frames, checks the last one.
	TypePtSession Type = "PtSession"
	// TypePtRun performs a one-shot action and checks the k=v it answers with.
	TypePtRun Type = "PtRun"
	// TypePtRaw sends any pt.* line and checks the reply.
	TypePtRaw Type = "PtRaw"

	// TypeTool runs an external program - a programmer, an instrument CLI, a
	// fixture utility - and judges its output or exit code. Configured, not
	// driven by code of ours: which tool and which arguments are plan data, so
	// swapping a programmer does not mean a new build.
	TypeTool Type = "Tool"

	// TypeUserConfirm asks a person. For the readings no board can report on
	// itself: whether the indicator actually lit, what the safety tester said.
	TypeUserConfirm Type = "UserConfirm"

	// TypeReadSN brings a serial number in from outside. One half of the
	// "interfaces only" boundary in DECISIONS.md 20 - this tool does not
	// allocate serial numbers, it is told one.
	TypeReadSN Type = "ReadSN"

	// TypePushResult hands the report to whatever comes next. The other half
	// of that boundary.
	TypePushResult Type = "PushResult"
)

// Condition gates a step on what the previous executed step decided.
type Condition string

const (
	// CondPass runs the step only if the last executed step passed. Default.
	CondPass Condition = "PASS"
	// CondFail runs the step only if the last executed step failed, for a
	// salvage or diagnostic step.
	CondFail Condition = "FAIL"
	// CondAlways runs the step whatever happened, for teardown that has to
	// happen even after a failure.
	CondAlways Condition = "ALWAYS"
)

// Params are the k=v tokens a pt command takes.
//
// The protocol is text, but nearly every parameter these ports accept is a
// number - period, duty, freq, mv, baud - so a plan writing `"period": 200`
// rather than `"200"` is the normal case, not a mistake, and is accepted.
type Params map[string]string

func (p *Params) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	out := make(Params, len(raw))
	for k, v := range raw {
		text := strings.TrimSpace(string(v))
		switch {
		case text == "":
			return fmt.Errorf("parameter %q has no value", k)
		case text[0] == '"':
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				return err
			}
			out[k] = s
		case text[0] == '{' || text[0] == '[' || text == "null":
			return fmt.Errorf("parameter %q must be a string or a number", k)
		default:
			out[k] = text
		}
	}
	*p = out
	return nil
}

// Peer says what has to answer a session on the far side of the link.
//
// A loop=link port - eth, usb, rs485 - is judged on whether the number it put
// on the link came back unchanged, and the firmware refuses to answer such a
// port over the control channel on purpose: doing so would let the counter
// climb with the link under test already dead (DECISIONS.md 9). So the station
// PC has to be the far end, and this is where a plan says which cable that is.
//
// Without it those ports can only ever fail, which is why eth's session step
// shipped disabled: there was nowhere to say "and something has to answer it".
//
// The JSON names are the file format.
type Peer struct {
	// TCP is "auto" to take the address from the session's own ip= and port=
	// fields, or an explicit "host:port". Auto is the useful one: the board's
	// address comes from DHCP, so a plan cannot know it in advance.
	TCP string `json:"tcp,omitempty"`

	// Serial is the adapter this machine recorded for the port, chosen by a
	// person in the panel. A plan never names it: port names change with the
	// machine and the plug order (DECISIONS.md 73 in $PROD).
	Serial bool `json:"serial,omitempty"`

	// USB finds the board's own CDC port by its USB ids. ⚠️ That port only
	// exists once the usb session has started, because that is when the board
	// initialises its USB stack - so it is looked up after pt.start, not
	// before.
	USB bool `json:"usb,omitempty"`

	// Baud for a serial peer. Zero means the default.
	Baud int `json:"baud,omitempty"`
}

// Step is one entry in a plan. The JSON names are the file format.
type Step struct {
	ID      string `json:"id"`
	Type    Type   `json:"type"`
	Enabled *bool  `json:"enabled,omitempty"`
	Note    string `json:"_note,omitempty"`

	// The six fields every step has, whatever its type. Zero means "not set"
	// for all of them, and the defaults are applied by the accessors below so
	// a hand-written plan can leave out everything it does not need.
	ExecuteCondition Condition `json:"execute_condition,omitempty"`
	RetryCount       int       `json:"retry_count,omitempty"`
	RetryIntervalMS  int       `json:"retry_interval_ms,omitempty"`
	SleepBeforeMS    int       `json:"sleep_before_ms,omitempty"`
	SleepAfterMS     int       `json:"sleep_after_ms,omitempty"`
	TimeoutMS        int       `json:"timeout_ms,omitempty"`

	// PtSession.
	Port   string `json:"port,omitempty"`
	Params Params `json:"params,omitempty"`
	Frames int    `json:"frames,omitempty"`
	Peer   *Peer  `json:"peer,omitempty"`

	// PtRun.
	Target string `json:"target,omitempty"`

	// PtRaw.
	Command string `json:"command,omitempty"`

	// Tool.
	ToolName string   `json:"tool_name,omitempty"`
	ToolArgs []string `json:"tool_args,omitempty"`
	ToolDir  string   `json:"tool_dir,omitempty"`

	// UserConfirm.
	Prompt string `json:"prompt,omitempty"`

	// ReadSN and PushResult. Source is "stdin", "file:<path>" or "arg" (the
	// --sn the run was started with); Sink is "file:<path>" or "dir:<path>".
	Source string `json:"source,omitempty"`
	Sink   string `json:"sink,omitempty"`

	Checks []ptcheck.Check `json:"checks,omitempty"`
}

// DefaultTimeoutMS is used by a step that names no timeout of its own. Long
// enough for a session to push several frames at the slowest sane period.
const DefaultTimeoutMS = 20000

// IsEnabled reports whether the step should run. Absent means enabled, so a
// plan only has to say when a step is switched off.
func (s Step) IsEnabled() bool { return s.Enabled == nil || *s.Enabled }

// Condition is the gate with its default applied.
func (s Step) Condition() Condition {
	if s.ExecuteCondition == "" {
		return CondPass
	}
	return s.ExecuteCondition
}

// Timeout is the step's own timeout, or the default.
func (s Step) Timeout() int {
	if s.TimeoutMS <= 0 {
		return DefaultTimeoutMS
	}
	return s.TimeoutMS
}

// FrameCount is how many frames a PtSession waits for, at least one.
func (s Step) FrameCount() int {
	if s.Frames <= 0 {
		return 1
	}
	return s.Frames
}

// Attempts is 1 plus the retries.
func (s Step) Attempts() int {
	if s.RetryCount < 0 {
		return 1
	}
	return 1 + s.RetryCount
}

// ParamArgs renders Params as the "k=v" tokens a pt command takes, sorted by
// key. The firmware accepts them in any order and commits them atomically, so
// sorting costs nothing and makes a command line reproducible.
func (s Step) ParamArgs() []string {
	keys := make([]string, 0, len(s.Params))
	for k := range s.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+s.Params[k])
	}
	return out
}

// Plan is a whole plan file.
type Plan struct {
	Schema       int          `json:"schema"`
	Name         string       `json:"name"`
	LimitVersion string       `json:"limit_version"`
	Note         string       `json:"_note,omitempty"`
	Calibration  *Calibration `json:"calibration,omitempty"`
	Steps        []Step       `json:"steps"`
}

// Calibration is the accuracy a board must reach after calibration
// (decision 70). In the plan so that changing it is changing a file
// (decision 30). See PRODUCTION-FRAMEWORK.md, "精度指标".
type Calibration struct {
	Note string `json:"_note,omitempty"`
	// TemperatureC is recorded, not judged: no all-temperature figure is set yet.
	TemperatureC float64      `json:"temperature_c,omitempty"`
	Channels     []CalChannel `json:"channels"`
}

// CalChannel is the limit for one calibrated channel.
type CalChannel struct {
	Channel          string  `json:"channel"`
	Unit             string  `json:"unit"`
	FullScale        float64 `json:"full_scale"`
	MaxResidualPctFS float64 `json:"max_residual_pct_fs"`
}

// Limit returns the limit for a channel, if the plan states one.
func (c *Calibration) Limit(channel string) (CalChannel, bool) {
	if c == nil {
		return CalChannel{}, false
	}
	for _, ch := range c.Channels {
		if ch.Channel == channel {
			return ch, true
		}
	}
	return CalChannel{}, false
}

func (c *Calibration) validate() error {
	if len(c.Channels) == 0 {
		return fmt.Errorf("calibration lists no channels")
	}
	seen := map[string]bool{}
	for _, ch := range c.Channels {
		i, ok := calarea.Index(ch.Channel)
		if !ok {
			return fmt.Errorf("calibration channel %q is not one of %v", ch.Channel, calarea.Names)
		}
		if seen[ch.Channel] {
			return fmt.Errorf("calibration channel %q is listed twice", ch.Channel)
		}
		seen[ch.Channel] = true
		// The coefficients go into the calibration area in that channel's unit;
		// a limit written in another unit would judge a different number.
		if ch.Unit != calarea.Units[i] {
			return fmt.Errorf("calibration channel %s is fitted in %s, not %q", ch.Channel, calarea.Units[i], ch.Unit)
		}
		if ch.FullScale <= 0 || ch.MaxResidualPctFS <= 0 {
			return fmt.Errorf("calibration channel %s needs a positive full_scale and max_residual_pct_fs", ch.Channel)
		}
	}
	return nil
}

// Load reads a plan file and validates it. A plan that does not validate is
// not returned: half a plan is worse on a production line than none.
func Load(path string) (Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Plan{}, err
	}
	return Parse(data)
}

// Parse reads a plan from JSON and validates it.
func Parse(data []byte) (Plan, error) {
	var p Plan
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		// A misspelled key is the most likely hand-editing mistake, and the one
		// that would otherwise be silently ignored - so unknown fields are an
		// error, and the decoder's message names the offending key.
		return Plan{}, fmt.Errorf("reading plan: %w", err)
	}
	if err := p.Validate(); err != nil {
		return Plan{}, err
	}
	return p, nil
}

// Validate checks everything decidable without a board.
func (p Plan) Validate() error {
	if p.Schema != Schema {
		return fmt.Errorf("plan schema is %d, this build understands %d", p.Schema, Schema)
	}
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("plan has no name")
	}
	if strings.TrimSpace(p.LimitVersion) == "" {
		// The production test guide requires every station record to name the
		// limit version it judged by; a plan without one cannot be traced back.
		return fmt.Errorf("plan has no limit_version")
	}
	if len(p.Steps) == 0 {
		return fmt.Errorf("plan has no steps")
	}
	if p.Calibration != nil {
		if err := p.Calibration.validate(); err != nil {
			return err
		}
	}

	seen := map[string]int{}
	for i, s := range p.Steps {
		where := fmt.Sprintf("step %d", i+1)
		if s.ID != "" {
			where = fmt.Sprintf("step %q", s.ID)
		}
		if strings.TrimSpace(s.ID) == "" {
			return fmt.Errorf("%s has no id", where)
		}
		if prev, dup := seen[s.ID]; dup {
			return fmt.Errorf("step id %q is used twice, at %d and %d", s.ID, prev+1, i+1)
		}
		seen[s.ID] = i

		switch s.Condition() {
		case CondPass, CondFail, CondAlways:
		default:
			return fmt.Errorf("%s has unknown execute_condition %q", where, s.ExecuteCondition)
		}
		if s.RetryCount < 0 {
			return fmt.Errorf("%s has a negative retry_count", where)
		}
		for _, neg := range []struct {
			name string
			v    int
		}{
			{"retry_interval_ms", s.RetryIntervalMS},
			{"sleep_before_ms", s.SleepBeforeMS},
			{"sleep_after_ms", s.SleepAfterMS},
			{"timeout_ms", s.TimeoutMS},
		} {
			if neg.v < 0 {
				return fmt.Errorf("%s has a negative %s", where, neg.name)
			}
		}

		if err := s.validateType(where); err != nil {
			return err
		}
		for _, c := range s.Checks {
			if err := c.Validate(); err != nil {
				return fmt.Errorf("%s: %w", where, err)
			}
		}
	}
	return nil
}

func (s Step) validateType(where string) error {
	switch s.Type {
	case TypePtSession:
		if s.Port == "" {
			return fmt.Errorf("%s is a PtSession with no port", where)
		}
		if len(s.Checks) == 0 {
			// A session with no limits starts and stops a port and reports
			// nothing about it, which on a production line reads as a pass.
			return fmt.Errorf("%s is a PtSession with no checks, so it can only ever pass", where)
		}
	case TypePtRun:
		if s.Target == "" {
			return fmt.Errorf("%s is a PtRun with no target", where)
		}
	case TypePtRaw:
		if s.Command == "" {
			return fmt.Errorf("%s is a PtRaw with no command", where)
		}
		if !strings.HasPrefix(s.Command, "pt.") {
			return fmt.Errorf("%s sends %q, which is not a pt.* command", where, s.Command)
		}
	case TypeTool:
		if s.ToolName == "" {
			return fmt.Errorf("%s is a Tool with no tool_name", where)
		}
	case TypeUserConfirm:
		if strings.TrimSpace(s.Prompt) == "" {
			return fmt.Errorf("%s is a UserConfirm with no prompt", where)
		}
		if len(s.Checks) > 0 {
			// The verdict is the person's answer. A limit here would be a
			// second opinion with nothing to form it from.
			return fmt.Errorf("%s is a UserConfirm, whose verdict is the answer, so it takes no checks", where)
		}
	case TypeReadSN:
		if err := validateSource(s.Source); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
	case TypePushResult:
		if err := validateSink(s.Sink); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
	case "":
		return fmt.Errorf("%s has no type", where)
	default:
		return fmt.Errorf("%s has unknown type %q", where, s.Type)
	}

	// Fields belonging to another type are rejected rather than ignored: a
	// step that names a port but is a PtRun would otherwise test something
	// other than what it reads like.
	stray := []string{}
	if s.Type != TypePtSession {
		if s.Port != "" {
			stray = append(stray, "port")
		}
		if s.Frames != 0 {
			stray = append(stray, "frames")
		}
	}
	if s.Type != TypePtRun && s.Target != "" {
		stray = append(stray, "target")
	}
	if s.Type != TypePtRaw && s.Command != "" {
		stray = append(stray, "command")
	}
	if s.Type != TypeTool && (s.ToolName != "" || len(s.ToolArgs) > 0 || s.ToolDir != "") {
		stray = append(stray, "tool_name/tool_args/tool_dir")
	}
	if s.Type != TypeUserConfirm && s.Prompt != "" {
		stray = append(stray, "prompt")
	}
	if s.Type != TypeReadSN && s.Source != "" {
		stray = append(stray, "source")
	}
	if s.Type != TypePushResult && s.Sink != "" {
		stray = append(stray, "sink")
	}
	if len(stray) > 0 {
		return fmt.Errorf("%s is a %s but sets %s", where, s.Type, strings.Join(stray, ", "))
	}
	return nil
}

// SourceKind and SinkKind split "file:/path" into its two halves, so the
// executor never has to re-parse the plan's spelling of them.
func SourceKind(source string) (kind, arg string) {
	if k, a, ok := strings.Cut(source, ":"); ok {
		return k, a
	}
	return source, ""
}

// SinkKind is SourceKind for the other direction.
func SinkKind(sink string) (kind, arg string) { return SourceKind(sink) }

func validateSource(source string) error {
	kind, arg := SourceKind(source)
	switch kind {
	case "arg", "stdin":
		return nil
	case "file":
		if arg == "" {
			return fmt.Errorf("source is %q but names no file", source)
		}
		return nil
	case "":
		return fmt.Errorf("a ReadSN step needs a source: arg, stdin or file:<path>")
	default:
		return fmt.Errorf("unknown source %q - use arg, stdin or file:<path>", source)
	}
}

func validateSink(sink string) error {
	kind, arg := SinkKind(sink)
	switch kind {
	case "file", "dir":
		if arg == "" {
			return fmt.Errorf("sink is %q but names no path", sink)
		}
		return nil
	case "":
		return fmt.Errorf("a PushResult step needs a sink: file:<path> or dir:<path>")
	default:
		return fmt.Errorf("unknown sink %q - use file:<path> or dir:<path>", sink)
	}
}

// counterFields are the loopback counters. On a loop=ctrl port they say only
// that the control port and the polling loop are alive, so a limit on them is
// not a limit on that port - see DECISIONS.md 9.
var counterFields = map[string]bool{"seq": true, "rx": true, "miss": true}

// CheckAgainstCaps reports what only a board can settle: ports that are not
// there, parameters the firmware does not accept, and limits that look like a
// verdict but are not one.
//
// It returns findings rather than one error because a plan is usually worth
// reading in full before touching it, and because the false-criterion finding
// is a warning about meaning, not a syntax error.
func (p Plan) CheckAgainstCaps(caps ptproto.Caps) []string {
	byName := map[string]ptproto.Port{}
	for _, port := range caps.Ports {
		byName[port.Name] = port
	}

	// Every pt.run target the firmware reports, so a plan that names one that
	// does not exist is caught here rather than mid-station. Keyed by target
	// name because a plan step names the target, not the hardware.
	runTargets := map[string]string{}
	for _, port := range caps.Ports {
		for _, t := range port.Runs {
			runTargets[t] = port.Name
		}
	}

	var out []string
	for _, s := range p.Steps {
		if s.Type == TypePtRun {
			// Older firmware reported no run rows at all. Saying nothing then
			// is the honest answer: the plan may well be right, and inventing
			// a finding for every step would bury the real ones.
			if len(runTargets) == 0 {
				continue
			}
			if _, ok := runTargets[s.Target]; !ok {
				out = append(out, fmt.Sprintf("step %q runs %q, which this firmware does not report", s.ID, s.Target))
			}
			continue
		}
		if s.Type != TypePtSession {
			continue
		}
		port, ok := byName[s.Port]
		if !ok {
			out = append(out, fmt.Sprintf("step %q names port %q, which this firmware does not report", s.ID, s.Port))
			continue
		}
		if port.Kind != ptproto.KindSession {
			out = append(out, fmt.Sprintf("step %q starts a session on %q, but the firmware reports it as %s", s.ID, s.Port, port.Kind))
			continue
		}
		accepted := map[string]bool{}
		for _, name := range port.Params {
			accepted[name] = true
		}
		for k, v := range s.Params {
			if !accepted[k] {
				out = append(out, fmt.Sprintf("step %q sets %s=… on %q, which accepts only %s",
					s.ID, k, s.Port, strings.Join(port.Params, ", ")))
				continue
			}
			// The firmware states what each parameter will take, so a value it
			// would refuse can be caught here instead of on the line with a
			// board in front of somebody.
			if lim, ok := port.Limits[k]; ok {
				if okv, why := lim.Accepts(v); !okv {
					out = append(out, fmt.Sprintf("step %q sets %s=%s on %q, but the firmware says it %s (%s)",
						s.ID, k, v, s.Port, why, lim.Spec))
				}
			}
		}
		if port.Loop == ptproto.LoopCtrl {
			for _, c := range s.Checks {
				name, _, err := splitFieldName(c.Field)
				if err == nil && counterFields[name] {
					out = append(out, fmt.Sprintf(
						"step %q judges %q on %s by %s, but that port's loop is ctrl: the counter proves the control port is alive, not that %s works",
						s.ID, c.Field, s.Port, name, s.Port))
				}
			}
		}
	}
	return out
}

// splitFieldName is the field-name half of ptcheck's field syntax, needed here
// to tell "miss" from "miss[0]" when looking for false criteria.
func splitFieldName(field string) (string, bool, error) {
	if i := strings.IndexByte(field, '['); i >= 0 {
		if i == 0 || !strings.HasSuffix(field, "]") {
			return "", false, fmt.Errorf("field %q is malformed", field)
		}
		return field[:i], true, nil
	}
	return field, false, nil
}
