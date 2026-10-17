package testcase

// T4-04, the reference board and the manual reading (decision 103): a plan
// step with on "ref" runs on the second board connection beside the step after
// it, and a UserReading's number is judged by ptcheck and kept as typed.
// Rules: $PROD/docs/modules/M4/PRODUCTION-FRAMEWORK.md "在标准机上跑（on）".

import (
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"PortTool/src/ptboard"
	"PortTool/src/ptplan"
	"PortTool/src/ptproto"
	"PortTool/src/ptreport"
	"PortTool/src/ptseq"
	"PortTool/src/simboard"
)

// withoutRefPairs drops every reference-board step and the step it
// accompanies, for a run with no second board.
func withoutRefPairs(p ptplan.Plan) ptplan.Plan {
	var keep []ptplan.Step
	for i := 0; i < len(p.Steps); i++ {
		if p.Steps[i].IsRefCompanion() {
			i++
			continue
		}
		if p.Steps[i].OnRef() {
			continue
		}
		keep = append(keep, p.Steps[i])
	}
	p.Steps = keep
	return p
}

// verdicts is the outcomes in order, space separated.
func verdicts(r ptreport.Report) string {
	var out []string
	for _, s := range r.Steps {
		out = append(out, string(s.Outcome))
	}
	return strings.Join(out, " ")
}

// bench is two scripted boards sharing one command log, so a test can see
// the order across both. A started session streams frames until stopped.
type bench struct {
	mu  sync.Mutex
	log []string
}

func (b *bench) note(s string) {
	b.mu.Lock()
	b.log = append(b.log, s)
	b.mu.Unlock()
}

func (b *bench) index(s string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, l := range b.log {
		if l == s {
			return i
		}
	}
	return -1
}

// board makes one side. frames maps a session port to the frame it streams.
func (b *bench) board(t *testing.T, name string, frames map[string]string) *ptboard.Board {
	t.Helper()
	var fake *scriptBoard
	stops := map[string]chan struct{}{}
	var smu sync.Mutex
	fake = newScriptBoard(t, func(cmd string, nth int) ([]string, []string) {
		b.note(name + ":" + cmd)
		switch {
		case strings.HasPrefix(cmd, "pt.hold "):
			return []string{"OK hold=" + strings.TrimPrefix(cmd, "pt.hold ")}, nil
		case strings.HasPrefix(cmd, "pt.start "):
			port := strings.Fields(cmd)[1]
			line, ok := frames[port]
			if !ok {
				return []string{"ERR no such port " + port}, nil
			}
			stop := make(chan struct{})
			smu.Lock()
			stops[port] = stop
			smu.Unlock()
			go func() {
				for {
					select {
					case <-stop:
						return
					case <-time.After(20 * time.Millisecond):
						fake.emit(line)
					}
				}
			}()
			return []string{"OK " + port + " started"}, nil
		case strings.HasPrefix(cmd, "pt.stop "):
			port := strings.TrimPrefix(cmd, "pt.stop ")
			smu.Lock()
			if st, ok := stops[port]; ok {
				close(st)
				delete(stops, port)
			}
			smu.Unlock()
		}
		if lines, ok := standardReplies(cmd); ok {
			return lines, nil
		}
		return []string{"ERR unexpected " + cmd}, nil
	})
	board := ptboard.New(fake, 0)
	t.Cleanup(func() {
		smu.Lock()
		for _, st := range stops {
			close(st)
		}
		stops = map[string]chan struct{}{}
		smu.Unlock()
		board.Close()
	})
	return board
}

const pairPlan = `{"schema":1,"name":"pair","limit_version":"v","steps":[
  {"id":"walk-ref","type":"PtSession","on":"ref","execute_condition":"ALWAYS",
   "port":"dout","params":{"ch":"3","duty":255},"timeout_ms":2000,
   "checks":[{"field":"ch3","op":"eq","value":"255"}]},
  {"id":"walk","type":"PtSession","port":"din","frames":2,"timeout_ms":2000,
   "checks":[{"field":"v","op":"eq","value":"0x04"}]},
  {"id":"after","type":"PtRaw","command":"pt.id","checks":[{"field":"uid","op":"contains","value":"00"}]}]}`

func runPair(t *testing.T, planJSON string, dinV string, withRef bool) (ptreport.Report, *bench) {
	t.Helper()
	plan, err := ptplan.Parse([]byte(planJSON))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	b := &bench{}
	dut := b.board(t, "dut", map[string]string{"din": "!din t=1 seq=1 rx=0 miss=0 v=" + dinV})
	runner := &ptseq.Runner{OpenPeer: refusePeer, FindCDC: noCDC, Board: dut,
		Sleep: func(time.Duration) {}, ToolVersion: "test"}
	if withRef {
		runner.Ref = b.board(t, "ref", map[string]string{"dout": "!dout t=1 seq=1 rx=0 miss=0 mode=hold res=8 tick=256000 ch3=255"})
	}
	rep, err := runner.Run(plan)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return rep, b
}

func TestRefSessionStartsBeforeAndStopsAfterTheStepItAccompanies(t *testing.T) {
	rep, b := runPair(t, pairPlan, "0x04", true)
	if got := verdicts(rep); got != "PASS PASS PASS" {
		t.Fatalf("outcomes %s, want PASS PASS PASS; %+v", got, rep.Steps)
	}
	order := []string{
		"ref:pt.hold 6000",
		"ref:pt.start dout ch=3 duty=255",
		"dut:pt.start din",
		"dut:pt.stop din",
		"ref:pt.stop dout",
		"ref:pt.hold 0",
	}
	last := -1
	for _, s := range order {
		i := b.index(s)
		if i < 0 || i < last {
			t.Fatalf("%q missing or out of order in %v", s, b.log)
		}
		last = i
	}
	if rep.RefUID == "" || rep.RefFirmware == "" {
		t.Errorf("the report should name the reference board, got uid=%q fw=%q", rep.RefUID, rep.RefFirmware)
	}
	if rep.Steps[0].ID != "walk-ref" || len(rep.Steps[0].Attempts[0].Raw) == 0 {
		t.Errorf("the reference step should keep its raw frames: %+v", rep.Steps[0])
	}
}

func TestDeadmanIsRenewedWhileTheReferenceDrives(t *testing.T) {
	// A step long enough for at least one renewal (every 2 s).
	plan := strings.Replace(pairPlan, `"frames":2,"timeout_ms":2000`, `"frames":130,"timeout_ms":8000`, 1)
	_, b := runPair(t, plan, "0x04", true)
	n := 0
	b.mu.Lock()
	for _, l := range b.log {
		if l == "ref:pt.hold 6000" {
			n++
		}
	}
	b.mu.Unlock()
	if n < 2 {
		t.Fatalf("the deadman was armed %d time(s) and never renewed: %v", n, b.log)
	}
}

func TestAFailingPairStopsThePassGatedStepAfterIt(t *testing.T) {
	rep, _ := runPair(t, pairPlan, "0x0C", true) // DI4 reads high too: shorted
	if got := verdicts(rep); got != "PASS FAIL SKIPPED" {
		t.Fatalf("outcomes %s, want PASS FAIL SKIPPED", got)
	}
}

func TestNoReferenceBoardIsAnErrorNotARunOnTheBoardUnderTest(t *testing.T) {
	rep, b := runPair(t, pairPlan, "0x04", false)
	if rep.Steps[0].Outcome != ptreport.OutcomeError || rep.Steps[1].Outcome != ptreport.OutcomeSkipped {
		t.Fatalf("outcomes %s, want ERROR SKIPPED", verdicts(rep))
	}
	if b.index("dut:pt.start dout ch=3 duty=255") >= 0 || b.index("dut:pt.start din") >= 0 {
		t.Fatalf("something ran on the board under test instead: %v", b.log)
	}
	if rep.Passed() {
		t.Fatal("a run missing its reference board must not pass")
	}
}

func TestSwitchedOffPairSkipsBoth(t *testing.T) {
	plan := strings.Replace(pairPlan, `"on":"ref",`, `"on":"ref","enabled":false,`, 1)
	plan = strings.Replace(plan, `{"id":"walk","type":"PtSession",`, `{"id":"walk","type":"PtSession","enabled":false,`, 1)
	rep, b := runPair(t, plan, "0x04", true)
	if !strings.HasPrefix(verdicts(rep), "SKIPPED SKIPPED") {
		t.Fatalf("outcomes %s", verdicts(rep))
	}
	if b.index("ref:pt.hold 6000") >= 0 {
		t.Fatal("a switched-off pair still armed the reference board")
	}
}

func TestPlanRejectsPairsThatCannotRunAsOne(t *testing.T) {
	ref := `{"id":"r","type":"PtSession","on":"ref","port":"dout","checks":[{"field":"ch1","op":"eq","value":"255"}]}`
	dut := `{"id":"d","type":"PtSession","port":"din","checks":[{"field":"v","op":"eq","value":"1"}]}`
	wrap := func(steps ...string) string {
		return `{"schema":1,"name":"p","limit_version":"v","steps":[` + strings.Join(steps, ",") + `]}`
	}
	for _, tc := range []struct{ name, plan, want string }{
		{"reference session with nothing after it", wrap(ref), "no step after it"},
		{"two reference sessions in a row", wrap(ref, strings.Replace(ref, `"r"`, `"r2"`, 1), dut), "reference board too"},
		{"own gate on the accompanied step", wrap(ref, strings.Replace(dut, `"type"`, `"execute_condition":"ALWAYS","type"`, 1)), "drop execute_condition"},
		{"only one of the pair switched off", wrap(ref, strings.Replace(dut, `"type"`, `"enabled":false,"type"`, 1)), "both are enabled or neither"},
		{"retries on the reference step", wrap(strings.Replace(ref, `"type"`, `"retry_count":1,"type"`, 1), dut), "retries belong"},
		{"a person on the reference board", wrap(`{"id":"q","type":"UserConfirm","on":"ref","prompt":"ok?"}`), "only runs on the board under test"},
		{"unknown on", wrap(strings.Replace(dut, `"type"`, `"on":"golden","type"`, 1)), "unknown on"},
		{"accompanying a person", wrap(ref, `{"id":"q","type":"UserConfirm","prompt":"ok?"}`), "does not talk to the board"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ptplan.Parse([]byte(tc.plan))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestRefStepsAreCheckedAgainstTheReferenceCaps(t *testing.T) {
	plan, err := ptplan.Parse([]byte(`{"schema":1,"name":"p","limit_version":"v","steps":[
	  {"id":"r","type":"PtSession","on":"ref","port":"nosuch","checks":[{"field":"x","op":"eq","value":"1"}]},
	  {"id":"d","type":"PtSession","port":"din","checks":[{"field":"v","op":"eq","value":"1"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	caps := parsedGoldenCaps(t)
	if f := plan.CheckAgainstCaps(caps); len(f) != 0 {
		t.Errorf("the board-under-test check looked at the reference step: %v", f)
	}
	if f := plan.CheckRefAgainstCaps(caps); len(f) != 1 || !strings.Contains(f[0], "nosuch") {
		t.Errorf("the reference check should name the missing port, got %v", f)
	}
}

// ---------- UserReading ----------

const readingPlan = `{"schema":1,"name":"drop","limit_version":"v","steps":[
  {"id":"drop","type":"UserReading","prompt":"volts across the switch","unit":"V",
   "checks":[{"field":"value","op":"range","max":1.0}]}]}`

func TestManualReadingIsJudgedAndKeptAsTyped(t *testing.T) {
	for _, tc := range []struct {
		name string
		ask  func(string, string) (string, error)
		want ptreport.Outcome
		kept string
	}{
		{"within the limit", func(string, string) (string, error) { return " 0.62 ", nil }, ptreport.OutcomePass, "0.62"},
		{"over the limit", func(string, string) (string, error) { return "1.4", nil }, ptreport.OutcomeFail, "1.4"},
		{"not a number", func(string, string) (string, error) { return "abc", nil }, ptreport.OutcomeError, "abc"},
		{"the person could not answer", func(string, string) (string, error) { return "", errors.New("stdin closed") }, ptreport.OutcomeError, ""},
		{"nobody to ask", nil, ptreport.OutcomeError, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ptplan.Parse([]byte(readingPlan))
			if err != nil {
				t.Fatal(err)
			}
			fake := newScriptBoard(t, func(cmd string, nth int) ([]string, []string) {
				lines, _ := standardReplies(cmd)
				return lines, nil
			})
			board := ptboard.New(fake, 0)
			t.Cleanup(func() { board.Close() })
			var gotUnit string
			ask := tc.ask
			if ask != nil {
				inner := ask
				ask = func(prompt, unit string) (string, error) { gotUnit = unit; return inner(prompt, unit) }
			}
			runner := &ptseq.Runner{OpenPeer: refusePeer, FindCDC: noCDC, Board: board,
				Sleep: func(time.Duration) {}, ToolVersion: "test", Ask: ask}
			rep, err := runner.Run(p)
			if err != nil {
				t.Fatal(err)
			}
			s := rep.Steps[0]
			if s.Outcome != tc.want {
				t.Fatalf("outcome %s, want %s (%s)", s.Outcome, tc.want, attemptErr(s))
			}
			if got := s.Attempts[0].Extra["value"]; got != tc.kept {
				t.Errorf("kept %q, want %q", got, tc.kept)
			}
			if tc.ask != nil && gotUnit != "V" {
				t.Errorf("the unit reached the question as %q", gotUnit)
			}
		})
	}
}

func TestManualReadingNeedsAPromptAndALimit(t *testing.T) {
	for _, bad := range []string{
		strings.Replace(readingPlan, `"prompt":"volts across the switch",`, ``, 1),
		strings.Replace(readingPlan, `,
   "checks":[{"field":"value","op":"range","max":1.0}]`, ``, 1),
	} {
		if _, err := ptplan.Parse([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

// ---------- the CANable as the CAN peer ----------

// fakeCANable answers SLCAN configuration and records what it transmits; the
// board's frame is put on its receive side once the session starts.
type fakeCANable struct {
	pr *io.PipeReader
	pw *io.PipeWriter

	mu   sync.Mutex
	buf  bytes.Buffer
	sent []string
}

func newFakeCANable() *fakeCANable {
	pr, pw := io.Pipe()
	return &fakeCANable{pr: pr, pw: pw}
}

func (f *fakeCANable) Read(p []byte) (int, error) { return f.pr.Read(p) }
func (f *fakeCANable) Close() error               { return f.pw.Close() }
func (f *fakeCANable) Write(p []byte) (int, error) {
	f.mu.Lock()
	f.buf.Write(p)
	var replies []string
	for {
		s := f.buf.String()
		i := strings.IndexByte(s, '\r')
		if i < 0 {
			break
		}
		cmd := s[:i]
		f.buf.Next(i + 1)
		if strings.HasPrefix(cmd, "t") {
			f.sent = append(f.sent, cmd)
			continue
		}
		replies = append(replies, "\r")
	}
	f.mu.Unlock()
	go func() {
		for _, r := range replies {
			_, _ = io.WriteString(f.pw, r)
		}
	}()
	return len(p), nil
}

func (f *fakeCANable) transmitted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

func TestCANPeerAnswersFromTheCANable(t *testing.T) {
	plan, err := ptplan.Parse([]byte(`{"schema":1,"name":"can","limit_version":"v","steps":[
	  {"id":"can-pc","type":"PtSession","port":"can","params":{"baud":250000,"mode":"normal","period":300},
	   "frames":1,"timeout_ms":3000,"peer":{"can":true},
	   "checks":[{"field":"rx_frames","op":"range","min":1}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	can := newFakeCANable()
	var fake *scriptBoard
	fake = newScriptBoard(t, func(cmd string, nth int) ([]string, []string) {
		if strings.HasPrefix(cmd, "pt.start can") {
			// The board puts seq 1 on the bus in mode=normal, and its frame
			// counts the reply only once the reply is back - so the frame is
			// emitted after the CANable has answered, as on a real bus.
			go func() {
				_, _ = io.WriteString(can.pw, "t123400000001\r")
				until := time.Now().Add(2 * time.Second)
				for len(can.transmitted()) == 0 && time.Now().Before(until) {
					time.Sleep(5 * time.Millisecond)
				}
				fake.emit("!can t=1 seq=1 rx=0 miss=0 bps=250000 mode=normal alive=1 tx=1 rx_frames=1 junk=0 replied=0 tec=0 rec=0 lec=0")
			}()
			return []string{"OK can started"}, nil
		}
		lines, _ := standardReplies(cmd)
		return lines, nil
	})
	board := ptboard.New(fake, 0)
	t.Cleanup(func() { board.Close() })
	var opened string
	runner := &ptseq.Runner{FindCDC: noCDC, Board: board, Sleep: func(time.Duration) {}, ToolVersion: "test",
		FindCAN: func() (string, error) { return "COM-can", nil },
		OpenPeer: func(kind, addr string, baud int) (io.ReadWriteCloser, error) {
			opened = kind + " " + addr
			return can, nil
		},
	}
	deadline := time.Now().Add(2 * time.Second)
	rep, err := runner.Run(plan)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Steps[0].Outcome != ptreport.OutcomePass {
		t.Fatalf("outcome %s: %s", rep.Steps[0].Outcome, attemptErr(rep.Steps[0]))
	}
	if opened != "serial COM-can" {
		t.Errorf("opened %q, want the CANable's port", opened)
	}
	for len(can.transmitted()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := can.transmitted(); len(got) == 0 || got[0] != "t123400000002" {
		t.Errorf("the CANable sent %v, want t123400000002 (seq plus one, as mode=echo does)", got)
	}
	if ex := rep.Steps[0].Attempts[0].Extra; ex["can_peer_received"] == "" {
		t.Errorf("the report should say what the CAN peer did: %v", ex)
	}
}

// ---------- the shipped plan on two wired simulated boards ----------

// refPairSteps are the shipped plan's DI walk and CAN steps: everything that
// needs the reference board.
func refPairSteps(p ptplan.Plan) ptplan.Plan {
	var keep []ptplan.Step
	for i, s := range p.Steps {
		if !s.IsEnabled() {
			continue
		}
		if s.IsRefCompanion() || (i > 0 && p.Steps[i-1].IsRefCompanion()) {
			keep = append(keep, s)
		}
	}
	p.Steps = keep
	return p
}

func openSimPair(t *testing.T) (*ptboard.Board, *ptboard.Board) {
	t.Helper()
	a, b, err := simboard.OpenPair("sim", "sim", 0)
	if errors.Is(err, simboard.ErrNotBuilt) {
		t.Skip("simulated board not built: tools/build.sh sim")
	}
	if err != nil {
		t.Fatal(err)
	}
	dut, ref := ptboard.New(a, 0), ptboard.New(b, 0)
	t.Cleanup(func() { dut.Close(); ref.Close() })
	return dut, ref
}

func TestShippedReferenceStepsPassOnTwoWiredSimulatedBoards(t *testing.T) {
	for _, name := range []string{"station6-poweron.json", "station6-poweron-relaxed.json"} {
		t.Run(name, func(t *testing.T) {
			shipped, err := ptplan.Load(filepath.Join("..", "..", "..", "src", "plans", name))
			if err != nil {
				t.Fatal(err)
			}
			plan := refPairSteps(shipped)
			if len(plan.Steps) < 2*(8+1) {
				t.Fatalf("expected the DI walk and CAN pairs, got %d steps", len(plan.Steps))
			}
			dut, ref := openSimPair(t)
			caps, err := dut.Caps()
			if err != nil {
				t.Fatal(err)
			}
			if f := shipped.CheckRefAgainstCaps(caps); len(f) != 0 {
				t.Fatalf("the reference steps do not fit the firmware: %v", f)
			}
			runner := &ptseq.Runner{OpenPeer: refusePeer, FindCDC: noCDC, Board: dut, Ref: ref, Caps: &caps, ToolVersion: "test"}
			rep, err := runner.Run(plan)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range rep.Steps {
				if s.Outcome != ptreport.OutcomePass {
					t.Errorf("%s is %s: %s", s.ID, s.Outcome, attemptErr(s))
				}
			}
		})
	}
}

// The walk has to be able to fail: a beat expecting the wrong input must not
// pass just because something reads high.
func TestDIWalkOnSimulatedBoardsCatchesTheWrongInput(t *testing.T) {
	plan, err := ptplan.Parse([]byte(`{"schema":1,"name":"walk","limit_version":"v","steps":[
	  {"id":"r","type":"PtSession","on":"ref","port":"dout","params":{"ch":"3","mode":"hold","duty":255,"period":100},
	   "timeout_ms":5000,"checks":[{"field":"ch3","op":"eq","value":"255"}]},
	  {"id":"d","type":"PtSession","port":"din","params":{"period":50},"frames":3,"timeout_ms":5000,
	   "checks":[{"field":"v","op":"eq","value":"0x08"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	dut, ref := openSimPair(t)
	runner := &ptseq.Runner{OpenPeer: refusePeer, FindCDC: noCDC, Board: dut, Ref: ref, ToolVersion: "test"}
	rep, err := runner.Run(plan)
	if err != nil {
		t.Fatal(err)
	}
	if got := verdicts(rep); got != "PASS FAIL" {
		t.Fatalf("outcomes %s, want PASS FAIL", got)
	}
	got := rep.Steps[1].Attempts[0].Checks[0].Got
	if !strings.EqualFold(got, "0x04") {
		t.Errorf("DI read %s with the reference driving DO3, want 0x04", got)
	}
}

func parsedGoldenCaps(t *testing.T) ptproto.Caps {
	t.Helper()
	c, err := ptproto.ParseCaps(goldenCaps(t))
	if err != nil {
		t.Fatalf("caps fixture: %v", err)
	}
	return c
}

// The panel opens the reference board after the board under test; Link wires
// them then, as OpenPair does at once (PORTTOOL-FLOW.md C.3.7).
func TestLinkWiresBoardsOpenedOneAfterTheOther(t *testing.T) {
	shipped, err := ptplan.Load(filepath.Join("..", "..", "..", "src", "plans", "station6-poweron.json"))
	if err != nil {
		t.Fatal(err)
	}
	plan := refPairSteps(shipped)
	plan.Steps = plan.Steps[:4] // two beats of the DI walk
	a, err := simboard.OpenPort("sim", 0)
	if errors.Is(err, simboard.ErrNotBuilt) {
		t.Skip("simulated board not built: tools/build.sh sim")
	}
	if err != nil {
		t.Fatal(err)
	}
	dut := ptboard.New(a, 0)
	t.Cleanup(func() { dut.Close() })
	b, err := simboard.OpenPort("sim", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !simboard.Link(a, b) {
		t.Fatal("two simulated boards were not linked")
	}
	ref := ptboard.New(b, 0)
	t.Cleanup(func() { ref.Close() })
	runner := &ptseq.Runner{OpenPeer: refusePeer, FindCDC: noCDC, Board: dut, Ref: ref, ToolVersion: "test"}
	rep, err := runner.Run(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range rep.Steps {
		if s.Outcome != ptreport.OutcomePass {
			t.Errorf("%s is %s: %s", s.ID, s.Outcome, attemptErr(s))
		}
	}
}
