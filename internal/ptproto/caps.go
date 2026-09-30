package ptproto

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Everything the panel knows about the board comes from pt.caps. Nothing here
// hard-codes a port: adding one to the firmware is meant to be the only edit
// needed for it to appear on screen, and that only holds if this file refuses
// to keep its own list.

// Kind separates a port that can be started and stopped from one that is a
// one-way door into a standalone test.
type Kind string

const (
	KindSession Kind = "session"
	// KindRun is hardware whose tests finish and answer with numbers -
	// pt.run. A production plan is built out of these, so having them in
	// caps is what lets a plan file be checked without a board attached.
	KindRun Kind = "run"
)

// Loop says where a port's echo counter travels, which decides what the
// counter is worth. See PORTTOOL-FLOW.md and DECISIONS.md 9.
type Loop string

const (
	// LoopLink rides the port under test: the counter proves that link works.
	LoopLink Loop = "link"
	// LoopCtrl rides the RS232 control port: the counter proves only that the
	// control port and the loop are alive. The port's own verdict is its
	// readings, never this number, and the panel must not conflate them.
	LoopCtrl Loop = "ctrl"
	// LoopSelf is rs232 and only rs232: the port under test IS the control
	// port, so the control-port round trip does prove the link. The counter is
	// the verdict here, unlike LoopCtrl - the panel has to say so, because for
	// this port there is no other reading to fall back on.
	LoopSelf Loop = "self"
	// LoopNone is not a link at all - there is no peer to answer.
	LoopNone Loop = "none"
)

// Port is one row of pt.caps.
type Port struct {
	Name string
	// Board is which of the product's PCBs the port lives on, as the firmware
	// reports it: "bridge", "upper", "lower", "junction", or "whole" for a
	// test that spans them. Empty from firmware older than 0.7.0.
	//
	// It comes from the board rather than from a table here for the same
	// reason nothing else about a port does: a table would go stale the moment
	// the firmware gains a port, and the port would land in the wrong group or
	// none at all.
	Board    string
	Kind     Kind
	Block    string // Klemmblock letter, "-" when the port has no terminal
	Term     string // terminal range, e.g. "D02-D09"
	Channels int
	Loop     Loop

	// Sessions only.
	Params  []string          // parameter names pt.start and pt.set accept
	Values  map[string]string // the port's current value for each of Params
	Running bool

	// Runs is this hardware's pt.run targets. A session row may carry them
	// too: eth's session is the TCP server and eth.link is the PHY probe, one
	// RJ45 between them, so they share a row.
	Runs []string

	terms []string // explicit per-channel labels, when the firmware sent them

	// Limits is what each parameter will accept, keyed by parameter name.
	// Empty when the firmware sent no limits= line for this port - older
	// firmware, or a port with nothing worth stating.
	Limits map[string]Limit
}

// Limit is one parameter's accepted values, as the firmware states them.
//
// Having these come from the board rather than from a table here is the whole
// point: the numbers are the ones the firmware's own checks use, so a panel
// that greys out an impossible value and a board that refuses it can never
// disagree.
type Limit struct {
	Spec string // exactly what the firmware said, e.g. "1..2000"

	// Min/Max are set for a range; Max is nil for an open-ended one.
	Min *float64
	Max *float64

	// Enum is set for "a|b|c".
	Enum []string
}

// Accepts reports whether a value is within this limit, and why not if it is
// not. A limit nothing could parse accepts everything: refusing on a spec this
// build does not understand would turn a newer firmware into a broken panel.
func (l Limit) Accepts(value string) (bool, string) {
	if len(l.Enum) > 0 {
		for _, e := range l.Enum {
			if strings.EqualFold(e, value) {
				return true, ""
			}
		}
		return false, fmt.Sprintf("must be one of %s", strings.Join(l.Enum, ", "))
	}
	if l.Min == nil && l.Max == nil {
		return true, ""
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		// Channel lists like "1,2,3" are checked by the firmware itself; a
		// limit here only speaks for single numbers.
		return true, ""
	}
	if l.Min != nil && n < *l.Min {
		return false, fmt.Sprintf("must be at least %g", *l.Min)
	}
	if l.Max != nil && n > *l.Max {
		return false, fmt.Sprintf("must be at most %g", *l.Max)
	}
	return true, ""
}

// parseLimits reads "ch:1..8 mode:hold|blink period:50.." into one Limit per
// parameter. A spec it cannot read is kept verbatim and accepts everything -
// see Accepts.
func parseLimits(specs string) map[string]Limit {
	out := map[string]Limit{}
	for _, tok := range strings.Fields(specs) {
		name, spec, ok := strings.Cut(tok, ":")
		if !ok || name == "" {
			continue
		}
		l := Limit{Spec: spec}
		switch {
		case strings.Contains(spec, "|"):
			l.Enum = strings.Split(spec, "|")
		case strings.Contains(spec, ".."):
			lo, hi, _ := strings.Cut(spec, "..")
			if v, err := strconv.ParseFloat(lo, 64); err == nil {
				l.Min = &v
			}
			if hi != "" {
				if v, err := strconv.ParseFloat(hi, 64); err == nil {
					l.Max = &v
				}
			}
		}
		out[name] = l
	}
	return out
}

// Caps is a whole pt.caps reply.
type Caps struct {
	Version string
	Ports   []Port
}

var termRange = regexp.MustCompile(`^([A-Za-z]+)(\d+)-([A-Za-z]+)(\d+)$`)

// TerminalLabels is what to write next to each channel's checkbox.
//
// Most ports are a plain run of terminals and the firmware sends only the
// range, so the labels are derived here. Where the channels do not line up
// with the terminals one for one - a relay is a contact pair, so six channels
// span twelve terminals - the firmware sends the labels explicitly, because
// that mapping cannot be guessed and guessing it wrong mislabels every box.
//
// Returns nil when neither is possible; the panel then shows channel numbers,
// which is honest, rather than invented terminal names.
func (p Port) TerminalLabels() []string {
	if len(p.terms) > 0 {
		return p.terms
	}
	if p.Channels == 1 && p.Term != "" && p.Term != "-" {
		return []string{p.Term}
	}
	m := termRange.FindStringSubmatch(p.Term)
	if m == nil {
		return nil
	}
	lo, err1 := strconv.Atoi(m[2])
	hi, err2 := strconv.Atoi(m[4])
	if err1 != nil || err2 != nil || m[1] != m[3] || hi-lo+1 != p.Channels {
		return nil
	}
	width := len(m[2])
	out := make([]string, 0, p.Channels)
	for n := lo; n <= hi; n++ {
		out = append(out, fmt.Sprintf("%s%0*d", m[1], width, n))
	}
	return out
}

// ParseCaps reads the reply to pt.caps: a header line then exactly the number
// of lines it declares.
//
// The count is taken from the header rather than by reading until something
// stops arriving, because nothing marks the end of a reply on this link and a
// sample frame from a running session can land in the middle of one.
func ParseCaps(lines []string) (Caps, error) {
	if len(lines) == 0 {
		return Caps{}, fmt.Errorf("pt.caps returned nothing")
	}

	kind, body := Classify(lines[0])
	if kind != LineOK {
		return Caps{}, fmt.Errorf("pt.caps header is not an OK line: %q", lines[0])
	}
	head := Fields(body)

	version, ok := Get(head, "porttool")
	if !ok {
		return Caps{}, fmt.Errorf("pt.caps header has no porttool= version: %q", lines[0])
	}
	declared, ok := GetU32(head, "lines")
	if !ok {
		return Caps{}, fmt.Errorf("pt.caps header has no lines= count: %q", lines[0])
	}
	if int(declared) != len(lines)-1 {
		return Caps{}, fmt.Errorf("pt.caps said lines=%d but %d followed", declared, len(lines)-1)
	}

	caps := Caps{Version: version}
	// Both of these arrive on their own line, which may come before or after
	// the port line it belongs to.
	//
	// Current values are a separate line because a port with a value per
	// channel does not fit its shape and its values inside the firmware's
	// 192-byte line limit - dout spends 31 characters on duty= alone. One
	// line would have to be trimmed, and a trimmed caps line is a parameter
	// the panel silently never renders.
	termsFor := map[string][]string{}
	valsFor := map[string][]Pair{}
	limitsFor := map[string]map[string]Limit{}

	for _, line := range lines[1:] {
		k, body := Classify(line)
		if k != LineOK {
			return Caps{}, fmt.Errorf("pt.caps emitted a non-OK line: %q", line)
		}

		if rest, cut := strings.CutPrefix(body, "terms="); cut {
			name, list, _ := strings.Cut(rest, " ")
			termsFor[name] = splitList(list)
			continue
		}

		if rest, cut := strings.CutPrefix(body, "vals="); cut {
			name, kv, _ := strings.Cut(rest, " ")
			valsFor[name] = Fields(kv)
			continue
		}

		if rest, cut := strings.CutPrefix(body, "limits="); cut {
			name, specs, _ := strings.Cut(rest, " ")
			limitsFor[name] = parseLimits(specs)
			continue
		}

		f := Fields(body)
		name, ok := Get(f, "port")
		if !ok {
			return Caps{}, fmt.Errorf("pt.caps line has neither port= nor terms=: %q", line)
		}

		p := Port{Name: name}
		if v, ok := Get(f, "kind"); ok {
			p.Kind = Kind(v)
		}
		p.Board, _ = Get(f, "board")
		p.Block, _ = Get(f, "blk")
		p.Term, _ = Get(f, "term")
		if n, ok := GetU32(f, "channels"); ok {
			p.Channels = int(n)
		}
		if v, ok := Get(f, "loop"); ok {
			p.Loop = Loop(v)
		}

		switch p.Kind {
		case KindRun:
			if v, ok := Get(f, "runs"); ok {
				p.Runs = splitList(v)
			}
			if len(p.Runs) == 0 {
				return Caps{}, fmt.Errorf("run port %s listed no runs=", p.Name)
			}
		case KindSession:
			// A session may also carry runs=: eth's session is a TCP server
			// and eth.link is the PHY probe, one RJ45 between them. Unlike a
			// run row, a session with no runs= is normal - most have none.
			if v, ok := Get(f, "runs"); ok {
				p.Runs = splitList(v)
			}
			if v, ok := Get(f, "running"); ok {
				p.Running = v != "0"
			}
			if v, ok := Get(f, "params"); ok {
				p.Params = splitList(v)
			}
			// Values arrive on the port's vals= line and are matched up once
			// every line has been read, since either may come first.
		default:
			return Caps{}, fmt.Errorf("port %s has an unknown kind=%q", p.Name, p.Kind)
		}

		switch p.Loop {
		case LoopLink, LoopCtrl, LoopSelf, LoopNone:
		default:
			return Caps{}, fmt.Errorf("port %s has an unknown loop=%q", p.Name, p.Loop)
		}

		caps.Ports = append(caps.Ports, p)
	}

	declaredPorts, ok := GetU32(head, "ports")
	if ok && int(declaredPorts) != len(caps.Ports) {
		return Caps{}, fmt.Errorf("pt.caps said ports=%d but %d were listed",
			declaredPorts, len(caps.Ports))
	}

	for i := range caps.Ports {
		p := &caps.Ports[i]
		if t, ok := termsFor[p.Name]; ok {
			p.terms = t
			delete(termsFor, p.Name)
		}
		if l, ok := limitsFor[p.Name]; ok {
			p.Limits = l
			delete(limitsFor, p.Name)
		}

		if p.Kind != KindSession {
			continue
		}
		kv, ok := valsFor[p.Name]
		if !ok {
			return Caps{}, fmt.Errorf("session %s sent no vals= line", p.Name)
		}
		delete(valsFor, p.Name)

		// A session must report a current value for everything it says it
		// accepts, otherwise the panel would draw a control with no state to
		// show. Missing one is the firmware's bug, not something to paper over
		// with a blank box.
		p.Values = map[string]string{}
		for _, param := range p.Params {
			v, ok := Get(kv, param)
			if !ok {
				return Caps{}, fmt.Errorf("port %s advertises params=%s but its vals= line has no %s=",
					p.Name, strings.Join(p.Params, ","), param)
			}
			p.Values[param] = v
		}
	}
	for name := range termsFor {
		return Caps{}, fmt.Errorf("pt.caps sent terms for %q, which is not a port it listed", name)
	}
	for name := range limitsFor {
		return Caps{}, fmt.Errorf("pt.caps sent limits for %q, which is not a port it listed", name)
	}
	for name := range valsFor {
		return Caps{}, fmt.Errorf("pt.caps sent vals for %q, which is not a session it listed", name)
	}

	return caps, nil
}

// ChannelValues reads a per-channel parameter - "1:20,5:75" - into a value per
// channel number. Returns nil when the parameter is absent or is a single
// value that applies to every channel.
//
// The panel needs this to draw one control per channel rather than one text
// box holding a list nobody can edit by hand. Which parameters are per-channel
// is not something to hard-code: it is whatever the firmware sent in this
// shape, so a new one needs no change here.
func (p Port) ChannelValues(param string) map[int]int {
	raw, ok := p.Values[param]
	if !ok || !strings.Contains(raw, ":") {
		return nil
	}
	out := map[int]int{}
	for _, entry := range strings.Split(raw, ",") {
		chStr, valStr, found := strings.Cut(entry, ":")
		if !found {
			return nil
		}
		ch, err1 := strconv.Atoi(chStr)
		val, err2 := strconv.Atoi(valStr)
		if err1 != nil || err2 != nil || ch < 1 || ch > p.Channels {
			return nil
		}
		out[ch] = val
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Port finds one by name.
func (c Caps) Port(name string) (Port, bool) {
	for _, p := range c.Ports {
		if p.Name == name {
			return p, true
		}
	}
	return Port{}, false
}

func splitList(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}
