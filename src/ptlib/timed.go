package ptlib

import (
	"context"
	"errors"
	"sync"
	"time"

	"PortTool/src/ptboard"
	"PortTool/src/ptcheck"
	"PortTool/src/ptplan"
	"PortTool/src/ptproto"
	"PortTool/src/ptreport"
	"PortTool/src/ptseq"
)

// Round is one finished round of a timed item, for the card's summary and
// one log line (decision 151): a looped job's run, or RoundEvery of a kept
// session's frames. PORTTOOL-FLOW.md C.3.6 「时长测试看得懂」.
type Round struct {
	No      int              `json:"no"`
	Outcome ptreport.Outcome `json:"outcome"`
	Frames  int              `json:"frames,omitempty"` // a kept session: frames judged this round
	Values  [][2]string      `json:"values,omitempty"` // the board's fields, criteria not met first
}

// RoundEvery is how long one round of a kept session is.
const RoundEvery = time.Minute

// roundOf is the round just finished, from the item's latest readings.
func roundOf(no int, o ptreport.Outcome, frames int, s *State) *Round {
	r := &Round{No: no, Outcome: o, Frames: frames}
	seen := map[string]bool{}
	for _, c := range s.Checks {
		if !c.Pass && !seen[c.Check.Field] {
			seen[c.Check.Field] = true
			r.Values = append(r.Values, [2]string{c.Check.Field, c.Got})
		}
	}
	for _, v := range s.Values {
		if !seen[v[0]] {
			seen[v[0]] = true
			r.Values = append(r.Values, v)
		}
	}
	if len(r.Values) > 4 {
		r.Values = r.Values[:4]
	}
	return r
}

// live is a session kept running for the whole timed phase.
type live struct {
	j       *job
	port    string
	warm    int // frames before the first judgement, as the plan's step would wait
	frames  int
	judged  int
	att     ptreport.Attempt
	round   int       // rounds finished
	inRnd   int       // frames judged in the round under way
	lastRnd int       // frames judged in the round just finished
	rndAt   time.Time // when the round under way began
}

// roundDone closes the round under way once RoundEvery has passed, or at
// once on a failed frame; it returns the round's number, 0 if still open.
func (l *live) roundDone(at time.Time, o ptreport.Outcome) int {
	if o != ptreport.OutcomeFail && at.Sub(l.rndAt) < RoundEvery {
		return 0
	}
	l.round++
	l.lastRnd, l.inRnd, l.rndAt = l.inRnd, 0, at
	return l.round
}

// looped is a job re-run back to back on its port for the timed phase.
type looped struct {
	j      *job
	rounds int
	last   ptreport.Outcome
}

// runTimed runs the timed phase: every job at once until dur is up, stopping
// at the first failure (decision 102, STG-26). A session with nothing else on
// its port stays running and every frame is judged; the rest repeat on their
// own port. Relays run once to their count alongside, and the phase does not
// end before they got there (decision 123).
func (e *Engine) runTimed(ctx context.Context, jobs []*job, dur time.Duration) {
	byPort := map[string][]*job{}
	var order []string
	var counted []*job
	for _, j := range jobs {
		if j.relay {
			counted = append(counted, j)
			continue
		}
		if _, ok := byPort[j.port()]; !ok {
			order = append(order, j.port())
		}
		byPort[j.port()] = append(byPort[j.port()], j)
	}
	var cont []*job
	var groups [][]*looped
	for _, p := range order {
		js := byPort[p]
		if len(js) == 1 && js[0].fail == "" && js[0].continuous() {
			cont = append(cont, js[0])
			continue
		}
		var g []*looped
		for _, j := range js {
			g = append(g, &looped{j: j})
		}
		groups = append(groups, g)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		failMu    sync.Mutex
		failedJob *job
		failWhy   string
		failSet   bool
	)
	isFailed := func() bool {
		failMu.Lock()
		defer failMu.Unlock()
		return failSet
	}
	fail := func(j *job, why string) {
		failMu.Lock()
		if !failSet {
			failSet, failedJob, failWhy = true, j, why
		}
		failMu.Unlock()
		cancel()
	}

	events, unsub := e.Board.Subscribe(1024)
	defer unsub()

	lives := map[string]*live{}
	var contPorts []string
	timeout := time.Duration(ptplan.DefaultTimeoutMS) * time.Millisecond
	for _, j := range cont {
		cmd, port, warm := j.sessionStart()
		e.change(j.item.ID, func(s *State) { s.Running = true })
		l := &live{j: j, port: port, warm: warm, att: ptreport.Attempt{StartedAt: time.Now(), Sent: []string{cmd}}, rndAt: time.Now()}
		lines, err := e.Board.Send(cmd, ptboard.ExpectOne, timeout)
		l.att.Raw = append(l.att.Raw, lines...)
		lives[port] = l
		contPorts = append(contPorts, port)
		if err != nil {
			var refused *ptboard.RefusedError
			l.att.Outcome, l.att.Err = ptreport.OutcomeError, err.Error()
			if errors.As(err, &refused) {
				l.att.Outcome, l.att.Err = ptreport.OutcomeFail, refused.Reason
			}
			fail(j, l.att.Err)
			break
		}
	}

	var ended <-chan struct{}
	holdEnded := false
	if !isFailed() {
		var err error
		if ended, err = e.Hold.Start(contPorts, dur); err != nil {
			fail(nil, err.Error())
		}
	}

	var wg sync.WaitGroup
	countedOut := make([]ptreport.Outcome, len(counted))
	countedWhy := make([]string, len(counted))
	countedReason := make([]string, len(counted))
	countedDone := make(chan struct{})
	if isFailed() || len(counted) == 0 {
		close(countedDone)
	} else {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(countedDone)
			for i, j := range counted {
				e.change(j.item.ID, func(s *State) { s.Running = true })
				countedOut[i], countedReason[i], countedWhy[i] = e.runRelay(runCtx, j, true)
				e.change(j.item.ID, func(s *State) { s.Rounds++ })
				if runCtx.Err() != nil {
					return
				}
				if countedOut[i] != Recorded {
					fail(j, countedWhy[i])
					return
				}
			}
		}()
	}
	if !isFailed() {
		for _, g := range groups {
			for _, lj := range g {
				e.change(lj.j.item.ID, func(s *State) { s.Running = true })
			}
			wg.Add(1)
			go func(g []*looped) {
				defer wg.Done()
				for runCtx.Err() == nil {
					for _, lj := range g {
						if runCtx.Err() != nil {
							return
						}
						o, _, detail := e.once(runCtx, lj.j)
						if runCtx.Err() != nil {
							return // a round cut short by the end proves nothing
						}
						lj.rounds++
						lj.last = o
						n := lj.rounds
						e.change(lj.j.item.ID, func(s *State) { s.Rounds, s.Round = n, roundOf(n, o, 0, s) })
						if failed(o) || o == ptreport.OutcomeStopped {
							fail(lj.j, detail)
							return
						}
					}
				}
			}(g)
		}
	}

	var deadline <-chan time.Time
	if dur > 0 {
		t := time.NewTimer(dur)
		defer t.Stop()
		deadline = t.C
	}
	timeUp := e.TimeUp
	// The phase ends once the time is up and the relays reached their counts.
	durOver, countsReached := false, false

watch:
	for !isFailed() {
		select {
		case <-runCtx.Done():
			break watch
		case <-deadline:
			deadline, durOver = nil, true
			if countsReached {
				break watch
			}
		case <-timeUp:
			timeUp, durOver = nil, true
			if countsReached {
				break watch
			}
		case <-countedDone:
			countedDone, countsReached = nil, true
			if durOver {
				break watch
			}
		case <-ended:
			holdEnded = true
			fail(nil, e.Hold.Why())
			break watch
		case ev, ok := <-events:
			if !ok {
				fail(nil, "the board stopped answering")
				break watch
			}
			if ev.Kind != ptproto.LineFrame {
				continue
			}
			l := lives[ev.Frame.Port]
			if l == nil {
				continue
			}
			l.frames++
			l.att.Raw = append(l.att.Raw[:min(len(l.att.Raw), 2)], ev.Line)
			vals := valuesOf(ptreport.Attempt{Raw: []string{ev.Line}})
			if l.j.record {
				n := l.frames
				l.inRnd++
				rnd := l.roundDone(ev.At, Recorded)
				e.change(l.j.item.ID, func(s *State) {
					s.Values, s.Rounds = vals, n
					if rnd > 0 {
						s.Round = roundOf(rnd, Recorded, l.lastRnd, s)
					}
				})
				continue
			}
			if l.frames < l.warm {
				continue
			}
			res, all := ptcheck.EvalAll(l.j.steps[0].Checks, ptseq.FrameLookup(ev.Frame))
			l.judged++
			l.att.Checks = res
			n := l.judged
			l.inRnd++
			o := ptreport.OutcomePass
			if !all {
				o = ptreport.OutcomeFail
			}
			rnd := l.roundDone(ev.At, o)
			e.change(l.j.item.ID, func(s *State) {
				s.Values, s.Checks, s.Rounds = vals, res, n
				if rnd > 0 {
					s.Round = roundOf(rnd, o, l.lastRnd, s)
				}
			})
			if !all {
				l.att.Outcome = ptreport.OutcomeFail
				fail(l.j, "")
				break watch
			}
		}
	}

	userStop := ctx.Err() != nil
	failMu.Lock()
	isFail := failSet && !userStop
	fj, why := failedJob, failWhy
	failMu.Unlock()
	cancel()

	// AO under load is read once at the end, its current still on (decision 128).
	loadOut := map[*job][3]string{}
	if !isFail && !userStop && !holdEnded {
		for _, port := range contPorts {
			if l := lives[port]; l.j.load {
				o, r, d := e.measureLoad(ctx, l.j)
				loadOut[l.j] = [3]string{string(o), r, d}
			}
		}
	}

	// Outputs off first, then wait for rounds still in flight: they end
	// quickly once their ports are stopped.
	switch {
	case holdEnded:
	case isFail:
		e.Hold.Fault(failText(fj, why))
	default:
		e.Hold.Stop()
	}
	wg.Wait()

	reasonLeft := ReasonStopped
	switch {
	case holdEnded:
		reasonLeft = ReasonHoldEnded
	case isFail:
		reasonLeft = ReasonAfterFail
	}
	finished := !isFail && !userStop
	// The failed item carries its own detail; the ones stopped after it do not repeat it.
	whyLeft := why
	if isFail {
		whyLeft = ""
	}

	for _, port := range contPorts {
		l := lives[port]
		l.att.DurationMS = time.Since(l.att.StartedAt).Milliseconds()
		switch {
		case l.j == fj && isFail:
			if l.att.Outcome == "" {
				l.att.Outcome = ptreport.OutcomeFail
			}
			e.addAttempts(l.j, l.att)
			e.finish(l.j, l.att.Outcome, "", l.att.Err)
		case finished && l.j.load:
			o := loadOut[l.j]
			l.att.Outcome = ptreport.Outcome(o[0])
			e.addAttempts(l.j, l.att)
			e.finish(l.j, l.att.Outcome, o[1], o[2])
		case finished && l.j.record:
			l.att.Outcome = Recorded
			e.addAttempts(l.j, l.att)
			e.finish(l.j, Recorded, "", "")
		case finished && l.judged > 0:
			l.att.Outcome = ptreport.OutcomePass
			e.addAttempts(l.j, l.att)
			e.finish(l.j, ptreport.OutcomePass, "", "")
		case finished:
			l.att.Outcome = ptreport.OutcomeTimeout
			e.addAttempts(l.j, l.att)
			e.finish(l.j, ptreport.OutcomeTimeout, ReasonNoFrames, "")
		default:
			l.att.Outcome = ptreport.OutcomeStopped
			e.addAttempts(l.j, l.att)
			e.finish(l.j, ptreport.OutcomeStopped, reasonLeft, whyLeft)
		}
	}
	for _, j := range cont {
		if _, ok := lives[j.port()]; !ok {
			e.finish(j, ptreport.OutcomeSkipped, reasonLeft, "")
		}
	}
	for _, g := range groups {
		for _, lj := range g {
			switch {
			case lj.j == fj && isFail:
				e.finish(lj.j, lj.last, lj.j.fail, why)
			case finished && lj.rounds > 0:
				e.finish(lj.j, lj.last, "", "")
			case lj.rounds > 0 || isFail || userStop:
				e.finish(lj.j, ptreport.OutcomeStopped, reasonLeft, "")
			default:
				e.finish(lj.j, ptreport.OutcomeStopped, ReasonStopped, "")
			}
		}
	}
	// Relays are judged last, with every output already off (decision 123).
	for i, j := range counted {
		switch {
		case j == fj && isFail:
			e.finish(j, countedOut[i], countedReason[i], countedWhy[i])
		case finished && countedOut[i] == Recorded:
			o, r := e.relayVerdict(ctx, j)
			e.finish(j, o, r, "")
		case countedOut[i] == "":
			e.finish(j, ptreport.OutcomeSkipped, reasonLeft, "")
		default:
			e.finish(j, ptreport.OutcomeStopped, reasonLeft, "")
		}
	}
}

// failText is the line the fault lamp's log entry carries, in English.
func failText(j *job, why string) string {
	if j == nil {
		return why
	}
	if why == "" {
		return j.item.ID + " failed its criteria"
	}
	return j.item.ID + ": " + why
}
