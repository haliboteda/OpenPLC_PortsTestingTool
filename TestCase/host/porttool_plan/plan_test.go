package testcase

// Part of H1: the plan file, the limit operators, the executor and the report.
//
// What these cover is the behaviour a production line depends on and that no
// amount of reading the code settles: a limit that cannot be met silently, a
// plan that judges a counter which does not travel over the port it names, a
// timeout told apart from a failed limit, a retry that keeps the failure it
// replaced, and the gate that stops a sequence after something fails.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"PortTool/internal/ptboard"
	"PortTool/internal/ptcheck"
	"PortTool/internal/ptplan"
	"PortTool/internal/ptproto"
	"PortTool/internal/ptreport"
	"PortTool/internal/ptseq"
)

func f64(v float64) *float64 { return &v }

// refusePeer is what a plan's peer= gets in these tests.
//
// ⚠️ The fake board is a Go type answering the protocol in memory - it has no
// TCP listener, no CDC pipe and nothing on an RS485 pair. Letting the runner
// use the real opener made it grab whatever COM16 happened to be on the
// machine running the test, so the result depended on what was plugged into
// somebody's bench. The frames the fake board sends already describe a healthy
// link, which is what these steps are meant to be judged on.
func refusePeer(kind, addr string, baud int) (io.ReadWriteCloser, error) {
	return nil, fmt.Errorf("no %s peer on this bench (%s)", kind, addr)
}

// ---------- limits ----------

func TestCheckOperators(t *testing.T) {
	fields := map[string]string{
		"v":     "0xFF",
		"miss":  "3",
		"ch1":   "21850/2071",
		"vdda":  "3287",
		"state": "SC-protect",
	}
	look := func(name string) (string, bool) {
		v, ok := fields[name]
		return v, ok
	}

	cases := []struct {
		name string
		c    ptcheck.Check
		pass bool
	}{
		// A bitfield written as hex has to match a limit written in decimal:
		// the firmware prints 0xFF, a person writes 255.
		{"hex equals decimal", ptcheck.Check{Field: "v", Op: ptcheck.OpEq, Value: "255"}, true},
		{"hex equals hex", ptcheck.Check{Field: "v", Op: ptcheck.OpEq, Value: "0xFF"}, true},
		{"wrong bitfield", ptcheck.Check{Field: "v", Op: ptcheck.OpEq, Value: "0xFE"}, false},
		{"in range", ptcheck.Check{Field: "vdda", Op: ptcheck.OpRange, Min: f64(2000), Max: f64(3600)}, true},
		{"below range", ptcheck.Check{Field: "vdda", Op: ptcheck.OpRange, Min: f64(3300)}, false},
		{"counter not zero", ptcheck.Check{Field: "miss", Op: ptcheck.OpCountZero}, false},
		{"contains", ptcheck.Check{Field: "state", Op: ptcheck.OpContains, Value: "protect"}, true},
		// ain reports raw/mV in one field, so a limit has to be able to name
		// the millivolts half without the raw count dragging it out of range.
		{"second part of a composite", ptcheck.Check{Field: "ch1[1]", Op: ptcheck.OpRange, Min: f64(2000), Max: f64(2100)}, true},
		{"first part of a composite", ptcheck.Check{Field: "ch1[0]", Op: ptcheck.OpRange, Min: f64(2000), Max: f64(2100)}, false},
		{"part that is not there", ptcheck.Check{Field: "ch1[7]", Op: ptcheck.OpEq, Value: "1"}, false},
		// A field the firmware stopped reporting must fail, not be skipped:
		// a silently skipped limit is how an untested board ships.
		{"absent field fails", ptcheck.Check{Field: "nosuch", Op: ptcheck.OpEq, Value: "1"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.c.Eval(look)
			if got.Pass != tc.pass {
				t.Fatalf("%s: pass=%v want %v (got %q, why %q)",
					tc.c.Describe(), got.Pass, tc.pass, got.Got, got.Why)
			}
			if !got.Pass && got.Why == "" {
				t.Fatalf("%s failed without saying why", tc.c.Describe())
			}
		})
	}
}

// ---------- the plan file ----------

func TestPlanRejectsWhatCannotBeJudged(t *testing.T) {
	cases := []struct {
		name string
		json string
		want string
	}{
		{
			"a session with no limits",
			`{"schema":1,"name":"p","limit_version":"v","steps":[
			  {"id":"a","type":"PtSession","port":"din"}]}`,
			"no checks",
		},
		{
			"a misspelled key",
			`{"schema":1,"name":"p","limit_version":"v","steps":[
			  {"id":"a","type":"PtSession","port":"din","timeout":100,
			   "checks":[{"field":"v","op":"eq","value":"1"}]}]}`,
			"timeout",
		},
		{
			"two steps with the same id",
			`{"schema":1,"name":"p","limit_version":"v","steps":[
			  {"id":"a","type":"PtRaw","command":"pt.id","checks":[{"field":"_text","op":"contains","value":"uid"}]},
			  {"id":"a","type":"PtRaw","command":"pt.id","checks":[{"field":"_text","op":"contains","value":"uid"}]}]}`,
			"used twice",
		},
		{
			"no limit version to trace the verdict back to",
			`{"schema":1,"name":"p","steps":[
			  {"id":"a","type":"PtRaw","command":"pt.id","checks":[{"field":"_text","op":"contains","value":"uid"}]}]}`,
			"limit_version",
		},
		{
			"a field belonging to another type",
			`{"schema":1,"name":"p","limit_version":"v","steps":[
			  {"id":"a","type":"PtRun","target":"sdram.crc","port":"din",
			   "checks":[{"field":"errors","op":"count_zero"}]}]}`,
			"sets port",
		},
		{
			"a range with no bounds",
			`{"schema":1,"name":"p","limit_version":"v","steps":[
			  {"id":"a","type":"PtSession","port":"din","checks":[{"field":"v","op":"range"}]}]}`,
			"neither min nor max",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ptplan.Parse([]byte(tc.json))
			if err == nil {
				t.Fatalf("this plan was accepted, expected a complaint about %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("complaint was %q, expected it to mention %q", err, tc.want)
			}
		})
	}
}

func TestNumericParamsAreAcceptedAsWritten(t *testing.T) {
	// Nearly every parameter these ports take is a number, so a plan writing
	// period: 200 rather than "200" is the normal case. The pt command it
	// becomes is text either way.
	plan, err := ptplan.Parse([]byte(`{"schema":1,"name":"p","limit_version":"v","steps":[
	  {"id":"a","type":"PtSession","port":"dout","params":{"freq":1000,"duty":"50","mode":"blink"},
	   "checks":[{"field":"seq","op":"range","min":1}]}]}`))
	if err != nil {
		t.Fatalf("a numeric parameter should be accepted: %v", err)
	}
	got := strings.Join(plan.Steps[0].ParamArgs(), " ")
	if got != "duty=50 freq=1000 mode=blink" {
		t.Fatalf("params rendered as %q", got)
	}
}

func TestPlanValuesCheckedAgainstTheFirmwaresOwnLimits(t *testing.T) {
	// The firmware states what each parameter accepts, so a value it would
	// refuse is caught here rather than on the line with a board in front of
	// somebody. The numbers come from the board, not from a table in this
	// program - that is the whole point of the limits= line.
	caps, err := ptproto.ParseCaps(capsReply())
	if err != nil {
		t.Fatalf("parsing the caps fixture: %v", err)
	}

	din, _ := caps.Port("din")
	if len(din.Limits) == 0 {
		t.Fatal("din reported no limits")
	}
	if got := din.Limits["period"].Spec; got != "50.." {
		t.Fatalf("din period limit = %q, want an open-ended 50..", got)
	}

	plan, err := ptplan.Parse([]byte(`{"schema":1,"name":"p","limit_version":"v","steps":[
	  {"id":"too-fast","type":"PtSession","port":"din","params":{"period":10},
	   "checks":[{"field":"v","op":"eq","value":"1"}]},
	  {"id":"odd-baud","type":"PtSession","port":"rs485","params":{"baud":250000},
	   "checks":[{"field":"miss","op":"count_zero"}]},
	  {"id":"fine","type":"PtSession","port":"din","params":{"period":200},
	   "checks":[{"field":"v","op":"eq","value":"1"}]}]}`))
	if err != nil {
		t.Fatalf("this plan is well formed, so it should parse: %v", err)
	}

	findings := strings.Join(plan.CheckAgainstCaps(caps), "\n")
	if !strings.Contains(findings, "too-fast") || !strings.Contains(findings, "at least 50") {
		t.Fatalf("a period below the firmware's floor was not caught:\n%s", findings)
	}
	if !strings.Contains(findings, "odd-baud") || !strings.Contains(findings, "must be one of") {
		t.Fatalf("a baud outside the firmware's list was not caught:\n%s", findings)
	}
	if strings.Contains(findings, "fine") {
		t.Fatalf("a value the firmware accepts was reported anyway:\n%s", findings)
	}
}

func TestPlanCheckedAgainstWhatTheBoardReports(t *testing.T) {
	caps, err := ptproto.ParseCaps(capsReply())
	if err != nil {
		t.Fatalf("parsing the caps fixture: %v", err)
	}

	plan, err := ptplan.Parse([]byte(`{"schema":1,"name":"p","limit_version":"v","steps":[
	  {"id":"ghost","type":"PtSession","port":"nosuch","checks":[{"field":"v","op":"eq","value":"1"}]},
	  {"id":"badparam","type":"PtSession","port":"din","params":{"duty":"50"},"checks":[{"field":"v","op":"eq","value":"1"}]},
	  {"id":"falsecrit","type":"PtSession","port":"din","checks":[{"field":"miss","op":"count_zero"}]},
	  {"id":"realcrit","type":"PtSession","port":"rs485","checks":[{"field":"miss","op":"count_zero"}]}]}`))
	if err != nil {
		t.Fatalf("this plan is well formed, so it should parse: %v", err)
	}

	findings := strings.Join(plan.CheckAgainstCaps(caps), "\n")
	for _, want := range []string{
		"does not report",          // the port that is not there
		"accepts only",             // duty= on a port that takes ch,period
		"that port's loop is ctrl", // a counter that proves only the control port
	} {
		if !strings.Contains(findings, want) {
			t.Fatalf("findings did not mention %q:\n%s", want, findings)
		}
	}
	// rs485 is loop=link, so the same limit there IS that link's verdict and
	// must not be reported.
	if strings.Contains(findings, "realcrit") {
		t.Fatalf("a miss counter on a loop=link port was called a false criterion:\n%s", findings)
	}
}

// ---------- the executor ----------

// runPlan wires a runner to a scripted board with the clock stubbed out, so
// retries and sleeps cost no wall time.
func runPlan(t *testing.T, planJSON string, handler func(cmd string, nth int) ([]string, []string)) (ptreport.Report, *scriptBoard) {
	t.Helper()
	plan, err := ptplan.Parse([]byte(planJSON))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	fake := newScriptBoard(t, handler)
	board := ptboard.New(fake, 0)
	t.Cleanup(func() { board.Close() })

	runner := &ptseq.Runner{OpenPeer: refusePeer,
		Board:       board,
		Sleep:       func(time.Duration) {},
		ToolVersion: "test",
	}
	rep, err := runner.Run(plan)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return rep, fake
}

func TestSessionPassesAndAlwaysReleasesThePort(t *testing.T) {
	rep, fake := runPlan(t,
		`{"schema":1,"name":"din","limit_version":"2026-09-07","steps":[
		  {"id":"din-all","type":"PtSession","port":"din","frames":2,"timeout_ms":2000,
		   "params":{"period":"100","ch":"1,2,3,4,5,6,7,8"},
		   "checks":[{"field":"v","op":"eq","value":"0xFF"}]}]}`,
		func(cmd string, nth int) ([]string, []string) {
			if lines, ok := standardReplies(cmd); ok {
				return lines, nil
			}
			if cmd == "pt.start din ch=1,2,3,4,5,6,7,8 period=100" {
				return []string{"OK din started"}, []string{
					"!din t=100 seq=1 rx=0 miss=0 v=0xFF",
					"!din t=200 seq=2 rx=1 miss=0 v=0xFF",
				}
			}
			return []string{"ERR unexpected " + cmd}, nil
		})

	if rep.Steps[0].Outcome != ptreport.OutcomePass {
		t.Fatalf("outcome %s, attempts %+v", rep.Steps[0].Outcome, rep.Steps[0].Attempts)
	}
	if !rep.Passed() {
		t.Fatal("the report should pass")
	}
	if !fake.sawCommand("pt.stop din") {
		t.Fatalf("the port was never released; commands were %v", fake.commands())
	}
	if rep.BoardUID == "" || rep.Firmware == "" {
		t.Fatalf("the report should name the board and firmware, got uid=%q fw=%q", rep.BoardUID, rep.Firmware)
	}
}

func TestRefusalIsAVerdictInTheBoardsOwnWords(t *testing.T) {
	// The firmware refuses to start the analog ports when VREFBUF will not come
	// up. That is a board that failed, not a transport problem, and the reason
	// has to survive into the report unreworded.
	const reason = "ain refused: VREFBUF did not become ready"
	rep, _ := runPlan(t,
		`{"schema":1,"name":"ain","limit_version":"v","steps":[
		  {"id":"ain","type":"PtSession","port":"ain","timeout_ms":2000,
		   "checks":[{"field":"ok","op":"eq","value":"1"}]}]}`,
		func(cmd string, nth int) ([]string, []string) {
			if lines, ok := standardReplies(cmd); ok {
				return lines, nil
			}
			if strings.HasPrefix(cmd, "pt.start ain") {
				return []string{"ERR " + reason}, nil
			}
			return []string{"ERR unexpected " + cmd}, nil
		})

	step := rep.Steps[0]
	if step.Outcome != ptreport.OutcomeFail {
		t.Fatalf("a refusal should be FAIL, got %s", step.Outcome)
	}
	if got := step.Attempts[0].Err; got != reason {
		t.Fatalf("the board's words were %q, report says %q", reason, got)
	}
}

func TestTooFewFramesIsATimeoutNotAFailure(t *testing.T) {
	// A timeout is usually wiring or a worn fixture probe; a failed limit is
	// usually the board. Recorded as one outcome, failure analysis is over.
	rep, _ := runPlan(t,
		`{"schema":1,"name":"din","limit_version":"v","steps":[
		  {"id":"din","type":"PtSession","port":"din","frames":3,"timeout_ms":250,
		   "checks":[{"field":"v","op":"eq","value":"0xFF"}]}]}`,
		func(cmd string, nth int) ([]string, []string) {
			if lines, ok := standardReplies(cmd); ok {
				return lines, nil
			}
			if strings.HasPrefix(cmd, "pt.start din") {
				return []string{"OK din started"}, []string{"!din t=1 seq=1 rx=0 miss=0 v=0xFF"}
			}
			return []string{"ERR unexpected " + cmd}, nil
		})

	step := rep.Steps[0]
	if step.Outcome != ptreport.OutcomeTimeout {
		t.Fatalf("outcome %s, want TIMEOUT (err %q)", step.Outcome, step.Attempts[0].Err)
	}
	if !strings.Contains(step.Attempts[0].Err, "got 1") {
		t.Fatalf("the timeout should say how many frames did arrive, got %q", step.Attempts[0].Err)
	}
}

func TestRetryKeepsTheFailureItReplaced(t *testing.T) {
	// The production test guide forbids overwriting failure data: a step that
	// passed on its second try still has to show the first.
	rep, _ := runPlan(t,
		`{"schema":1,"name":"din","limit_version":"v","steps":[
		  {"id":"din","type":"PtSession","port":"din","timeout_ms":2000,
		   "retry_count":1,"retry_interval_ms":200,
		   "checks":[{"field":"v","op":"eq","value":"0xFF"}]}]}`,
		func(cmd string, nth int) ([]string, []string) {
			if lines, ok := standardReplies(cmd); ok {
				return lines, nil
			}
			if strings.HasPrefix(cmd, "pt.start din") {
				if nth == 0 {
					return []string{"OK din started"}, []string{"!din t=1 seq=1 rx=0 miss=0 v=0x7F"}
				}
				return []string{"OK din started"}, []string{"!din t=2 seq=1 rx=0 miss=0 v=0xFF"}
			}
			return []string{"ERR unexpected " + cmd}, nil
		})

	step := rep.Steps[0]
	if step.Outcome != ptreport.OutcomePass {
		t.Fatalf("the second attempt passed, so the step should: %s", step.Outcome)
	}
	if len(step.Attempts) != 2 {
		t.Fatalf("expected both attempts kept, got %d", len(step.Attempts))
	}
	if step.Attempts[0].Outcome != ptreport.OutcomeFail {
		t.Fatalf("the first attempt should still read FAIL, got %s", step.Attempts[0].Outcome)
	}
	if got := step.Attempts[0].Checks[0].Got; got != "0x7F" {
		t.Fatalf("the first attempt's raw reading was lost, got %q", got)
	}
}

func TestRunStepReadsThroughTheChecksOwnProse(t *testing.T) {
	// pt.run answers with one OK line, but the checks it performs print their
	// own prose first. The executor has to judge the OK line and not the prose
	// - this is the shape T4-01 locks down on the firmware side.
	rep, _ := runPlan(t,
		`{"schema":1,"name":"sdram","limit_version":"v","steps":[
		  {"id":"sdram","type":"PtRun","target":"sdram.probe","timeout_ms":2000,
		   "checks":[{"field":"ready","op":"eq","value":"1"},
		             {"field":"databus","op":"eq","value":"1"},
		             {"field":"size","op":"eq","value":"67108864","unit":"bytes"}]}]}`,
		func(cmd string, nth int) ([]string, []string) {
			if lines, ok := standardReplies(cmd); ok {
				return lines, nil
			}
			if cmd == "pt.run sdram.probe" {
				return []string{
					"SDRAM_TEST: FMC Bank1 @ 0xC0000000, 64 MiB (AS4C32M16SB-7BIN)",
					"SDRAM_TEST: data bus OK - all 16 data lines (D0-D15) independent",
					"OK sdram.probe base=0xC0000000 size=67108864 ready=1 databus=1 addrbus=1",
				}, nil
			}
			return []string{"ERR unexpected " + cmd}, nil
		})

	step := rep.Steps[0]
	if step.Outcome != ptreport.OutcomePass {
		t.Fatalf("outcome %s, attempts %+v", step.Outcome, step.Attempts)
	}
	if n := len(step.Attempts[0].Checks); n != 3 {
		t.Fatalf("expected all three limits judged, got %d", n)
	}
}

func TestRunStepFailsOnWhatTheBoardMeasured(t *testing.T) {
	// A board whose FMC never came up answers with a parseable line and
	// ready=0. The verdict is the PC's, per DECISIONS.md 22.
	rep, _ := runPlan(t,
		`{"schema":1,"name":"sdram","limit_version":"v","steps":[
		  {"id":"sdram","type":"PtRun","target":"sdram.probe","timeout_ms":2000,
		   "checks":[{"field":"ready","op":"eq","value":"1"}]}]}`,
		func(cmd string, nth int) ([]string, []string) {
			if lines, ok := standardReplies(cmd); ok {
				return lines, nil
			}
			if cmd == "pt.run sdram.probe" {
				return []string{
					"SDRAM_TEST: FAIL reason=not_initialised (state=0)",
					"OK sdram.probe base=0xC0000000 size=67108864 ready=0 databus=0 addrbus=0",
				}, nil
			}
			return []string{"ERR unexpected " + cmd}, nil
		})

	step := rep.Steps[0]
	if step.Outcome != ptreport.OutcomeFail {
		t.Fatalf("ready=0 should fail the limit, got %s", step.Outcome)
	}
	if got := step.Attempts[0].Checks[0].Got; got != "0" {
		t.Fatalf("the raw reading should be kept, got %q", got)
	}
}

func TestGateStopsTheSequenceButAlwaysStillRuns(t *testing.T) {
	rep, _ := runPlan(t,
		`{"schema":1,"name":"gate","limit_version":"v","steps":[
		  {"id":"fails","type":"PtSession","port":"din","timeout_ms":2000,
		   "checks":[{"field":"v","op":"eq","value":"0xFF"}]},
		  {"id":"gated","type":"PtRaw","command":"pt.id",
		   "checks":[{"field":"_text","op":"contains","value":"uid"}]},
		  {"id":"teardown","type":"PtRaw","command":"pt.stop all","execute_condition":"ALWAYS",
		   "checks":[{"field":"_text","op":"contains","value":"OK"}]}]}`,
		func(cmd string, nth int) ([]string, []string) {
			if lines, ok := standardReplies(cmd); ok {
				return lines, nil
			}
			switch {
			case strings.HasPrefix(cmd, "pt.start din"):
				return []string{"OK din started"}, []string{"!din t=1 seq=1 rx=0 miss=0 v=0x00"}
			case cmd == "pt.stop all":
				return []string{"OK all stopped"}, nil
			}
			return []string{"ERR unexpected " + cmd}, nil
		})

	want := []ptreport.Outcome{ptreport.OutcomeFail, ptreport.OutcomeSkipped, ptreport.OutcomePass}
	for i, w := range want {
		if rep.Steps[i].Outcome != w {
			t.Fatalf("step %q is %s, want %s", rep.Steps[i].ID, rep.Steps[i].Outcome, w)
		}
	}
	if !strings.Contains(rep.Steps[1].Reason, "PASS") {
		t.Fatalf("a skipped step should say why: %q", rep.Steps[1].Reason)
	}
	if rep.Passed() {
		t.Fatal("a run with a failed step must not pass")
	}
}

func TestDisabledStepStaysInTheReport(t *testing.T) {
	// A step switched off has to be visible: one that vanished is one nobody
	// knows was not run.
	rep, _ := runPlan(t,
		`{"schema":1,"name":"off","limit_version":"v","steps":[
		  {"id":"skipme","type":"PtRaw","command":"pt.id","enabled":false,
		   "checks":[{"field":"_text","op":"contains","value":"uid"}]}]}`,
		func(cmd string, nth int) ([]string, []string) {
			lines, _ := standardReplies(cmd)
			return lines, nil
		})

	if len(rep.Steps) != 1 || rep.Steps[0].Outcome != ptreport.OutcomeSkipped {
		t.Fatalf("the disabled step is missing or not marked skipped: %+v", rep.Steps)
	}
	if rep.Passed() {
		t.Fatal("a run where nothing ran proved nothing, so it must not pass")
	}
}

// ---------- the four non-device step types ----------

func TestToolStepIsJudgedByWhatItPrintedOrReturned(t *testing.T) {
	plan, err := ptplan.Parse([]byte(`{"schema":1,"name":"t","limit_version":"v","steps":[
	  {"id":"exit-only","type":"Tool","tool_name":"programmer","tool_args":["--verify"]},
	  {"id":"by-output","type":"Tool","tool_name":"psu","tool_args":["read"],
	   "checks":[{"field":"_text","op":"contains","value":"24.0"},
	             {"field":"_exit","op":"eq","value":"0"}]}]}`))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	var called [][]string
	fake := newScriptBoard(t, func(cmd string, nth int) ([]string, []string) {
		lines, _ := standardReplies(cmd)
		return lines, nil
	})
	board := ptboard.New(fake, 0)
	t.Cleanup(func() { board.Close() })

	runner := &ptseq.Runner{OpenPeer: refusePeer,
		Board: board, Sleep: func(time.Duration) {}, ToolVersion: "test",
		RunTool: func(name string, args []string, dir string, timeout time.Duration) (string, int, error) {
			called = append(called, append([]string{name}, args...))
			if name == "psu" {
				return "rail 24.0 V\n", 0, nil
			}
			return "", 0, nil
		},
	}
	rep, err := runner.Run(plan)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	for _, s := range rep.Steps {
		if s.Outcome != ptreport.OutcomePass {
			t.Fatalf("step %q is %s: %s", s.ID, s.Outcome, attemptErr(s))
		}
	}
	if len(called) != 2 || called[0][0] != "programmer" || called[1][1] != "read" {
		t.Fatalf("the tools and arguments came from the plan, got %v", called)
	}
	// A non-zero exit with no checks is a failure, not an error: the program
	// ran and told us the answer.
	if got := rep.Steps[0].Attempts[0].Extra["exit"]; got != "0" {
		t.Fatalf("the exit code should be recorded, got %q", got)
	}
}

func TestToolStepFailsOnANonZeroExit(t *testing.T) {
	rep := runWithTool(t,
		`{"schema":1,"name":"t","limit_version":"v","steps":[
		  {"id":"prog","type":"Tool","tool_name":"jlink"}]}`,
		func(string, []string, string, time.Duration) (string, int, error) {
			return "cannot connect to target\n", 1, nil
		})
	if rep.Steps[0].Outcome != ptreport.OutcomeFail {
		t.Fatalf("a non-zero exit should fail the step, got %s", rep.Steps[0].Outcome)
	}
}

func TestOperatorAnswerIsTheVerdict(t *testing.T) {
	const plan = `{"schema":1,"name":"led","limit_version":"v","steps":[
	  {"id":"lamp","type":"UserConfirm","prompt":"did the indicator blink six times?"}]}`

	for _, tc := range []struct {
		name    string
		confirm func(string) (bool, error)
		want    ptreport.Outcome
	}{
		{"a person says yes", func(string) (bool, error) { return true, nil }, ptreport.OutcomePass},
		{"a person says no", func(string) (bool, error) { return false, nil }, ptreport.OutcomeFail},
		// Nobody to ask must be an error, never a pass: a step that exists
		// because only a person can see the answer, passing itself, is the
		// worst outcome available.
		{"nobody to ask", nil, ptreport.OutcomeError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ptplan.Parse([]byte(plan))
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			fake := newScriptBoard(t, func(cmd string, nth int) ([]string, []string) {
				lines, _ := standardReplies(cmd)
				return lines, nil
			})
			board := ptboard.New(fake, 0)
			t.Cleanup(func() { board.Close() })
			runner := &ptseq.Runner{OpenPeer: refusePeer,
				Board: board, Sleep: func(time.Duration) {},
				ToolVersion: "test", Confirm: tc.confirm,
			}
			rep, err := runner.Run(p)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if rep.Steps[0].Outcome != tc.want {
				t.Fatalf("outcome %s, want %s", rep.Steps[0].Outcome, tc.want)
			}
		})
	}
}

func TestSerialNumberComesFromOutsideAndReachesTheReport(t *testing.T) {
	dir := t.TempDir()
	snFile := filepath.Join(dir, "sn.txt")
	if err := os.WriteFile(snFile, []byte("OPLC-2026-000123\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "reports")

	plan, err := ptplan.Parse([]byte(`{"schema":1,"name":"trace","limit_version":"v","steps":[
	  {"id":"sn","type":"ReadSN","source":"file:sn.txt",
	   "checks":[{"field":"sn","op":"contains","value":"OPLC-"}]},
	  {"id":"push","type":"PushResult","sink":"dir:reports","execute_condition":"ALWAYS"}]}`))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	fake := newScriptBoard(t, func(cmd string, nth int) ([]string, []string) {
		lines, _ := standardReplies(cmd)
		return lines, nil
	})
	board := ptboard.New(fake, 0)
	t.Cleanup(func() { board.Close() })
	runner := &ptseq.Runner{OpenPeer: refusePeer,
		Board: board, Sleep: func(time.Duration) {},
		ToolVersion: "test", BaseDir: dir,
	}
	rep, err := runner.Run(plan)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if rep.SN != "OPLC-2026-000123" {
		t.Fatalf("the serial number did not reach the report, got %q", rep.SN)
	}
	for _, s := range rep.Steps {
		if s.Outcome != ptreport.OutcomePass {
			t.Fatalf("step %q is %s: %s", s.ID, s.Outcome, attemptErr(s))
		}
	}

	// The file is named after the board and the moment, so a retest lands
	// beside the first attempt and never on top of it.
	written, err := os.ReadDir(out)
	if err != nil || len(written) != 1 {
		t.Fatalf("expected one report file in %s, got %v (%v)", out, written, err)
	}
	if !strings.HasPrefix(written[0].Name(), "OPLC-2026-000123-") {
		t.Fatalf("report file is named %q, expected the serial number in it", written[0].Name())
	}
}

func TestMissingSerialNumberFailsRatherThanPassingEmpty(t *testing.T) {
	plan, err := ptplan.Parse([]byte(`{"schema":1,"name":"trace","limit_version":"v","steps":[
	  {"id":"sn","type":"ReadSN","source":"arg"}]}`))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	fake := newScriptBoard(t, func(cmd string, nth int) ([]string, []string) {
		lines, _ := standardReplies(cmd)
		return lines, nil
	})
	board := ptboard.New(fake, 0)
	t.Cleanup(func() { board.Close() })
	runner := &ptseq.Runner{OpenPeer: refusePeer, Board: board, Sleep: func(time.Duration) {}, ToolVersion: "test"}
	rep, err := runner.Run(plan)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.Steps[0].Outcome == ptreport.OutcomePass {
		t.Fatal("a run with no serial number must not pass a ReadSN step")
	}
}

func runWithTool(t *testing.T, planJSON string,
	tool func(string, []string, string, time.Duration) (string, int, error)) ptreport.Report {
	t.Helper()
	p, err := ptplan.Parse([]byte(planJSON))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	fake := newScriptBoard(t, func(cmd string, nth int) ([]string, []string) {
		lines, _ := standardReplies(cmd)
		return lines, nil
	})
	board := ptboard.New(fake, 0)
	t.Cleanup(func() { board.Close() })
	runner := &ptseq.Runner{OpenPeer: refusePeer,
		Board: board, Sleep: func(time.Duration) {}, ToolVersion: "test", RunTool: tool,
	}
	rep, err := runner.Run(p)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return rep
}

func attemptErr(s ptreport.StepResult) string {
	if len(s.Attempts) == 0 {
		return s.Reason
	}
	return s.Attempts[len(s.Attempts)-1].Err
}

// ---------- the plan that actually ships ----------

// TestBenchSmokePlanRuns drives the real plan file, so a mistake in it is
// found here rather than with a board on the bench and an engineer waiting.
//
// It also covers the echo answering: rs232 is loop=self, and its miss counter
// only stays at zero if the runner sends back the number the board gave it.
func TestBenchSmokePlanRuns(t *testing.T) {
	plan, err := ptplan.Load(filepath.Join("..", "..", "plans", "bench-smoke.json"))
	if err != nil {
		t.Fatalf("the plan we ship does not load: %v", err)
	}

	// One caps reply carrying every port the plan names, with the loop types
	// the firmware really reports.
	caps := []string{
		"OK porttool=0.10.0 ports=2 lines=5",
		"OK port=temp board=lower kind=session blk=- term=- channels=2 loop=ctrl params=ch,period running=0",
		"OK vals=temp ch=1,2 period=1000",
		"OK terms=temp SC-protect,HS-switch",
		"OK port=rs232 kind=session blk=C term=C05,C06 channels=1 loop=self params=period running=0",
		"OK vals=rs232 period=3000",
	}

	var echoed []string
	fake := newScriptBoard(t, func(cmd string, nth int) ([]string, []string) {
		switch {
		case cmd == "pt.caps":
			return caps, nil
		case cmd == "pt.id":
			return []string{"OK uid=003400413135511439303538 porttool=0.10.0"}, nil
		case cmd == "pt.run sdram.probe":
			return []string{
				"SDRAM_TEST: controller not up yet, running MX_FMC_Init()",
				"OK sdram.probe base=0xC0000000 size=67108864 ready=1 databus=1 addrbus=1",
			}, nil
		case cmd == "pt.run eth.link":
			// What the board printed on 2026-09-08 with a cable plugged in.
			return []string{
				"ETH_TEST: MDIO bring-up - PC1 = MDC, PA2 = MDIO, AF11",
				"ETH_TEST: PHY at address 0, id 0x0007C131, link UP, 100 Mbit/s full duplex",
				"OK eth.link found=1 addr=0 id=0x0007C131 link=1 autoneg=1 speed=100 fd=1 " +
					"bsr=0x782D scsr=0x1058 mdio_errors=0",
			}, nil
		case cmd == "pt.run rtc.read":
			return []string{
				"RTC_TEST: not up yet, running MX_RTC_Init()",
				"OK rtc.read init=0 clk=lse date=26-09-07 time=13:45:07",
			}, nil
		case cmd == "pt.run led.blink":
			return []string{
				"OK led.blink pin=PE2 pulses=6 half_ms=250 observed=unknown",
			}, nil
		case cmd == "pt.start temp ch=1,2 period=200":
			return []string{"OK temp started"}, []string{
				"!temp t=1 seq=1 rx=0 miss=0 vdda=2501 ok=1 ch1=738/238 ch2=751/251",
				"!temp t=2 seq=2 rx=1 miss=0 vdda=2501 ok=1 ch1=738/238 ch2=751/251",
				"!temp t=3 seq=3 rx=2 miss=0 vdda=2501 ok=1 ch1=739/239 ch2=750/250",
			}
		case cmd == "pt.start rs232 period=500":
			return []string{"OK rs232 started"}, []string{
				"!rs232 t=1 seq=1 rx=0 miss=0 rxlines=4",
				"!rs232 t=2 seq=2 rx=1 miss=0 rxlines=5",
				"!rs232 t=3 seq=3 rx=2 miss=0 rxlines=6",
				"!rs232 t=4 seq=4 rx=3 miss=0 rxlines=7",
			}
		case strings.HasPrefix(cmd, "pt.echo "):
			echoed = append(echoed, cmd)
			return []string{"OK " + strings.TrimPrefix(cmd, "pt.echo ") + " noted"}, nil
		case cmd == "pt.stop all":
			return []string{"OK all stopped"}, nil
		case strings.HasPrefix(cmd, "pt.stop "):
			return []string{"OK " + strings.TrimPrefix(cmd, "pt.stop ") + " stopped"}, nil
		}
		return []string{"ERR unexpected " + cmd}, nil
	})

	board := ptboard.New(fake, 0)
	t.Cleanup(func() { board.Close() })
	parsed, err := ptproto.ParseCaps(caps)
	if err != nil {
		t.Fatalf("caps fixture: %v", err)
	}

	runner := &ptseq.Runner{OpenPeer: refusePeer,
		Board: board, Caps: &parsed,
		Sleep: func(time.Duration) {}, ToolVersion: "test",
	}
	rep, err := runner.Run(plan)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	for _, s := range rep.Steps {
		if s.Outcome != ptreport.OutcomePass {
			t.Errorf("step %q is %s: %s", s.ID, s.Outcome, s.Reason)
			for _, a := range s.Attempts {
				t.Logf("  attempt %d: %s %s", a.N, a.Outcome, a.Err)
				for _, c := range a.Checks {
					t.Logf("    %s -> got %q pass=%v %s",
						c.Check.Describe(), c.Got, c.Pass, c.Why)
				}
			}
		}
	}
	if !rep.Passed() {
		t.Fatal("the smoke plan should pass against a board answering correctly")
	}

	// loop=ctrl (temp) and loop=self (rs232) both get answered; a loop=link
	// port would not, but this plan has none.
	if len(echoed) != 7 {
		t.Fatalf("expected one echo per frame from both ports, got %v", echoed)
	}
}

// ---------- the report ----------

func TestReportCarriesTheLimitItJudgedBy(t *testing.T) {
	// Without the limit in the report, a pass six months old cannot be told
	// apart from a pass under looser limits.
	rep, _ := runPlan(t,
		`{"schema":1,"name":"vdda","limit_version":"2026-09-07-a","steps":[
		  {"id":"vdda","type":"PtSession","port":"ain","timeout_ms":2000,
		   "checks":[{"field":"vdda","op":"range","min":2000,"max":3600,"unit":"mV"}]}]}`,
		func(cmd string, nth int) ([]string, []string) {
			if lines, ok := standardReplies(cmd); ok {
				return lines, nil
			}
			if strings.HasPrefix(cmd, "pt.start ain") {
				return []string{"OK ain started"}, []string{"!ain t=1 seq=1 rx=0 miss=0 vdda=3287 ok=1 ch1=21850/2071"}
			}
			return []string{"ERR unexpected " + cmd}, nil
		})

	var csv strings.Builder
	if err := rep.WriteCSV(&csv); err != nil {
		t.Fatalf("writing csv: %v", err)
	}
	for _, want := range []string{"2026-09-07-a", "vdda in 2000..3600 mV", "3287"} {
		if !strings.Contains(csv.String(), want) {
			t.Fatalf("the csv is missing %q:\n%s", want, csv.String())
		}
	}

	var buf strings.Builder
	if err := rep.WriteJSON(&buf); err != nil {
		t.Fatalf("writing json: %v", err)
	}
	var back ptreport.Report
	if err := json.Unmarshal([]byte(buf.String()), &back); err != nil {
		t.Fatalf("the report does not read back: %v", err)
	}
	if back.Steps[0].Attempts[0].Checks[0].Got != "3287" {
		t.Fatalf("the raw reading did not survive the round trip: %+v", back.Steps[0].Attempts[0].Checks)
	}
}

func TestPlanRunTargetsCheckedAgainstWhatTheBoardReports(t *testing.T) {
	// A pt.run target is a string in a JSON file. Without caps carrying the
	// target list, a typo in it is only found by a station operator halfway
	// through a run, with the board in front of them.
	caps, err := ptproto.ParseCaps(capsReply())
	if err != nil {
		t.Fatalf("parsing the caps fixture: %v", err)
	}

	plan, err := ptplan.Parse([]byte(`{"schema":1,"name":"p","limit_version":"v","steps":[
	  {"id":"typo","type":"PtRun","target":"sdram.sweeep",
	   "checks":[{"field":"mismatches","op":"eq","value":"0"}]},
	  {"id":"not-a-run-target","type":"PtRun","target":"sdram.capacity",
	   "checks":[{"field":"mismatches","op":"eq","value":"0"}]},
	  {"id":"real","type":"PtRun","target":"sdram.sweep",
	   "checks":[{"field":"mismatches","op":"eq","value":"0"}]}]}`))
	if err != nil {
		t.Fatalf("this plan is well formed, so it should parse: %v", err)
	}

	findings := strings.Join(plan.CheckAgainstCaps(caps), "\n")
	if !strings.Contains(findings, "typo") {
		t.Fatalf("a misspelled run target was not caught:\n%s", findings)
	}
	// A name the firmware does not report as a run target is refused, however
	// much it looks like one: pt.run would answer ERR and the step would hang
	// waiting for a reply shape that never comes.
	if !strings.Contains(findings, "not-a-run-target") {
		t.Fatalf("an unreported target was not caught:\n%s", findings)
	}
	if strings.Contains(findings, `"real"`) {
		t.Fatalf("a target the firmware reports was reported anyway:\n%s", findings)
	}
}

func TestPlanRunTargetsUncheckedAgainstOlderFirmware(t *testing.T) {
	// Firmware before 0.9.0 sent no run rows. Reporting every PtRun step as
	// unknown then would bury the findings that are real, so silence is the
	// honest answer - the plan may well be correct.
	caps, err := ptproto.ParseCaps([]string{
		"OK porttool=0.4.0 ports=1 lines=3",
		"OK port=din board=upper kind=session blk=D term=D02-D09 channels=8 loop=ctrl params=ch,period running=0",
		"OK vals=din ch=1 period=200",
		"OK limits=din ch:1..8 period:50..",
	})
	if err != nil {
		t.Fatalf("parsing the older caps reply: %v", err)
	}

	plan, err := ptplan.Parse([]byte(`{"schema":1,"name":"p","limit_version":"v","steps":[
	  {"id":"one-shot","type":"PtRun","target":"sdram.sweep",
	   "checks":[{"field":"mismatches","op":"eq","value":"0"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}

	if findings := plan.CheckAgainstCaps(caps); len(findings) != 0 {
		t.Fatalf("older firmware produced findings it cannot support:\n%s",
			strings.Join(findings, "\n"))
	}
}

// TestStation6PlanRuns drives the production plan file, so a mistake in it -
// a field name the firmware does not send, a target that does not exist, a
// parameter string the port would refuse - is found here rather than at a
// station with an operator waiting.
//
// The frames below are the shapes the firmware really prints, copied from the
// T4-01 transcript. That is the point: a fake board answering in some other shape
// would let a broken plan pass here.
func TestStation6PlanRuns(t *testing.T) {
	plan, err := ptplan.Load(filepath.Join("..", "..", "plans", "station6-poweron.json"))
	if err != nil {
		t.Fatalf("the plan we ship does not load: %v", err)
	}

	caps := []string{
		"OK porttool=0.10.0 ports=17 lines=33",
		"OK port=din board=upper kind=session blk=D term=D02-D09 channels=8 loop=ctrl params=ch,period running=0",
		"OK vals=din ch=1,2,3,4,5,6,7,8 period=200",
		"OK port=dout board=lower kind=session blk=A term=A03-A10 channels=8 loop=ctrl params=ch,mode,duty,freq,period running=0",
		"OK vals=dout ch=1 mode=hold duty=1:0 freq=1:1000,2:1000,3:1000,4:1000,5:1000,6:1000,7:1000,8:1000 period=500",
		"OK port=relay board=lower kind=session blk=B term=B01-B12 channels=6 loop=ctrl params=ch,mode,on,period running=0",
		"OK vals=relay ch=1 mode=hold on=1:0 period=1000",
		"OK port=temp board=lower kind=session blk=- term=- channels=2 loop=ctrl params=ch,period running=0",
		"OK vals=temp ch=1,2 period=1000",
		"OK port=ain board=upper kind=session blk=D term=D12,D13 channels=2 loop=ctrl params=ch,period running=0",
		"OK vals=ain ch=1,2 period=500",
		"OK port=aout board=upper kind=session blk=D term=D14,D15 channels=2 loop=ctrl params=ch,mv,period running=0",
		"OK vals=aout ch=1 mv=1:0 period=500",
		"OK port=rs232 board=upper kind=session blk=C term=C05,C06 channels=1 loop=self params=period running=0",
		"OK vals=rs232 period=3000",
		"OK port=rs485 board=upper kind=session blk=C term=C10,C11 channels=1 loop=link params=baud,period running=0 runs=rs485.pins",
		"OK vals=rs485 baud=115200 period=3000",
		"OK port=can board=upper kind=session blk=C term=C07,C08 channels=1 loop=link params=baud,mode,period running=0",
		"OK vals=can baud=500000 mode=extloop period=1000",
		"OK port=knx board=upper kind=session blk=C term=C03,C04 channels=1 loop=link params=mode,period running=0",
		"OK vals=knx mode=loopback period=1000",
		// sdram became a session on 2026-09-13: retention only means anything
		// over a long run, so the waiting moved onto the PC's clock instead of
		// blocking the board (DECISIONS.md 40). Its one-shots ride on that row.
		"OK port=sdram board=bridge kind=session blk=- term=U6 channels=1 loop=ctrl params=wait,period running=0 runs=sdram.probe,sdram.sweep,sdram.crc",
		"OK vals=sdram wait=5000 period=1000",
		"OK limits=sdram wait:1000.. period:50..",
		"OK port=sd board=bridge kind=session blk=- term=J6 channels=1 loop=ctrl params=period running=0 runs=sd.probe,sd.integrity,sd.speed",
		"OK vals=sd period=500",
		"OK limits=sd period:50..",
		// A session with a one-shot on the same row: the TCP server and the PHY
		// probe are one RJ45 (DECISIONS.md 28).
		"OK port=eth board=bridge kind=session blk=- term=J1 channels=1 loop=link params=mode,port,ip,period running=0 runs=eth.link",
		"OK vals=eth mode=echo port=5000 ip=dhcp period=1000",
		"OK port=usb board=bridge kind=session blk=- term=J2 channels=1 loop=link params=mode,period running=0",
		"OK vals=usb mode=echo period=1000",
		"OK port=rtc board=bridge kind=run blk=- term=- channels=1 loop=none runs=rtc.read",
		"OK port=reset board=bridge kind=run blk=- term=- channels=1 loop=none runs=reset.cause",
		"OK port=led board=bridge kind=run blk=- term=- channels=1 loop=none runs=led.blink",
	}

	fake := newScriptBoard(t, func(cmd string, nth int) ([]string, []string) {
		switch {
		case cmd == "pt.caps":
			return caps, nil
		case cmd == "pt.id":
			return []string{"OK uid=003400413135511439303538 porttool=0.10.0"}, nil

		case cmd == "pt.run sdram.probe":
			return []string{"OK sdram.probe base=0xC0000000 size=67108864 ready=1 databus=1 addrbus=1"}, nil
		case cmd == "pt.run sdram.sweep":
			return []string{
				"SDRAM_TEST: full sweep 0x00 OK (write 7100ms, verify 5400ms)",
				"OK sdram.sweep ready=1 patterns=4 words_each=16777216 mismatches=0 " +
					"first_bad=0x00000000 bad_pattern=0x00000000 write_ms=28400 verify_ms=21600",
			}, nil
		// Added with the targets themselves, 2026-09-13. This board is hand
		// written: a target the plan names and this switch does not answer is
		// reported as "unexpected", not as a board that said nothing.
		// The cause is whatever this board says it came up from. POR is the
		// ordinary one at a test station; the step only refuses IWDG and WWDG,
		// which mean the board hung and its own watchdog restarted it.
		case cmd == "pt.run reset.cause":
			return []string{"OK reset.cause cause=POR rsr=0x10000000"}, nil
		case cmd == "pt.run rs485.pins":
			return []string{
				"OK rs485.pins checked=1 busy=0 dir_low=0 tx_low=0 dir_high=1 tx_high=1 follows=1",
			}, nil
		case cmd == "pt.run sdram.crc bytes=65536 offset=0":
			return []string{
				"OK sdram.crc ready=1 offset=0 bytes=65536 crc=0x1A2B3C4D",
			}, nil
		case cmd == "pt.run sd.probe":
			return []string{"OK sd.probe detected=1 ready=1 blocks=62333952 block_size=512 " +
				"mib=30436 v2x=1 class=1461 fs=fat32 err=0x00000000"}, nil
		case cmd == "pt.run sd.integrity bytes=1048576":
			return []string{
				"OK sd.integrity mounted=1 identical=1 passes=1 passed=1 " +
					"bytes_each=1048576 bytes_total=1048576 elapsed_ms=210 " +
					"first_bad_pass=0 fresult=0",
			}, nil
		// ⚠️ Rate only, and the plan records rather than judges it - there is
		// no measured threshold yet. Answering with a plausible number keeps
		// the step exercised without asserting a limit nobody has set.
		case cmd == "pt.run sd.speed bytes=1048576":
			return []string{
				"OK sd.speed mounted=1 bytes=1048576 write_ms=2000 read_ms=1000 " +
					"write_bps=524288 read_bps=1048576 fresult=0",
			}, nil
		// The stress step is the same target with more rounds (DECISIONS.md 48).
		case cmd == "pt.run sd.integrity passes=64":
			return []string{
				"SDCARD_TEST: stress - 64 rounds of 4096 bytes write/read/verify",
				"OK sd.integrity mounted=1 identical=1 passes=64 passed=64 " +
					"bytes_each=4096 bytes_total=262144 elapsed_ms=9130 " +
					"first_bad_pass=0 fresult=0",
			}, nil
		case cmd == "pt.run eth.link":
			// What the board printed on 2026-09-08 with a cable plugged in.
			return []string{
				"ETH_TEST: MDIO bring-up - PC1 = MDC, PA2 = MDIO, AF11",
				"ETH_TEST: PHY at address 0, id 0x0007C131, link UP, 100 Mbit/s full duplex",
				"OK eth.link found=1 addr=0 id=0x0007C131 link=1 autoneg=1 speed=100 fd=1 " +
					"bsr=0x782D scsr=0x1058 mdio_errors=0",
			}, nil
		case cmd == "pt.run rtc.read":
			return []string{"OK rtc.read init=0 clk=lse date=26-09-08 time=09:14:22"}, nil
		case cmd == "pt.run led.blink":
			return []string{"OK led.blink pin=PE2 pulses=6 half_ms=250 observed=unknown"}, nil

		case cmd == "pt.start din ch=1,2,3,4,5,6,7,8 period=200":
			return []string{"OK din started"}, []string{
				"!din t=1 seq=1 rx=0 miss=0 v=0xFF ch1=1 ch8=1",
				"!din t=2 seq=2 rx=1 miss=0 v=0xFF ch1=1 ch8=1",
				"!din t=3 seq=3 rx=2 miss=0 v=0xFF ch1=1 ch8=1",
			}
		case strings.HasPrefix(cmd, "pt.start dout "):
			return []string{"OK dout started"}, []string{
				"!dout t=1 seq=1 rx=0 miss=0 mode=hold tick=100000 ch1=100 ch8=100",
				"!dout t=2 seq=2 rx=1 miss=0 mode=hold tick=100000 ch1=100 ch8=100",
			}
		case strings.HasPrefix(cmd, "pt.start relay "):
			return []string{"OK relay started"}, []string{
				"!relay t=1 seq=1 rx=0 miss=0 mode=hold ch1=1 ch6=1",
				"!relay t=2 seq=2 rx=1 miss=0 mode=hold ch1=1 ch6=1",
			}
		case cmd == "pt.start temp ch=1,2 period=200":
			return []string{"OK temp started"}, []string{
				"!temp t=1 seq=1 rx=0 miss=0 vdda=2501 ok=1 ch1=807/307 ch2=814/314",
				"!temp t=2 seq=2 rx=1 miss=0 vdda=2501 ok=1 ch1=790/290 ch2=825/325",
				"!temp t=3 seq=3 rx=2 miss=0 vdda=2501 ok=1 ch1=809/309 ch2=813/313",
			}
		case cmd == "pt.start ain ch=1,2 period=200":
			return []string{"OK ain started"}, []string{
				"!ain t=1 seq=1 rx=0 miss=0 vdda=2501 ok=1 ch1=21850/2071 ch2=8300/787",
				"!ain t=2 seq=2 rx=1 miss=0 vdda=2501 ok=1 ch1=21851/2071 ch2=8301/787",
				"!ain t=3 seq=3 rx=2 miss=0 vdda=2501 ok=1 ch1=21849/2071 ch2=8299/787",
			}
		case strings.HasPrefix(cmd, "pt.start aout "):
			return []string{"OK aout started"}, []string{
				"!aout t=1 seq=1 rx=0 miss=0 ch1=1000/999/9756 ch2=2000/1999/19512",
				"!aout t=2 seq=2 rx=1 miss=0 ch1=1000/999/9756 ch2=2000/1999/19512",
			}
		case cmd == "pt.start rs232 period=400":
			return []string{"OK rs232 started"}, []string{
				"!rs232 t=1 seq=1 rx=0 miss=0 rxlines=4",
				"!rs232 t=2 seq=2 rx=1 miss=0 rxlines=5",
				"!rs232 t=3 seq=3 rx=2 miss=0 rxlines=6",
				"!rs232 t=4 seq=4 rx=3 miss=0 rxlines=7",
			}
		// The eth session, answered: conn=1 and the counter closing.
		// ⚠️ The parameter order is the one the runner sends, alphabetical - a
		// mismatch reads as "unexpected command" and looks like a firmware
		// fault rather than a stale fake.
		case cmd == "pt.start eth ip=dhcp mode=echo period=500 port=5000":
			return []string{"OK eth started"}, []string{
				"!eth t=1 seq=1 rx=0 miss=0 ip=192.168.0.30 link=1 conn=1 mode=echo port=5000 rx_bytes=0 tx_bytes=2 kbps=0",
				"!eth t=2 seq=2 rx=1 miss=0 ip=192.168.0.30 link=1 conn=1 mode=echo port=5000 rx_bytes=2 tx_bytes=4 kbps=0",
				"!eth t=3 seq=3 rx=2 miss=0 ip=192.168.0.30 link=1 conn=1 mode=echo port=5000 rx_bytes=4 tx_bytes=6 kbps=0",
				"!eth t=4 seq=4 rx=3 miss=0 ip=192.168.0.30 link=1 conn=1 mode=echo port=5000 rx_bytes=6 tx_bytes=8 kbps=0",
			}
		// 手写假板子，加端口/加步骤都要跟一次。sd 2026-09-10 变成会话之后
		// station6 多了 sd-detect 这一步。
		// wait=1000 is the floor: cells hold for tens of milliseconds without
		// refresh, so a second already proves refresh is running, and cycles
		// has to reach 1 or "failed=0" would be a step that never ran.
		case cmd == "pt.start sdram period=500 wait=1000":
			return []string{"OK started sdram"}, []string{
				"!sdram t=1 seq=1 rx=0 miss=0 ready=1 phase=wait cycles=0 checked=64 failed=0 first_bad=0x00000000 wait_ms=1000 seed=0x11111111",
				"!sdram t=2 seq=2 rx=1 miss=0 ready=1 phase=verify cycles=0 checked=64 failed=0 first_bad=0x00000000 wait_ms=1000 seed=0x11111111",
				"!sdram t=3 seq=3 rx=2 miss=0 ready=1 phase=write cycles=1 checked=64 failed=0 first_bad=0x00000000 wait_ms=1000 seed=0x22222222",
				"!sdram t=4 seq=4 rx=3 miss=0 ready=1 phase=wait cycles=1 checked=64 failed=0 first_bad=0x00000000 wait_ms=1000 seed=0x22222222",
				"!sdram t=5 seq=5 rx=4 miss=0 ready=1 phase=verify cycles=1 checked=64 failed=0 first_bad=0x00000000 wait_ms=1000 seed=0x22222222",
				"!sdram t=6 seq=6 rx=5 miss=0 ready=1 phase=write cycles=2 checked=64 failed=0 first_bad=0x00000000 wait_ms=1000 seed=0x33333333",
			}
		case cmd == "pt.start sd period=300":
			return []string{"OK sd started"}, []string{
				"!sd t=1 seq=1 rx=0 miss=0 detected=1 changes=0 in=0 out=0",
				"!sd t=2 seq=2 rx=1 miss=0 detected=1 changes=0 in=0 out=0",
				"!sd t=3 seq=3 rx=2 miss=0 detected=1 changes=0 in=0 out=0",
			}
		case cmd == "pt.start usb mode=echo period=500":
			return []string{"OK usb started"}, []string{
				"!usb t=1 seq=1 rx=0 miss=0 state=cfg enum=0 mode=echo rx_bytes=0 tx_bytes=2 kbps=0 busy=0",
				"!usb t=2 seq=2 rx=1 miss=0 state=cfg enum=0 mode=echo rx_bytes=2 tx_bytes=4 kbps=0 busy=0",
				"!usb t=3 seq=3 rx=2 miss=0 state=cfg enum=0 mode=echo rx_bytes=4 tx_bytes=6 kbps=0 busy=0",
				"!usb t=4 seq=4 rx=3 miss=0 state=cfg enum=0 mode=echo rx_bytes=6 tx_bytes=8 kbps=0 busy=0",
			}
		case cmd == "pt.start rs485 baud=115200 period=2000":
			return []string{"OK rs485 started"}, []string{
				"!rs485 t=1 seq=1 rx=0 miss=0 baud=115200 rxbytes=8 junk=0 overrun=0",
				"!rs485 t=2 seq=2 rx=1 miss=0 baud=115200 rxbytes=16 junk=0 overrun=0",
				"!rs485 t=3 seq=3 rx=2 miss=0 baud=115200 rxbytes=24 junk=0 overrun=0",
				"!rs485 t=4 seq=4 rx=3 miss=0 baud=115200 rxbytes=32 junk=0 overrun=0",
			}
		// Exactly what the board printed in mode=extloop on 2026-09-08:
		// seq advancing every period with rx_frames tracking tx, which is
		// what the fixed wait-for-your-own-frame tick produces.
		case cmd == "pt.start can baud=500000 mode=extloop period=300":
			return []string{"OK can started"}, []string{
				"!can t=1 seq=1 rx=0 miss=0 bps=500000 mode=extloop alive=1 tx=1 rx_frames=1 junk=0 tec=0 rec=0 lec=0",
				"!can t=2 seq=2 rx=1 miss=0 bps=500000 mode=extloop alive=1 tx=2 rx_frames=2 junk=0 tec=0 rec=0 lec=0",
				"!can t=3 seq=3 rx=2 miss=0 bps=500000 mode=extloop alive=1 tx=3 rx_frames=3 junk=0 tec=0 rec=0 lec=0",
				"!can t=4 seq=4 rx=3 miss=0 bps=500000 mode=extloop alive=1 tx=4 rx_frames=4 junk=0 tec=0 rec=0 lec=0",
			}
		case cmd == "pt.start knx mode=loopback period=1000":
			return []string{"OK knx started"}, []string{
				"!knx t=1 seq=1 rx=0 miss=0 mode=loopback bus=ok vcc=1 ok=1 idle=0 pulses=26 dropped=0 chars=2 bad=0 w_avg=35 d_avg=7 quiet_ms=12",
				"!knx t=2 seq=2 rx=1 miss=0 mode=loopback bus=ok vcc=1 ok=1 idle=0 pulses=52 dropped=0 chars=4 bad=0 w_avg=35 d_avg=7 quiet_ms=12",
				"!knx t=3 seq=3 rx=2 miss=0 mode=loopback bus=ok vcc=1 ok=1 idle=0 pulses=78 dropped=0 chars=6 bad=0 w_avg=35 d_avg=7 quiet_ms=12",
			}

		case strings.HasPrefix(cmd, "pt.echo "):
			return []string{"OK " + strings.TrimPrefix(cmd, "pt.echo ") + " noted"}, nil
		case cmd == "pt.stop all":
			return []string{"OK all stopped"}, nil
		case strings.HasPrefix(cmd, "pt.stop "):
			return []string{"OK " + strings.TrimPrefix(cmd, "pt.stop ") + " stopped"}, nil
		}
		return []string{"ERR unexpected " + cmd}, nil
	})

	board := ptboard.New(fake, 0)
	t.Cleanup(func() { board.Close() })
	parsed, err := ptproto.ParseCaps(caps)
	if err != nil {
		t.Fatalf("caps fixture: %v", err)
	}

	// A production run has an operator, so the indicator step gets an answer.
	// Leaving Confirm nil is what a headless run does, and that turns the step
	// into an error rather than a silent pass - covered elsewhere.
	runner := &ptseq.Runner{OpenPeer: refusePeer,
		Board: board, Caps: &parsed,
		Sleep:       func(time.Duration) {},
		ToolVersion: "test",
		Confirm:     func(string) (bool, error) { return true, nil },
		// The RS485 step's peer is "serial": somebody has bound an adapter.
		SerialPeer: func(string) (string, int, bool) { return "bound-adapter", 0, true },
		// The plan's last step archives the report to "dir:reports/station6",
		// relative to BaseDir. Left empty that resolves against the working
		// directory, so every `go test` dropped another report into the source
		// tree - 48 of them had piled up by 2026-09-08. A temp dir puts them
		// where the run can still be inspected and Go deletes them afterwards.
		BaseDir: t.TempDir(),
	}
	rep, err := runner.Run(plan)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// Steps the plan deliberately ships switched off, each with a _note saying
	// why. Named here rather than skipped over generically, so a step that
	// quietly stops running still fails this test.
	offByDesign := map[string]bool{
		// PLACEHOLDER being replaced by a USB test inside the tool image, with
		// selectable modes. See the step's own note.
		"usb-enumerates": true,
	}

	for _, s := range rep.Steps {
		if offByDesign[s.ID] {
			if s.Outcome != ptreport.OutcomeSkipped {
				t.Errorf("step %q is meant to ship switched off, but it %s",
					s.ID, s.Outcome)
			}
			continue
		}
		if s.Outcome != ptreport.OutcomePass {
			t.Errorf("step %q is %s: %s", s.ID, s.Outcome, s.Reason)
			for _, a := range s.Attempts {
				t.Logf("  attempt %d: %s %s", a.N, a.Outcome, a.Err)
				for _, c := range a.Checks {
					t.Logf("    %s -> got %q pass=%v %s",
						c.Check.Describe(), c.Got, c.Pass, c.Why)
				}
			}
		}
	}
	if !rep.Passed() {
		t.Fatal("the station 6 plan should pass against a board answering correctly")
	}

	// The plan has to survive its own offline check too: a target or parameter
	// this firmware does not report would be a plan that only fails on a line.
	if findings := plan.CheckAgainstCaps(parsed); len(findings) != 0 {
		t.Fatalf("the shipped plan disagrees with the caps it was written for:\n%s",
			strings.Join(findings, "\n"))
	}
}

// ---------- serial peer ----------

// A plan never names the adapter (DECISIONS.md 73 in $PROD): the runner asks
// for the one recorded on this machine, and with none it stops before starting
// the port - otherwise the step fails on miss and reads like a broken board.
const serialPeerPlan = `{"schema":1,"name":"rs485","limit_version":"2026-09-30","steps":[
  {"id":"rs485","type":"PtSession","port":"rs485","frames":1,"timeout_ms":2000,
   "peer":{"serial":true},
   "checks":[{"field":"miss","op":"count_zero"}]}]}`

func serialPeerReplies(cmd string, nth int) ([]string, []string) {
	if lines, ok := standardReplies(cmd); ok {
		return lines, nil
	}
	if strings.HasPrefix(cmd, "pt.start rs485") {
		return []string{"OK rs485 started"}, []string{"!rs485 t=1 seq=1 rx=0 miss=0 junk=0 overrun=0"}
	}
	return []string{"ERR unexpected " + cmd}, nil
}

func runSerialPeer(t *testing.T, chosen func(string) (string, int, bool), open func(string, string, int) (io.ReadWriteCloser, error)) (ptreport.Report, *scriptBoard) {
	t.Helper()
	plan, err := ptplan.Parse([]byte(serialPeerPlan))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	fake := newScriptBoard(t, serialPeerReplies)
	board := ptboard.New(fake, 0)
	t.Cleanup(func() { board.Close() })
	runner := &ptseq.Runner{OpenPeer: open, SerialPeer: chosen,
		Board: board, Sleep: func(time.Duration) {}, ToolVersion: "test"}
	rep, err := runner.Run(plan)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return rep, fake
}

func TestSerialPeerNotChosenStopsBeforeTheBoard(t *testing.T) {
	rep, fake := runSerialPeer(t, func(string) (string, int, bool) { return "", 0, false }, refusePeer)
	st := rep.Steps[0]
	if st.Outcome != ptreport.OutcomeError {
		t.Fatalf("outcome %s, want ERROR", st.Outcome)
	}
	if err := st.Attempts[len(st.Attempts)-1].Err; !strings.Contains(err, "bind one") {
		t.Fatalf("the error should say what to do, got %q", err)
	}
	for _, c := range fake.commands() {
		if strings.HasPrefix(c, "pt.start rs485") {
			t.Fatalf("the port was started with no peer; commands %v", fake.commands())
		}
	}
}

func TestSerialPeerOpensTheRecordedAdapter(t *testing.T) {
	var gotAddr string
	var gotBaud int
	open := func(kind, addr string, baud int) (io.ReadWriteCloser, error) {
		gotAddr, gotBaud = addr, baud
		return nil, fmt.Errorf("fake")
	}
	runSerialPeer(t, func(port string) (string, int, bool) {
		if port != "rs485" {
			t.Errorf("asked for %q, want rs485", port)
		}
		return "/dev/ttyUSB0", 57600, true
	}, open)
	if gotAddr != "/dev/ttyUSB0" || gotBaud != 57600 {
		t.Fatalf("opened %q at %d, want /dev/ttyUSB0 at 57600", gotAddr, gotBaud)
	}
}
