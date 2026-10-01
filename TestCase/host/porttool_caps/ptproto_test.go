package testcase

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"PortTool/internal/ptproto"
)

// The fixture is the transcript case T4-01 captures by running the real firmware
// source natively (TestCase/host/porttool_caps). Testing against a hand-typed
// copy of what the board "should" say would only prove this file agrees with
// itself; testing against what the firmware actually printed is the point.
const goldenPath = "caps_golden.txt"

// transcript splits the fixture into the commands and the lines each produced.
func transcript(t *testing.T) map[string][][]string {
	t.Helper()
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read fixture: %v\n"+
			"Run TestCase/host/porttool_caps/build.py to regenerate it.", err)
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

	if caps.Version != "0.10.0" {
		t.Errorf("version = %q, want 0.10.0", caps.Version)
	}
	// Deliberately not a hard port count: adding a port to the firmware is
	// meant to cost nothing here, and ParseCaps already refuses a reply whose
	// ports= disagrees with the rows that followed. What matters is that the
	// ports the panel is built around are all present and typed correctly.
	// One piece of hardware gets one row: sdram's retention is the session and
	// its one-shot checks ride on that row as runs=, the shape sd and eth
	// already have.
	wantSessions := []string{"din", "dout", "relay", "ain", "aout", "temp",
		"rs232", "rs485", "can", "knx", "sd", "sdram"}
	// sd left this list on 2026-09-10 and sdram on 2026-09-13, both for the
	// same reason: the hardware got a session, and one piece of hardware gets
	// one row, so the one-shot checks became runs= on that row.
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
	if len(relay.Params) != 4 {
		t.Errorf("relay params = %v, want four of them", relay.Params)
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
	// A bare log line is the protocol's fourth kind and is legal - it is the
	// bring-up printf a person reads. What must not happen is one appearing
	// where the PC is waiting for a reply and nobody knows what printed it, so
	// every source of prose is named here and anything else fails.
	//
	// pt.run is the first command in this transcript that produces any: the
	// checks it runs print as they go, before its OK line.
	// Matched by shape, not by a list of names: a new pt.run target should not
	// have to be registered here, while a stray printf still fails.
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
		t.Fatal("the fixture has no din frames; run build.py to regenerate it")
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
	// 49.7 days in, the board's counter starts over. The timeline must not.
	//
	// *** The pre-wrap value has to be near the top of the range. *** Until
	// 2026-09-14 this case was written as 2000 -> 5 and called a wrap, which is
	// the shape of a RESTART: a board two seconds up cannot have exhausted a
	// 49.7-day counter. The test encoded the bug it should have caught.
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

// The event an ageing run exists to catch. A board that resets mid-run sends
// its tick back to near zero from wherever it had got to, and that has to be
// visible - the production test guide's criterion is "0 abnormal resets", and
// a reset counted as a wrap is a reset nobody hears about.
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
