package ptreport

// Failure codes are spelled from the criteria and reason codes, never kept in a
// table (decision 150, PORTTOOL-FLOW.md C.3.6 「失败码」).

import (
	"strconv"
	"strings"

	"PortTool/src/ptcheck"
)

// FailCode is "<where>.<reason>" for an outcome that did not pass, "" for one
// that did, was skipped or only recorded. where is the item id, plus ".<channel
// name>" for one channel; ch > 0 prefers a failed check naming ch<ch>.
func FailCode(where string, ch int, o Outcome, reason string, checks []ptcheck.Result) string {
	var why string
	switch o {
	case OutcomeFail:
		if c := firstFailed(checks, ch); c != nil {
			why = checkCode(c.Check)
		} else {
			why = firstNonEmpty(reasonCode(reason), "fail")
		}
	case OutcomeTimeout:
		why = firstNonEmpty(reasonCode(reason), "timeout")
	case OutcomeError:
		why = firstNonEmpty(reasonCode(reason), "error")
	case OutcomeStopped:
		why = firstNonEmpty(reasonCode(reason), "stopped")
	default:
		return ""
	}
	return codePart(where) + "." + why
}

// ChannelPart is a channel name as a code segment: DI1/DI2 becomes DI1+DI2.
func ChannelPart(name string) string { return codePart(strings.ReplaceAll(name, "/", "+")) }

// AttemptChecks is every check of every attempt, in order.
func AttemptChecks(atts []Attempt) []ptcheck.Result {
	var out []ptcheck.Result
	for _, a := range atts {
		out = append(out, a.Checks...)
	}
	return out
}

// firstFailed is the first failed check naming ch<ch>, else the first failed one.
func firstFailed(checks []ptcheck.Result, ch int) *ptcheck.Result {
	if ch > 0 {
		want := "ch" + strconv.Itoa(ch)
		for i, c := range checks {
			if !c.Pass && fieldName(c.Check.Field) == want {
				return &checks[i]
			}
		}
	}
	for i, c := range checks {
		if !c.Pass {
			return &checks[i]
		}
	}
	return nil
}

func fieldName(f string) string {
	if i := strings.IndexByte(f, '['); i >= 0 {
		return f[:i]
	}
	return f
}

// checkCode is "<field>-<op>", a field's [n] written _n.
func checkCode(c ptcheck.Check) string {
	f := strings.NewReplacer("[", "_", "]", "").Replace(c.Field)
	return codePart(f) + "-" + string(c.Op)
}

// reasonCode is a reason that is a code (ptlib's Reason* are lower_snake),
// or "" for a sentence.
func reasonCode(reason string) string {
	r := strings.TrimSpace(reason)
	if r == "" {
		return ""
	}
	for _, c := range r {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return ""
		}
	}
	return r
}

// codePart keeps a segment free of spaces, which would split it on a line.
func codePart(s string) string { return strings.ReplaceAll(strings.TrimSpace(s), " ", "_") }

// CollectFailCodes is every code in the report, in order, each once.
func (r Report) CollectFailCodes() []string {
	var out []string
	seen := map[string]bool{}
	add := func(c string) {
		if c != "" && !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	for _, s := range r.Steps {
		add(s.FailCode)
		for _, c := range s.Channels {
			add(c.FailCode)
		}
		for _, it := range s.Items {
			add(it.FailCode)
		}
	}
	return out
}
