// Package ptseq runs a plan against one board.
//
// It owns the meaning of the six fields every step carries - the condition
// gate, the retries, the two sleeps and the timeout. Those fields are what
// separates a framework from a pile of scripts: without the gate a sequence
// can only run to the end, and without the retries a worn fixture probe reads
// as a bad board.
//
// Nothing here knows what any port measures. The plan says which port, which
// parameters and which limits; this package sends, collects and hands the
// readings to ptcheck.
package ptseq

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"PortTool/internal/ptboard"
	"PortTool/internal/ptcheck"
	"PortTool/internal/ptecho"
	"PortTool/internal/ptplan"
	"PortTool/internal/ptproto"
	"PortTool/internal/ptreport"
)

// Runner executes plans. Log, Sleep and Now are injectable so a plan can be
// run against a fake board with no real clock.
type Runner struct {
	Board *ptboard.Board

	// Caps, when set, is used instead of asking the board again.
	Caps *ptproto.Caps

	// Log receives one progress line per step.
	Log func(string)

	// Confirm asks a person to look and decide. A nil Confirm makes every
	// UserConfirm step an error rather than a silent pass: a step meant for a
	// person that quietly passed itself is the worst outcome available.
	Confirm func(prompt string) (bool, error)

	// RunTool starts an external program. Nil uses os/exec.
	RunTool func(name string, args []string, dir string, timeout time.Duration) (output string, exit int, err error)

	// OpenPeer opens the far end of a link=loop session - a serial port or a
	// TCP connection. Nil uses the real ones.
	//
	// ⚠️ A test MUST inject this. Without it a plan naming peer.com "COM16"
	// opens whatever COM16 happens to be on the machine running the test,
	// which is how a unit test starts depending on what is plugged into the
	// bench. kind is "serial" or "tcp".
	OpenPeer func(kind, addr string, baud int) (io.ReadWriteCloser, error)

	// FindCDC names the board's USB CDC port for a step whose peer is usb.
	// Nil uses the real enumeration; a test injects it for the same reason it
	// injects OpenPeer - otherwise the result depends on what is plugged in.
	FindCDC func() (string, error)

	// SerialPeer names the adapter this machine recorded for a board port, for
	// a step whose peer is "serial". Nil or !ok means nobody has chosen one.
	SerialPeer func(boardPort string) (com string, baud int, ok bool)

	// ReadSN and WriteReport are the two ends of the "interfaces only"
	// boundary. Nil uses the file and stdin handling below.
	ReadSN      func(source string) (string, error)
	WriteReport func(sink string, rep ptreport.Report) (path string, err error)

	// BaseDir is what a relative path in a plan is relative to - the plan's
	// own directory, so a plan and the files it names travel together.
	BaseDir string

	// current is the report being built, so a ReadSN step can put the serial
	// number into it and a PushResult step can hand it on mid-run.
	current *ptreport.Report

	// Sleep and Now are the clock. Nil uses the real one.
	Sleep func(time.Duration)
	Now   func() time.Time

	// ToolVersion and SN go straight into the report.
	ToolVersion string
	SN          string
	PortName    string
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) sleep(d time.Duration) {
	if d <= 0 {
		return
	}
	if r.Sleep != nil {
		r.Sleep(d)
		return
	}
	time.Sleep(d)
}

func (r *Runner) logf(format string, args ...any) {
	if r.Log == nil {
		return
	}
	r.Log(fmt.Sprintf(format, args...))
}

// Run executes every step in order and returns the report. The error is only
// for a run that could not start; a board that fails its limits is a report
// with failures in it, not an error.
func (r *Runner) Run(p ptplan.Plan) (ptreport.Report, error) {
	if r.Board == nil {
		return ptreport.Report{}, errors.New("no board to run against")
	}

	rep := ptreport.Report{
		Plan:         p.Name,
		LimitVersion: p.LimitVersion,
		ToolVersion:  r.ToolVersion,
		SN:           r.SN,
		Port:         r.PortName,
		StartedAt:    r.now(),
	}
	r.current = &rep
	defer func() { r.current = nil }()

	rep.Firmware, rep.BoardUID = r.identify()

	// The gate looks at the last step that actually ran. A step skipped by its
	// own condition leaves the previous verdict standing, so a failure early on
	// keeps propagating instead of being cleared by the steps it skipped.
	lastRan := ptreport.OutcomePass
	anyRan := false

	for _, step := range p.Steps {
		res := r.runStep(step, lastRan, anyRan)
		rep.Steps = append(rep.Steps, res)
		if res.Outcome != ptreport.OutcomeSkipped {
			lastRan = res.Outcome
			anyRan = true
		}
	}

	rep.EndedAt = r.now()
	return rep, nil
}

// identify records what the board says it is, so a report names the firmware
// and the chip it was produced by. Both are best effort: a board too broken to
// answer still deserves a report of how it failed.
func (r *Runner) identify() (firmware, uid string) {
	caps := r.Caps
	if caps == nil {
		if c, err := r.Board.Caps(); err == nil {
			caps = &c
		}
	}
	if caps != nil {
		firmware = caps.Version
	}
	if lines, err := r.Board.Send("pt.id", ptboard.ExpectOne, ptboard.DefaultTimeout); err == nil && len(lines) > 0 {
		if kind, body := ptproto.Classify(lines[0]); kind == ptproto.LineOK {
			if v, ok := ptproto.Get(ptproto.Fields(body), "uid"); ok {
				uid = v
			}
		}
	}
	return firmware, uid
}

func (r *Runner) runStep(step ptplan.Step, lastRan ptreport.Outcome, anyRan bool) ptreport.StepResult {
	res := ptreport.StepResult{
		ID:        step.ID,
		Type:      string(step.Type),
		StartedAt: r.now(),
	}

	if !step.IsEnabled() {
		res.Outcome = ptreport.OutcomeSkipped
		res.Reason = "switched off in the plan"
		r.logf("SKIP %s - %s", step.ID, res.Reason)
		return res
	}
	if reason, ok := gate(step.Condition(), lastRan, anyRan); !ok {
		res.Outcome = ptreport.OutcomeSkipped
		res.Reason = reason
		r.logf("SKIP %s - %s", step.ID, reason)
		return res
	}

	r.sleep(time.Duration(step.SleepBeforeMS) * time.Millisecond)

	attempts := step.Attempts()
	for n := 1; n <= attempts; n++ {
		att := r.attempt(step, n)
		res.Attempts = append(res.Attempts, att)
		res.Outcome = att.Outcome
		if att.Outcome.IsPass() {
			break
		}
		if n < attempts {
			r.logf("retry %d/%d %s - %s", n, attempts-1, step.ID, attemptWhy(att))
			r.sleep(time.Duration(step.RetryIntervalMS) * time.Millisecond)
		}
	}

	r.sleep(time.Duration(step.SleepAfterMS) * time.Millisecond)

	res.DurationMS = r.now().Sub(res.StartedAt).Milliseconds()
	if res.Outcome.IsPass() {
		r.logf("PASS %s (%s)", step.ID, step.Type)
	} else {
		r.logf("%s %s (%s) - %s", res.Outcome, step.ID, step.Type,
			attemptWhy(res.Attempts[len(res.Attempts)-1]))
	}
	return res
}

// gate applies execute_condition.
func gate(cond ptplan.Condition, lastRan ptreport.Outcome, anyRan bool) (string, bool) {
	switch cond {
	case ptplan.CondAlways:
		return "", true
	case ptplan.CondFail:
		if !anyRan {
			return "condition is FAIL but nothing has run yet", false
		}
		if lastRan.IsPass() {
			return "condition is FAIL but the last step that ran passed", false
		}
		return "", true
	default: // CondPass
		if !anyRan {
			return "", true // nothing has failed yet
		}
		if !lastRan.IsPass() {
			return fmt.Sprintf("condition is PASS but the last step that ran was %s", lastRan), false
		}
		return "", true
	}
}

func attemptWhy(a ptreport.Attempt) string {
	if a.Err != "" {
		return a.Err
	}
	for _, c := range a.Checks {
		if !c.Pass {
			return fmt.Sprintf("%s: %s", c.Check.Describe(), c.Why)
		}
	}
	return string(a.Outcome)
}

func (r *Runner) attempt(step ptplan.Step, n int) ptreport.Attempt {
	att := ptreport.Attempt{N: n, StartedAt: r.now()}
	timeout := time.Duration(step.Timeout()) * time.Millisecond

	switch step.Type {
	case ptplan.TypePtSession:
		r.doSession(step, timeout, &att)
	case ptplan.TypePtRun:
		r.doRun(step, timeout, &att)
	case ptplan.TypePtRaw:
		r.doRaw(step, timeout, &att)
	case ptplan.TypeTool:
		r.doTool(step, timeout, &att)
	case ptplan.TypeUserConfirm:
		r.doConfirm(step, &att)
	case ptplan.TypeReadSN:
		r.doReadSN(step, &att)
	case ptplan.TypePushResult:
		r.doPushResult(step, &att)
	default:
		att.Outcome = ptreport.OutcomeError
		att.Err = fmt.Sprintf("step type %q is not implemented", step.Type)
	}

	att.DurationMS = r.now().Sub(att.StartedAt).Milliseconds()
	return att
}

// doSession starts a port, waits for the frames the plan asked for, judges the
// last one, then stops the port whatever happened.
//
// The subscription opens before pt.start: a session at a short period can push
// its first frame before the reply to pt.start has been read.
func (r *Runner) doSession(step ptplan.Step, timeout time.Duration, att *ptreport.Attempt) {
	var peerCOM string
	var peerBaud int
	if step.Peer != nil && step.Peer.Serial {
		ok := false
		if r.SerialPeer != nil {
			peerCOM, peerBaud, ok = r.SerialPeer(step.Port)
		}
		if !ok {
			att.Outcome = ptreport.OutcomeError
			att.Err = fmt.Sprintf("no serial adapter chosen for %s on this machine: bind one once in the panel", step.Port)
			return
		}
	}

	events, unsubscribe := r.Board.Subscribe(256)
	defer unsubscribe()

	start := "pt.start " + step.Port
	if args := step.ParamArgs(); len(args) > 0 {
		start += " " + strings.Join(args, " ")
	}
	att.Sent = append(att.Sent, start)

	deadline := r.now().Add(timeout)
	lines, err := r.Board.Send(start, ptboard.ExpectOne, timeout)
	att.Raw = append(att.Raw, lines...)
	if err != nil {
		r.stopQuietly(step.Port, att)
		r.classifyLinkError(err, deadline, att)
		return
	}

	// Peers are opened from inside the collect loop, once per frame until they
	// are up.
	//
	// ⚠️ Not once before, and not once on the first frame. A board that has
	// just been powered on has no DHCP address for several seconds and has not
	// finished enumerating its CDC pipe - and "just powered on" is exactly
	// when a production station tests it. Trying once and giving up made every
	// link port fail on the first run after a reset, which is the run that
	// matters.
	var peers []*ptecho.Peer
	defer func() {
		for _, p := range peers {
			p.Stop()
		}
	}()
	open := r.OpenPeer
	if open == nil {
		open = realPeerOpener
	}
	findCDC := r.FindCDC
	if findCDC == nil {
		findCDC = ptecho.FindCDC
	}
	pending := newPeerPlan(step, peerCOM, peerBaud, open, findCDC, r.now())

	want := step.FrameCount()
	var last ptproto.Frame
	got := 0
	linkGone := false

collect:
	for got < want || pending.outstanding(r.now()) {
		remaining := deadline.Sub(r.now())
		if remaining <= 0 {
			break
		}
		// Capped while a peer is still missing, so the loop can re-check
		// whether the peer came up or its budget ran out. Without the cap the
		// select blocks until the step's own deadline the moment the frames
		// stop, and the budget never gets spent.
		wait := remaining
		if pending.outstanding(r.now()) && wait > 500*time.Millisecond {
			wait = 500 * time.Millisecond
		}
		timer := time.NewTimer(wait)
		select {
		case ev, ok := <-events:
			timer.Stop()
			if !ok {
				linkGone = true
				break collect
			}
			if ev.Kind != ptproto.LineFrame || ev.Frame.Port != step.Port {
				continue
			}
			last = ev.Frame
			got++
			att.Raw = append(att.Raw, ev.Line)
			r.answerEcho(ev.Frame, att)

			// Every frame is another chance to get the peers up: this one
			// may be the first that carries a DHCP address, or the CDC port
			// may have finished enumerating since the last one.
			if opened := pending.tryOpen(ev.Frame, att); len(opened) > 0 {
				peers = append(peers, opened...)
				// The frames before a peer existed went unanswered, so the
				// board's miss is already non-zero through no fault of the
				// link. Restarting the count is the difference between judging
				// the link and judging how fast this program got ready.
				got = 0
			}
		case <-timer.C:
			// Only the real deadline ends the collection. A short wait above
			// is a re-check tick for the peer, not a timeout - treating it as
			// one cut the frame count short and reported a timeout on a board
			// that was still sending.
			if !r.now().Before(deadline) {
				break collect
			}
		}
	}

	// Peers stop before the port does, so nothing is still writing at a board
	// that has been told to stop.
	recordPeerStats(peers, att)
	for _, p := range peers {
		p.Stop()
	}
	peers = nil

	r.stopQuietly(step.Port, att)

	if linkGone {
		att.Outcome = ptreport.OutcomeError
		att.Err = fmt.Sprintf("the board stopped answering after %d of %d frame(s) from %s", got, want, step.Port)
		return
	}
	if got < want {
		att.Outcome = ptreport.OutcomeTimeout
		att.Err = fmt.Sprintf("wanted %d frame(s) from %s within %s, got %d",
			want, step.Port, timeout, got)
		return
	}

	r.judge(step.Checks, FrameLookup(last), att)
}

// answerEcho closes the loopback count for one frame, the way the panel's
// responder does.
//
// Without it the board's seq stays put and miss climbs on a perfectly healthy
// board, because the counter only advances when the PC sends back the number
// it was given - so any plan judging miss would fail every time. The panel has
// answered these since the counter existed; a production run has to as well.
//
// Which frames take one is ptproto.Caps.EchoCommand's to say.
func (r *Runner) answerEcho(f ptproto.Frame, att *ptreport.Attempt) {
	if r.Caps == nil {
		return
	}
	cmd, ok := r.Caps.EchoCommand(f)
	if !ok {
		return
	}
	if _, err := r.Board.Send(cmd, ptboard.ExpectOne, ptboard.DefaultTimeout); err != nil {
		// Recorded, never fatal: the reading is what the step judges, and a
		// refused echo shows up as the miss counter climbing anyway.
		att.SetExtra("echo_error", err.Error())
	}
}

// stopQuietly releases the port. A failure to stop is recorded but does not
// change the step's verdict: the reading already happened, and leaving the
// verdict to the cleanup would blame the board for a port that went away.
func (r *Runner) stopQuietly(port string, att *ptreport.Attempt) {
	cmd := "pt.stop " + port
	att.Sent = append(att.Sent, cmd)
	lines, err := r.Board.Send(cmd, ptboard.ExpectOne, ptboard.DefaultTimeout)
	att.Raw = append(att.Raw, lines...)
	if err != nil {
		att.SetExtra("stop_error", err.Error())
	}
}

func (r *Runner) doRun(step ptplan.Step, timeout time.Duration, att *ptreport.Attempt) {
	cmd := "pt.run " + step.Target
	if args := step.ParamArgs(); len(args) > 0 {
		cmd += " " + strings.Join(args, " ")
	}
	att.Sent = append(att.Sent, cmd)

	deadline := r.now().Add(timeout)
	lines, err := r.Board.Send(cmd, ptboard.ExpectOne, timeout)
	att.Raw = append(att.Raw, lines...)
	if err != nil {
		r.classifyLinkError(err, deadline, att)
		return
	}
	r.judge(step.Checks, ReplyLookup(lines), att)
}

func (r *Runner) doRaw(step ptplan.Step, timeout time.Duration, att *ptreport.Attempt) {
	att.Sent = append(att.Sent, step.Command)
	deadline := r.now().Add(timeout)
	lines, err := r.Board.Send(step.Command, ptboard.ExpectFor(step.Command), timeout)
	att.Raw = append(att.Raw, lines...)
	if err != nil {
		r.classifyLinkError(err, deadline, att)
		return
	}
	r.judge(step.Checks, ReplyLookup(lines), att)
}

// doTool runs an external program and judges what it printed or returned.
//
// Which program and which arguments are plan data, so a new instrument or a
// different programmer is a plan edit, not a build. That is how the reference
// architecture drives adb, and it is why there is no driver layer here.
func (r *Runner) doTool(step ptplan.Step, timeout time.Duration, att *ptreport.Attempt) {
	att.Sent = append(att.Sent,
		strings.TrimSpace(step.ToolName+" "+strings.Join(step.ToolArgs, " ")))

	run := r.RunTool
	if run == nil {
		run = execTool
	}
	dir := step.ToolDir
	if dir == "" {
		dir = r.BaseDir
	}

	out, exit, err := run(step.ToolName, step.ToolArgs, dir, timeout)
	for _, line := range strings.Split(strings.TrimRight(out, "\r\n"), "\n") {
		if line != "" {
			att.Raw = append(att.Raw, strings.TrimRight(line, "\r"))
		}
	}
	att.SetExtra("exit", strconv.Itoa(exit))
	if err != nil {
		att.Outcome = ptreport.OutcomeError
		att.Err = err.Error()
		return
	}

	if len(step.Checks) == 0 {
		// Judged by exit code, which is what a programmer or an instrument CLI
		// already reports. Unlike a board reading, that IS a verdict already.
		if exit == 0 {
			att.Outcome = ptreport.OutcomePass
		} else {
			att.Outcome = ptreport.OutcomeFail
			att.Err = fmt.Sprintf("%s exited %d", step.ToolName, exit)
		}
		return
	}
	r.judge(step.Checks, toolLookup(out, exit), att)
}

// doConfirm asks a person. For the readings no board can report on itself.
func (r *Runner) doConfirm(step ptplan.Step, att *ptreport.Attempt) {
	att.Sent = append(att.Sent, "ask the operator: "+step.Prompt)
	if r.Confirm == nil {
		att.Outcome = ptreport.OutcomeError
		att.Err = "this step needs a person to answer, and this run has no way to ask"
		return
	}
	ok, err := r.Confirm(step.Prompt)
	if err != nil {
		att.Outcome = ptreport.OutcomeError
		att.Err = err.Error()
		return
	}
	if ok {
		att.SetExtra("answer", "pass")
		att.Outcome = ptreport.OutcomePass
		return
	}
	att.SetExtra("answer", "fail")
	att.Outcome = ptreport.OutcomeFail
	att.Err = "the operator marked this a failure"
}

// doReadSN brings a serial number in from outside and puts it in the report.
// This tool never invents one - see DECISIONS.md 20.
func (r *Runner) doReadSN(step ptplan.Step, att *ptreport.Attempt) {
	att.Sent = append(att.Sent, "read serial number from "+step.Source)

	read := r.ReadSN
	if read == nil {
		read = r.readSNDefault
	}
	sn, err := read(step.Source)
	if err != nil {
		att.Outcome = ptreport.OutcomeError
		att.Err = err.Error()
		return
	}
	sn = strings.TrimSpace(sn)
	if sn == "" {
		att.Outcome = ptreport.OutcomeFail
		att.Err = "no serial number came back from " + step.Source
		return
	}

	r.SN = sn
	if r.current != nil {
		r.current.SN = sn
	}
	att.SetExtra("sn", sn)
	att.Raw = append(att.Raw, "sn="+sn)

	if len(step.Checks) == 0 {
		att.Outcome = ptreport.OutcomePass
		return
	}
	// A plan can insist on a shape - a prefix, a length - so a mistyped or
	// truncated scan is caught here rather than in the MES a day later.
	r.judge(step.Checks, func(field string) (string, bool) {
		if field == "sn" || field == "_text" {
			return sn, true
		}
		return "", false
	}, att)
}

// doPushResult hands the report so far to whatever comes next.
//
// "So far" is exact: this step is running, so it is not in what it writes.
// That is the honest thing to write - a report claiming to contain the step
// that produced it would be describing a run that had not finished.
func (r *Runner) doPushResult(step ptplan.Step, att *ptreport.Attempt) {
	att.Sent = append(att.Sent, "push report to "+step.Sink)
	if r.current == nil {
		att.Outcome = ptreport.OutcomeError
		att.Err = "no report to push"
		return
	}

	write := r.WriteReport
	if write == nil {
		write = r.writeReportDefault
	}
	path, err := write(step.Sink, *r.current)
	if err != nil {
		att.Outcome = ptreport.OutcomeError
		att.Err = err.Error()
		return
	}
	att.SetExtra("wrote", path)
	att.Raw = append(att.Raw, "wrote "+path)
	att.Outcome = ptreport.OutcomePass
}

// readSNDefault handles the three sources a plan may name.
func (r *Runner) readSNDefault(source string) (string, error) {
	kind, arg := ptplan.SourceKind(source)
	switch kind {
	case "arg":
		if r.SN == "" {
			return "", errors.New("the plan reads the serial number from --sn, and none was given")
		}
		return r.SN, nil
	case "stdin":
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", fmt.Errorf("reading a serial number from stdin: %w", err)
		}
		return line, nil
	case "file":
		data, err := os.ReadFile(r.resolve(arg))
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	return "", fmt.Errorf("unknown source %q", source)
}

func (r *Runner) writeReportDefault(sink string, rep ptreport.Report) (string, error) {
	kind, arg := ptplan.SinkKind(sink)
	path := r.resolve(arg)
	if kind == "dir" {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return "", err
		}
		// Named so nothing is ever overwritten: a second run of the same board
		// has to sit beside the first, never on top of it (DECISIONS.md 20 and
		// the production guide's "failure data must not be overwritten").
		name := rep.SN
		if name == "" {
			name = rep.BoardUID
		}
		if name == "" {
			name = "unknown"
		}
		path = filepath.Join(path, fmt.Sprintf("%s-%s.json",
			sanitise(name), r.now().Format("20060102-150405.000")))
	}
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	err = rep.WriteJSON(f)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return path, err
}

// resolve makes a plan-relative path absolute against the plan's directory, so
// a plan and the files it names travel together.
func (r *Runner) resolve(p string) string {
	if p == "" || filepath.IsAbs(p) || r.BaseDir == "" {
		return p
	}
	return filepath.Join(r.BaseDir, p)
}

func sanitise(s string) string {
	return strings.Map(func(c rune) rune {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.':
			return c
		}
		return '_'
	}, s)
}

func execTool(name string, args []string, dir string, timeout time.Duration) (string, int, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir

	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.CombinedOutput()
		close(done)
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
		return string(out), -1, fmt.Errorf("%s did not finish within %s", name, timeout)
	}

	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// A non-zero exit is a result, not a failure to run.
			return string(out), ee.ExitCode(), nil
		}
		return string(out), -1, fmt.Errorf("could not run %s: %w", name, err)
	}
	return string(out), 0, nil
}

func toolLookup(output string, exit int) ptcheck.Lookup {
	return func(field string) (string, bool) {
		switch field {
		case "_text":
			return output, true
		case "_exit":
			return strconv.Itoa(exit), true
		}
		return "", false
	}
}

// judge runs the limits and sets the verdict. No limits at all is an error,
// not a pass: a step that reads a board and decides nothing is worse than a
// missing step, because it appears in the report as if it proved something.
func (r *Runner) judge(checks []ptcheck.Check, look ptcheck.Lookup, att *ptreport.Attempt) {
	if len(checks) == 0 {
		att.Outcome = ptreport.OutcomeError
		att.Err = "this step has no checks, so there is nothing to judge"
		return
	}
	results, all := ptcheck.EvalAll(checks, look)
	att.Checks = results
	if all {
		att.Outcome = ptreport.OutcomePass
	} else {
		att.Outcome = ptreport.OutcomeFail
	}
}

// classifyLinkError decides whether a failed command is the board refusing
// (a verdict), running out of time, or the link itself going away.
func (r *Runner) classifyLinkError(err error, deadline time.Time, att *ptreport.Attempt) {
	var refused *ptboard.RefusedError
	if errors.As(err, &refused) {
		// A refusal is a real verdict, not a transport problem: the firmware
		// refuses to start the analog ports when VREFBUF will not come up, and
		// that is a board that failed, reported in the board's own words.
		att.Outcome = ptreport.OutcomeFail
		att.Err = refused.Reason
		return
	}
	att.Err = err.Error()
	if !r.now().Before(deadline) {
		att.Outcome = ptreport.OutcomeTimeout
		return
	}
	att.Outcome = ptreport.OutcomeError
}

// FrameLookup reads fields out of a sample frame. "_text" is the whole line,
// for a contains check. The panel judges by it too, so a field means the same
// thing on screen and on a line.
func FrameLookup(f ptproto.Frame) ptcheck.Lookup {
	return func(field string) (string, bool) {
		if field == "_text" {
			return f.Raw, true
		}
		return ptproto.Get(f.Fields, field)
	}
}

// ReplyLookup reads fields out of a command reply. Fields come from the first
// OK line; "_text" is every line, so a check can look for a word anywhere in
// a multi-line answer. The panel judges by it too.
func ReplyLookup(lines []string) ptcheck.Lookup {
	var pairs []ptproto.Pair
	for _, l := range lines {
		if kind, body := ptproto.Classify(l); kind == ptproto.LineOK {
			pairs = ptproto.Fields(body)
			break
		}
	}
	text := strings.Join(lines, "\n")
	return func(field string) (string, bool) {
		if field == "_text" {
			return text, true
		}
		return ptproto.Get(pairs, field)
	}
}
