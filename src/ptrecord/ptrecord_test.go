package ptrecord_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"PortTool/src/ptrecord"
	"PortTool/src/ptreport"
)

const uid = "003400413135511439303538"

func setDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	ptrecord.Dir = d
	t.Cleanup(func() { ptrecord.Dir = "" })
	return d
}

func report(start time.Time, sn string, steps ...ptreport.StepResult) ptreport.Report {
	return ptreport.Report{BoardUID: uid, SN: sn, StartedAt: start, Steps: steps}
}

func step(n string, o ptreport.Outcome) ptreport.StepResult {
	return ptreport.StepResult{ID: "s" + n, Type: n, Outcome: o}
}

func TestRecordIsFiledByUIDNamedByStartAndSNAndNeverOverwritten(t *testing.T) {
	dir := setDir(t)
	start := time.Date(2026, 10, 11, 9, 5, 7, 0, time.Local)
	path, err := ptrecord.Write(report(start, "SN 12/34", step("5", ptreport.OutcomePass)))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, uid, "20261011-090507-SN_12_34.json"); path != want {
		t.Fatalf("path %s, want %s", path, want)
	}
	if _, err := ptrecord.Write(report(start, "SN 12/34")); err == nil {
		t.Fatal("a second record with the same name overwrote the first")
	}
	if _, err := ptrecord.Write(ptreport.Report{BoardUID: "sim", StartedAt: start}); err == nil {
		t.Fatal("a record without a real UID was written")
	}
	if got := ptrecord.FileName(start, ""); got != "20261011-090507.json" {
		t.Fatalf("no SN: %s", got)
	}
}

func TestProgressAsksToContinueOnlyFromALaterStep(t *testing.T) {
	setDir(t)
	picked := []int{2, 4, 5, 6}
	r := report(time.Now(), "", step("2", ptreport.OutcomePass), step("4", ptreport.OutcomeStopped), step("5", ptreport.OutcomeSkipped))
	if err := ptrecord.Update(uid, picked, r, "a.json"); err != nil {
		t.Fatal(err)
	}
	p, err := ptrecord.LoadProgress(uid)
	if err != nil {
		t.Fatal(err)
	}
	if got := ptrecord.ResumeAt(picked, p); got != 1 {
		t.Fatalf("resume at %d, want 1 (step 4)", got)
	}
	if f, ok := p.Has(2); !ok || f != "a.json" {
		t.Fatalf("step 2 passed in a.json: %v", p)
	}
	if got := ptrecord.ResumeAt([]int{4, 5}, p); got != 0 {
		t.Fatalf("nothing picked has passed, yet resume at %d", got)
	}

	// The rest passes: progress is gone and the next run starts over.
	r = report(time.Now(), "", step("4", ptreport.OutcomePass), step("5", ptreport.OutcomePass), step("6", ptreport.OutcomePass))
	if err := ptrecord.Update(uid, picked, r, "b.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ptrecord.BoardDir(uid), "progress.json")); !os.IsNotExist(err) {
		t.Fatal("progress kept after every picked step passed")
	}
}
