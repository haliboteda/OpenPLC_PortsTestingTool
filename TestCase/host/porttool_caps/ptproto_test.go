package testcase

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"PortTool/src/calarea"
	"PortTool/src/ptproto"
)

// The fixture is the transcript T4-01 captures by running the real firmware source
// natively, not a hand-typed copy of what the board should say.
const goldenPath = "caps_golden.txt"

// transcript splits the fixture into the commands and the lines each produced.
func transcript(t *testing.T) map[string][][]string {
	t.Helper()
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read fixture: %v\n"+
			"Regenerate it: TestCase/test.sh t4-01.", err)
	}
	out := map[string][][]string{}
	for _, chunk := range strings.Split(string(raw), ">>> ")[1:] {
		lines := strings.Split(chunk, "\n")
		cmd := strings.TrimRight(lines[0], "\r")
		var body []string
		for _, l := range lines[1:] {
			l = strings.TrimRight(l, "\r")
			if l != "" && !strings.HasPrefix(l, "TEST ") {
				body = append(body, l)
			}
		}
		out[cmd] = append(out[cmd], body)
	}
	return out
}

func TestParseGoldenCaps(t *testing.T) {
	runs := transcript(t)["pt.caps"]
	// The first three are the ones the lifecycle test reads by position:
	// before any session, with din running, and after pt.stop all. Later runs
	// cover the per-channel parameters and may grow.
	if len(runs) < 3 {
		t.Fatalf("fixture has %d pt.caps runs, want at least 3", len(runs))
	}

	caps, err := ptproto.ParseCaps(runs[0])
	if err != nil {
		t.Fatalf("ParseCaps: %v", err)
	}

	if caps.Version != "0.18.1" {
		t.Errorf("version = %q, want 0.18.1", caps.Version)
	}
	// No hard port count: adding a port must cost nothing here, and ParseCaps already
	// checks ports= against the rows. One piece of hardware gets one row, so sd's and
	// sdram's one-shot checks ride on their session as runs=.
	wantSessions := []string{"din", "dout", "relay", "ain", "aout", "temp",
		"rs232", "rs485", "can", "knx", "sd", "sdram"}
	wantRuns := []string{"rtc", "led"}

	for _, name := range wantSessions {
		p, ok := caps.Port(name)
		if !ok {
			t.Errorf("session %s missing", name)
			continue
		}
		if p.Kind != ptproto.KindSession {
			t.Errorf("%s kind = %s, want session", name, p.Kind)
		}
		if p.Values == nil {
			t.Errorf("%s has no current values", name)
		}
	}
	for _, name := range wantRuns {
		p, ok := caps.Port(name)
		if !ok {
			t.Errorf("run port %s missing", name)
			continue
		}
		if p.Kind != ptproto.KindRun || len(p.Runs) == 0 {
			t.Errorf("%s = %s with runs %v", name, p.Kind, p.Runs)
		}
	}
	// The production plan checker looks a target up by name, so the run
	// targets a plan can name have to be reachable that way.
	sd, _ := caps.Port("sd")
	if !slices.Contains(sd.Runs, "sd.integrity") {
		t.Errorf("sd runs = %v, want sd.integrity among them", sd.Runs)
	}
}

func TestGoldenSessionDetail(t *testing.T) {
	caps, err := ptproto.ParseCaps(transcript(t)["pt.caps"][0])
	if err != nil {
		t.Fatal(err)
	}

	din, ok := caps.Port("din")
	if !ok {
		t.Fatal("no din port")
	}
	if din.Channels != 8 || din.Block != "D" || din.Loop != ptproto.LoopCtrl {
		t.Errorf("din = %d ch, blk %s, loop %s; want 8, D, ctrl",
			din.Channels, din.Block, din.Loop)
	}
	if din.Running {
		t.Error("din should not be running in the first caps")
	}
	if got := din.Values["period"]; got != "200" {
		t.Errorf("din period = %q, want 200", got)
	}

	// din's terminals are a plain run, so the labels are derived from term=.
	want := []string{"D02", "D03", "D04", "D05", "D06", "D07", "D08", "D09"}
	if got := din.TerminalLabels(); !equal(got, want) {
		t.Errorf("din labels = %v, want %v", got, want)
	}

	relay, ok := caps.Port("relay")
	if !ok {
		t.Fatal("no relay port")
	}
	if relay.Channels != 6 {
		t.Errorf("relay channels = %d, want 6", relay.Channels)
	}
	// Six channels across twelve terminals: the firmware has to spell these
	// out because no rule derives them from B01-B12.
	wantRelay := []string{"B01+B02", "B03+B04", "B05+B06", "B07+B08", "B09+B10", "B11+B12"}
	if got := relay.TerminalLabels(); !equal(got, wantRelay) {
		t.Errorf("relay labels = %v, want %v", got, wantRelay)
	}
	if len(relay.Params) != 6 {
		t.Errorf("relay params = %v, want six of them", relay.Params)
	}
	for _, p := range relay.Params {
		if _, ok := relay.Values[p]; !ok {
			t.Errorf("relay advertises %s but reported no value", p)
		}
	}
}

func TestGoldenLifecycle(t *testing.T) {
	runs := transcript(t)["pt.caps"]
	states := make([]ptproto.Port, 0, 3)
	for _, r := range runs {
		c, err := ptproto.ParseCaps(r)
		if err != nil {
			t.Fatal(err)
		}
		p, _ := c.Port("din")
		states = append(states, p)
	}
	if states[0].Running || !states[1].Running || states[2].Running {
		t.Errorf("din running across the three caps = %v/%v/%v, want false/true/false",
			states[0].Running, states[1].Running, states[2].Running)
	}
	if got := states[1].Values["ch"]; got != "1,3,5" {
		t.Errorf("running din ch = %q, want the 1,3,5 that pt.start asked for", got)
	}
}

func TestClassifyGoldenLines(t *testing.T) {
	// A bare log line is legal (the bring-up printf); what must not happen is one of
	// unknown origin where the PC waits for a reply. pt.run's checks print before its
	// OK line, matched by shape so a new target needs no registration here.
	prose := regexp.MustCompile(`^[A-Z][A-Z0-9_]*_TEST: `)
	for cmd, runs := range transcript(t) {
		for _, body := range runs {
			for _, l := range body {
				if k, _ := ptproto.Classify(l); k == ptproto.LineLog && !prose.MatchString(l) {
					t.Errorf("%s produced a line that reads as a bare log: %q", cmd, l)
				}
			}
		}
	}
	// A refusal must stay a refusal, with its reason intact.
	errLine := transcript(t)["pt.start din ch=1,9"][0][0]
	k, body := ptproto.Classify(errLine)
	if k != ptproto.LineErr || !strings.Contains(body, "must be channels") {
		t.Errorf("refusal did not survive classification: %q", errLine)
	}
}

func TestRejectedCapsShapes(t *testing.T) {
	good := transcript(t)["pt.caps"][0]

	cases := []struct {
		name  string
		lines []string
	}{
		{"empty", nil},
		{"header is not OK", []string{"ERR nope"}},
		{"lines= disagrees with what follows", good[:len(good)-1]},
		{"a port with an unknown kind", []string{
			"OK porttool=0.2.0 ports=1 lines=1",
			"OK port=x kind=mystery blk=- term=- channels=1 loop=none",
		}},
		{"a session that hides a parameter it advertises", []string{
			"OK porttool=0.2.0 ports=1 lines=1",
			"OK port=x kind=session blk=- term=- channels=1 loop=ctrl params=ch,period running=0 ch=1",
		}},
		{"an unknown loop", []string{
			"OK porttool=0.2.0 ports=1 lines=1",
			"OK port=x kind=session blk=- term=- channels=1 loop=sideways params= running=0",
		}},
		{"terms for a port that was never listed", []string{
			"OK porttool=0.2.0 ports=1 lines=2",
			"OK port=x kind=session blk=- term=- channels=1 loop=ctrl params= running=0",
			"OK terms=ghost A01,A02",
		}},
	}
	for _, tc := range cases {
		if _, err := ptproto.ParseCaps(tc.lines); err == nil {
			t.Errorf("%s: accepted, want an error", tc.name)
		}
	}
}

func TestGoldenFramesCarryTheEchoCounter(t *testing.T) {
	// The panel renders the link-health area from these three fields, and
	// renders the readings from the rest. Both have to survive parsing off the
	// same line, or one of the two areas silently goes blank.
	var frames []ptproto.Frame
	for _, runs := range transcript(t) {
		for _, body := range runs {
			for _, l := range body {
				if kind, b := ptproto.Classify(l); kind == ptproto.LineFrame {
					if f, ok := ptproto.ParseFrame(b); ok && f.Port == "din" {
						frames = append(frames, f)
					}
				}
			}
		}
	}
	if len(frames) == 0 {
		t.Fatal("the fixture has no din frames; regenerate it with TestCase/test.sh t4-01")
	}
	for _, f := range frames {
		for _, k := range []string{"seq", "rx", "miss", "v"} {
			if _, ok := ptproto.Get(f.Fields, k); !ok {
				t.Fatalf("frame %q is missing %s=", f.Raw, k)
			}
		}
		if f.Tick == 0 {
			t.Errorf("frame %q has no board timestamp", f.Raw)
		}
	}
}

func TestParseFrame(t *testing.T) {
	f, ok := ptproto.ParseFrame("din t=48213 v=0x16 ch1=0 ch3=1")
	if !ok {
		t.Fatal("frame rejected")
	}
	if f.Port != "din" || f.Tick != 48213 {
		t.Errorf("port/tick = %s/%d, want din/48213", f.Port, f.Tick)
	}
	if v, _ := ptproto.Get(f.Fields, "v"); v != "0x16" {
		t.Errorf("v = %q, want 0x16", v)
	}
	if _, present := ptproto.Get(f.Fields, "t"); present {
		t.Error("t should have been consumed into Tick, not left in Fields")
	}
	if _, ok := ptproto.ParseFrame(""); ok {
		t.Error("an empty frame body should be rejected")
	}
}

func TestTickUnwrap(t *testing.T) {
	var u ptproto.TickUnwrapper
	if got, restarted := u.Unwrap(1000); got != 1000 || restarted {
		t.Errorf("first = %d restarted=%v, want 1000 false", got, restarted)
	}
	if got, restarted := u.Unwrap(2000); got != 2000 || restarted {
		t.Errorf("forward = %d restarted=%v, want 2000 false", got, restarted)
	}
	// 49.7 days in, the board's counter starts over; the timeline must not. The
	// pre-wrap value has to be near the top of the range: a small value dropping is a
	// restart, not a wrap.
	const nearTop = ^uint32(0) - 1000
	if got, restarted := u.Unwrap(nearTop); restarted {
		t.Error("climbing towards the wrap is not a restart")
	} else if got != uint64(nearTop) {
		t.Errorf("pre-wrap = %d, want %d", got, nearTop)
	}
	if got, restarted := u.Unwrap(5); got != 1<<32+5 {
		t.Errorf("after wrap = %d, want %d", got, uint64(1)<<32+5)
	} else if restarted {
		t.Error("a genuine wrap must not be reported as a restart")
	}
	if u.Restarts != 0 {
		t.Errorf("restarts = %d after a clean wrap, want 0", u.Restarts)
	}
}

// A board that resets mid-run sends its tick back near zero; that must count as a
// restart, not a wrap (criterion "0 abnormal resets", production test guide).
func TestTickUnwrapSeesARestart(t *testing.T) {
	var u ptproto.TickUnwrapper

	// Four hours in, which is a whole ageing run and still only 0.3 % of the
	// counter's range.
	u.Unwrap(14_400_000)

	ms, restarted := u.Unwrap(12)
	if !restarted {
		t.Fatal("a tick falling from four hours to 12 ms is a restart, not a wrap")
	}
	if u.Restarts != 1 {
		t.Errorf("restarts = %d, want 1", u.Restarts)
	}
	// And the timeline still has to move forward across it: a run's log is
	// read in order, and a second sample that sorts before the first one is
	// worse than a gap.
	if ms <= 14_400_000 {
		t.Errorf("timeline went backwards: %d", ms)
	}

	// A second restart counts separately - "how many times" is the criterion,
	// not "did it ever".
	u.Unwrap(60_000)
	if _, restarted := u.Unwrap(8); !restarted || u.Restarts != 2 {
		t.Errorf("restarts = %d restarted=%v, want 2 true", u.Restarts, restarted)
	}
}

func TestFieldsKeepsDuplicates(t *testing.T) {
	// A repeated key means the firmware is emitting something ambiguous. The
	// parser has to preserve that so a test can catch it, rather than let a
	// map pick a winner.
	got := ptproto.Fields("a=1 b=2 a=3")
	if len(got) != 3 {
		t.Fatalf("got %d pairs, want 3 with the duplicate kept", len(got))
	}
	if v, _ := ptproto.Get(got, "a"); v != "1" {
		t.Errorf("Get returned %q, want the first occurrence", v)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// can's clock source and bit timing reach the parsed caps as read-only facts,
// not as parameters (DECISIONS.md 87, PORTTOOL-FLOW.md A.4 in $PROD).
func TestGoldenCanInfo(t *testing.T) {
	caps, err := ptproto.ParseCaps(transcript(t)["pt.caps"][0])
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range caps.Ports {
		if p.Name != "can" {
			continue
		}
		if v, ok := ptproto.Get(p.Info, "clk"); !ok || v == "" {
			t.Fatalf("can has no clk in its info: %v", p.Info)
		}
		if _, ok := ptproto.Get(p.Info, "bps"); !ok {
			t.Fatalf("can has no bps in its info: %v", p.Info)
		}
		if _, isParam := p.Values["clk"]; isParam {
			t.Fatal("clk turned into a parameter")
		}
		return
	}
	t.Fatal("no can port in the golden caps")
}

// pt.cal, written then erased (harness/test_main.c). The coefficients are the
// ones test_main.c writes; the UID is the one pt.id reports in the same run.
func TestGoldenCal(t *testing.T) {
	tr := transcript(t)
	runs := tr["pt.cal"]
	if len(runs) != 2 || len(runs[0]) == 0 || len(runs[1]) == 0 {
		t.Fatalf("want two pt.cal replies in the golden, got %v", runs)
	}

	kind, body := ptproto.Classify(runs[0][0])
	if kind != ptproto.LineOK {
		t.Fatalf("written area: %q is not an OK line", runs[0][0])
	}
	got, err := ptproto.ParseCal(body)
	if err != nil || !got.Present {
		t.Fatalf("written area: Present=%v err=%v", got.Present, err)
	}
	_, idBody := ptproto.Classify(tr["pt.id"][0][0])
	uid, _ := ptproto.Get(ptproto.Fields(idBody), "uid")
	if s := fmt.Sprintf("%08X%08X%08X", got.UID[0], got.UID[1], got.UID[2]); s != uid {
		t.Errorf("UID %s, pt.id says %s", s, uid)
	}
	want := [calarea.Channels]calarea.Channel{
		{Gain: 1.0625, Offset: -12.5},
		{Gain: 0.96875, Offset: 0.125},
		{Gain: 1.03125, Offset: -0.25},
		{Gain: 0.9375, Offset: 0.5},
	}
	if got.Ch != want {
		t.Errorf("coefficients %v, want %v", got.Ch, want)
	}

	// Erased flash is a board without calibration, not a failed read.
	_, body = ptproto.Classify(runs[1][0])
	blank, err := ptproto.ParseCal(body)
	if err != nil || blank.Present {
		t.Errorf("erased area: Present=%v err=%v, want no calibration and no error", blank.Present, err)
	}
}

// A damaged area and a damaged reply are different failures.
func TestParseCalRejects(t *testing.T) {
	rec := calarea.Encode([3]uint32{1, 2, 3}, [calarea.Channels]calarea.Channel{{Gain: 1}, {Gain: 1}, {Gain: 1}, {Gain: 1}})
	rec[20] ^= 1
	if _, err := ptproto.ParseCal("cal=" + strings.ToUpper(hex.EncodeToString(rec))); !errors.Is(err, calarea.ErrCorrupt) {
		t.Errorf("bad CRC: err=%v, want calarea.ErrCorrupt", err)
	}
	for _, body := range []string{
		"uid=00340041",
		"cal=4F43414C",
		"cal=" + strings.Repeat("ZZ", calarea.Size),
	} {
		if _, err := ptproto.ParseCal(body); err == nil || errors.Is(err, calarea.ErrCorrupt) {
			t.Errorf("%q: err=%v, want a malformed-reply error", body, err)
		}
	}
}
