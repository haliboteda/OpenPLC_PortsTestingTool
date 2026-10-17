package ptstages_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"PortTool/src/ptreport"
	"PortTool/src/ptstages"
)

func fixed(o ptreport.Outcome, ran *[]ptstages.Step, s ptstages.Step) ptstages.Func {
	return func(ctx context.Context) ptreport.StepResult {
		*ran = append(*ran, s)
		return ptreport.StepResult{Outcome: o}
	}
}

func outcomes(r ptreport.Report) []string {
	var out []string
	for _, s := range r.Steps {
		out = append(out, s.Type+":"+string(s.Outcome))
	}
	return out
}

func TestAStepThatDoesNotPassStopsTheRest(t *testing.T) {
	var ran []ptstages.Step
	run := ptstages.Run{
		Steps: ptstages.Order,
		Do: map[ptstages.Step]ptstages.Func{
			2: fixed(ptreport.OutcomePass, &ran, 2),
			4: fixed(ptreport.OutcomeFail, &ran, 4),
			5: fixed(ptreport.OutcomePass, &ran, 5),
			6: fixed(ptreport.OutcomePass, &ran, 6),
		},
	}
	rep := run.Execute(context.Background(), ptreport.Report{})
	if got := strings.Join(outcomes(rep), " "); got != "2:PASS 4:FAIL 5:SKIPPED 6:SKIPPED" {
		t.Fatal(got)
	}
	if len(ran) != 2 {
		t.Fatalf("ran %v after a failure", ran)
	}
	if rep.Steps[0].ID != "functional" || !strings.Contains(rep.Steps[3].Reason, "step 4") {
		t.Fatalf("ids / reasons: %+v", rep.Steps)
	}
}

func TestStopMarksTheRunningStepStoppedAndSkipsTheRest(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	var ran []ptstages.Step
	run := ptstages.Run{
		Steps: []ptstages.Step{2, 5, 6},
		Do: map[ptstages.Step]ptstages.Func{
			2: fixed(ptreport.OutcomePass, &ran, 2),
			5: func(ctx context.Context) ptreport.StepResult {
				cancel(errors.New("control port lost"))
				<-ctx.Done()
				return ptreport.StepResult{Outcome: ptreport.OutcomeFail}
			},
			6: fixed(ptreport.OutcomePass, &ran, 6),
		},
	}
	rep := run.Execute(ctx, ptreport.Report{})
	if got := strings.Join(outcomes(rep), " "); got != "2:PASS 5:STOPPED 6:SKIPPED" {
		t.Fatal(got)
	}
	if !strings.Contains(rep.Steps[1].Reason, "control port lost") {
		t.Fatalf("stop reason: %q", rep.Steps[1].Reason)
	}
}

func TestContinuingRecordsTheEarlierPassesAsSkipped(t *testing.T) {
	var ran []ptstages.Step
	run := ptstages.Run{
		Steps:   []ptstages.Step{2, 4, 5},
		From:    1,
		Earlier: map[ptstages.Step]string{2: "20261011-090000.json"},
		Do: map[ptstages.Step]ptstages.Func{
			2: fixed(ptreport.OutcomePass, &ran, 2),
			4: fixed(ptreport.OutcomePass, &ran, 4),
			5: fixed(ptreport.OutcomePass, &ran, 5),
		},
	}
	rep := run.Execute(context.Background(), ptreport.Report{})
	if got := strings.Join(outcomes(rep), " "); got != "2:SKIPPED 4:PASS 5:PASS" {
		t.Fatal(got)
	}
	if !strings.Contains(rep.Steps[0].Reason, "20261011-090000.json") || len(ran) != 2 {
		t.Fatalf("%v %v", rep.Steps[0].Reason, ran)
	}
}

type ops struct {
	calls                      []string
	bootErr, uploadErr, rstErr error
	test                       ptreport.Outcome
	cancelDuringTest           context.CancelCauseFunc
}

func (o *ops) build() ptstages.UserLevelOps {
	return ptstages.UserLevelOps{
		FlashBootloader: func() error { o.calls = append(o.calls, "boot"); return o.bootErr },
		UploadDemo:      func() error { o.calls = append(o.calls, "upload"); return o.uploadErr },
		Test: func(ctx context.Context) ptreport.StepResult {
			o.calls = append(o.calls, "test")
			if o.cancelDuringTest != nil {
				o.cancelDuringTest(errors.New("stopped by operator"))
			}
			return ptreport.StepResult{Outcome: o.test}
		},
		FactoryReset: func() error { o.calls = append(o.calls, "reset"); return o.rstErr },
	}
}

func step6(ctx context.Context, o *ops) ptreport.StepResult {
	run := ptstages.Run{Steps: []ptstages.Step{6}, Do: map[ptstages.Step]ptstages.Func{6: ptstages.UserLevelStep(o.build())}}
	return run.Execute(ctx, ptreport.Report{}).Steps[0]
}

func TestStep6RunsInOrderAndAlwaysFactoryResetsAfterTheUpload(t *testing.T) {
	cases := []struct {
		name    string
		o       ops
		calls   string
		outcome ptreport.Outcome
		root    bool
	}{
		{"pass", ops{test: ptreport.OutcomePass}, "boot upload test reset", ptreport.OutcomePass, false},
		{"test fails", ops{test: ptreport.OutcomeFail}, "boot upload test reset", ptreport.OutcomeFail, false},
		{"bootloader fails", ops{bootErr: errors.New("no target")}, "boot", ptreport.OutcomeError, false},
		{"upload fails", ops{uploadErr: errors.New("timeout")}, "boot upload reset", ptreport.OutcomeError, false},
		{"reset fails", ops{test: ptreport.OutcomePass, rstErr: errors.New("no ST-Link")}, "boot upload test reset", ptreport.OutcomeError, true},
	}
	for _, c := range cases {
		res := step6(context.Background(), &c.o)
		if got := strings.Join(c.o.calls, " "); got != c.calls {
			t.Errorf("%s: calls %q, want %q", c.name, got, c.calls)
		}
		if res.Outcome != c.outcome {
			t.Errorf("%s: outcome %s, want %s", c.name, res.Outcome, c.outcome)
		}
		if strings.Contains(res.Reason, ptstages.RootLeft) != c.root {
			t.Errorf("%s: reason %q", c.name, res.Reason)
		}
	}
}

func TestStep6StoppedDuringTheTestStillFactoryResets(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	o := ops{test: ptreport.OutcomeFail, cancelDuringTest: cancel}
	res := step6(ctx, &o)
	if got := strings.Join(o.calls, " "); got != "boot upload test reset" {
		t.Fatalf("calls %q", got)
	}
	if res.Outcome != ptreport.OutcomeStopped {
		t.Fatalf("outcome %s", res.Outcome)
	}
}
