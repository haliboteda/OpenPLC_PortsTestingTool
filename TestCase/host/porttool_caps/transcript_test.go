package testcase

import (
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// row is one port= line of the first pt.caps.
type row struct {
	line string
	d    map[string]string
	kv   []kvPair
}

// tc is one pass over the transcript; each method is one block of checks.
type tc struct {
	checker
	f         *firmware
	capsRuns  [][]string
	first     []string
	sessions  []row
	runs      []row
	valsLines map[string]map[string]string
}

func (x *tc) shapeOf(i int, name string) map[string]string {
	return x.capsLine(i, "OK port="+name+" ")
}

func (x *tc) valsOf(i int, name string) map[string]string {
	return x.capsLine(i, "OK vals="+name+" ")
}

func (x *tc) capsLine(i int, prefix string) map[string]string {
	if i < len(x.capsRuns) && len(x.capsRuns[i]) > 0 {
		for _, l := range x.capsRuns[i][1:] {
			if strings.HasPrefix(l, prefix) {
				return kvMap(l)
			}
		}
	}
	return map[string]string{}
}

func (x *tc) limitsOf(i int, name string) map[string]string {
	out := map[string]string{}
	if i >= len(x.capsRuns) || len(x.capsRuns[i]) == 0 {
		return out
	}
	for _, l := range x.capsRuns[i][1:] {
		if strings.HasPrefix(l, "OK limits="+name+" ") {
			parts := strings.SplitN(l, " ", 3)
			if len(parts) == 3 {
				for _, tok := range strings.Fields(parts[2]) {
					if k, v, ok := strings.Cut(tok, ":"); ok {
						out[k] = v
					}
				}
			}
			return out
		}
	}
	return out
}

func (x *tc) all() []row { return append(append([]row{}, x.sessions...), x.runs...) }

func TestFirmwareTranscript(t *testing.T) {
	if fw == nil {
		t.Skip("needs -build: go test ./TestCase/host/porttool_caps/ -args -build (TestCase/test.sh t4-01)")
	}
	x := &tc{checker: checker{t}, f: fw}
	x.lineDiscipline()
	x.capsHeader()
	x.capsRows()
	x.runTargets()
	x.lifecycle()
	x.rxErrors()
	x.cal()
	x.calw()
	x.doutFreq()
	x.doutRes()
	x.rtcClock()
	x.sdHotPlug()
	x.quadIn()
	x.quadOut()
	x.echoLoop()
	x.rs485()
	x.rs232Line()
	x.analogGate()
	x.levels()
	x.relayCycle()
	x.refusals()
	x.oneShots()
	x.limits()
	x.sd()
	x.can()
	x.usb()
	x.eth()
	x.hold()
	x.led()
}

func (x *tc) lineDiscipline() {
	f := x.f
	x.section("line discipline")
	var board []string
	for _, s := range f.sections {
		for _, l := range s.body {
			if !strings.HasPrefix(l, "TEST ") {
				board = append(board, l)
			}
		}
	}
	var over []string
	maxOver, longest := 0, 0
	for _, l := range board {
		if len(l) > f.lineMax {
			over = append(over, l)
			if len(l) > maxOver {
				maxOver = len(l)
			}
		}
		if len(l) > longest {
			longest = len(l)
		}
	}
	firstOver := ""
	if len(over) > 0 {
		firstOver = cut(over[0], 70)
	}
	x.check(len(over) == 0, fmt.Sprintf("every line the board sends within PORTTOOL_REPLY_MAX (%d bytes)", f.lineMax),
		fmt.Sprintf("longest %d bytes: %s", maxOver, firstOver))
	x.t.Logf("longest line is %d bytes, %d to spare", longest, f.lineMax-longest)

	// Log text (ptproto LineLog) is legal only from pt.run's checks; any other
	// bare line is a stray printf that would split a reply.
	prose := regexp.MustCompile(`^[A-Z][A-Z0-9_]*_TEST: `)
	var bad []string
	for _, l := range board {
		if !(strings.HasPrefix(l, "OK ") || strings.HasPrefix(l, "ERR ") || strings.HasPrefix(l, "!") || prose.MatchString(l)) {
			bad = append(bad, l)
		}
	}
	x.check(len(bad) == 0, "every line is a reply, a frame, or prose from a named source", strings.Join(head(bad, 1), ""))
}

func (x *tc) capsHeader() {
	x.capsRuns = x.f.bodies("pt.caps")
	x.check(len(x.capsRuns) >= 3, "pt.caps ran at least three times in the transcript")
	if len(x.capsRuns) == 0 || len(x.capsRuns[0]) == 0 {
		x.t.Fatal("the transcript has no pt.caps reply")
	}
	x.first = x.capsRuns[0]
	hdr := kvMap(x.first[0])
	x.section("pt.caps header")
	x.t.Log(x.first[0])
	x.check(hdr["porttool"] == x.f.version, "header version matches PORTTOOL_VERSION", pyGet(hdr, "porttool"))
	x.check(has(hdr, "lines"), "header declares lines=")
	declared := atoiOr(hdr["lines"], -1)
	x.check(declared == len(x.first)-1, "lines= matches the lines that follow",
		fmt.Sprintf("declared %d, got %d", declared, len(x.first)-1))
	n := 0
	for _, l := range x.first[1:] {
		if strings.HasPrefix(l, "OK port=") {
			n++
		}
	}
	x.check(atoiOr(hdr["ports"], -1) == n, "ports= matches the number of port= lines",
		fmt.Sprintf("declared %s, got %d", pyGet(hdr, "ports"), n))
}

func (x *tc) capsRows() {
	x.section("pt.caps rows")
	var dupKeys, unknownKind []string
	for _, line := range x.first[1:] {
		if !strings.HasPrefix(line, "OK port=") {
			continue
		}
		kv := parseKV(line)
		count := map[string]int{}
		for _, p := range kv {
			count[p.k]++
		}
		for k, n := range count {
			if n > 1 {
				dupKeys = append(dupKeys, k)
			}
		}
		d := kvMap(line)
		switch d["kind"] {
		case "session":
			x.sessions = append(x.sessions, row{line, d, kv})
		case "run":
			x.runs = append(x.runs, row{line, d, kv})
		default:
			unknownKind = append(unknownKind, pyGet(d, "port")+"/"+pyGet(d, "kind"))
		}
		x.t.Logf("%-9s %-9s blk=%s term=%-9s ch=%s loop=%s", d["port"], d["kind"], d["blk"], d["term"], d["channels"], d["loop"])
	}
	x.check(len(dupKeys) == 0, "no caps line repeats a key", fmt.Sprint(dupKeys))
	x.check(len(unknownKind) == 0, "every caps row carries a kind the panel knows", fmt.Sprint(unknownKind))

	// board= is what the panel groups by; an unknown value is a typo that would
	// put the port in a group of its own.
	boards := map[string]bool{"bridge": true, "upper": true, "lower": true, "junction": true, "whole": true}
	var badBoard []string
	byBoard := map[string][]string{}
	for _, r := range x.all() {
		if !boards[r.d["board"]] || !has(r.d, "board") {
			badBoard = append(badBoard, r.d["port"]+"/"+pyGet(r.d, "board"))
		}
		byBoard[r.d["board"]] = append(byBoard[r.d["board"]], r.d["port"])
	}
	x.check(len(badBoard) == 0, "every port names one of the product's boards", fmt.Sprint(badBoard))
	for b, ps := range byBoard {
		sort.Strings(ps)
		x.t.Logf("%-9s %s", b, strings.Join(ps, " "))
	}

	var missing []string
	loopsOK, chOK := true, true
	for _, r := range x.all() {
		for _, fld := range []string{"port", "board", "kind", "blk", "term", "channels", "loop"} {
			if !has(r.d, fld) {
				missing = append(missing, r.d["port"]+"."+fld)
			}
		}
		switch r.d["loop"] {
		case "ctrl", "link", "self", "none":
		default:
			loopsOK = false
		}
		ch := r.d["channels"]
		if !has(r.d, "channels") {
			ch = "0"
		}
		if !isDigits(ch) || atoiOr(ch, 0) < 1 {
			chOK = false
		}
	}
	x.check(len(missing) == 0, "every port row carries the fields the panel needs", fmt.Sprint(head(missing, 3)))
	x.check(loopsOK, "loop= is one of ctrl / link / self / none")
	x.check(chOK, "channels= is a positive integer everywhere")

	// Current values live on the port's own vals= line: shape and values do not
	// both fit inside the line limit once a port has a value per channel.
	x.valsLines = map[string]map[string]string{}
	for _, l := range x.first[1:] {
		if rest, ok := strings.CutPrefix(l, "OK vals="); ok {
			if name := strings.Fields(rest); len(name) > 0 {
				x.valsLines[name[0]] = kvMap(l)
			}
		}
	}
	var vk, sp []string
	for k := range x.valsLines {
		vk = append(vk, k)
	}
	for _, r := range x.sessions {
		sp = append(sp, r.d["port"])
	}
	sort.Strings(vk)
	sort.Strings(sp)
	x.check(eqStrings(vk, sp), "every session sent a vals= line and nothing else did", fmt.Sprintf("%v vs %v", vk, sp))

	// A session must report a current value for every parameter it advertises,
	// otherwise the panel renders a control it cannot show the state of.
	for _, r := range x.sessions {
		reported := x.valsLines[r.d["port"]]
		var absent []string
		for _, p := range strings.Split(r.d["params"], ",") {
			if p != "" && !has(reported, p) {
				absent = append(absent, p)
			}
		}
		x.check(len(absent) == 0, fmt.Sprintf("%s reports a value for each of params=%s", r.d["port"], pyGet(r.d, "params")),
			fmt.Sprintf("missing %v", absent))
	}
}

func (x *tc) runTargets() {
	x.section("run targets")
	allRuns := true
	for _, r := range x.runs {
		if r.d["runs"] == "" {
			allRuns = false
		}
	}
	x.check(allRuns, "every run row lists its runs")

	var listed []string
	for _, b := range x.f.bodies("pt.run") {
		listed = nil
		for _, l := range b {
			if strings.HasPrefix(l, "OK run=") {
				if fs := strings.Fields(strings.SplitN(l, "=", 2)[1]); len(fs) > 0 {
					listed = append(listed, fs[0])
				}
			}
		}
	}
	// A session row may carry runs= too (eth.link on eth), so both kinds are swept.
	var inCaps []string
	for _, r := range append(append([]row{}, x.runs...), x.sessions...) {
		if r.d["runs"] != "" {
			inCaps = append(inCaps, strings.Split(r.d["runs"], ",")...)
		}
	}
	// Every pt.run target must be in caps: that list is how a plan's targets are
	// checked with no board attached.
	x.t.Logf("pt.run lists %d targets; caps offers %d", len(listed), len(inCaps))
	a, b := append([]string{}, listed...), append([]string{}, inCaps...)
	sort.Strings(a)
	sort.Strings(b)
	x.check(eqStrings(a, b), "every pt.run target appears in caps and nothing else does",
		fmt.Sprintf("list %v / caps %v", a, b))

	// One row per piece of hardware (DECISIONS.md 17).
	seen := map[string]int{}
	var dup []string
	for _, r := range x.all() {
		if seen[r.d["port"]]++; seen[r.d["port"]] == 2 {
			dup = append(dup, r.d["port"])
		}
	}
	x.check(len(dup) == 0, "no two caps rows share a port name", fmt.Sprint(dup))
}

func (x *tc) lifecycle() {
	f := x.f
	x.section("session lifecycle")
	x.check(x.shapeOf(0, "din")["running"] == "0", "din starts not running")
	x.check(x.shapeOf(1, "din")["running"] == "1", "din reports running=1 once started")
	x.check(x.shapeOf(2, "din")["running"] == "0", "din reports running=0 after pt.stop all")
	during := x.valsOf(1, "din")
	x.check(during["ch"] == "1,3,5", "caps echoes back the channels pt.start was given", pyGet(during, "ch"))
	energised := ""
	for _, l := range f.lines("TEST relay_energised=") {
		energised = strings.SplitN(l, "=", 2)[1]
	}
	x.check(energised != "" && strings.Trim(energised, "0") == "", "pt.stop all released every relay", "energised="+energised)
	x.t.Log(last(f.lines("TEST relay_init_count=")))
}

func (x *tc) rxErrors() {
	f := x.f
	x.section("receive errors on the command port")
	// The HAL does not clear a receive error for a zero-timeout HAL_UART_Receive,
	// so the firmware clears it itself; a latched flag makes the board deaf.
	rx := map[string]string{}
	for _, s := range f.sections {
		for _, l := range s.body {
			if strings.HasPrefix(l, "TEST rx_before=") || strings.HasPrefix(l, "TEST rx_after=") {
				for k, v := range kvMap(l) {
					rx[k] = v
				}
			}
		}
	}
	x.check(rx["rx_before"] == "0" && rx["rx_after"] == "1", "a raised receive error is counted, not ignored",
		fmt.Sprintf("before=%s after=%s", pyGet(rx, "rx_before"), pyGet(rx, "rx_after")))
	// 0xE is ORECF | NECF | FECF, bits 3, 2, 1: any other value leaves a flag latched.
	x.check(strings.ToUpper(rx["icr_written"]) == "0XE", "all three receive-error flags are cleared",
		"ICR="+pyGet(rx, "icr_written")+" want 0xE (ORECF|NECF|FECF)")
	// 32 is HAL_UART_STATE_READY; a handle left BUSY_RX makes every later receive HAL_BUSY.
	x.check(rx["state"] == "32" && rx["error_code"] == "0", "the handle is left ready with no error code",
		fmt.Sprintf("state=%s error_code=%s", pyGet(rx, "state"), pyGet(rx, "error_code")))

	// A command fed one byte at a time through the real ISR is dispatched.
	fed := ""
	for _, s := range f.sections {
		if !strings.Contains(s.cmd, "one byte at a time") {
			continue
		}
		for _, l := range s.body {
			if strings.HasPrefix(l, "OK uid=") {
				fed = l
			}
		}
	}
	x.check(fed != "", "a command delivered through the ISR is dispatched", "no reply followed the fed bytes")
	// rx_dropped is the ring overflowing; non-zero inside a six-byte feed makes all of the above suspect.
	x.check(strings.Contains(fed, "rx_dropped=0"), "the receive ring did not overflow", fed)
}

func (x *tc) cal() {
	f := x.f
	// Raw bytes only; decoding them is the Go side's half (ptproto_test.go).
	x.section("pt.cal: the calibration area, raw")
	const calSize = 56 // sizeof(calib_area_t), locked by its _Static_assert
	var cals []string
	for _, b := range f.bodies("pt.cal") {
		if len(b) > 0 {
			cals = append(cals, b[0])
		} else {
			cals = append(cals, "(nothing)")
		}
	}
	x.check(len(cals) == 2, "pt.cal answered twice (written, erased)", fmt.Sprintf("got %d", len(cals)))
	if len(cals) != 2 {
		return
	}
	re := regexp.MustCompile(fmt.Sprintf(`^OK cal=([0-9A-F]{%d})$`, 2*calSize))
	w, e := re.FindStringSubmatch(cals[0]), re.FindStringSubmatch(cals[1])
	d := cals[1]
	if w == nil {
		d = cals[0]
	}
	x.check(w != nil && e != nil, fmt.Sprintf("each reply is one line of %d hex digits", 2*calSize), d)
	if w == nil || e == nil {
		return
	}
	area, _ := hex.DecodeString(w[1])
	x.check(string(area[:4]) == "OCAL", "a written area starts with the magic, little-endian", hex.EncodeToString(area[:4]))
	idLine := ""
	for _, b := range f.bodies("pt.id") {
		if len(b) > 0 {
			idLine = b[0]
			break
		}
	}
	uidHex := kvMap(idLine)["uid"]
	var want []byte
	if len(uidHex) == 24 {
		for i := 0; i < 24; i += 8 {
			v, _ := strconv.ParseUint(uidHex[i:i+8], 16, 32)
			want = append(want, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
		}
	}
	x.check(string(area[8:20]) == string(want), "its UID bytes are the words pt.id reports",
		hex.EncodeToString(area[8:20])+" vs "+uidHex)
	x.check(strings.Trim(e[1], "F") == "", "an erased area is reported as it is, all FF", cals[1])
}

func (x *tc) calw() {
	f := x.f
	// The only flash the fixture writes (decision 142): sector 15, nothing else.
	x.section("pt.calw: writes sector 15 and nothing else")
	var calws []string
	for _, s := range f.sections {
		if strings.HasPrefix(s.cmd, "pt.calw") {
			if len(s.body) > 0 {
				calws = append(calws, s.body[0])
			} else {
				calws = append(calws, "(nothing)")
			}
		}
	}
	want := []string{"ERR calw refused: crc does not match",
		"ERR calw refused: uid is not this board's",
		"ERR calw refused: want 112 hex digits",
		"ERR calw refused: want 112 hex digits",
		"ERR calw refused: not hex",
		"ERR calw refused: flash banks are swapped"}
	x.check(eqStrings(head(calws, 6), want), "a bad record or a swapped bank is refused with its reason", fmt.Sprint(head(calws, 6)))
	x.check(len(calws) == 8 && strings.HasPrefix(calws[6], "OK calw words="), "a good record is written", fmt.Sprint(calws))
	x.check(len(calws) == 8 && strings.HasPrefix(calws[7], "ERR calw failed: erase"), "a failed erase is reported as such", fmt.Sprint(calws))
	tl := map[string]map[string]string{}
	for _, l := range f.rawLines("TEST calw_") {
		if fs := strings.Fields(l); len(fs) > 1 {
			tl[strings.SplitN(fs[1], "=", 2)[0]] = kvMap(l)
		}
	}
	x.check(tl["calw_refused_erases"]["calw_refused_erases"] == "0", "a refused pt.calw never erases", fmt.Sprint(tl["calw_refused_erases"]))
	w := tl["calw_erases"]
	x.check(w["calw_erases"] == "1" && w["stray"] == "0" && w["record"] == "1" && w["tail_blank"] == "1",
		"one erase of sector 15, the record in place, the rest of the area blank", fmt.Sprint(w))
	x.check(w["root_kept"] == "1" && w["after_root_blank"] == "1", "the root area survives and blank words stay blank", fmt.Sprint(w))
	g := tl["calw_guard"]
	x.check(g["below"] == "0" && g["above"] == "0" && g["misaligned"] == "0",
		"the guard refuses a word below, above or across sector 15", fmt.Sprint(g))
	x.check(g["inside"] == "1" && g["stray"] == "0",
		"the guard lets a word inside through, and nothing stray reached the HAL", fmt.Sprint(g))
}

func anyContains(lines []string, subs ...string) bool {
	for _, l := range lines {
		ok := true
		for _, s := range subs {
			if !strings.Contains(l, s) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// refusedWith: some reply line to cmd is an ERR naming the reason.
func (x *tc) refusedWith(cmd, reason string) {
	var got []string
	for _, b := range x.f.bodies(cmd) {
		got = append(got, b...)
	}
	ok := false
	for _, l := range got {
		if strings.HasPrefix(l, "ERR") && strings.Contains(l, reason) {
			ok = true
		}
	}
	x.check(ok, "refused with the reason: "+cmd, cut(strings.Join(got, " / "), 160))
}

func (x *tc) doutFreq() {
	f := x.f
	// The eight pins share four complementary timer pairs, so only the software
	// PWM gives eight independent frequencies; this holds it.
	x.section("Digital Out: a frequency per channel")
	dvals := f.lines("OK vals=dout ")
	tail := cut(strings.Join(dvals[max(0, len(dvals)-3):], " / "), 200)
	x.check(anyContains(dvals, "freq=1:2000,2:1000,3:250"), "caps reports three different frequencies at once", tail)
	x.check(anyContains(dvals, "freq=1:500,2:500,3:500"), "one value with no colon still sets every selected channel", tail)
	// The widest spread, quantised hardest; truncation would report 1999 and 0 Hz.
	x.check(anyContains(dvals, "freq=1:2000,2:1,"), "2000 Hz beside 1 Hz comes back as 2000 and 1, not 1999 and 0", tail)

	// The interrupt rate is the fastest channel times the duty resolution, so the frame says it.
	var dframes []map[string]string
	for _, l := range f.lines("!dout ") {
		if strings.Contains(l+" ", "ch1=64 ") {
			dframes = append(dframes, kvMap(l))
		}
	}
	var ticks []string
	plain := true
	for _, d := range dframes {
		ticks = append(ticks, d["tick"])
		if strings.Contains(d["ch1"], "@") {
			plain = false
		}
	}
	x.check(contains(ticks, "256000"), "and the interrupt rate follows the fastest of them", fmt.Sprint(ticks))
	x.check(contains(ticks, "64000"), "then drops with them - it is not left at the peak", fmt.Sprint(ticks))
	// A duty stays a plain number, or every existing criterion on it breaks.
	x.check(plain, "the duty in a frame is still a plain number")

	// Every channel, not only the selected ones: a missing one becomes 0 Hz, which is refused.
	var short []string
	for _, l := range dvals {
		if strings.Count(kvMap(l)["freq"], ":") != 8 {
			short = append(short, l)
		}
	}
	x.check(len(short) == 0, "caps lists a frequency for all eight outputs, whatever is selected", cut(strings.Join(head(short, 1), ""), 170))
	// A bad per-channel frequency takes the whole line down rather than half-applying.
	x.refusedWith("pt.start dout freq=1:99999", "1..1000 Hz at res=8")
	x.refusedWith("pt.start dout freq=9:500", "outputs 1..8")
}

func (x *tc) doutRes() {
	f := x.f
	// DECISIONS.md 101: res= sets the bits, duty is a raw count that is not
	// rescaled, the frequency limit is 256000 >> res.
	x.section("Digital Out: duty resolution in bits")
	for _, c := range []struct {
		cmd    string
		ok     bool
		expect string
	}{
		{"pt.set dout res=10", false, "keeps 500 Hz, over 250 Hz at res=10"},
		{"pt.set dout res=10 freq=250", true, ""},
		{"pt.set dout res=10 duty=1023", true, ""},
		{"pt.set dout res=4", false, "keeps duty 1023, over 15 at res=4"},
		{"pt.set dout res=4 duty=15 freq=16000", true, ""},
		{"pt.set dout res=0", false, "1..16 bits"},
		{"pt.set dout res=17", false, "1..16 bits"},
		{"pt.set dout freq=1001", false, "1..1000 Hz at res=8"},
		{"pt.start dout duty=1:256", false, "0..255 at res=8"},
	} {
		if !c.ok {
			x.refusedWith(c.cmd, c.expect)
			continue
		}
		var got []string
		for _, b := range f.bodies(c.cmd) {
			got = append(got, b...)
		}
		accepted := false
		for _, l := range got {
			if strings.HasPrefix(l, "OK") {
				accepted = true
			}
		}
		x.check(accepted, "accepted: "+c.cmd, cut(strings.Join(got, " / "), 160))
	}
	x.check(anyContains(f.lines("OK vals=dout "), " res=10 ", "duty=1:1023,2:1023,3:1023"), "caps reports res and a 10-bit full-scale duty")
	res4 := f.lines("TEST dout_res4_duty=")
	x.check(len(res4) > 0 && strings.HasSuffix(last(res4), "=15,15,15,0,0,0,0,0"),
		"a changed res parks the unselected outputs and drives the new duty", last(res4))
	var res []string
	for _, l := range f.lines("!dout ") {
		if !strings.Contains(l, "mode=quad") {
			res = append(res, kvMap(l)["res"])
		}
	}
	x.check(contains(res, "4") && contains(res, "10"), "the frame says which res its counts are in")
}

func (x *tc) rtcClock() {
	// clk= must be read from the register, not a literal: moving the register
	// is the only way to tell the two apart.
	x.section("rtc.read names the oscillator it actually found")
	var seen []string
	for _, d := range x.f.frames("OK rtc.read ") {
		seen = append(seen, d["clk"])
	}
	for _, want := range []string{"lse", "lsi", "none"} {
		x.check(contains(seen, want), fmt.Sprintf("RTCSEL=%s comes back as clk=%s", want, want), fmt.Sprint(seen))
	}
	x.check(len(seen) > 0 && last(seen) == "lse", "and it follows the register back, not just away from the default", fmt.Sprint(seen))
}

// frameSeq checks the first len(want) frames field by field.
func (x *tc) frameSeq(frames []map[string]string, fields []string, want [][]string, name string) {
	for i, w := range want {
		if i >= len(frames) {
			break
		}
		var got []string
		for _, k := range fields {
			got = append(got, frames[i][k])
		}
		why := w[len(w)-1]
		x.check(eqStrings(got, w[:len(w)-1]), fmt.Sprintf(name, i+1, why),
			fmt.Sprintf("%s = %s, want %s", strings.Join(fields, "/"), strings.Join(got, "/"), strings.Join(w[:len(w)-1], "/")))
	}
}

func (x *tc) sdHotPlug() {
	// An insertion must be COUNTED, not only visible as a level.
	x.section("SD hot-plug")
	sdf := x.f.frames("!sd ")
	want := [][]string{
		{"1", "0", "0", "0", "a card sitting in the slot is no edge at all"},
		{"0", "1", "0", "1", "pulling it out is seen, and counted as a removal"},
		{"1", "2", "1", "1", "putting it back is counted as an insertion"},
		{"1", "4", "2", "2", "out and in between two frames loses neither edge"},
		{"1", "0", "0", "0", "starting again zeroes the counters"},
	}
	x.check(len(sdf) >= len(want), fmt.Sprintf("the transcript stages %d hot-plug frames", len(want)), fmt.Sprintf("got %d", len(sdf)))
	x.frameSeq(sdf, []string{"detected", "changes", "in", "out"}, want, "frame %d - %s")
}

func (x *tc) framesFrom(at int, pred func(string) bool) []map[string]string {
	var out []map[string]string
	if at < 0 {
		at = 0
	}
	for _, s := range x.f.sections[at:] {
		for _, l := range s.body {
			if pred(l) {
				out = append(out, kvMap(l))
			}
		}
	}
	return out
}

func (x *tc) quadIn() {
	// Four encoders on the eight digital-in pins, A/B driven directly: judged
	// is the decoding - direction, independent pairs, a directionless jump reported.
	x.section("quadrature decoding")
	at := x.f.index("pt.start din ch=1,2,3,4,5,6,7,8 mode=quad period=1000")
	if !x.check(at >= 0, "the transcript drives the encoders") {
		return
	}
	quad := x.framesFrom(at, func(l string) bool { return strings.HasPrefix(l, "!din ") && strings.Contains(l, "mode=quad") })
	// d1 is the last direction seen, so it stays through frames that moved nothing.
	want := [][]string{
		{"0", "0", "0", "0", "a session that just started has counted nothing"},
		{"4", "1", "0", "0", "four states forward is four counts up"},
		{"0", "-1", "0", "0", "and the same four back is four down, not eight more"},
		{"0", "-1", "0", "1", "both phases at once has no direction, so it is a miss"},
		// err is 2: coming back out of the jumped-into state is a jump as well.
		{"0", "-1", "4", "2", "encoder 2 moves on its own pins, encoder 1 stays put"},
	}
	x.check(len(quad) >= len(want), fmt.Sprintf("the transcript produced %d encoder frames", len(want)), fmt.Sprintf("got %d", len(quad)))
	x.frameSeq(quad, []string{"g1", "d1", "g2", "err"}, want, "encoder frame %d - %s")
	allV, noCh := true, true
	var moved []string
	for i, q := range quad {
		if !has(q, "v") {
			allV = false
		}
		for k := range q {
			if strings.HasPrefix(k, "ch") {
				noCh = false
			}
		}
		if i < 5 {
			moved = append(moved, q["moved"])
		}
	}
	x.check(allV, "an encoder frame still carries the raw pins as v=")
	// Per-channel pairs next to four counters would run past PORTTOOL_REPLY_MAX.
	x.check(noCh, "and does NOT repeat them one per channel - the line would not fit")
	// moved names the pins that changed, so a dead pin of a pair is named (decision 107).
	x.check(eqStrings(moved, []string{"0x00", "0x03", "0x03", "0x03", "0x0F"}),
		"moved collects every pin that changed since the session started", fmt.Sprint(moved))
}

func (x *tc) quadOut() {
	f := x.f
	x.section("dout quadrature bursts (decision 107)")
	var ab []string
	for _, l := range f.lines("TEST quad_ab=") {
		ab = append(ab, strings.SplitN(l, "=", 2)[1])
	}
	x.check(eqStrings(ab, []string{"01,11,10,00,01,11,10,00,00,00,00,00"}),
		"B leads A, all four pairs in step, one edge per pass, then all low after 2 cycles", fmt.Sprint(ab))
	qi := f.index("pt.start dout mode=quad cycles=2 period=1000")
	if x.check(qi >= 0, "the transcript sends a burst") {
		var got []string
		clean := true
		for _, s := range f.sections[qi:min(qi+14, len(f.sections))] {
			for _, l := range s.body {
				if strings.HasPrefix(l, "!dout ") && strings.Contains(l, "mode=quad") {
					d := kvMap(l)
					got = append(got, d["sent"]+"/"+d["done"])
					for k := range d {
						if k == "res" || strings.HasPrefix(k, "ch") {
							clean = false
						}
					}
				}
			}
		}
		x.check(eqStrings(got, []string{"1/0", "8/1"}), "a frame on the period, and one the moment the burst ends", fmt.Sprint(got))
		x.check(clean, "a burst frame carries no duty fields")
	}
	for _, cmd := range []string{"pt.start dout mode=quad cycles=0", "pt.start dout mode=quad cycles=10001"} {
		got := f.firstReply(cmd)
		x.check(strings.HasPrefix(got, "ERR ") && strings.Contains(got, "must be 1..10000"), "refused: "+cmd, got)
	}
}

// refused: the first reply to cmd is an ERR, naming why when why is given.
func (x *tc) refused(cmd, why string) {
	got := x.f.replyTo(cmd)
	x.check(strings.HasPrefix(got, "ERR ") && strings.Contains(got, why), "refused: "+cmd, got)
}

func (x *tc) echoLoop() {
	f := x.f
	x.section("echo loop")
	// From the command that starts that section onwards: din frames appear elsewhere too.
	const start = "pt.start din ch=1 period=200"
	at := f.index(start)
	x.check(at >= 0, "the echo section still starts with "+start)
	frames := x.framesFrom(at, func(l string) bool { return strings.HasPrefix(l, "!din ") })
	// These three numbers tell the loop's three states apart from the frames alone.
	want := [][]string{
		{"1", "0", "0", "first frame: nothing to have answered yet"},
		{"2", "1", "0", "the PC answered, so seq advances from what came back"},
		{"3", "2", "0", "and again"},
		{"3", "2", "1", "no answer: miss counts, seq must NOT advance"},
		{"3", "2", "2", "still none"},
		{"3", "2", "3", "a stale answer is a miss, not a closed loop"},
	}
	x.check(len(frames) >= len(want), fmt.Sprintf("the transcript produced %d echo frames", len(want)), fmt.Sprintf("got %d", len(frames)))
	x.frameSeq(frames, []string{"seq", "rx", "miss"}, want, "frame %d - %s")
	allV := true
	for _, d := range frames {
		if !has(d, "v") {
			allV = false
		}
	}
	// The counter is about the link, the bit field about the pins.
	x.check(allV, "every echo frame still carries the whole v= bit field")
	x.refused("pt.echo relay 1", "is not running")
	x.refused("pt.echo nosuch 1", "no such port")
	x.refused("pt.echo din notanumber", "is not a number")
	// A loop=link port refuses an echo offered on the control port.
	x.refused("pt.echo can 1", "takes its echo on its own link")
}

func (x *tc) rs485() {
	f := x.f
	x.section("rs485: the first loop=link port")
	fr := f.frames("!rs485 ")
	x.check(len(fr) >= 4, "rs485 produced the four frames the sequence needs", fmt.Sprintf("got %d", len(fr)))
	if len(fr) >= 4 {
		// Here the reply travelled over the pair under test, so the counter is its verdict.
		x.frameSeq(fr, []string{"seq", "rx", "miss"}, [][]string{
			{"1", "0", "0", "first send: nothing to have answered yet"},
			{"2", "1", "0", "the far end answered on the pair"},
			{"2", "1", "1", "silence on the pair: miss climbs, seq holds"},
			{"2", "1", "2", "something that is not an answer is still a miss"},
		}, "rs485 frame %d - %s")
		l := fr[len(fr)-1]
		x.check(l["junk"] == "1", "a reply that was not a number is counted, not ignored", "junk="+pyGet(l, "junk"))
		rxb := l["rxbytes"]
		if !has(l, "rxbytes") {
			rxb = "0"
		}
		x.check(atoiOr(rxb, 0) > 0, "the receiver reports the bytes it took off the pair", "rxbytes="+pyGet(l, "rxbytes"))
	}
	sentLines := f.lines("TEST rs485_sent=")
	sent := ""
	for _, l := range sentLines {
		sent = strings.SplitN(l[len("TEST rs485_sent="):], " driving=", 2)[0]
	}
	x.check(strings.HasPrefix(sent, "1|"), "the board put the sequence number on the pair, one line at a time", fmt.Sprintf("sent=%q", sent))
	x.check(strings.Contains(strings.Join(sentLines, ""), " driving=0"), "the driver is left off, so the session does not hold the pair")

	// Answering pt.echo on the control port would let the counter climb with the pair dead.
	got := f.replyTo("pt.echo rs485 2")
	x.check(strings.HasPrefix(got, "ERR ") && strings.Contains(got, "own link"), "pt.echo is refused for a link port", got)
	got = f.replyTo("pt.start rs485 baud=12345")
	x.check(strings.HasPrefix(got, "ERR "), "an unsupported baud rate is refused", got)

	// rs485.pins: while the session owns the pins, with nothing running, with PD4 wedged low.
	var pins []string
	for _, b := range f.bodies("pt.run rs485.pins") {
		if len(b) > 0 {
			pins = append(pins, okLine(b))
		}
	}
	x.check(len(pins) == 3, "pt.run rs485.pins answered all three times", fmt.Sprintf("got %d", len(pins)))
	if len(pins) == 3 {
		busy, healthy, stuck := kvMap(pins[0]), kvMap(pins[1]), kvMap(pins[2])
		// Re-muxing PD4 under a live session would break it, so the target declines.
		x.check(busy["checked"] == "0" && busy["busy"] == "1", "declined while the rs485 session holds the pins", pins[0])
		x.check(healthy["follows"] == "1" && healthy["dir_high"] == "1" && healthy["tx_high"] == "1" &&
			healthy["dir_low"] == "0" && healthy["tx_low"] == "0", "both pins read back what was written to them", pins[1])
		// A direction pin that cannot go high must be told apart from "nobody on the far end".
		x.check(stuck["follows"] == "0" && stuck["dir_high"] == "0", "a pin that will not follow is reported as such, and named", pins[2])
		x.check(stuck["tx_high"] == "1", "the pin that still works is not dragged down with it", pins[2])
	}
	// rs232 is the one port whose control-port round trip IS the link test.
	got = f.replyTo("pt.echo rs232 1")
	x.check(strings.HasPrefix(got, "OK "), "pt.echo is accepted for rs232, whose link is the control port", got)
}

func (x *tc) rs232Line() {
	f := x.f
	// The control line switches with the rs232 session (decision 104): announced
	// in the old rate's reply, applied after it.
	x.section("rs232: the control line at another rate")
	const start = "pt.start rs232 baud=19200 period=200"
	x.check(f.replyTo(start) == "OK started rs232 line=19200", "a start that changes the rate says so in its reply", f.replyTo(start))
	tests := map[string]string{}
	for _, l := range f.rawLines("TEST line_") {
		p := strings.SplitN(l, " ", 3)
		if len(p) < 3 {
			x.t.Fatalf("malformed observation %q", l)
		}
		tests[p[1]] = p[2]
	}
	x.check(tests["line_after_start"] == "uart4_baud=19200", "UART4 is at 19200 once the reply is out", fmt.Sprint(tests))
	// start, 200 ms with nothing heard, a byte arrives, 200 ms more
	i := f.index(start)
	if i < 0 || i+3 >= len(f.sections) {
		x.t.Fatalf("the transcript lacks %q and the three sections after it", start)
	}
	var held []string
	for _, l := range f.sections[i+1].body {
		if strings.HasPrefix(l, "!rs232 ") {
			held = append(held, l)
		}
	}
	var sent []map[string]string
	for _, l := range f.sections[i+3].body {
		if strings.HasPrefix(l, "!rs232 ") {
			sent = append(sent, kvMap(l))
		}
	}
	x.check(len(held) == 0, "no frame goes out before the PC is heard at the new rate", fmt.Sprint(held))
	x.check(len(sent) > 0 && sent[0]["baud"] == "19200", "the first frame after that reports the new rate", fmt.Sprint(sent))
	x.check(f.replyTo("pt.stop rs232") == "OK stopped rs232 line=115200", "stopping announces the way back", f.replyTo("pt.stop rs232"))
	x.check(tests["line_after_stop"] == "uart4_baud=115200", "and UART4 is back at 115200", fmt.Sprint(tests))
	var quiet []string
	for _, b := range f.bodies("(host) 3000 ms later") {
		for _, l := range b {
			if strings.HasPrefix(l, "!line ") {
				quiet = append(quiet, l)
			}
		}
	}
	x.check(len(quiet) > 0 && strings.Contains(quiet[0], "reverted=1") && strings.Contains(quiet[0], "baud=115200"),
		"3 s with nothing heard switches back on its own, and says so", fmt.Sprint(quiet))
	x.check(tests["line_after_silence"] == "uart4_baud=115200", "UART4 really is back", fmt.Sprint(tests))
	got := f.replyTo("pt.start rs232 baud=4800")
	x.check(strings.HasPrefix(got, "ERR ") && strings.Contains(got, "230400"), "a rate outside the list is refused, naming the list", got)
}

func (x *tc) analogGate() {
	f := x.f
	x.section("analog: the reference gate")
	// With no reference all three refuse rather than report numbers that look real.
	for _, cmd := range []string{"pt.start ain", "pt.start aout mv=500", "pt.start temp"} {
		got := f.replyTo(cmd)
		x.check(strings.HasPrefix(got, "ERR ") && strings.Contains(got, "VREFBUF"), "refused with no reference: "+cmd, got)
	}
	listed := f.bodies("pt.list")
	x.check(len(listed) > 0 && len(listed[len(listed)-1]) > 0 && listed[len(listed)-1][0] == "OK running=none",
		"nothing was left running after the three refusals", fmt.Sprint(listed))

	// The frame carries asked / quantised / microamps: either alone hides something.
	aout := f.lines("!aout ")
	x.check(len(aout) > 0, "aout produced a frame once the reference was up")
	if len(aout) > 0 {
		ch2 := kvMap(last(aout))["ch2"]
		// ch2: a value off the 2500/4095 grid, so quantising must change it.
		parts := strings.Split(ch2, "/")
		x.check(len(parts) == 3, "aout reports asked/quantised/microamps", ch2)
		if len(parts) == 3 {
			x.check(parts[0] == "1234", "the asked value is what was asked", parts[0])
			x.check(parts[1] != parts[0], "the quantised value differs from the request, as a 12-bit DAC must", ch2)
			x.check(atoiOr(parts[2], 0) > 0, "a current is computed for the terminal", parts[2])
		}
	}
	ain := f.lines("!ain ")
	x.check(len(ain) > 0, "ain produced a frame")
	if len(ain) > 0 {
		d := kvMap(last(ain))
		x.check(d["ok"] == "1", "ain says its reference is trustworthy", pyGet(d, "ok"))
		x.check(strings.Contains(d["ch1"], "/"), "ain reports raw/millivolts", pyGet(d, "ch1"))
	}
	for _, cmd := range []string{"pt.start aout mv=9999", "pt.start aout mv=1:4000", "pt.start ain ch=3"} {
		x.refused(cmd, "")
	}
	dac := ""
	for _, l := range f.lines("TEST dac_after_stop=") {
		dac = strings.SplitN(l, "=", 2)[1]
	}
	x.check(dac == "0,0", "pt.stop all put both analog outputs back to zero", "dac="+dac)
}

func (x *tc) levels() {
	x.section("per-channel levels")
	afterSet := x.valsOf(3, "relay")
	x.check(afterSet["on"] == "1:1,2:0,3:1", "relay took a level per channel", pyGet(afterSet, "on"))
	afterOne := x.valsOf(4, "relay")
	x.check(afterOne["on"] == "1:1,2:1,3:1", "pt.set changed only the channel it named", pyGet(afterOne, "on"))
	// The four malformed on= lines are refused and leave the state as it was.
	for _, cmd := range []string{"pt.start relay on=1:9", "pt.start relay on=7:1", "pt.start relay on=1:1,1:0", "pt.start relay on=1:"} {
		x.refused(cmd, "")
	}
	unchanged := x.valsOf(5, "relay")
	x.check(unchanged["on"] == afterOne["on"], "four refused on= lines left the levels exactly as they were",
		pyGet(afterOne, "on")+" -> "+pyGet(unchanged, "on"))
}

func (x *tc) relayCycle() {
	f := x.f
	x.section("relay mode=cycle: per-relay holds and counts")
	const start = "pt.start relay ch=1,2 mode=cycle close=1:1000,2:2000 open=1000 count=1:2,2:1"
	i0 := f.index(start)
	if i0 < 0 {
		x.t.Fatalf("the transcript lacks %q", start)
	}
	x.check(eqStrings(head(f.sections[i0].body, 1), []string{"OK started relay"}), "relay cycle started", fmt.Sprint(head(f.sections[i0].body, 1)))
	var got []string
	allCycle := true
	j := i0 + 1
	for ; j < len(f.sections) && f.sections[j].cmd != "pt.caps"; j++ {
		for _, l := range f.sections[j].body {
			if strings.HasPrefix(l, "!relay ") {
				d := kvMap(l)
				got = append(got, strings.Join([]string{d["ch1"], d["n1"], d["ch2"], d["n2"], d["done"]}, "/"))
				if d["mode"] != "cycle" {
					allCycle = false
				}
			}
		}
	}
	if j >= len(f.sections) {
		x.t.Fatal("no pt.caps after the relay cycle")
	}
	want := []string{
		"0/0/0/0/0", // session start
		"1/0/1/0/0", // both close after 1 s open
		"0/1/1/0/0", // 1 releases after 1 s closed
		"1/1/0/1/0", // 1 closes, 2 releases after 2 s: 2 reached 1
		"0/2/0/1/1", // 1 reached 2: done, and no frame after it
	}
	x.check(eqStrings(got, want), "one frame per edge, counts per relay, done=1 when all reached", fmt.Sprint(got))
	x.check(allCycle, "every cycle frame says mode=cycle")
	en := f.lines("TEST relay_cycle_energised=")
	x.check(eqStrings(en, []string{"TEST relay_cycle_energised=0"}), "every relay released once its count is reached", fmt.Sprint(en))
	vals := map[string]string{}
	for _, l := range f.sections[j].body {
		if strings.HasPrefix(l, "OK vals=relay ") {
			vals = kvMap(l)
		}
	}
	x.check(vals["ch"] == "1,2" && !has(vals, "close") && !has(vals, "open") && !has(vals, "count"),
		"caps leaves close / open / count out while the relays differ", fmt.Sprint(vals))
	rf := x.valsOf(0, "relay")
	x.check(rf["close"] == "2000" && rf["open"] == "2000" && rf["count"] == "10" && has(rf, "close") && has(rf, "open") && has(rf, "count"),
		"and reports one value each while they agree", fmt.Sprint(rf))
	var six []string
	for _, s := range f.sections {
		if strings.HasPrefix(s.cmd, "pt.start relay ch=1,2,3,4,5,6 mode=cycle close=1:1000001") {
			six = append(six, s.cmd)
		}
	}
	x.check(len(six) == 1 && len(six[0]) > 192 && len(six[0]) <= 256 && f.replyTo(six[0]) == "OK started relay",
		"six different 7-digit values in one line over 192 bytes are taken", fmt.Sprint(six))
	k := 0
	if len(six) > 0 {
		k = f.index(six[0])
	}
	sixVals := map[string]string{}
	if k+1 < len(f.sections) {
		for _, l := range f.sections[k+1].body {
			if strings.HasPrefix(l, "OK vals=relay ") {
				sixVals = kvMap(l)
			}
		}
	}
	x.check(sixVals["mode"] == "cycle" && !has(sixVals, "close"), "and caps for them stays short", fmt.Sprint(sixVals))
	for _, cmd := range []string{"pt.start relay mode=cycle close=999", "pt.start relay mode=cycle open=1:500",
		"pt.start relay mode=cycle count=0", "pt.start relay mode=cycle count=1000001", "pt.start relay mode=square"} {
		x.refused(cmd, "")
	}
	// A line refused on its second parameter must not have applied its first.
	before, after := x.valsOf(5, "din"), x.valsOf(6, "din")
	x.check(before["ch"] == after["ch"], "a valid ch= in a line refused for its period= did not take effect",
		pyGet(before, "ch")+" -> "+pyGet(after, "ch"))
}

func (x *tc) refusals() {
	x.section("refusals")
	x.refused("pt.start din ch=1,9", "start refused")
	x.refused("pt.start din period=abc", "start refused")
	x.refused("pt.start dinn", "no such port")
	x.refused("pt.set rtc ch=1", "no such port")
	x.refused("pt.foo", "unknown command")
	x.check(x.valsOf(0, "din")["ch"] != "1,9", "a rejected ch= never reached the session")
}

func noVerdict(l string) bool { return !strings.Contains(l, "PASS") && !strings.Contains(l, "FAIL") }

func (x *tc) oneShots() {
	f := x.f
	x.section("pt.run: one-shot actions")
	probes := f.bodies("pt.run sdram.probe")
	x.check(len(probes) == 2, "pt.run sdram.probe answered both times it was asked", fmt.Sprintf("%d section(s)", len(probes)))
	cat := f.bodies("pt.run")
	allRun := len(cat) > 0
	if allRun {
		for _, l := range cat[0] {
			if !strings.HasPrefix(l, "OK run=") {
				allRun = false
			}
		}
	}
	x.check(allRun, "bare pt.run lists the targets, one OK per target")
	if len(probes) == 2 {
		for i, label := range []string{"a good board", "an FMC that never came up"} {
			wantReady := []string{"1", "0"}[i]
			// TEST lines are the harness observing the stub, not bytes on the wire.
			var body []string
			for _, l := range probes[i] {
				if !strings.HasPrefix(l, "TEST ") {
					body = append(body, l)
				}
			}
			oks := okLines(body)
			x.check(len(oks) == 1, label+": exactly one OK line", fmt.Sprint(body))
			if len(oks) == 0 {
				continue
			}
			// The checks print prose as they go; the OK line still arrives whole and last.
			x.check(strings.HasPrefix(body[0], "SDRAM_TEST:"), label+": the check's own prose comes first", body[0])
			x.check(last(body) == oks[0], label+": the OK line is last, not split by the prose", fmt.Sprint(body))
			d := kvMap(oks[0])
			x.check(d["ready"] == wantReady, label+": ready="+wantReady, pyGet(d, "ready"))
			x.check(d["size"] == strconv.Itoa(0x04000000), label+": reports the window size in bytes", pyGet(d, "size"))
			// No verdict anywhere in the reply - DECISIONS.md 22.
			x.check(noVerdict(oks[0]), label+": the reply carries measurements, not a verdict", oks[0])
		}
	}

	// sdram.crc: the default window, one the plan named, one past the end of the array.
	var crcs []string
	for _, s := range f.sections {
		if strings.HasPrefix(s.cmd, "pt.run sdram.crc") && len(s.body) > 0 {
			crcs = append(crcs, okLine(s.body))
		}
	}
	x.check(len(crcs) == 3, "pt.run sdram.crc answered all three times", fmt.Sprintf("got %d", len(crcs)))
	if len(crcs) == 3 {
		whole, window, past := kvMap(crcs[0]), kvMap(crcs[1]), kvMap(crcs[2])
		x.check(whole["offset"] == "0" && whole["bytes"] == "67108864", "no window given means the whole array", crcs[0])
		x.check(window["offset"] == "1024" && window["bytes"] == "4096", "the plan's window is what gets summed, and is said back", crcs[1])
		// An offset past the mapping wraps on this part: a CRC there would look like an answer.
		x.check(past["offset"] == "0", "an offset past the end of the array is pulled back inside", crcs[2])
		x.check(past["bytes"] == "67108864", "a length past the end is clamped, not wrapped", crcs[2])
		x.check(window["crc"] != whole["crc"], "a different window gives a different CRC", crcs[1])
	}

	// reset.cause: PIN is the reset line, IWDG the board hanging and its watchdog rescuing it.
	var causes []string
	for _, b := range f.bodies("pt.run reset.cause") {
		if len(b) > 0 {
			causes = append(causes, okLine(b))
		}
	}
	x.check(len(causes) == 2, "pt.run reset.cause answered both times", fmt.Sprintf("got %d", len(causes)))
	if len(causes) == 2 {
		pin, iwdg := kvMap(causes[0]), kvMap(causes[1])
		x.check(pin["cause"] == "PIN", "reports the decoded cause", causes[0])
		x.check(iwdg["cause"] == "IWDG", "and follows the register rather than answering from memory", causes[1])
		// The raw word too: the name stops at the first flag, and two can be set.
		x.check(pin["rsr"] == "0x04000000" && iwdg["rsr"] == "0x20000000", "the raw RSR goes out beside the name",
			pyGet(pin, "rsr")+" / "+pyGet(iwdg, "rsr"))
	}

	// The XTR111 fault flags, raw pin level: which level means fault is not settled in Hardware/.
	aout := f.frames("!aout ")
	x.check(len(aout) > 0, "aout produced frames")
	if len(aout) > 0 {
		both := true
		var staged []map[string]string
		for _, d := range aout {
			if !has(d, "ef1") || !has(d, "ef2") {
				both = false
			}
			if d["ef1"] == "1" {
				staged = append(staged, d)
			}
		}
		x.check(both, "every aout frame carries both fault flags", fmt.Sprint(aout[0]))
		// One high, one low at once: a firmware reading one pin twice would report them equal.
		x.check(len(staged) > 0, "a staged fault on channel 1 reached the frame")
		if len(staged) > 0 {
			x.check(staged[len(staged)-1]["ef2"] == "0", "and the other channel was not dragged with it", fmt.Sprint(staged[len(staged)-1]))
		}
	}
	got := f.replyTo("pt.run nosuch")
	x.check(strings.HasPrefix(got, "ERR "), "an unknown run target is refused", got)

	// The RTC is not up in this image, so the target brings it up itself.
	rtc := f.bodies("pt.run rtc.read")
	x.check(len(rtc) >= 2, "pt.run rtc.read answered at least twice", fmt.Sprintf("%d section(s)", len(rtc)))
	inits := f.lines("TEST rtc_init_count=")
	x.check(len(inits) > 0 && strings.HasSuffix(inits[0], "=1"), "rtc.read brought the RTC up itself, exactly once", strings.Join(head(inits, 1), ""))
	if len(rtc) >= 2 {
		for i, label := range []string{"a set calendar", "a calendar nobody set"} {
			wantInit := []string{"1", "0"}[i]
			oks := okLines(rtc[i])
			x.check(len(oks) == 1, label+": one OK line", fmt.Sprint(rtc[i]))
			if len(oks) == 0 {
				continue
			}
			d := kvMap(oks[0])
			x.check(d["init"] == wantInit, label+": init="+wantInit, pyGet(d, "init"))
			// Which oscillator is checked where the register is moved; here only that it is named.
			x.check(contains([]string{"lse", "lsi", "hse", "none"}, d["clk"]) && has(d, "clk"), label+": names the clock source", pyGet(d, "clk"))
			x.check(has(d, "time") && has(d, "date"), label+": reports a date and a time", oks[0])
		}
	}

	led := f.bodies("pt.run led.blink")
	x.check(len(led) > 0, "pt.run led.blink answered")
	if len(led) > 0 {
		oks := okLines(led[0])
		x.check(len(oks) == 1, "led.blink: one OK line", fmt.Sprint(led[0]))
		if len(oks) > 0 {
			d := kvMap(oks[0])
			// There is no readback on this pin, so the reply must not claim one.
			x.check(d["observed"] == "unknown", "led.blink does not claim the lamp was seen", oks[0])
			x.check(d["pin"] == "PE2", "led.blink names the pin", pyGet(d, "pin"))
		}
	}
	if drives := f.lines("TEST led_configured="); len(drives) > 0 {
		d := kvMap(drives[0])
		x.check(d["led_configured"] == "1", "led.blink configured PE2 itself (MX_GPIO_Init leaves it alone)", drives[0])
		// Six pulses is twelve writes, half high; counters zeroed just before the run.
		x.check(d["led_writes"] == "12" && d["led_high"] == "6", "led.blink drove the pin as many times as it reported", drives[0])
	}
}

func (x *tc) limits() {
	x.section("limits: what each parameter accepts")
	for _, port := range []string{"din", "dout", "relay", "ain", "aout", "temp", "rs232", "rs485", "can"} {
		lim := x.limitsOf(0, port)
		x.check(len(lim) > 0, port+" states its limits")
		// A parameter with no stated limit is one the panel cannot validate.
		var missing []string
		for _, p := range strings.Split(x.shapeOf(0, port)["params"], ",") {
			if p != "" && !has(lim, p) {
				missing = append(missing, p)
			}
		}
		x.check(len(missing) == 0, port+" states a limit for every parameter it accepts", fmt.Sprintf("missing %v", missing))
	}
	dl := x.limitsOf(0, "dout")
	x.check(dl["duty"] == "0..65535", "dout duty limit", pyGet(dl, "duty"))
	x.check(dl["res"] == "1..16", "dout res limit", pyGet(dl, "res"))
	x.check(dl["mode"] == "hold|blink|quad", "dout mode enum", pyGet(dl, "mode"))
	x.check(strings.HasSuffix(dl["period"], ".."), "an open-ended limit is written lo..", pyGet(dl, "period"))
	// The numbers must be the firmware's own: freq is the widest over every res (256000 >> 1).
	x.check(dl["freq"] == "1..128000", "dout freq limit is the widest over res", pyGet(dl, "freq"))
	al := x.limitsOf(0, "aout")
	refusal := x.f.replyTo("pt.start aout mv=9999")
	if strings.HasPrefix(refusal, "ERR ") && strings.Contains(al["mv"], "..") {
		_, hi, _ := strings.Cut(al["mv"], "..")
		x.check(strings.Contains(refusal, hi), "the stated mv limit matches what the firmware refuses with", "limit "+al["mv"]+" vs "+refusal)
	}
	cl := x.limitsOf(0, "can")
	x.check(cl["baud"] == "125000|250000|500000|1000000", "can's rate list is generated from the driver's table", pyGet(cl, "baud"))
}

func firstOK(body []string) map[string]string {
	if oks := okLines(body); len(oks) > 0 {
		return kvMap(oks[0])
	}
	return map[string]string{}
}

func (x *tc) sd() {
	f := x.f
	x.section("sd: the two one-shots")
	probes := f.bodies("pt.run sd.probe")
	x.check(len(probes) == 2, "pt.run sd.probe answered both times", fmt.Sprintf("%d section(s)", len(probes)))
	if len(probes) == 2 {
		good, gone := firstOK(probes[0]), firstOK(probes[1])
		x.check(good["detected"] == "1" && good["ready"] == "1", "a card that is there reads detected=1 ready=1", fmt.Sprint(good))
		mib := good["mib"]
		if !has(good, "mib") {
			mib = "0"
		}
		x.check(mib != "0", "a ready card reports a capacity", pyGet(good, "mib"))
		// An unidentifiable card in the slot is a different fault from an empty slot.
		x.check(gone["detected"] == "0" && gone["ready"] == "0", "an absent card reads detected=0 ready=0", fmt.Sprint(gone))
	}
	rounds := f.bodies("pt.run sd.integrity")
	x.check(len(rounds) == 3, "pt.run sd.integrity answered all three times", fmt.Sprintf("%d section(s)", len(rounds)))
	if len(rounds) == 3 {
		okr, bad, none := firstOK(rounds[0]), firstOK(rounds[1]), firstOK(rounds[2])
		x.check(okr["identical"] == "1" && okr["write_crc"] == okr["read_crc"], "a good round reports identical=1 and matching CRCs", fmt.Sprint(okr))
		// It mounted, wrote, read - and the bytes differ: identical=0 alone would lose that.
		x.check(bad["identical"] == "0" && bad["mounted"] == "1" && bad["wrote"] == "1" && bad["read_back"] == "1",
			"a corrupt round still says how far it got", fmt.Sprint(bad))
		x.check(bad["write_crc"] != bad["read_crc"], "a corrupt round reports both CRCs so the difference is visible", fmt.Sprint(bad))
		x.check(none["mounted"] == "0" && none["fresult"] == "-1", "with no card it never mounted, and says so", fmt.Sprint(none))
		for _, b := range rounds {
			l := okLines(b)
			if len(l) == 0 {
				x.t.Fatal("an sd.integrity round has no OK line")
			}
			x.check(noVerdict(l[0]), "the sd.integrity reply carries measurements, not a verdict", l[0])
		}
	}
}

func (x *tc) can() {
	f := x.f
	x.section("can: the session")
	shape := x.shapeOf(0, "can")
	x.check(shape["kind"] == "session", "can is a session", pyGet(shape, "kind"))
	x.check(shape["loop"] == "link", "can's loop travels over the CAN pair, so its counter is a verdict", pyGet(shape, "loop"))
	x.check(!has(shape, "targets"), "the deep CAN entries no longer ride on the session row", pyGet(shape, "targets"))

	// Clock source and bit timing ride caps as read-only facts (DECISIONS.md 87);
	// bps is what that clock and timing really produce.
	var info []map[string]string
	for _, l := range x.first[1:] {
		if strings.HasPrefix(l, "OK info=can ") {
			info = append(info, kvMap(l))
		}
	}
	x.check(len(info) == 1, "caps carries one info= line for can", fmt.Sprint(info))
	if len(info) > 0 {
		ci := info[0]
		for _, fld := range []string{"clk", "clk_hz", "presc", "seg1", "seg2", "sjw", "tq", "bps"} {
			x.check(has(ci, fld), "can info reports "+fld, fmt.Sprint(ci))
		}
		num := func(k string) (int, bool) {
			if !has(ci, k) {
				return 0, false
			}
			n, err := strconv.Atoi(ci[k])
			return n, err == nil
		}
		seg1, ok1 := num("seg1")
		seg2, ok2 := num("seg2")
		clk, ok3 := num("clk_hz")
		presc, ok4 := num("presc")
		_, okT := ci["tq"]
		_, okB := ci["bps"]
		tq := 1 + seg1 + seg2
		if ok1 && ok2 && ok3 && ok4 && okT && okB && presc*tq != 0 {
			bps := clk / (presc * tq)
			x.check(ci["tq"] == strconv.Itoa(tq) && ci["bps"] == strconv.Itoa(bps) && ci["bps"] == x.valsLines["can"]["baud"] && has(x.valsLines["can"], "baud"),
				"can info's bit rate follows from its clock and timing, and is baud=", fmt.Sprint(ci))
		} else {
			x.check(false, "can info is numeric", fmt.Sprint(ci))
		}
	}

	// The three KNX diagnostics as pt.run targets (DECISIONS.md 87).
	edges, hist, pull := f.frames("OK knx.edges "), f.frames("OK knx.hist "), f.frames("OK knx.pullup ")
	hasAll := func(d map[string]string, ks ...string) bool {
		for _, k := range ks {
			if !has(d, k) {
				return false
			}
		}
		return true
	}
	x.check(len(edges) > 0 && edges[0]["shots"] == "2" && edges[0]["bus"] == "ok" &&
		hasAll(edges[0], "moved", "edges", "dropped", "first_us", "width_us"),
		"knx.edges reports its shots and edge timing", fmt.Sprint(edges))
	x.check(len(hist) == 2 && hist[0]["busy"] == "0" && len(strings.Split(hist[0]["w_hist"], ",")) == 8 &&
		len(strings.Split(hist[0]["d_hist"], ",")) == 8, "knx.hist reports two eight-cell histograms", fmt.Sprint(hist))
	x.check(len(hist) == 2 && hist[1]["busy"] == "1" && !has(hist[1], "tx"),
		"knx.hist answers busy=1 and measures nothing while the session runs", fmt.Sprint(hist))
	x.check(len(pull) > 0 && hasAll(pull[0], "pu_miss", "np_miss", "pu_w_avg", "np_w_avg"), "knx.pullup reports both legs", fmt.Sprint(pull))
	x.check(x.shapeOf(0, "knx")["runs"] == "knx.edges,knx.hist,knx.pullup", "the knx session row carries the three diagnostics",
		pyGet(x.shapeOf(0, "knx"), "runs"))

	frames := f.lines("!can ")
	x.check(len(frames) > 0, "can produced frames")
	if len(frames) > 0 {
		d := kvMap(frames[0])
		for _, fld := range []string{"bps", "mode", "alive", "tx", "rx_frames", "junk", "replied", "tec", "rec"} {
			x.check(has(d, fld), "the can frame reports "+fld, frames[0])
		}
		x.check(d["bps"] == "250000", "the frame reports the rate that was asked for", pyGet(d, "bps"))
		x.check(d["mode"] == "loopback", "the frame names the mode", pyGet(d, "mode"))
	}
	if o := f.lines("TEST can_open="); len(o) > 0 {
		d := kvMap(o[0])
		// 1 = 250 kbit/s in the rate table, 3 = FDCAN_MODE_INTERNAL_LOOPBACK.
		x.check(d["rate"] == "1" && d["mode"] == "3", "baud= and mode= reached the controller", o[0])
		x.check(d["sent"] == "2", "one frame per tick went out in loopback", o[0])
	}
	if l := f.lines("TEST can_listen_sent="); len(l) > 0 {
		// A "passive" mode that injected frames would be worse than none.
		x.check(strings.HasSuffix(l[0], "=0"), "listen mode transmitted nothing at all", l[0])
	}
	// mode=echo never originates and answers on the same id and length with the payload changed.
	if l := f.lines("TEST can_echo_idle_sent="); len(l) > 0 {
		x.check(strings.HasSuffix(l[0], "=0"), "echo mode originates nothing on its own", l[0])
	}
	if l := f.lines("TEST can_echo_sent="); len(l) > 0 {
		d := kvMap(l[0])
		x.check(d["can_echo_sent"] == "1", "one frame arrived and exactly one answer went out", l[0])
		x.check(d["id"] == "123" && d["len"] == "4", "the answer went back on the same identifier and length", l[0])
		x.check(d["payload"] == "42", "the answer carries the payload incremented, not the payload", l[0])
	}
	var echo, bus []map[string]string
	for _, l := range frames {
		if strings.Contains(l, " mode=echo ") {
			echo = append(echo, kvMap(l))
		}
		if strings.Contains(l, " mode=bus ") {
			bus = append(bus, kvMap(l))
		}
	}
	x.check(len(echo) > 0, "echo mode produced frames")
	if len(echo) > 0 {
		x.check(echo[len(echo)-1]["replied"] == "1", "the frame reports replied=, which is the verdict in echo mode", fmt.Sprint(echo[len(echo)-1]))
	}
	// mode=bus (decision 111): only what tx= asks for goes out, each frame both
	// ways is an event, a burst beyond eight a pass is counted.
	idle := f.lines("TEST can_bus_idle_sent=")
	x.check(len(idle) > 0 && strings.HasSuffix(idle[0], "=0"), "bus mode originates nothing on its own", fmt.Sprint(head(idle, 1)))
	bs := f.frames("TEST can_bus_sent=")
	x.check(len(bs) > 0 && bs[0]["can_bus_sent"] == "1" && bs[0]["id"] == "123" && bs[0]["len"] == "3" && bs[0]["last"] == "170",
		"tx= sends one frame with that identifier and those bytes", fmt.Sprint(head(f.lines("TEST can_bus_sent="), 1)))
	after := f.lines("TEST can_bus_after_refusals=")
	x.check(len(after) > 0 && strings.HasSuffix(after[0], "=2"), "a refused tx= sends nothing", fmt.Sprint(head(after, 1)))
	txe := f.frames("!can.tx ")
	x.check(len(txe) == 2 && txe[0]["id"] == "0x123" && txe[0]["dlc"] == "3" && txe[0]["data"] == "1122AA" && txe[0]["ok"] == "1" &&
		txe[1]["id"] == "0x7FF" && txe[1]["data"] == "-", "each sent frame is reported as !can.tx", fmt.Sprint(txe))
	rxe := f.frames("!can.rx ")
	rxOK := len(rxe) == 8
	for _, e := range rxe {
		if e["id"] != "0x456" || e["data"] != "BEEF" {
			rxOK = false
		}
	}
	x.check(rxOK, "received frames are reported as !can.rx, eight a pass", fmt.Sprint(head(f.lines("!can.rx "), 2)))
	x.check(len(bus) > 0 && bus[len(bus)-1]["lost"] == "2" && bus[len(bus)-1]["bus"] == "active",
		"the bus-mode frame counts what it did not report and names the error state", fmt.Sprint(bus))
	for _, c := range [][2]string{
		{"pt.set can baud=500000", "when the controller opens"},
		{"pt.set can mode=listen", "when the controller opens"},
		{"pt.start can baud=999", "must be one of"},
		{"pt.start can mode=sideways", "must be normal, listen, loopback, extloop, echo or bus"},
		{"pt.set can tx=800:11", "id hex up to 7FF"},
		{"pt.set can tx=123:112", "id hex up to 7FF"},
		{"pt.set can tx=123:112233445566778899", "id hex up to 7FF"},
		{"pt.set can tx=123:11 period=abc", "is not a number"},
		{"pt.set can tx=123:11", "tx= is for mode=bus only"},
		{"pt.start relay close=100", "must be at least 1000 ms"},
	} {
		x.refused(c[0], c[1])
	}
	// An unclocked controller looks exactly like a dead bus; refusing says which.
	got := f.replyTo("pt.start can")
	x.check(strings.HasPrefix(got, "ERR ") && strings.Contains(got, "not clocked"), "an unclocked FDCAN is refused, not reported as a silent bus", got)
}

func (x *tc) sessionRow(port string) map[string]string {
	for _, r := range x.sessions {
		if r.d["port"] == port {
			return r.d
		}
	}
	return nil
}

func (x *tc) usb() {
	f := x.f
	x.section("usb: the CDC session")
	shape := x.sessionRow("usb")
	x.check(shape != nil, "usb is a session")
	if shape != nil {
		x.check(shape["loop"] == "link", "usb closes its loop over the pipe under test", pyGet(shape, "loop"))
	}
	if d, l := f.testLine("TEST usb_inits="); d != nil {
		// MX_USB_DEVICE_Init enumerates; per start the PC's COM port would vanish mid-test.
		x.check(d["usb_inits"] == "1", "the USB stack was brought up once", l)
	}
	if d, l := f.testLine("TEST usb_info_sent="); d != nil {
		x.check(d["inits"] == "1", "still once after four more starts", l)
		x.check(d["usb_info_sent"] == "0", "info mode put nothing on the wire", l)
	}
	if d, l := f.testLine("TEST usb_sink_sent="); d != nil {
		x.check(d["usb_sink_sent"] == "0", "sink mode transmits nothing - it only counts", l)
	}
	if d, l := f.testLine("TEST usb_source_pushed="); d != nil {
		x.check(d["usb_source_pushed"] == "1", "source mode pushes without being asked", l)
	}
	frames := f.frames("!usb ")
	x.check(len(frames) > 0, "usb produced frames")
	if len(frames) > 0 {
		for _, fld := range []string{"state", "enum", "mode", "rx_bytes", "tx_bytes", "kbps", "busy", "drop"} {
			x.check(has(frames[0], fld), "the usb frame reports "+fld, fmt.Sprint(frames[0]))
		}
	}
	mode := func(m string) []map[string]string {
		var out []map[string]string
		for _, d := range frames {
			if d["mode"] == m {
				out = append(out, d)
			}
		}
		return out
	}
	if e := mode("echo"); len(e) >= 8 {
		x.check(e[1]["seq"] == "2" && e[1]["miss"] == "0", "a PC that answers closes the loop", fmt.Sprint(e[1]))
		x.check(e[3]["seq"] == "3" && e[3]["miss"] == "1", "a stale reply counts as a miss, not as a closed loop", fmt.Sprint(e[3]))
		// A host that stopped reading is not a dead cable; busy= is what says which.
		x.check(e[4]["busy"] == "1" && e[4]["seq"] == "3", "a host that stopped reading shows as busy, with seq held", fmt.Sprint(e[4]))
		x.check(e[6]["state"] == "default" && e[6]["enum"] == "1", "losing enumeration is reported as a state, not as silence", fmt.Sprint(e[6]))
		x.check(e[7]["enum"] == "2", "and re-enumerating counts again - a flapping host is visible", fmt.Sprint(e[7]))
	}
	x.sinkChecks(mode("sink"))
	if info := mode("info"); len(info) >= 3 {
		noMiss, noTx := true, true
		for _, d := range info {
			if d["miss"] != "0" {
				noMiss = false
			}
			if d["tx_bytes"] != "0" {
				noTx = false
			}
		}
		// A mode that sends nothing gave nobody anything to answer.
		x.check(noMiss, "info mode never counts a miss - it asked nothing of anybody")
		x.check(noTx, "and moves no bytes at all", fmt.Sprint(info[0]))
	}
	// mirror: every byte back unchanged, an overflowing burst counted (PORTTOOL-FLOW.md B.9).
	d, l := f.testLine("TEST usb_mirror_same=")
	x.check(d != nil && d["usb_mirror_same"] == "1", "usb mirror sends back every byte unchanged", l)
	m := mode("mirror")
	x.check(len(m) >= 2, "usb mirror produced frames", strconv.Itoa(len(m)))
	if len(m) >= 2 {
		x.check(m[0]["drop"] == "0" && m[0]["miss"] == "0", "a burst the ring holds drops nothing and counts no miss", fmt.Sprint(m[0]))
		x.check(m[1]["drop"] == "952", "a burst past the 2048-byte ring is counted in drop=", fmt.Sprint(m[1]))
	}
	x.check(strings.HasPrefix(f.replyTo("pt.echo usb 1"), "ERR "),
		"pt.echo is refused for usb - its reply rides the pipe, not the control port", f.replyTo("pt.echo usb 1"))
	x.refused("pt.start usb mode=sideways", "")
	x.refused("pt.start usb period=abc", "")
}

// sinkChecks: sink counts what arrives and does not parse it.
func (x *tc) sinkChecks(sink []map[string]string) {
	if len(sink) == 0 {
		return
	}
	rx := sink[0]["rx_bytes"]
	if !has(sink[0], "rx_bytes") {
		rx = "0"
	}
	x.check(atoiOr(rx, 0) > 0, "sink counted what arrived", fmt.Sprint(sink[0]))
	x.check(sink[0]["seq"] == "1" && sink[0]["rx"] == "0", "sink does not let payload bytes move the echo counter", fmt.Sprint(sink[0]))
}

func (x *tc) eth() {
	f := x.f
	x.section("eth: the TCP session")
	shape := x.sessionRow("eth")
	x.check(shape != nil, "eth is a session, not a one-shot")
	if shape != nil {
		// DECISIONS.md 28: one row for one RJ45, the PHY probe hanging off the session.
		x.check(shape["runs"] == "eth.link", "the eth session carries eth.link as a run target", pyGet(shape, "runs"))
		x.check(shape["loop"] == "link", "eth closes its loop over the link under test", pyGet(shape, "loop"))
	}
	n := 0
	for _, r := range x.all() {
		if r.d["port"] == "eth" {
			n++
		}
	}
	x.check(n == 1, "eth appears exactly once in caps - one card, not two", fmt.Sprintf("%d rows", n))
	if d, l := f.testLine("TEST eth_inits="); d != nil {
		// MX_LWIP_Init registers a netif and starts DHCP; per start it would register a second.
		x.check(d["eth_inits"] == "1", "lwIP was brought up exactly once", l)
		x.check(d["listening"] == "1" && d["port"] == "5000", "the session listened on the port it was asked for", l)
	}
	if d, l := f.testLine("TEST eth_static_dhcp="); d != nil {
		x.check(d["eth_static_dhcp"] == "0", "a static address stops the DHCP client, so no lease overwrites it", l)
		x.check(d["inits"] == "1", "still one lwIP init after four more starts", l)
	}
	if d, l := f.testLine("TEST eth_second_peer="); d != nil {
		// A second peer's bytes would mix two sessions into one rate.
		x.check(d["eth_second_peer"] == "0", "a second peer is refused", l)
	}
	if d, l := f.testLine("TEST eth_rebound="); d != nil {
		x.check(d["eth_rebound"] == "5001", "changing port= moved the listener, it did not just record it", l)
	}
	if d, l := f.testLine("TEST eth_source_pushed="); d != nil {
		x.check(d["eth_source_pushed"] == "1", "source mode pushes without being asked", l)
	}
	lines := f.lines("!eth ")
	x.check(len(lines) > 0, "eth produced frames")
	if len(lines) > 0 {
		d := kvMap(lines[0])
		for _, fld := range []string{"ip", "link", "conn", "mode", "port", "rx_bytes", "tx_bytes", "kbps", "drop"} {
			x.check(has(d, fld), "the eth frame reports "+fld, lines[0])
		}
		// A session with nobody connected must say so rather than look idle.
		x.check(d["conn"] == "0", "the first frame says no peer has connected yet", lines[0])
	}
	mode := func(m string) []map[string]string {
		var out []map[string]string
		for _, l := range lines {
			if d := kvMap(l); d["mode"] == m {
				out = append(out, d)
			}
		}
		return out
	}
	if e := mode("echo"); len(e) >= 6 {
		x.check(e[1]["seq"] == "1" && e[1]["miss"] == "1", "with no peer the counter stalls and counts a miss", fmt.Sprint(e[1]))
		x.check(e[3]["seq"] == "2" && e[3]["miss"] == "0", "a peer that answers closes the loop", fmt.Sprint(e[3]))
		x.check(e[5]["seq"] == "3" && e[5]["miss"] == "1", "a stale reply counts as a miss, not as a closed loop", fmt.Sprint(e[5]))
	}
	x.sinkChecks(mode("sink"))
	d, l := f.testLine("TEST eth_mirror_same=")
	x.check(d != nil && d["eth_mirror_same"] == "1", "eth mirror sends back every byte unchanged, across two send buffers", l)
	m := mode("mirror")
	clean := len(m) > 0
	for _, d := range m {
		if d["drop"] != "0" || d["miss"] != "0" {
			clean = false
		}
	}
	x.check(clean, "eth mirror drops nothing and counts no miss", fmt.Sprint(head(lines, 2)))
	x.check(strings.HasPrefix(f.replyTo("pt.echo eth 1"), "ERR "),
		"pt.echo is refused for eth - its reply rides the link, not the control port", f.replyTo("pt.echo eth 1"))

	// SD file transfer over eth mode=file (PORTTOOL-FLOW.md B.11).
	hello := fmt.Sprintf("0x%08X", crc32.ChecksumIEEE([]byte("0123456789")))
	_, l = f.testLine("TEST sdf_ack=")
	x.check(strings.TrimSpace(l) == "TEST sdf_ack=A10", "the board acknowledges the written total on the data line", l)
	put := f.replyTo("pt.run sd.put name=hello.txt size=10")
	x.check(strings.HasPrefix(put, "OK sd.put ok=1"), "sd.put arms a write", put)
	var good, slow []map[string]string
	for _, d := range f.frames("!sd.put ") {
		switch d["name"] {
		case "hello.txt":
			good = append(good, d)
		case "slow.bin":
			slow = append(slow, d)
		}
	}
	x.check(len(good) > 0 && good[0]["ok"] == "1" && good[0]["wrote"] == "10" && strings.EqualFold(good[0]["crc"], hello),
		fmt.Sprintf("the written file is read back and its CRC32 reported (%s)", hello), fmt.Sprint(good))
	x.check(len(slow) > 0 && slow[0]["ok"] == "0" && slow[0]["error"] == "timeout", "a stalled transfer ends with error=timeout", fmt.Sprint(slow))
	listed := false
	for _, d := range f.frames("!sd.file ") {
		if d["name"] == "hello.txt" && d["size"] == "10" && d["date"] == "2026-10-13" && d["time"] == "12:34" {
			listed = true
		}
	}
	x.check(listed, "sd.ls lists the file with its size and time", fmt.Sprint(head(f.lines("!sd.file "), 2)))
	get := f.replyTo("pt.run sd.get name=hello.txt")
	x.check(strings.HasPrefix(get, "OK sd.get ok=1") && strings.Contains(strings.ToUpper(get), strings.ToUpper("crc="+hello)),
		"sd.get replies size and CRC before sending", get)
	d, l = f.testLine("TEST sdf_get_same=")
	x.check(d != nil && d["sdf_get_same"] == "1", "sd.get sends the file's bytes unchanged on the data line", l)
	gets := f.frames("!sd.get ")
	x.check(len(gets) > 0 && gets[0]["ok"] == "1" && gets[0]["sent"] == "10", "sd.get reports what it sent", fmt.Sprint(gets))
	bad := f.replyTo("pt.run sd.put name=a/b size=1")
	x.check(strings.Contains(bad, "error=name"), "a name with a path character is refused", bad)
	x.check(strings.HasPrefix(f.replyTo("pt.run sd.rm name=hello.txt"), "OK sd.rm ok=1"), "sd.rm removes a file", f.replyTo("pt.run sd.rm name=hello.txt"))
	x.check(strings.HasPrefix(f.replyTo("pt.run sd.eject"), "OK sd.eject ok=1"), "sd.eject answers", f.replyTo("pt.run sd.eject"))
	for _, cmd := range []string{"pt.start eth mode=sideways", "pt.start eth port=0", "pt.start eth port=70000", "pt.start eth ip=192.168.1"} {
		x.refused(cmd, "")
	}
}

func (x *tc) hold() {
	f := x.f
	// The PC owns the clock for a timed run, so the board has to survive the PC going away.
	x.section("the deadman: pt.hold")
	hh := f.lines("OK porttool=")
	h0 := strings.Join(head(hh, 1), "")
	x.check(len(hh) > 0 && strings.Contains(hh[0], "hold=1"), "caps advertises pt.hold, so the PC never has to probe for it", h0)
	x.check(len(hh) > 0 && strings.Contains(hh[0], "led=1"), "caps advertises pt.led", h0)
	x.check(strings.HasPrefix(f.replyTo("pt.hold"), "OK hold="), "a bare pt.hold answers rather than arming", f.replyTo("pt.hold"))
	x.refused("pt.hold abc", "")
	x.refused("pt.hold 999999999", "")
	if l := f.lines("TEST hold_live_driving="); len(l) > 0 {
		d := kvMap(l[0])
		x.check(d["dout_duty_any"] == "1" && d["hold_live_driving"] == "1", "an armed hold does not disturb what is running", l[0])
	}
	if l := f.lines("TEST hold_renewed_driving="); len(l) > 0 {
		d := kvMap(l[0])
		x.check(d["dout_duty_any"] == "1" && d["hold_renewed_driving"] == "1", "renewing before the deadline keeps the outputs driven", l[0])
	}
	if l := f.lines("TEST hold_expired_driving="); len(l) > 0 {
		d := kvMap(l[0])
		// Outputs released without anybody on the PC side.
		x.check(d["dout_duty_any"] == "0" && d["hold_expired_driving"] == "0", "a lapsed hold released every output", l[0])
		x.check(d["led_high"] == "1", "a lapsed hold lit the indicator itself - the PC is gone, so nobody else would", l[0])
	}
	frames := f.lines("!hold ")
	x.check(len(frames) > 0, "the board says out loud that the hold lapsed")
	if len(frames) > 0 {
		x.check(kvMap(last(frames))["expired"] == "1", "the hold frame reports expiry", last(frames))
	}
	x.check(contains(f.lines("OK running="), "OK running=none"), "every session is stopped after the hold lapsed")
	if l := f.lines("TEST hold_disarmed_dout_duty_any="); len(l) > 0 {
		// pt.hold 0 means "stop watching the clock", not "stop the test".
		x.check(strings.HasSuffix(l[0], "=1"), "disarming the hold left the running session alone", l[0])
	}
}

func (x *tc) led() {
	f := x.f
	x.section("the indicator: pt.led")
	if l := f.lines("TEST led_fault_on="); len(l) > 0 {
		d := kvMap(l[0])
		x.check(d["led_fault_on"] == "1", "pt.led fault=1 lit the pin", l[0])
		x.check(d["writes"] != "0", "pt.led actually drove the pin", l[0])
	}
	if l := f.lines("TEST led_fault_off="); len(l) > 0 {
		d := kvMap(l[0])
		x.check(d["led_fault_off"] == "0" && d["writes"] != "0", "pt.led fault=0 drove the pin without driving it high", l[0])
	}
	x.refused("pt.led fault=2", "")
	x.refused("pt.led", "")
	// A run must not disturb what was already going.
	if l := f.lines("TEST sdram_probe_count="); len(l) > 0 {
		x.check(strings.HasSuffix(last(l), "=2"), "the probe ran once per pt.run and not otherwise", last(l))
	}
}
