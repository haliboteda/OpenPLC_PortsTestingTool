package ptproto

import (
	"strconv"
	"strings"
)

// The fault lamp, lit by a failed verdict or a fault and put out when a run starts.
const (
	LEDFaultOn  = "pt.led fault=1"
	LEDFaultOff = "pt.led fault=0"
)

// One serial line from the board is exactly one of four things, and the first
// character says which. That is the whole protocol difference, so the reader
// needs no state machine - see PORTTOOL-FLOW.md A.4.
type LineKind int

const (
	LineLog   LineKind = iota // bare printf, for a person
	LineOK                    // successful reply to a command
	LineErr                   // refused command; show the text as it came
	LineFrame                 // "!<port> ..." periodic sample, arrives unprompted
)

func (k LineKind) String() string {
	switch k {
	case LineOK:
		return "ok"
	case LineErr:
		return "err"
	case LineFrame:
		return "frame"
	default:
		return "log"
	}
}

// Classify reports which of the four a line is, and the line with its marker
// removed. A frame keeps its "!" stripped but its port name intact.
func Classify(line string) (LineKind, string) {
	switch {
	case strings.HasPrefix(line, "OK "):
		return LineOK, line[3:]
	case strings.HasPrefix(line, "ERR "):
		return LineErr, line[4:]
	case strings.HasPrefix(line, "!"):
		return LineFrame, line[1:]
	default:
		return LineLog, line
	}
}

// Pair is one "k=v" token, kept in the order it appeared.
type Pair struct {
	Key   string
	Value string
}

// Fields splits "port=din kind=session ch=1,3,5" into ordered pairs, dropping
// bare words. Order is kept and duplicates are not merged: on this protocol a
// repeated key means the firmware is emitting something ambiguous, and a map
// would silently pick a winner instead of letting anyone notice.
func Fields(s string) []Pair {
	var out []Pair
	for _, tok := range strings.Fields(s) {
		if k, v, ok := strings.Cut(tok, "="); ok {
			out = append(out, Pair{k, v})
		}
	}
	return out
}

// Get returns the first value for key, and whether it was there at all - the
// caller usually needs to tell "absent" from "present and empty".
func Get(pairs []Pair, key string) (string, bool) {
	for _, p := range pairs {
		if p.Key == key {
			return p.Value, true
		}
	}
	return "", false
}

// GetU32 is Get plus a decimal parse; ok is false if absent or not a number.
func GetU32(pairs []Pair, key string) (uint32, bool) {
	s, ok := Get(pairs, key)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(n), true
}

// LineRate is the control-line rate a reply announces ("OK started rs232
// line=19200"), 0 when it announces none. The board switches once that reply
// is out and the PC must follow (decision 104, PORTTOOL-FLOW.md B.7).
func LineRate(reply string) int {
	kind, body := Classify(reply)
	if kind != LineOK {
		return 0
	}
	v, _ := GetU32(Fields(body), "line")
	return int(v)
}

// Frame is one periodic sample: "!din t=48213 v=0x16 ch1=0 ch3=1".
type Frame struct {
	Port   string
	Tick   uint32 // HAL_GetTick() at the board, wraps every 49.7 days
	Fields []Pair // everything after t=, in the order the board wrote it
	Raw    string
}

// ParseFrame reads a frame body, i.e. a line whose "!" has been stripped.
// ok is false when there is no port name, which is the only way a frame can be
// malformed enough to be useless.
func ParseFrame(body string) (Frame, bool) {
	port, rest, _ := strings.Cut(body, " ")
	if port == "" {
		return Frame{}, false
	}
	f := Frame{Port: port, Raw: "!" + body}
	all := Fields(rest)
	for i, p := range all {
		if p.Key == "t" {
			if n, err := strconv.ParseUint(p.Value, 10, 32); err == nil {
				f.Tick = uint32(n)
			}
			f.Fields = append(f.Fields, all[:i]...)
			f.Fields = append(f.Fields, all[i+1:]...)
			return f, true
		}
	}
	f.Fields = all
	return f, true
}

// TickUnwrapper turns the board's 32-bit tick (wraps every 49.7 days) into a
// monotonic millisecond count, and says when the board restarted.
//
// A backwards step is a wrap only from the top of the range (wrapFloor),
// otherwise a restart: an ageing run exists to catch restarts, so one must
// never be absorbed as a wrap (PORTTOOL-FLOW.md C.3.1). Why it restarted is
// the board's pt.run reset.cause.
type TickUnwrapper struct {
	last  uint32
	wraps uint64
	begun bool

	// Restarts counts how many times the tick fell back from somewhere it
	// could not have wrapped from.
	Restarts int
}

// wrapFloor is how high the counter must have been for a backwards step to be
// a wrap rather than a restart: the upper half of the range, about 24.8 days
// of uptime. A restart after more than that is missed; production runs are
// hours.
const wrapFloor = uint32(1) << 31

// Unwrap returns the monotonic millisecond count, and whether this sample is
// the first one after the board restarted. The flag is returned, not left on
// the struct, so that ignoring it is a deliberate act.
func (u *TickUnwrapper) Unwrap(tick uint32) (ms uint64, restarted bool) {
	if u.begun && tick < u.last {
		if u.last >= wrapFloor {
			u.wraps++
		} else {
			// The board started over: the high bits move on so the timeline
			// keeps going forward.
			u.wraps++
			u.Restarts++
			restarted = true
		}
	}
	u.last = tick
	u.begun = true
	return u.wraps<<32 | uint64(tick), restarted
}
