// Package ptstages runs a full fixture test: the picked steps of 2, 4, 5 and 6
// in order, stopping at the first that does not pass, and the inside of step 6.
// Rules: $PROD/docs/modules/M4/PORTTOOL-FLOW.md C.3.3 and C.3.5; decisions 89,
// 94, 96; STG-01, STG-08.
package ptstages

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"PortTool/src/ptreport"
)

// Step is a step number of the fixture test (decision 89).
type Step int

const (
	Functional Step = 2
	Aging      Step = 4
	Safety     Step = 5
	UserLevel  Step = 6
)

// Order is every step a full test can pick, in the order they run.
var Order = []Step{Functional, Aging, Safety, UserLevel}

// Name is the step's id in a report.
func (s Step) Name() string {
	switch s {
	case Functional:
		return "functional"
	case Aging:
		return "aging"
	case Safety:
		return "safety"
	case UserLevel:
		return "user"
	}
	return "step" + strconv.Itoa(int(s))
}

// Func runs one step. ctx is cancelled when the run is stopped; a step that
// returns because of it is recorded STOPPED whatever outcome it gives.
type Func func(ctx context.Context) ptreport.StepResult

// Run is one full test.
type Run struct {
	Steps []Step // picked, in Order
	// From is the index into Steps to start at; the ones before it passed in
	// an earlier run, in the report Earlier names.
	From    int
	Earlier map[Step]string
	Do      map[Step]Func
	Now     func() time.Time
	// Progress is told each step as it starts and ends.
	Progress func(s Step, r *ptreport.StepResult)
}

// Execute fills header's steps and end time. The cause of a cancelled ctx is
// the reason the stopped step carries.
func (r Run) Execute(ctx context.Context, header ptreport.Report) ptreport.Report {
	now := r.Now
	if now == nil {
		now = time.Now
	}
	rep := header
	notRun := ""
	for i, s := range r.Steps {
		res := ptreport.StepResult{ID: s.Name(), Type: strconv.Itoa(int(s)), StartedAt: now()}
		switch {
		case i < r.From:
			res.Outcome = ptreport.OutcomeSkipped
			res.Reason = "passed earlier, in " + r.Earlier[s]
		case notRun != "":
			res.Outcome = ptreport.OutcomeSkipped
			res.Reason = notRun
		case ctx.Err() != nil:
			res.Outcome = ptreport.OutcomeSkipped
			notRun = "not run: " + cause(ctx)
			res.Reason = notRun
		default:
			if r.Progress != nil {
				r.Progress(s, nil)
			}
			got := r.Do[s](ctx)
			got.ID, got.Type, got.StartedAt = res.ID, res.Type, res.StartedAt
			got.DurationMS = now().Sub(res.StartedAt).Milliseconds()
			if ctx.Err() != nil && !got.Outcome.IsPass() {
				got.Outcome = ptreport.OutcomeStopped
				got.Reason = joinReason(cause(ctx), got.Reason)
			}
			res = got
			if !res.Outcome.IsPass() {
				notRun = fmt.Sprintf("not run: step %d did not pass", s)
				if res.Outcome == ptreport.OutcomeStopped {
					notRun = fmt.Sprintf("not run: stopped at step %d", s)
				}
			}
		}
		if r.Progress != nil {
			r.Progress(s, &res)
		}
		rep.Steps = append(rep.Steps, res)
	}
	rep.EndedAt = now()
	return rep
}

func cause(ctx context.Context) string {
	if c := context.Cause(ctx); c != nil {
		return c.Error()
	}
	return "stopped"
}

func joinReason(a, b string) string {
	if b == "" {
		return a
	}
	return a + "; " + b
}

// RootLeft is the reason step 6 carries when the factory reset did not happen
// after the production root was claimed (decision 96).
const RootLeft = "board still carries the production root"

// UserLevelOps is what step 6 is made of (STG-01). FlashBootloader and
// FactoryReset write flash and are never interrupted: a stop waits for them.
type UserLevelOps struct {
	FlashBootloader func() error
	// UploadDemo uploads the demo with IAPTool, which claims the production root.
	UploadDemo   func() error
	Test         Func
	FactoryReset func() error
}

// UserLevelStep builds step 6. Once the upload was attempted the board may
// carry the production root, so the factory reset runs whatever happened
// after it - pass, fail or stop.
func UserLevelStep(ops UserLevelOps) Func {
	return func(ctx context.Context) ptreport.StepResult {
		att := ptreport.Attempt{N: 1, StartedAt: time.Now()}
		res := ptreport.StepResult{}
		finish := func(o ptreport.Outcome, reason string) ptreport.StepResult {
			att.Outcome = o
			att.DurationMS = time.Since(att.StartedAt).Milliseconds()
			res.Outcome, res.Reason = o, joinReason(res.Reason, reason)
			res.Attempts = append(res.Attempts, att)
			return res
		}
		if err := ops.FlashBootloader(); err != nil {
			att.SetExtra("bootloader", err.Error())
			return finish(ptreport.OutcomeError, "bootloader: "+err.Error())
		}
		att.SetExtra("bootloader", "flashed")
		if ctx.Err() != nil {
			return finish(ptreport.OutcomeStopped, "")
		}

		outcome, reason := ptreport.OutcomePass, ""
		if err := ops.UploadDemo(); err != nil {
			att.SetExtra("demo", err.Error())
			outcome, reason = ptreport.OutcomeError, "demo upload: "+err.Error()
		} else {
			att.SetExtra("demo", "uploaded, production root claimed")
			test := ops.Test(ctx)
			res.Attempts = append(res.Attempts, test.Attempts...)
			att.N = len(res.Attempts) + 1
			outcome, reason = test.Outcome, test.Reason
		}

		if err := ops.FactoryReset(); err != nil {
			att.SetExtra("factory_reset", err.Error())
			if outcome.IsPass() {
				outcome = ptreport.OutcomeError
			}
			return finish(outcome, joinReason(reason, RootLeft+": "+err.Error()))
		}
		att.SetExtra("factory_reset", "done")
		return finish(outcome, reason)
	}
}
