// Package ptcheck decides whether one value read from a board is acceptable.
//
// It is the only place a limit is compared to a reading. The panel and the
// production sequence both call it, so the two can never disagree about
// whether a board passed - the same rule ptproto follows for parsing.
//
// A check carries no knowledge of ports, frames or serial lines: it is handed
// a field name and a way to look one up. That keeps limits comparable no
// matter which kind of step produced the reading.
package ptcheck

import (
	"fmt"
	"strconv"
	"strings"
)

// Op is how a reading is compared to the limit.
type Op string

const (
	// OpRange accepts Min <= got <= Max. Either bound may be omitted.
	OpRange Op = "range"
	// OpEq and OpNe compare numerically when both sides parse as numbers,
	// so 0xFF and 255 are the same value, and case-insensitively otherwise.
	OpEq Op = "eq"
	OpNe Op = "ne"
	// OpContains looks for Value as a substring, case-insensitively.
	OpContains Op = "contains"
	// OpCountZero requires the reading to be the number zero. It exists so a
	// plan can say "no misses" without spelling out eq 0, and so the failure
	// message can name what was counted.
	OpCountZero Op = "count_zero"
)

// Check is one limit. It is a plan-file element, so the JSON names are part of
// the file format.
type Check struct {
	Field string   `json:"field"`
	Op    Op       `json:"op"`
	Min   *float64 `json:"min,omitempty"`
	Max   *float64 `json:"max,omitempty"`
	Value string   `json:"value,omitempty"`
	Unit  string   `json:"unit,omitempty"`
	Note  string   `json:"_note,omitempty"`
}

// Lookup returns the raw text of a field, exactly as the board wrote it, and
// whether the field was there at all.
type Lookup func(field string) (string, bool)

// Result is what one check decided, kept together with what it decided on so a
// report can be re-judged later against a different limit.
type Result struct {
	Check Check  `json:"check"`
	Got   string `json:"got"`
	Pass  bool   `json:"pass"`
	Why   string `json:"why,omitempty"`
	// Missing says the board never reported the field, as opposed to
	// reporting a value that failed. The panel says those two differently,
	// and Got cannot carry the difference: a field reported empty and a
	// field not reported at all both leave Got "".
	Missing bool `json:"missing,omitempty"`
}

// Validate reports a check that could never pass, or never fail, because of
// how it is written rather than what the board reports.
func (c Check) Validate() error {
	if strings.TrimSpace(c.Field) == "" {
		return fmt.Errorf("check has no field")
	}
	if _, _, err := splitField(c.Field); err != nil {
		return err
	}
	switch c.Op {
	case OpRange:
		if c.Min == nil && c.Max == nil {
			return fmt.Errorf("check on %q is a range with neither min nor max", c.Field)
		}
		if c.Min != nil && c.Max != nil && *c.Min > *c.Max {
			return fmt.Errorf("check on %q has min %g above max %g", c.Field, *c.Min, *c.Max)
		}
	case OpEq, OpNe, OpContains:
		if c.Value == "" {
			return fmt.Errorf("check on %q is %s but has no value", c.Field, c.Op)
		}
	case OpCountZero:
		if c.Value != "" || c.Min != nil || c.Max != nil {
			return fmt.Errorf("check on %q is count_zero, which takes no value or bounds", c.Field)
		}
	case "":
		return fmt.Errorf("check on %q has no op", c.Field)
	default:
		return fmt.Errorf("check on %q has unknown op %q", c.Field, c.Op)
	}
	return nil
}

// Describe states the limit in one line, for a report or a panel row.
func (c Check) Describe() string {
	unit := ""
	if c.Unit != "" {
		unit = " " + c.Unit
	}
	switch c.Op {
	case OpRange:
		switch {
		case c.Min != nil && c.Max != nil:
			return fmt.Sprintf("%s in %g..%g%s", c.Field, *c.Min, *c.Max, unit)
		case c.Min != nil:
			return fmt.Sprintf("%s >= %g%s", c.Field, *c.Min, unit)
		default:
			return fmt.Sprintf("%s <= %g%s", c.Field, *c.Max, unit)
		}
	case OpCountZero:
		return fmt.Sprintf("%s is zero", c.Field)
	default:
		return fmt.Sprintf("%s %s %s%s", c.Field, c.Op, c.Value, unit)
	}
}

// Eval reads the field and applies the limit.
//
// A field that is not there fails rather than being skipped: a plan naming a
// field the firmware stopped reporting has to be noticed, and a silently
// skipped limit is how a board ships untested.
func (c Check) Eval(look Lookup) Result {
	res := Result{Check: c}
	if err := c.Validate(); err != nil {
		res.Why = err.Error()
		return res
	}

	name, idx, _ := splitField(c.Field)
	raw, ok := look(name)
	if !ok {
		res.Missing = true
		res.Why = fmt.Sprintf("no field %q in what the board reported", name)
		return res
	}

	got := raw
	if idx >= 0 {
		parts := strings.Split(raw, "/")
		if idx >= len(parts) {
			res.Got = raw
			res.Why = fmt.Sprintf("%s has %d slash-separated part(s), wanted part %d", name, len(parts), idx)
			return res
		}
		got = parts[idx]
	}
	res.Got = got

	switch c.Op {
	case OpRange:
		n, err := parseNum(got)
		if err != nil {
			res.Why = fmt.Sprintf("%s is %q, which is not a number", c.Field, got)
			return res
		}
		if c.Min != nil && n < *c.Min {
			res.Why = fmt.Sprintf("%g is below %g", n, *c.Min)
			return res
		}
		if c.Max != nil && n > *c.Max {
			res.Why = fmt.Sprintf("%g is above %g", n, *c.Max)
			return res
		}
		res.Pass = true
	case OpEq:
		if sameValue(got, c.Value) {
			res.Pass = true
		} else {
			res.Why = fmt.Sprintf("expected %s, got %s", c.Value, got)
		}
	case OpNe:
		if sameValue(got, c.Value) {
			res.Why = fmt.Sprintf("expected anything but %s", c.Value)
		} else {
			res.Pass = true
		}
	case OpContains:
		if strings.Contains(strings.ToLower(got), strings.ToLower(c.Value)) {
			res.Pass = true
		} else {
			res.Why = fmt.Sprintf("%q does not contain %q", got, c.Value)
		}
	case OpCountZero:
		n, err := parseNum(got)
		if err != nil {
			res.Why = fmt.Sprintf("%s is %q, which is not a number", c.Field, got)
			return res
		}
		if n == 0 {
			res.Pass = true
		} else {
			res.Why = fmt.Sprintf("%s counted %g, expected none", c.Field, n)
		}
	}
	return res
}

// EvalAll runs every check and reports whether they all passed.
func EvalAll(checks []Check, look Lookup) ([]Result, bool) {
	out := make([]Result, 0, len(checks))
	all := true
	for _, c := range checks {
		r := c.Eval(look)
		if !r.Pass {
			all = false
		}
		out = append(out, r)
	}
	return out, all
}

// splitField pulls the part index out of "ch1[1]". idx is -1 when there is
// none. The index is zero-based: for ain's "raw/mV", [0] is raw and [1] is mV.
func splitField(field string) (name string, idx int, err error) {
	open := strings.IndexByte(field, '[')
	if open < 0 {
		if strings.IndexByte(field, ']') >= 0 {
			return "", -1, fmt.Errorf("field %q has a ] with no [", field)
		}
		return field, -1, nil
	}
	if !strings.HasSuffix(field, "]") {
		return "", -1, fmt.Errorf("field %q has a [ with no closing ]", field)
	}
	name = field[:open]
	if name == "" {
		return "", -1, fmt.Errorf("field %q has an index but no name", field)
	}
	inner := field[open+1 : len(field)-1]
	n, convErr := strconv.Atoi(inner)
	if convErr != nil || n < 0 {
		return "", -1, fmt.Errorf("field %q has index %q, which is not a whole number", field, inner)
	}
	return name, n, nil
}

// parseNum accepts what the firmware actually prints: decimals, 0x hex masks,
// and negative numbers such as a deci-Celsius temperature.
func parseNum(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	if i, err := strconv.ParseInt(s, 0, 64); err == nil {
		return float64(i), nil
	}
	if u, err := strconv.ParseUint(s, 0, 64); err == nil {
		return float64(u), nil
	}
	return strconv.ParseFloat(s, 64)
}

func sameValue(a, b string) bool {
	na, errA := parseNum(a)
	nb, errB := parseNum(b)
	if errA == nil && errB == nil {
		return na == nb
	}
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}
