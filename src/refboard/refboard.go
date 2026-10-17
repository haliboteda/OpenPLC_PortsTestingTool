// Package refboard is the reference board's registration (decision 135,
// PORTTOOL-FLOW.md C.3.6 「标准机设置页」): which of its channels is wired to
// which channel of the board under test, through which relay, and the
// parameters the panel runs it with. Filed by the reference board's UID next
// to its ranges.json; the ranges themselves stay in calstore.
package refboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Relays is how many relays a board under test has (RY1-RY6).
const Relays = 6

// Link is one digital channel of the board under test: Via 0 is a direct
// wire, 1..6 is relay RYn under test; To is the reference board's channel,
// 0 when not wired.
type Link struct {
	Via int `json:"via"`
	To  int `json:"to"`
}

// Params are the settings the panel runs the reference board with.
type Params struct {
	RS232Baud   int    `json:"rs232_baud"`
	EthIP       string `json:"eth_ip"` // "dhcp" or a static address
	CANBaud     int    `json:"can_baud"`
	CANID       string `json:"can_id"`
	RS485Baud   int    `json:"rs485_baud"`
	RS485Period int    `json:"rs485_period_ms"`
	DIPeriod    int    `json:"di_period_ms"`
	DISettle    int    `json:"di_settle_frames"`
	DOHold      int    `json:"do_hold_ms"`
	AIPeriod    int    `json:"ai_period_ms"`
	AIFrames    int    `json:"ai_frames"`
}

// Wiring is the whole registration.
type Wiring struct {
	// Analog is a reference channel (AO1, AO2, AI1, AI2) -> the board under
	// test's channel it is wired to, "" for none.
	Analog map[string]string `json:"analog"`
	DO     map[int]Link      `json:"do"` // board-under-test DOn -> a reference DI
	DI     map[int]Link      `json:"di"` // board-under-test DIn <- a reference DO
	Params Params            `json:"params"`
}

// AnalogNames are the reference board's analog channels in page order.
var AnalogNames = []string{"AO1", "AO2", "AI1", "AI2"}

// Default is an unregistered reference board: digital one to one with DO1-6
// through RY1-6 (decision 133), analog same number to same number (decision
// 134 "默认档位"), and DefaultParams.
func Default() Wiring {
	w := Wiring{Analog: map[string]string{"AO1": "AI1", "AO2": "AI2", "AI1": "AO1", "AI2": "AO2"},
		DO: map[int]Link{}, DI: map[int]Link{}, Params: DefaultParams()}
	for n := 1; n <= 8; n++ {
		via := 0
		if n <= Relays {
			via = n
		}
		w.DO[n] = Link{Via: via, To: n}
		w.DI[n] = Link{To: n}
	}
	return w
}

// DefaultParams are the parameters of an unregistered reference board: the
// 115200 control line, DHCP, the plan's 500 kbit/s CAN on the firmware's 0x123,
// din at its 50 ms floor settled over 2 frames, a DO beat held for those 2
// frames, ain at 200 ms averaged over 8 frames.
func DefaultParams() Params {
	return Params{RS232Baud: 115200, EthIP: "dhcp", CANBaud: 500000, CANID: "0x123", RS485Baud: 115200,
		RS485Period: 200, DIPeriod: 50, DISettle: 2, DOHold: 100, AIPeriod: 200, AIFrames: 8}
}

// Problem is one thing wrong with a registration; Key and Args word it on the page.
type Problem struct {
	Key  string         `json:"key"`
	Args map[string]any `json:"args"`
}

func (p Problem) Error() string { return fmt.Sprintf("%s %v", p.Key, p.Args) }

// Check refuses what cannot be wired: a channel of either board wired twice,
// one relay in two paths, AO to AO, a parameter out of range.
func (w Wiring) Check() []Problem {
	var out []Problem
	dut := map[string]string{}
	for _, ref := range AnalogNames {
		to := w.Analog[ref]
		if to == "" {
			continue
		}
		okPair := (ref[:2] == "AO" && (to == "AI1" || to == "AI2")) || (ref[:2] == "AI" && (to == "AO1" || to == "AO2"))
		if !okPair {
			out = append(out, Problem{"refboard.bad_pair", map[string]any{"ref": ref, "dut": to}})
			continue
		}
		if other, ok := dut[to]; ok {
			out = append(out, Problem{"refboard.dut_twice", map[string]any{"dut": to, "a": other, "b": ref}})
		}
		dut[to] = ref
	}
	relay := map[int]string{}
	for _, side := range []struct {
		name, ref string
		link      func(int) Link
	}{{"DO", "DI", w.DOLink}, {"DI", "DO", w.DILink}} {
		seen := map[int]int{}
		for n := 1; n <= 8; n++ {
			l := side.link(n)
			here := fmt.Sprintf("%s%d", side.name, n)
			if l.To < 0 || l.To > 8 || l.Via < 0 || l.Via > Relays {
				out = append(out, Problem{"refboard.bad_link", map[string]any{"dut": here}})
				continue
			}
			if l.To > 0 {
				if m, ok := seen[l.To]; ok {
					out = append(out, Problem{"refboard.ref_twice", map[string]any{"ref": fmt.Sprintf("%s%d", side.ref, l.To),
						"a": fmt.Sprintf("%s%d", side.name, m), "b": here}})
				}
				seen[l.To] = n
			}
			if l.Via > 0 {
				if other, ok := relay[l.Via]; ok {
					out = append(out, Problem{"refboard.relay_twice", map[string]any{"relay": fmt.Sprintf("RY%d", l.Via), "a": other, "b": here}})
				}
				relay[l.Via] = here
			}
		}
	}
	p := w.Params
	bad := func(name string, v any) {
		out = append(out, Problem{"refboard.bad_param", map[string]any{"param": name, "value": fmt.Sprint(v)}})
	}
	switch p.RS232Baud {
	case 9600, 19200, 38400, 57600, 115200, 230400:
	default:
		bad("rs232_baud", p.RS232Baud)
	}
	if p.EthIP != "dhcp" && !validIP(p.EthIP) {
		bad("eth_ip", p.EthIP)
	}
	if p.CANBaud <= 0 {
		bad("can_baud", p.CANBaud)
	}
	if !validCANID(p.CANID) {
		bad("can_id", p.CANID)
	}
	if p.RS485Baud <= 0 {
		bad("rs485_baud", p.RS485Baud)
	}
	if p.RS485Period < 1 {
		bad("rs485_period_ms", p.RS485Period)
	}
	if p.DIPeriod < 50 {
		bad("di_period_ms", p.DIPeriod)
	}
	if p.DISettle < 1 || p.DISettle > 10 {
		bad("di_settle_frames", p.DISettle)
	}
	if p.DOHold < 0 {
		bad("do_hold_ms", p.DOHold)
	}
	if p.AIPeriod < 1 {
		bad("ai_period_ms", p.AIPeriod)
	}
	if p.AIFrames < 1 || p.AIFrames > 100 {
		bad("ai_frames", p.AIFrames)
	}
	return out
}

func validIP(s string) bool {
	var a, b, c, d int
	if n, err := fmt.Sscanf(s, "%d.%d.%d.%d", &a, &b, &c, &d); err != nil || n != 4 {
		return false
	}
	for _, x := range []int{a, b, c, d} {
		if x < 0 || x > 255 {
			return false
		}
	}
	return fmt.Sprintf("%d.%d.%d.%d", a, b, c, d) == s
}

// validCANID is a standard 11-bit ID as the firmware's tx= takes it.
func validCANID(s string) bool {
	var v int
	if n, err := fmt.Sscanf(s, "0x%x", &v); err != nil || n != 1 {
		return false
	}
	return v >= 0 && v <= 0x7FF
}

// DOLink and DILink are one digital channel's registration, the default
// where it was never set.
func (w Wiring) DOLink(n int) Link { return linkOr(w.DO, n, Default().DO) }
func (w Wiring) DILink(n int) Link { return linkOr(w.DI, n, Default().DI) }

func linkOr(m map[int]Link, n int, def map[int]Link) Link {
	if l, ok := m[n]; ok {
		return l
	}
	return def[n]
}

// RefFor is the reference channel wired to the board under test's analog
// channel, "" when none.
func (w Wiring) RefFor(dut string) string {
	for _, ref := range AnalogNames {
		if w.Analog[ref] == dut {
			return ref
		}
	}
	return ""
}

// DOOfRelay is the board-under-test DO a relay sits on, 0 when it is on no DO.
func (w Wiring) DOOfRelay(ry int) int {
	for n := 1; n <= 8; n++ {
		if w.DOLink(n).Via == ry {
			return n
		}
	}
	return 0
}

const fileName = "refboard.json"

type file struct {
	Schema  int    `json:"schema"`
	UID     string `json:"uid"`
	SavedAt string `json:"saved_at"`
	Wiring
}

// Load is the registration in dir; saved is false when there is none and the
// default is returned.
func Load(dir string) (w Wiring, saved bool, err error) {
	data, err := os.ReadFile(filepath.Join(dir, fileName))
	if errors.Is(err, os.ErrNotExist) {
		return Default(), false, nil
	}
	if err != nil {
		return Default(), false, err
	}
	f := file{Wiring: Default()}
	if err := json.Unmarshal(data, &f); err != nil {
		return Default(), false, fmt.Errorf("reading %s: %w", fileName, err)
	}
	return f.Wiring, true, nil
}

// Save checks and writes the registration; nothing is written when Check
// finds a problem.
func Save(dir, uid string, w Wiring, now time.Time) (string, []Problem, error) {
	if p := w.Check(); len(p) > 0 {
		return "", p, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, err
	}
	data, err := json.MarshalIndent(file{Schema: 1, UID: uid, SavedAt: now.Format(time.RFC3339), Wiring: w}, "", "  ")
	if err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, fileName)
	return path, nil, os.WriteFile(path, append(data, '\n'), 0o644)
}
