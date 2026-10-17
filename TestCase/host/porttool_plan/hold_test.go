package testcase

// The deadman behind a timed item run, at the server: /api/lib/start with a
// duration arms the continuous run through ptlib.Holder, /api/lib/stop ends it.
// Covers the unhappy paths: no renewal, a second start, a verdict stopping
// everything, a board unplugged, restarted or silent mid-run.
//
// Bench: a scripted fake board whose temp session pushes a frame every 200 ms
// from its own clock, judged by one criteria step (ok=1).

import (
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"PortTool/src/portmap"
	"PortTool/src/ptpanel"
)

// holdPlanJSON is the criteria plan temp.read judges every frame against.
const holdPlanJSON = `{"schema":1,"name":"hold","limit_version":"t","steps":[
 {"id":"temperature","type":"PtSession","port":"temp","frames":1,"timeout_ms":2000,"params":{"period":"200"},
  "checks":[{"field":"ok","op":"eq","value":"1"}]}]}`

// holdCaps is capsReply plus a loop=ctrl temp session, as the real firmware
// reports it, so the panel echoes its frames.
func holdCaps() []string {
	body := append(capsReply()[1:],
		"OK port=temp board=lower kind=session blk=- term=- channels=2 loop=ctrl params=ch,period running=0",
		"OK vals=temp ch=1,2 period=1000",
		"OK limits=temp ch:1..2 period:50..")
	return append([]string{"OK porttool=0.9.0 ports=4 lines=" + strconv.Itoa(len(body))}, body...)
}

// tempPump is the temp session's frame source: one frame per period while
// started, with a tick a test can send backwards and a verdict field it can
// turn bad.
type tempPump struct {
	mu    sync.Mutex
	board *scriptBoard
	on    bool
	tick  int
	seq   int
	ok    string
	quit  chan struct{}
}

func newTempPump(t *testing.T) *tempPump {
	p := &tempPump{tick: 90000, ok: "1", quit: make(chan struct{})}
	t.Cleanup(func() { close(p.quit) })
	go func() {
		tk := time.NewTicker(200 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-p.quit:
				return
			case <-tk.C:
			}
			p.mu.Lock()
			if !p.on || p.board == nil {
				p.mu.Unlock()
				continue
			}
			p.seq++
			p.tick += 200
			line := "!temp t=" + strconv.Itoa(p.tick) + " seq=" + strconv.Itoa(p.seq) +
				" rx=" + strconv.Itoa(p.seq-1) + " miss=0 ok=" + p.ok
			b := p.board
			p.mu.Unlock()
			b.emit(line)
		}
	}()
	return p
}

func (p *tempPump) set(f func(p *tempPump)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f(p)
}

// holdPanel is planPanel with the criteria plan written, the run log kept in
// a temp dir, and the fake board handed back so a test can unplug it.
func holdPanel(t *testing.T, pump *tempPump) (*httptest.Server, *scriptBoard) {
	t.Helper()
	dir := t.TempDir()
	was := ptpanel.PlanDir
	ptpanel.PlanDir = dir
	t.Cleanup(func() { ptpanel.PlanDir = was })
	ptpanel.RunLogDir = t.TempDir()
	t.Cleanup(func() { ptpanel.RunLogDir = "" })
	portmap.File = filepath.Join(t.TempDir(), "porttool_ports.json")
	t.Cleanup(func() { portmap.File = "" })
	if err := os.WriteFile(filepath.Join(dir, "station6-poweron.json"), []byte(holdPlanJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	fake := newScriptBoard(t, func(cmd string, nth int) ([]string, []string) {
		switch {
		case cmd == "pt.caps":
			return holdCaps(), nil
		case strings.HasPrefix(cmd, "pt.hold "):
			return []string{"OK hold=" + strings.TrimPrefix(cmd, "pt.hold ")}, nil
		case strings.HasPrefix(cmd, "pt.led "):
			return []string{"OK led " + strings.TrimPrefix(cmd, "pt.led ")}, nil
		case strings.HasPrefix(cmd, "pt.echo "):
			return []string{"OK echo " + strings.TrimPrefix(cmd, "pt.echo ")}, nil
		case strings.HasPrefix(cmd, "pt.start temp"):
			pump.set(func(p *tempPump) { p.on = true })
			return []string{"OK temp started"}, nil
		case strings.HasPrefix(cmd, "pt.stop "):
			pump.set(func(p *tempPump) { p.on = false })
		}
		if lines, ok := standardReplies(cmd); ok {
			return lines, nil
		}
		return []string{"ERR unknown " + cmd}, nil
	})
	pump.set(func(p *tempPump) { p.board = fake })

	pn := ptpanel.New()
	pn.Open = func(name string, baud int) (io.ReadWriteCloser, error) { return fake, nil }
	srv := httptest.NewServer(pn.Handler())
	t.Cleanup(func() { srv.Close(); pn.Close() })
	postJSON(t, srv, "/api/connect", map[string]any{"port": "fake"})
	return srv, fake
}

// startTimed ticks temp.read and starts it for duration ("1".."4", "forever").
func startTimed(t *testing.T, srv *httptest.Server, duration string) map[string]any {
	t.Helper()
	return postJSON(t, srv, "/api/lib/start", map[string]any{"stage": "rd", "duration": duration,
		"items": []any{map[string]any{"id": "temp.read", "on": true}}})
}

// judgedRounds is how many temp frames the run has judged so far.
func judgedRounds(j map[string]any) float64 {
	st, _ := j["states"].(map[string]any)
	row, _ := st["temp.read"].(map[string]any)
	n, _ := row["rounds"].(float64)
	return n
}

// waitFor polls until cond holds, so a timing test fails with a message
// rather than by sleeping exactly long enough to be flaky.
func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out after %s waiting for %s", d, what)
}

func TestTimedRunRenewsTheDeadmanUntilItIsStopped(t *testing.T) {
	pump := newTempPump(t)
	srv, f := holdPanel(t, pump)

	began := time.Now()
	if st := startTimed(t, srv, "forever"); st["started"] != true {
		t.Fatalf("starting a run: %v", st)
	}
	// Armed straight away, not on the first renewal tick: the session's
	// outputs are already live in between.
	waitFor(t, "the deadman to be armed", time.Second, func() bool { return f.sawCommand("pt.hold 6000") })
	if took := time.Since(began); took > 1500*time.Millisecond {
		t.Errorf("the deadman was armed %s after the start, later than the first renewal would be", took)
	}
	run, _ := getJSON(t, srv, "/api/state")["run"].(map[string]any)
	if run == nil {
		t.Fatal("the state does not report a run in flight")
	}
	// forever has no deadline, which the page draws differently.
	if run["left_s"] != nil {
		t.Errorf("left_s = %v, want null for forever", run["left_s"])
	}

	// Renewed, not armed once: a run that forgot would drop the outputs mid-test.
	waitFor(t, "a second renewal", 8*time.Second, func() bool {
		return count(f.commands(), "pt.hold 6000") >= 2
	})

	postJSON(t, srv, "/api/lib/stop", nil)
	waitLib(t, srv, "the run to end", func(j map[string]any) bool { return j["running"] == false })
	if st := getJSON(t, srv, "/api/state"); st["run"] != nil {
		t.Errorf("the run is still reported after stopping: %v", st["run"])
	}

	cmds := strings.Join(f.commands(), "\n")
	if !strings.Contains(cmds, "pt.stop all") || !strings.Contains(cmds, "pt.hold 0") {
		t.Fatalf("stopping did not stop the sessions and disarm the deadman:\n%s", cmds)
	}
	// Disarming first would leave the outputs driven with nothing watching the PC.
	if strings.Index(cmds, "pt.stop all") > strings.LastIndex(cmds, "pt.hold 0") {
		t.Error("the deadman was disarmed before the outputs were released")
	}

	// One more renewal would mean a goroutine outliving its run.
	n := count(f.commands(), "pt.hold 6000")
	time.Sleep(3 * time.Second)
	if got := count(f.commands(), "pt.hold 6000"); got != n {
		t.Errorf("renewals carried on after the run ended: %d -> %d", n, got)
	}
}

func TestTimedRunWritesTheRunToAFile(t *testing.T) {
	pump := newTempPump(t)
	srv, f := holdPanel(t, pump)

	if st := startTimed(t, srv, "2"); st["started"] != true {
		t.Fatalf("starting a run: %v", st)
	}
	if left, ok := getJSON(t, srv, "/api/lib")["left_s"].(float64); !ok || left < 7000 {
		t.Errorf("left_s = %v, want about two hours", left)
	}
	waitFor(t, "the run to be published", 2*time.Second, func() bool {
		return getJSON(t, srv, "/api/state")["run"] != nil
	})
	// The page's own log holds minutes; a long run must say where it is written.
	run := getJSON(t, srv, "/api/state")["run"].(map[string]any)
	path, _ := run["log"].(string)
	if path == "" {
		t.Fatal("the state does not say where the run is being written")
	}
	// Shown under Start for the whole run (decision 106).
	if got, _ := getJSON(t, srv, "/api/lib")["log"].(string); got != path {
		t.Errorf("/api/lib log = %q, want %q", got, path)
	}
	waitFor(t, "a few echoed frames", 3*time.Second, func() bool {
		return count(f.commands(), "pt.echo temp 2") > 0
	})

	postJSON(t, srv, "/api/lib/stop", nil)
	waitLib(t, srv, "the run to end", func(j map[string]any) bool { return j["running"] == false })

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the run log: %v", err)
	}
	body := string(b)
	for _, want := range []string{"# port tool run log", "# ports    temp", "# duration 2 h", "OK hold=6000", "!temp t=", "# stopped"} {
		if !strings.Contains(body, want) {
			t.Errorf("the run log does not contain %q:\n%s", want, body)
		}
	}
	// Echo acknowledgements say nothing the frame's miss= does not, and triple the file.
	if strings.Contains(body, "OK echo ") {
		t.Error("the run log kept the echo acknowledgements")
	}
	// One file per run, named so two runs never collide.
	if ents, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "run-*.log")); len(ents) != 1 {
		t.Errorf("%d run logs, want exactly one", len(ents))
	}
}

func TestSecondTimedRunIsRefusedRatherThanStacked(t *testing.T) {
	pump := newTempPump(t)
	srv, f := holdPanel(t, pump)

	startTimed(t, srv, "forever")
	waitFor(t, "the deadman to be armed", time.Second, func() bool { return f.sawCommand("pt.hold 6000") })
	st := startTimed(t, srv, "forever")

	// Two runs on one deadman: the second one's stop would leave the first
	// running with nothing renewing it.
	if e, _ := st["error"].(map[string]any); e["$t"] != "lib.err.busy" || st["started"] == true {
		t.Fatalf("a second run was accepted on top of the first: %v", st)
	}
	if n := count(f.commands(), "pt.start temp period=200"); n != 1 {
		t.Errorf("the session was started %d times, want once", n)
	}
	postJSON(t, srv, "/api/lib/stop", nil)
	waitLib(t, srv, "the run to end", func(j map[string]any) bool { return j["running"] == false })
}

func TestTimedRunRejectsADurationThePageCannotOffer(t *testing.T) {
	srv, f := holdPanel(t, newTempPump(t))

	for _, d := range []string{"0", "5", "7", "-1", "99"} {
		st := startTimed(t, srv, d)
		if e, _ := st["error"].(map[string]any); e["$t"] != "lib.err.bad_request" || st["started"] == true {
			t.Errorf("duration %q was accepted; the picker only offers once, 1..4 and forever: %v", d, st)
		}
	}
	if getJSON(t, srv, "/api/lib")["running"] == true || f.sawCommand("pt.hold 6000") {
		t.Errorf("a refused duration still started something: %v", f.commands())
	}
}

func TestAFaultStopsEverythingAndLightsTheLamp(t *testing.T) {
	pump := newTempPump(t)
	srv, f := holdPanel(t, pump)

	startTimed(t, srv, "1")
	waitLib(t, srv, "two judged frames", func(j map[string]any) bool { return judgedRounds(j) >= 2 })
	pump.set(func(p *tempPump) { p.ok = "0" })

	waitFor(t, "the failed frame to light the lamp", 3*time.Second, func() bool {
		return f.sawCommand("pt.led fault=1")
	})
	// One port failing stops every port: production wants "this board is bad".
	if st := getJSON(t, srv, "/api/state"); st["run"] != nil {
		t.Errorf("the run survived a fault: %v", st["run"])
	}
	cmds := strings.Join(f.commands(), "\n")
	lamp := strings.Index(cmds, "pt.led fault=1")
	for _, want := range []string{"pt.stop all", "pt.hold 0"} {
		if i := strings.Index(cmds, want); i < 0 || i > lamp {
			t.Errorf("a fault did not send %q before lighting the lamp; sent:\n%s", want, cmds)
		}
	}
	j := waitLib(t, srv, "the run to end", func(j map[string]any) bool { return j["running"] == false })
	if o := j["states"].(map[string]any)["temp.read"].(map[string]any)["outcome"]; o != "FAIL" {
		t.Errorf("temp.read: %v, want FAIL", o)
	}
}

func TestDisconnectingStopsTheRunBeforeThePortCloses(t *testing.T) {
	pump := newTempPump(t)
	srv, f := holdPanel(t, pump)

	startTimed(t, srv, "forever")
	waitFor(t, "the deadman to be armed", time.Second, func() bool { return f.sawCommand("pt.hold 6000") })

	if st := postJSON(t, srv, "/api/disconnect", nil); st["connected"] != false {
		t.Fatalf("disconnect did not take: %v", st)
	}
	// Released while the port was still open: the board only sees what was sent before it closed.
	cmds := strings.Join(f.commands(), "\n")
	if !strings.Contains(cmds, "pt.stop all") || !strings.Contains(cmds, "pt.hold 0") {
		t.Errorf("disconnecting left the sessions running:\n%s", cmds)
	}

	n := count(f.commands(), "pt.hold 6000")
	time.Sleep(3 * time.Second)
	if got := count(f.commands(), "pt.hold 6000"); got != n {
		t.Errorf("renewals carried on after disconnect: %d -> %d", n, got)
	}
}

// A board that restarts mid-run sends its tick back towards zero: a fault
// (decision 77), not a wrap.
func TestABoardRestartMidRunIsAFault(t *testing.T) {
	pump := newTempPump(t)
	srv, f := holdPanel(t, pump)

	startTimed(t, srv, "1")
	waitLib(t, srv, "two judged frames", func(j map[string]any) bool { return judgedRounds(j) >= 2 })
	pump.set(func(p *tempPump) { p.tick = -80 }) // next frame t=120

	waitFor(t, "the restart to light the lamp", 3*time.Second, func() bool {
		return f.sawCommand("pt.led fault=1")
	})
	if st := getJSON(t, srv, "/api/state"); st["run"] != nil {
		t.Errorf("the run survived a restart: %v", st["run"])
	}
}

// A board that hangs, or restarts with its sessions gone, stops sending
// frames: silence past three of the port's periods plus the floor is a fault.
func TestAPortGoingSilentMidRunIsAFault(t *testing.T) {
	pump := newTempPump(t)
	srv, f := holdPanel(t, pump)

	startTimed(t, srv, "1")
	waitLib(t, srv, "two judged frames", func(j map[string]any) bool { return judgedRounds(j) >= 2 })
	pump.set(func(p *tempPump) { p.on = false })

	// Three periods of 0.2 s plus the 5 s floor, then the once-a-second check.
	waitFor(t, "the silence to light the lamp", 9*time.Second, func() bool {
		return f.sawCommand("pt.led fault=1")
	})
	if st := getJSON(t, srv, "/api/state"); st["run"] != nil {
		t.Errorf("the run survived its port going silent: %v", st["run"])
	}
}

// An unplugged adapter or a board that lost power ends the reader; the page
// must say so instead of showing "已连上" over a board that answers nothing.
func TestLosingTheControlPortIsShown(t *testing.T) {
	srv, f := holdPanel(t, newTempPump(t))
	_ = f.Close()

	var st map[string]any
	waitFor(t, "the lost port to show in the state", 3*time.Second, func() bool {
		st = getJSON(t, srv, "/api/state")
		e, _ := st["linkError"].(map[string]any)
		return e != nil
	})
	if e := st["linkError"].(map[string]any); e["$t"] != "go.lost.control" {
		t.Errorf("the message does not say the control port is gone: %v", st["linkError"])
	}
}

// Switching the criteria plan clears every result judged by the old limits,
// and is refused while a run is still producing them (decision 30).
func TestSwitchingCriteriaClearsResultsAndWaitsForARun(t *testing.T) {
	pump := newTempPump(t)
	srv, f := holdPanel(t, pump)

	startTimed(t, srv, "forever")
	waitFor(t, "the deadman to be armed", time.Second, func() bool { return f.sawCommand("pt.hold 6000") })
	r := postJSON(t, srv, "/api/criteria", map[string]any{"plan": "station6-poweron.json"})
	if e, _ := r["error"].(map[string]any); e["$t"] != "lib.err.busy" {
		t.Fatalf("the plan was switched under a running test: %v", r)
	}
	postJSON(t, srv, "/api/lib/stop", nil)
	waitLib(t, srv, "the run to end", func(j map[string]any) bool { return j["running"] == false })
	if st, _ := getJSON(t, srv, "/api/lib")["states"].(map[string]any); len(st) == 0 {
		t.Fatal("the run left no result to clear")
	}

	if r := postJSON(t, srv, "/api/criteria", map[string]any{"plan": "station6-poweron.json"}); r["error"] != nil {
		t.Fatalf("switching after the run: %v", r["error"])
	}
	j := getJSON(t, srv, "/api/lib")
	if st, _ := j["states"].(map[string]any); len(st) != 0 {
		t.Errorf("results from the old limits are still shown: %v", st)
	}
	if p, _ := j["ports"].(map[string]any); len(p) != 0 {
		t.Errorf("port verdicts from the old limits are still shown: %v", p)
	}
}
