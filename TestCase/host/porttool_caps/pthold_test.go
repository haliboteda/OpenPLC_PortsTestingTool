package testcase

// The deadman, tested at the server rather than through a browser.
//
// This is the one piece of the panel that decides whether 24 V stays on, and
// until now the only thing covering it was case T4-02 - a browser driving a real
// simulated board, which is slow, and which proves the happy path and little
// else. What matters here is the unhappy ones: a run that nobody renews, a
// second run started on top of the first, a verdict that has to stop everything
// at once, and a board unplugged mid-run.
//
// The fake board answers from the T4-01 transcript, so every reply asserted on is
// one the real firmware actually printed - except pt.hold 6000, which the
// server picks itself and which is registered below.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"PortTool/internal/ptpanel"
)

// The span the server renews at. Registered because the harness exercises
// pt.hold with its own numbers, not with the one the server chose.
func allowHold(f *fakeBoard) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies["pt.hold 6000"] = [][]string{{"OK hold=6000"}}
}

// countCmd is how many times the board was told exactly this.
func countCmd(f *fakeBoard, cmd string) int {
	n := 0
	for _, c := range f.commands() {
		if c == cmd {
			n++
		}
	}
	return n
}

// waitFor polls until cond holds, so a timing test fails by timing out with a
// message rather than by sleeping exactly long enough to be flaky.
func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", d, what)
}

func TestTimedRunRenewsTheDeadmanUntilItIsStopped(t *testing.T) {
	ptpanel.RunLogDir = t.TempDir()
	t.Cleanup(func() { ptpanel.RunLogDir = "" })

	srv, f := newPanel(t)
	allowHold(f)
	postJSON(t, srv, "/api/connect", map[string]any{"port": "COM_TEST"})

	st := postJSON(t, srv, "/api/hold", map[string]any{
		"ports": []string{"din"}, "hours": 0,
	})
	if msg, _ := st["error"].(string); msg != "" {
		t.Fatalf("starting a run: %s", msg)
	}
	run, _ := st["run"].(map[string]any)
	if run == nil {
		t.Fatal("the state does not report a run in flight")
	}
	// 一直跑 has no deadline, which the page draws differently from a timed run.
	if run["left_s"] != nil {
		t.Errorf("left_s = %v, want null for 一直跑", run["left_s"])
	}

	// Armed straight away, not on the first tick: between the start and that
	// tick the outputs are already live.
	if countCmd(f, "pt.hold 6000") < 1 {
		t.Fatalf("the deadman was not armed before the run was published: %v",
			f.commands())
	}

	// Renewed, not armed once. A run that forgot would drop the outputs
	// mid-test on a bench, which is the failure this whole mechanism exists to
	// avoid creating.
	waitFor(t, "a second renewal", 8*time.Second, func() bool {
		return countCmd(f, "pt.hold 6000") >= 2
	})

	st = postJSON(t, srv, "/api/hold", map[string]any{"stop": true})
	if st["run"] != nil {
		t.Errorf("the run is still reported after stopping: %v", st["run"])
	}

	cmds := strings.Join(f.commands(), "\n")
	if !strings.Contains(cmds, "pt.stop all") {
		t.Error("stopping the run did not stop the sessions")
	}
	if !strings.Contains(cmds, "pt.hold 0") {
		t.Error("stopping the run did not disarm the deadman")
	}
	// Order matters: disarming first would leave a window with the outputs
	// driven and nothing watching the PC.
	if strings.Index(cmds, "pt.stop all") > strings.LastIndex(cmds, "pt.hold 0") {
		t.Error("the deadman was disarmed before the outputs were released")
	}

	// Renewals stop with the run. One more would mean a goroutine outliving
	// the connection it was sending on.
	n := countCmd(f, "pt.hold 6000")
	time.Sleep(3 * time.Second)
	if got := countCmd(f, "pt.hold 6000"); got != n {
		t.Errorf("renewals carried on after the run ended: %d -> %d", n, got)
	}
}

func TestTimedRunWritesTheRunToAFile(t *testing.T) {
	dir := t.TempDir()
	ptpanel.RunLogDir = dir
	t.Cleanup(func() { ptpanel.RunLogDir = "" })

	srv, f := newPanel(t)
	allowHold(f)
	postJSON(t, srv, "/api/connect", map[string]any{"port": "COM_TEST"})

	st := postJSON(t, srv, "/api/hold", map[string]any{
		"ports": []string{"din", "temp"}, "hours": 2,
	})
	run, _ := st["run"].(map[string]any)
	if run == nil {
		t.Fatal("no run in flight")
	}
	// The page needs the path: the browser's own log holds minutes, and a
	// four-hour run that never said where it was being written is a run whose
	// first three hours nobody can read afterwards.
	path, _ := run["log"].(string)
	if path == "" {
		t.Fatal("the state does not say where the run is being written")
	}
	if left, ok := run["left_s"].(float64); !ok || left < 7000 {
		t.Errorf("left_s = %v, want about two hours", run["left_s"])
	}

	postJSON(t, srv, "/api/hold", map[string]any{"stop": true})

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the run log: %v", err)
	}
	body := string(b)
	for _, want := range []string{
		"# port tool run log",
		"din, temp",
		"# duration 2 h", // the file is English whatever the page shows (decision 11)
		"OK hold=6000",
		"# stopped",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the run log does not contain %q:\n%s", want, body)
		}
	}
	// Echo acknowledgements are two lines in three and say nothing the frame's
	// own miss= does not. Keeping them roughly triples a four-hour file.
	if strings.Contains(body, "OK echo ") {
		t.Error("the run log kept the echo acknowledgements")
	}
	// One file per run, named so two runs never collide.
	ents, _ := filepath.Glob(filepath.Join(dir, "run-*.log"))
	if len(ents) != 1 {
		t.Errorf("%d run logs in %s, want exactly one", len(ents), dir)
	}
}

func TestSecondTimedRunIsRefusedRatherThanStacked(t *testing.T) {
	ptpanel.RunLogDir = t.TempDir()
	t.Cleanup(func() { ptpanel.RunLogDir = "" })

	srv, f := newPanel(t)
	allowHold(f)
	postJSON(t, srv, "/api/connect", map[string]any{"port": "COM_TEST"})

	postJSON(t, srv, "/api/hold", map[string]any{"ports": []string{"din"}, "hours": 0})
	st := postJSON(t, srv, "/api/hold", map[string]any{"ports": []string{"temp"}, "hours": 0})

	// Two renewal goroutines on one deadman is not a state anybody could reason
	// about afterwards - and the second one's stop would leave the first
	// running with nothing renewing it.
	// Messages reach the page as a dictionary key plus arguments (decision 82).
	if e, _ := st["error"].(map[string]any); e["$t"] != "go.hold.already" {
		t.Fatalf("a second run was accepted on top of the first: %v", st["error"])
	}
	postJSON(t, srv, "/api/hold", map[string]any{"stop": true})
}

func TestTimedRunRejectsADurationThePageCannotOffer(t *testing.T) {
	srv, f := newPanel(t)
	allowHold(f)
	postJSON(t, srv, "/api/connect", map[string]any{"port": "COM_TEST"})

	for _, h := range []int{-1, 5, 99} {
		resp, err := srv.Client().Post(srv.URL+"/api/hold", "application/json",
			strings.NewReader(`{"ports":["din"],"hours":`+strconv.Itoa(h)+`}`))
		if err != nil {
			t.Fatalf("POST /api/hold: %v", err)
		}
		code := resp.StatusCode
		resp.Body.Close()
		if code == 200 {
			t.Errorf("hours=%d was accepted; the picker only offers 0..4", h)
		}
	}
}

func TestAFaultStopsEverythingAndLightsTheLamp(t *testing.T) {
	ptpanel.RunLogDir = t.TempDir()
	t.Cleanup(func() { ptpanel.RunLogDir = "" })

	srv, f := newPanel(t)
	allowHold(f)
	postJSON(t, srv, "/api/connect", map[string]any{"port": "COM_TEST"})
	postJSON(t, srv, "/api/hold", map[string]any{
		"ports": []string{"din", "temp"}, "hours": 1,
	})

	st := postJSON(t, srv, "/api/fault", map[string]any{
		"on": true, "why": "din \u7b2c 3 \u8def\u6ca1\u53cd\u5e94",
	})
	if msg, _ := st["error"].(string); msg != "" {
		t.Fatalf("reporting a fault: %s", msg)
	}
	// One port failing stops every port. Production wants "this board is bad",
	// not which of five noticed first.
	if st["run"] != nil {
		t.Errorf("the run survived a fault: %v", st["run"])
	}

	cmds := strings.Join(f.commands(), "\n")
	for _, want := range []string{"pt.stop all", "pt.hold 0", "pt.led fault=1"} {
		if !strings.Contains(cmds, want) {
			t.Errorf("a fault did not send %q; sent:\n%s", want, cmds)
		}
	}
}

func TestDisconnectingStopsTheRunBeforeThePortCloses(t *testing.T) {
	ptpanel.RunLogDir = t.TempDir()
	t.Cleanup(func() { ptpanel.RunLogDir = "" })

	srv, f := newPanel(t)
	allowHold(f)
	postJSON(t, srv, "/api/connect", map[string]any{"port": "COM_TEST"})
	postJSON(t, srv, "/api/hold", map[string]any{"ports": []string{"din"}, "hours": 0})

	st := postJSON(t, srv, "/api/disconnect", nil)
	if st["connected"] != false {
		t.Fatalf("disconnect did not take: %v", st)
	}

	// Released while the port was still open. The board's own deadman would do
	// it a few seconds later anyway, but there is no reason to leave 24 V on
	// for those seconds - and a renewal goroutine outliving the connection
	// would be writing into a closed port.
	cmds := strings.Join(f.commands(), "\n")
	if !strings.Contains(cmds, "pt.stop all") {
		t.Errorf("disconnecting left the sessions running:\n%s", cmds)
	}

	n := countCmd(f, "pt.hold 6000")
	time.Sleep(3 * time.Second)
	if got := countCmd(f, "pt.hold 6000"); got != n {
		t.Errorf("renewals carried on after disconnect: %d -> %d", n, got)
	}
}

// A board that restarts mid-run sends its tick back towards zero. That is the
// event a burn-in exists to catch, so it stops the run and lights the lamp
// (decision 77) rather than being folded into the timeline as a wrap.
func TestABoardRestartMidRunIsAFault(t *testing.T) {
	ptpanel.RunLogDir = t.TempDir()
	t.Cleanup(func() { ptpanel.RunLogDir = "" })

	srv, f := newPanel(t)
	allowHold(f)
	postJSON(t, srv, "/api/connect", map[string]any{"port": "COM_TEST"})
	postJSON(t, srv, "/api/hold", map[string]any{"ports": []string{"din"}, "hours": 1})

	f.emit("!din t=90000 seq=7 rx=6 miss=0 v=0x00")
	f.emit("!din t=120 seq=1 rx=0 miss=0 v=0x00")

	waitFor(t, "the restart to light the lamp", 3*time.Second,
		func() bool { return countCmd(f, "pt.led fault=1") > 0 })
	if st := getJSON(t, srv, "/api/state"); st["run"] != nil {
		t.Errorf("the run survived a restart: %v", st["run"])
	}
}

// A board that hangs, or restarts and comes back with its sessions gone, simply
// stops sending frames. Silence past three of the port's own periods is a fault.
func TestAPortGoingSilentMidRunIsAFault(t *testing.T) {
	ptpanel.RunLogDir = t.TempDir()
	t.Cleanup(func() { ptpanel.RunLogDir = "" })

	srv, f := newPanel(t)
	allowHold(f)
	postJSON(t, srv, "/api/connect", map[string]any{"port": "COM_TEST"})
	postJSON(t, srv, "/api/hold", map[string]any{"ports": []string{"din"}, "hours": 1})

	f.emit("!din t=1000 seq=1 rx=0 miss=0 v=0x00")
	time.Sleep(200 * time.Millisecond)
	f.emit("!din t=1200 seq=2 rx=1 miss=0 v=0x00")

	// Three periods of 0.2 s plus the 5 s floor, then the once-a-second check.
	waitFor(t, "the silence to light the lamp", 9*time.Second,
		func() bool { return countCmd(f, "pt.led fault=1") > 0 })
	if st := getJSON(t, srv, "/api/state"); st["run"] != nil {
		t.Errorf("the run survived its port going silent: %v", st["run"])
	}
}

// An unplugged adapter or a board that lost power ends the reader. The page
// must say so instead of showing "已连上" over a board that answers nothing.
func TestLosingTheControlPortIsShown(t *testing.T) {
	srv, f := newPanel(t)
	postJSON(t, srv, "/api/connect", map[string]any{"port": "COM_TEST"})
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
