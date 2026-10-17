package testcase

// AI read / AO output items on the item runner (src/ptlib/analog.go):
// each point is asked through the question box, converted with the board's
// own k, b from pt.cal and judged by the plan step's ±1 % FS
// (PORTTOOL-FLOW.md C.3.2 "读数 / 输出项").
//
// Bench: scripted fake boards; frames are pushed by the test. What a meter or
// a signal source would read is typed by the fake person.

import (
	"PortTool/src/calstore"
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"PortTool/src/calarea"
	"PortTool/src/ptboard"
	"PortTool/src/ptitems"
	"PortTool/src/ptlib"
	"PortTool/src/ptplan"
	"PortTool/src/ptproto"
	"PortTool/src/ptreport"
	"PortTool/src/ptseq"
)

const analogPlanJSON = `{"schema":1,"name":"analog","limit_version":"t","steps":[
 {"id":"ai1-read","type":"UserReading","enabled":false,"prompt":"p","unit":"% FS",
  "checks":[{"field":"value","op":"range","min":-1,"max":1,"unit":"% FS"}]},
 {"id":"ao1-out","type":"UserReading","enabled":false,"prompt":"p","unit":"% FS",
  "checks":[{"field":"value","op":"range","min":-1,"max":1,"unit":"% FS"}]},
 {"id":"ao2-out","type":"UserReading","enabled":false,"prompt":"p","unit":"% FS",
  "checks":[{"field":"value","op":"range","min":-1,"max":1,"unit":"% FS"}]}]}`

func catalogueItem(t *testing.T, id string) ptitems.Item {
	t.Helper()
	for _, it := range ptitems.Catalogue {
		if it.ID == id {
			return it
		}
	}
	t.Fatalf("no item %s", id)
	return ptitems.Item{}
}

// walkBoard answers the walk's commands and, while running, pushes an ain
// frame with raw and an aout frame every 10 ms.
func walkBoard(t *testing.T, raw int) (*ptboard.Board, *scriptBoard) {
	t.Helper()
	fake := newScriptBoard(t, func(cmd string, nth int) ([]string, []string) {
		if lines, ok := standardReplies(cmd); ok {
			return lines, nil
		}
		return []string{"OK " + strings.Fields(cmd)[1]}, nil
	})
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for n := 1; ; n++ {
			select {
			case <-done:
				return
			case <-time.After(10 * time.Millisecond):
			}
			fake.emit(fmt.Sprintf("!ain t=%d ch1=%d/0/0", n*10, raw))
			fake.emit(fmt.Sprintf("!aout t=%d ch1=0/0/0", n*10))
		}
	}()
	b := ptboard.New(fake, 0)
	t.Cleanup(func() { close(done); wg.Wait(); b.Close() })
	return b, fake
}

// typed answers the walk's questions in order and records them.
type typed struct {
	mu   sync.Mutex
	vals []string
	qs   []ptlib.Question
}

func (p *typed) ask(ctx context.Context, q ptlib.Question) (ptlib.Answer, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.qs = append(p.qs, q)
	if len(p.vals) == 0 {
		return ptlib.Answer{}, ptlib.ErrStopped
	}
	v := p.vals[0]
	p.vals = p.vals[1:]
	return ptlib.Answer{Value: v}, nil
}

func analogEngine(t *testing.T, b *ptboard.Board, src ptlib.AnalogSource, p *typed, items ...ptitems.Item) *ptlib.Engine {
	t.Helper()
	plan, err := ptplan.Parse([]byte(analogPlanJSON))
	if err != nil {
		t.Fatal(err)
	}
	return &ptlib.Engine{Board: b, Plan: plan, Items: items, Hold: &fakeHold{b: b}, Analog: src, Ask: p.ask,
		Runner: func() *ptseq.Runner { return &ptseq.Runner{Board: b} }}
}

func runOne(t *testing.T, e *ptlib.Engine, pick ptlib.Pick) (ptreport.StepResult, ptlib.State) {
	t.Helper()
	rep, err := e.Run(context.Background(), []ptlib.Pick{pick}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range e.States() {
		if s.ID == pick.ID {
			return rep.Steps[0], s
		}
	}
	t.Fatalf("no state for %s", pick.ID)
	return ptreport.StepResult{}, ptlib.State{}
}

// The panel's source decodes the board's own k, b from pt.cal and takes the
// unit and full scale from the registered range; the runner judges each
// point's error in % FS by the plan step.
func TestAIReadIsJudgedWithTheBoardsFormula(t *testing.T) {
	var ch [calarea.Channels]calarea.Channel
	ch[calarea.AI1] = calarea.Channel{Gain: 1.01, Offset: -5}
	ch[calarea.AI2] = calarea.Channel{Gain: 1, Offset: 0}
	ch[calarea.AO1] = calarea.Channel{Gain: 1, Offset: 0.1}
	ch[calarea.AO2] = calarea.Channel{Gain: 1, Offset: 0}
	_, panel := calPanel(t, calArea(t, boardUID, ch))
	src := panel.AnalogSource()

	cal, err := src("AI1")
	if err != nil || math.Abs(cal.K-1.01) > 1e-6 || math.Abs(cal.B+5) > 1e-6 || cal.Unit != "mV" || cal.FS != 10000 {
		t.Fatalf("AI1 source = %+v, %v", cal, err)
	}
	// raw -> pin mV -> divider nominal -> k * nominal + b, as the app does.
	nominal := 26000.0 * 2500 / 65535 * 90.6 / 22.6
	if math.Abs(cal.Nominal(26000)-nominal) > 1e-6 {
		t.Fatalf("AI1 nominal = %v, want %v", cal.Nominal(26000), nominal)
	}
	shown := cal.K*nominal + cal.B

	b, _ := walkBoard(t, 26000)
	p := &typed{vals: []string{fmt.Sprint(shown + 50), fmt.Sprint(shown - 150), fmt.Sprint(shown)}}
	e := analogEngine(t, b, src, p, catalogueItem(t, "ain.read1"))
	res, st := runOne(t, e, ptlib.Pick{ID: "ain.read1", On: true, Peer: ptitems.PeerHuman, Points: []float64{4000, 5000, 6000}})
	if res.Outcome != ptreport.OutcomeFail || st.Analog == nil || len(st.Analog.Rows) != 3 {
		t.Fatalf("outcome %s, analog %+v", res.Outcome, st.Analog)
	}
	rows := st.Analog.Rows
	if !*rows[0].Pass || *rows[1].Pass || !*rows[2].Pass || math.Abs(*rows[0].Dev+0.5) > 0.01 || math.Abs(*rows[1].Dev-1.5) > 0.01 {
		t.Fatalf("points not judged at ±1 %% FS through the board's formula: %+v %+v %+v", rows[0], rows[1], rows[2])
	}
	if len(p.qs) != 3 || p.qs[0].Walk != "ai" || p.qs[1].Point != 2 || p.qs[1].Set != 5000 || p.qs[0].Unit != "mV" {
		t.Fatalf("each point is asked through the question box: %+v", p.qs)
	}
	if n := len(res.Attempts); n != 3 || len(res.Attempts[1].Checks) != 1 || res.Attempts[1].Extra["dev_pct_fs"] == "" {
		t.Fatalf("each point is one attempt with its check and its error: %+v", res.Attempts)
	}

	have, err := panel.CalibratedChannels()
	if err != nil || strings.Join(have, ",") != "AI1,AI2,AO1,AO2" {
		t.Fatalf("CalibratedChannels = %v, %v", have, err)
	}
}

// AO is told (target - b) / k through the XTR111's 102.4 mV / mA and the
// meter is compared with the target.
func TestAOOutputDrivesTheCorrectedNominal(t *testing.T) {
	src := func(string) (ptlib.AnalogCal, error) {
		return ptlib.AnalogCal{K: 1, B: 0.1, Unit: "mA", Lo: 0, FS: 20, Range: calstore.RangeAO020}, nil
	}
	b, fake := walkBoard(t, 0)
	p := &typed{vals: []string{"5.05", "15.05", "17.9"}}
	e := analogEngine(t, b, src, p, catalogueItem(t, "aout.out1"))
	res, st := runOne(t, e, ptlib.Pick{ID: "aout.out1", On: true, Peer: ptitems.PeerHuman})
	if res.Outcome != ptreport.OutcomePass {
		t.Fatalf("AO1 within ±1 %% FS must pass: %s %+v", res.Outcome, st.Analog)
	}
	if got := []float64{st.Analog.Rows[0].Set, st.Analog.Rows[1].Set, st.Analog.Rows[2].Set}; got[0] != 5 || got[1] != 15 || got[2] != 18 {
		t.Fatalf("no points given: the AO default is 5 / 15 / 18 mA, got %v", got)
	}
	// (5 - 0.1) / 1 * 102.4 = 501.76, and so on.
	for _, want := range []string{"pt.set aout mv=1:502", "pt.set aout mv=1:1526", "pt.set aout mv=1:1833", "pt.stop aout"} {
		if !fake.sawCommand(want) {
			t.Errorf("never sent %q: %v", want, fake.commands())
		}
	}
	if p.qs[0].Walk != "ao" {
		t.Fatalf("AO asks for the meter: %+v", p.qs[0])
	}
	// Decision 136: the result and the report keep the range and k, b used.
	want := ptreport.AnalogUsed{Board: "dut", Channel: "AO1", Range: "i20", Unit: "mA", K: 1, B: 0.1}
	if len(res.Analog) != 1 || res.Analog[0] != want || st.Analog.Range != "i20" {
		t.Errorf("used %+v, range %q; want %+v", res.Analog, st.Analog.Range, want)
	}

	// Decision 136: a 4-20 mA AO converts with its own formula, not 102.4 mV / mA.
	src420 := func(string) (ptlib.AnalogCal, error) {
		return ptlib.AnalogCal{K: 1, Unit: "mA", Lo: 4, Min: calstore.RangeAO420.Min(), FS: 20, Range: calstore.RangeAO420}, nil
	}
	b2, fake2 := walkBoard(t, 0)
	e2 := analogEngine(t, b2, src420, &typed{vals: []string{"10", "16", "18"}}, catalogueItem(t, "aout.out1"))
	runOne(t, e2, ptlib.Pick{ID: "aout.out1", On: true, Peer: ptitems.PeerHuman, Points: []float64{10, 16, 18}})
	if mv := int(math.Round(calstore.RangeAO420.AODACmV(10))); !fake2.sawCommand("pt.set aout mv=1:" + strconv.Itoa(mv)) {
		t.Errorf("4-20 mA at 10 mA: want %d mV at the DAC, sent %v", mv, fake2.commands())
	}
	// Under what the range can output is refused, not driven at the floor.
	if _, st := runOne(t, e2, ptlib.Pick{ID: "aout.out1", On: true, Peer: ptitems.PeerHuman, Points: []float64{4.5, 12, 18}}); st.Outcome != ptreport.OutcomeError || st.Reason != ptlib.ReasonBelowMin || st.Detail != "4.86 mA" {
		t.Errorf("4.5 mA on 4-20 mA: %s %s %q", st.Outcome, st.Reason, st.Detail)
	}
	// Decision 129: in R&D a point left blank stops the item instead of passing on the rest.
	e.RD = true
	p.vals = []string{"5.05", "", "17.9"}
	if _, st := runOne(t, e, ptlib.Pick{ID: "aout.out1", On: true, Peer: ptitems.PeerHuman}); st.Outcome != ptreport.OutcomeStopped || st.Reason != ptlib.ReasonStopped {
		t.Fatalf("a blank point in R&D: %s %s", st.Outcome, st.Reason)
	}
}

// A board without its own calibration is not judged: ERROR, reason
// not_calibrated, and nobody is asked. The panel's source says so for a blank
// area and for one written for another board.
func TestUncalibratedBoardIsNotJudged(t *testing.T) {
	var ch [calarea.Channels]calarea.Channel
	for i := range ch {
		ch[i] = calarea.Channel{Gain: 1}
	}
	for name, area := range map[string][]byte{
		"blank": blankArea(),
		"other": calArea(t, "0021003A3135511439303539", ch),
	} {
		_, panel := calPanel(t, area)
		src := panel.AnalogSource()
		if _, err := src("AI2"); err == nil {
			t.Errorf("%s: the source gave a formula", name)
		}
		have, err := panel.CalibratedChannels()
		if err != nil || len(have) != 0 {
			t.Errorf("%s: CalibratedChannels = %v, %v", name, have, err)
		}
		b, _ := walkBoard(t, 26000)
		p := &typed{vals: []string{"1", "2", "3", "4", "5"}}
		e := analogEngine(t, b, src, p, catalogueItem(t, "ain.read1"))
		res, st := runOne(t, e, ptlib.Pick{ID: "ain.read1", On: true, Peer: ptitems.PeerHuman})
		if res.Outcome != ptreport.OutcomeError || st.Reason != ptlib.ReasonNotCalibrated || len(p.qs) != 0 {
			t.Errorf("%s: %s %s, asked %d", name, res.Outcome, st.Reason, len(p.qs))
		}
	}
}

// The read / output defaults sit between the calibration's default points,
// never on one, so they check more than the residual (decision 107; C.3.2).
func TestDefaultTestPointsAvoidTheFitPoints(t *testing.T) {
	for _, c := range []struct {
		port   string
		lo, fs float64
		want   string
	}{
		{"ain", 0, 10000, "[1250 3750 6250 8750]"},
		{"ain", 0, 20, "[2.5 7.5 12.5 17.5]"},
		{"aout", 0, 20, "[5 15 18]"},
		{"aout", 4, 20, "[8 16 18]"},
	} {
		got := ptlib.DefaultPoints(c.port, c.lo, c.fs)
		if fmt.Sprint(got) != c.want {
			t.Errorf("%s %g-%g: %v, want %s", c.port, c.lo, c.fs, got, c.want)
		}
		for _, f := range ptlib.FitPoints(c.port, c.lo, c.fs) {
			for _, g := range got {
				if f == g {
					t.Errorf("%s %g-%g: test point %g is a fit point", c.port, c.lo, c.fs, g)
				}
			}
		}
	}
}

// Points outside 3-5 are refused before the board is touched (decision 90);
// the reference board as the peer, when none is connected, says so (STG-31).
func TestAnalogPicksAreChecked(t *testing.T) {
	src := func(string) (ptlib.AnalogCal, error) {
		return ptlib.AnalogCal{K: 1, Unit: "mA", FS: 20, Range: calstore.RangeAO020}, nil
	}
	b, _ := walkBoard(t, 0)
	e := analogEngine(t, b, src, &typed{}, catalogueItem(t, "aout.out1"))
	if _, err := e.Run(context.Background(), []ptlib.Pick{{ID: "aout.out1", On: true, Points: []float64{1, 2}}}, 0); err == nil {
		t.Fatal("two points were accepted")
	}
	res, st := runOne(t, e, ptlib.Pick{ID: "aout.out1", On: true, Peer: ptitems.PeerRef})
	if res.Outcome != ptreport.OutcomeError || st.Reason != ptlib.ReasonNoRef {
		t.Fatalf("reference board as the AO peer: %s %s", res.Outcome, st.Reason)
	}
}

// aout.load drives each channel's own target (out1 / out2, STG-38) through its
// own k, b, keeps the targets on the PC, and refuses one the AO cannot drive
// (decision 106). Each channel's typed reading is judged like AO output,
// +-1 % FS against its own target (decision 128). An old "ma" fills both.
func TestAOLoadDrivesTheTypedCurrentThroughEachChannelsKB(t *testing.T) {
	src := func(ch string) (ptlib.AnalogCal, error) {
		if ch == "AO2" {
			return ptlib.AnalogCal{K: 0.98, B: 0.2, Unit: "mA", FS: 20, Range: calstore.RangeAO020}, nil
		}
		return ptlib.AnalogCal{K: 1, B: 0.1, Unit: "mA", FS: 20, Range: calstore.RangeAO020}, nil
	}
	it := catalogueItem(t, "aout.load")
	// The target has no default, in any unit (decisions 129, 134).
	if d := ptlib.Defaults(it, ptplan.Plan{}, ptproto.Caps{}); !it.Load || d[ptitems.ParamOut1] != "" || d[ptitems.ParamOut2] != "" {
		t.Fatalf("aout.load must start with no target: %+v", it)
	}
	b, fake := walkBoard(t, 0)
	p := &typed{vals: []string{"9.9", "5.05"}}
	e := analogEngine(t, b, src, p, it)
	both := func(v1, v2 string) map[string]string {
		return map[string]string{ptitems.ParamOut1: v1, ptitems.ParamOut2: v2}
	}
	lastStart := func() string {
		last := ""
		for _, c := range fake.commands() {
			if strings.HasPrefix(c, "pt.start aout") {
				last = c
			}
		}
		return last
	}
	res, st := runOne(t, e, ptlib.Pick{ID: "aout.load", On: true, Peer: ptitems.PeerHuman, Params: both("10", "5")})
	if res.Outcome != ptreport.OutcomePass || len(st.Checks) != 2 {
		t.Fatalf("aout.load: %s %+v %+v", res.Outcome, res.Attempts, st.Checks)
	}
	if q := p.qs[0]; q.Channel != "AO1" || q.Set != 10 {
		t.Fatalf("first question: %+v", q)
	}
	if q := p.qs[1]; q.Channel != "AO2" || q.Set != 5 || q.Walk != "ao" {
		t.Fatalf("second question: %+v", q)
	}
	// (10 - 0.1) / 1 * 102.4 = 1014; (5 - 0.2) / 0.98 * 102.4 = 502.
	if c := lastStart(); !strings.Contains(c, "mv=1:1014,2:502") || strings.Contains(c, "out1=") || strings.Contains(c, "out2=") || len(p.qs) != 2 {
		t.Fatalf("sent %q, want mv=1:1014,2:502 without the targets (asked %d)", c, len(p.qs))
	}
	// A configuration saved with the old shared "ma" drives both channels with it.
	p.vals = []string{"10", "10"}
	if res, _ := runOne(t, e, ptlib.Pick{ID: "aout.load", On: true, Peer: ptitems.PeerHuman, Params: map[string]string{"ma": "10"}}); res.Outcome != ptreport.OutcomePass || !strings.Contains(lastStart(), "mv=1:1014,2:1024") {
		t.Fatalf("old ma: %s, sent %q", res.Outcome, lastStart())
	}
	// 9.7 mA on a 20 mA scale is -1.5 % FS: out.
	p.vals = []string{"9.7", "10"}
	if res, _ := runOne(t, e, ptlib.Pick{ID: "aout.load", On: true, Peer: ptitems.PeerHuman, Params: both("10", "10")}); res.Outcome != ptreport.OutcomeFail {
		t.Fatalf("1.5 %% FS off passed: %s", res.Outcome)
	}
	// Decision 129: a reading left blank is "not measured to the end" in R&D.
	pick := ptlib.Pick{ID: "aout.load", On: true, Peer: ptitems.PeerHuman, Params: both("10", "10")}
	p.vals = []string{"10", ""}
	if _, st := runOne(t, e, pick); st.Outcome != ptreport.OutcomeError || st.Reason != ptlib.ReasonNoReading {
		t.Fatalf("a blank reading outside R&D: %s %s", st.Outcome, st.Reason)
	}
	e.RD = true
	p.vals = []string{"10", ""}
	if _, st := runOne(t, e, pick); st.Outcome != ptreport.OutcomeStopped || st.Reason != ptlib.ReasonStopped {
		t.Fatalf("a blank reading in R&D: %s %s", st.Outcome, st.Reason)
	}
	p.vals = []string{"9.7", ""}
	res, st = runOne(t, e, pick)
	if st.Outcome != ptreport.OutcomeFail {
		t.Fatalf("a reading out of limits fails even with the other blank: %s", st.Outcome)
	}
	// Decision 129 「多路」: each channel its own result, AO1 failed and AO2 stopped.
	chans := func(cs []ptreport.ChannelResult) string {
		var s []string
		for _, c := range cs {
			s = append(s, c.Name+"="+string(c.Outcome))
		}
		return strings.Join(s, " ")
	}
	if got := chans(st.Chans); got != "AO1=FAIL AO2=STOPPED" || chans(res.Channels) != got {
		t.Fatalf("channels on the card %q and in the report %q, want AO1=FAIL AO2=STOPPED", got, chans(res.Channels))
	}
	e.RD = false
	for _, params := range []map[string]string{nil, {ptitems.ParamOut1: "10"}, {ptitems.ParamOut2: "10"},
		both("-1", "10"), both("10", "x"), both("20000", "10")} {
		_, err := e.Run(context.Background(), []ptlib.Pick{{ID: "aout.load", On: true, Peer: ptitems.PeerHuman, Params: params}}, 0)
		if err == nil {
			t.Errorf("%v accepted", params)
		}
	}
	// 25 fits a voltage range but not this 0-20 mA one: refused at the start of the run.
	if _, st := runOne(t, e, ptlib.Pick{ID: "aout.load", On: true, Peer: ptitems.PeerHuman, Params: both("10", "25")}); st.Outcome != ptreport.OutcomeError || st.Reason != ptlib.ReasonNoTarget {
		t.Errorf("25 on a 0-20 mA range: %s %s", st.Outcome, st.Reason)
	}
}

// With a duration (aging) the load output holds its current for the whole run
// (decision 114) and each channel is read once at the end, the current still
// on (decision 128).
func TestAOLoadHoldsItsCurrentForTheDurationThenIsReadOnce(t *testing.T) {
	src := func(ch string) (ptlib.AnalogCal, error) {
		return ptlib.AnalogCal{K: 1, B: 0.1, Unit: "mA", FS: 20, Range: calstore.RangeAO020}, nil
	}
	b, fake := walkBoard(t, 0)
	p := &typed{vals: []string{"20.1", "19.9"}}
	e := analogEngine(t, b, src, p, catalogueItem(t, "aout.load"))
	start := time.Now()
	var firstAsk time.Duration
	ask := e.Ask
	e.Ask = func(ctx context.Context, q ptlib.Question) (ptlib.Answer, error) {
		if firstAsk == 0 {
			firstAsk = time.Since(start)
		}
		return ask(ctx, q)
	}
	rep, err := e.Run(context.Background(), []ptlib.Pick{{ID: "aout.load", On: true, Peer: ptitems.PeerHuman, Params: map[string]string{ptitems.ParamOut1: "20", ptitems.ParamOut2: "20"}}}, 800*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 700*time.Millisecond {
		t.Errorf("the load output ended after %v, before the duration", time.Since(start))
	}
	if len(p.qs) != 2 || firstAsk < 700*time.Millisecond {
		t.Errorf("asked %d questions, the first after %v: want two, at the end", len(p.qs), firstAsk)
	}
	held := false
	for _, c := range fake.commands() {
		held = held || strings.HasPrefix(c, "pt.start aout") && strings.Contains(c, "mv=1:2038,2:2038") // (20 - 0.1) * 102.4
	}
	if !held {
		t.Fatalf("20 mA never sent: %v", fake.commands())
	}
	if o := rep.Steps[0].Outcome; o != ptreport.OutcomePass {
		t.Fatalf("aout.load held for the duration: %s %+v", o, rep.Steps[0])
	}
}
