package ptproto

import (
	"strconv"
	"strings"
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

// TickUnwrapper turns the board's 32-bit tick into a monotonic millisecond
// count, and says when the board appears to have restarted underneath it.
//
// HAL_GetTick() wraps at 2^32 ms, about 49.7 days, and a long run that goes
// through the wrap would otherwise show its timeline jump back to zero. The
// board cannot help with this - it has no idea how long it has been up beyond
// that counter - so the PC keeps the high bits.
//
// *** A restart also sends the tick backwards, and the two mean opposite
// *** things. *** A wrap is a board that has been running so long the counter
// ran out; a restart is a board that stopped. Treating a restart as a wrap -
// which this did until 2026-09-14 - silently adds 49.7 days to the timeline
// and makes the restart disappear, which is exactly the event an ageing run
// exists to catch ("异常复位/死机 0 次" in the production test guide).
//
// They are told apart by where the counter was: a wrap can only happen from
// near the top of the range. The board says WHY it restarted separately, in
// pt.run reset.cause - only it can know that, and this cannot.
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
// of uptime.
//
// Generous on purpose. A production run is hours, so its ticks sit in the
// first thousandth of the range and no restart there can be mistaken for a
// wrap. The cost is at the other end: a board that restarts after more than
// 24.8 days of continuous uptime is counted as a wrap and its restart is
// missed. That case is not one this tool is used for, and a threshold tight
// enough to catch it would start calling real wraps restarts instead.
const wrapFloor = uint32(1) << 31

// Unwrap returns the monotonic millisecond count, and whether this sample is
// the first one after the board restarted.
//
// The restart flag is returned rather than left on the struct because ignoring
// it has to be a deliberate act: a caller that just wants a timeline is
// exactly the caller that used to lose the restart.
func (u *TickUnwrapper) Unwrap(tick uint32) (ms uint64, restarted bool) {
	if u.begun && tick < u.last {
		if u.last >= wrapFloor {
			u.wraps++
		} else {
			// The board started over. Its own clock is back at zero, so the
			// high bits move on instead - the timeline has to keep going
			// forward across an event whose whole significance is that it
			// happened.
			u.wraps++
			u.Restarts++
			restarted = true
		}
	}
	u.last = tick
	u.begun = true
	return u.wraps<<32 | uint64(tick), restarted
}
