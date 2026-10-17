package ptstages_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"PortTool/src/calstore"
	"PortTool/src/ptreport"
	"PortTool/src/ptstages"
	"PortTool/src/stlink"
	"PortTool/src/stlink/stlinktest"
)

const uid = "0123456789ABCDEF01234567"

// bench is step 6 against the ST-Link fake and a fake IAPTool: no board, no
// programmer, no IAPTool. It proves the order and the arguments, not what
// CubeProgrammer or the bootloader would do with them.
type bench struct {
	flash   *stlinktest.Flash
	iap     []string // each IAPTool call's arguments, joined
	iapErr  error
	test    ptreport.Outcome
	tested  bool
	calDir  string
	stage   ptstages.Stage6
	rootOff int
}

func newBench(t *testing.T) *bench {
	t.Helper()
	f, restore := stlinktest.Install(t.TempDir())
	t.Cleanup(restore)
	calstore.Dir = t.TempDir()
	t.Cleanup(func() { calstore.Dir = "" })

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ptstages.Stage6Dir), 0o755); err != nil {
		t.Fatal(err)
	}
	files, err := ptstages.LocateUserLevelFiles(dir)
	if err == nil {
		t.Fatal("located files in an empty folder")
	}
	write := func(p string, data []byte) {
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(files.Bootloader, bytes.Repeat([]byte{0xB0}, 64))
	write(files.IAPTool, []byte("fake"))
	if _, err := ptstages.LocateUserLevelFiles(dir); err == nil || !strings.Contains(err.Error(), ptstages.DemoName) {
		t.Fatalf("a missing demo is not named: %v", err)
	}
	demo := files.Demo
	write(demo, []byte("demo"))
	if files, err = ptstages.LocateUserLevelFiles(dir); err != nil {
		t.Fatal(err)
	}

	// Sector 15 as a claimed, calibrated board has it.
	f.Put(stlink.Sector15, bytes.Repeat([]byte{0xCA}, 64))
	b := &bench{flash: f, calDir: calstore.Dir, test: ptreport.OutcomePass, rootOff: 0x2000}
	f.Put(stlink.Sector15+b.rootOff, bytes.Repeat([]byte{0x4F}, 160))
	f.Put(stlink.FlashBase+stlink.SectorSize, bytes.Repeat([]byte{0xAA}, 64)) // an old app

	b.stage = ptstages.Stage6{
		Files: files,
		Demo:  demo,
		Port:  func() (string, error) { return "COM9", nil },
		UID:   uid,
		Test: func(ctx context.Context) ptreport.StepResult {
			b.tested = true
			return ptreport.StepResult{Outcome: b.test, Reason: map[bool]string{true: "", false: "demo echo failed"}[b.test.IsPass()]}
		},
		RunIAPTool: func(exe string, args ...string) (string, error) {
			if exe != files.IAPTool {
				t.Errorf("IAPTool from %s, not the delivery's", exe)
			}
			b.iap = append(b.iap, strings.Join(args, " "))
			if b.iapErr != nil {
				return "Upload failed: no answer", b.iapErr
			}
			return "Upload complete", nil
		},
	}
	return b
}

func (b *bench) run() ptreport.StepResult {
	run := ptstages.Run{Steps: []ptstages.Step{6}, Do: map[ptstages.Step]ptstages.Func{6: b.stage.Step()}}
	return run.Execute(context.Background(), ptreport.Report{}).Steps[0]
}

func (b *bench) reset(t *testing.T) bool {
	t.Helper()
	for _, c := range b.flash.Calls {
		if strings.Contains(c, "-e 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15") {
			return true
		}
	}
	return false
}

func (b *bench) rootGone() bool {
	return bytes.Equal(b.flash.At(stlink.Sector15+b.rootOff, 160), bytes.Repeat([]byte{0xFF}, 160))
}

func TestStep6PassFlashesUploadsLikeTheIDEAndFactoryResets(t *testing.T) {
	b := newBench(t)
	res := b.run()
	if res.Outcome != ptreport.OutcomePass {
		t.Fatalf("outcome %s: %s", res.Outcome, res.Reason)
	}
	if !strings.Contains(strings.Join(b.flash.Calls, "\n"), "-e 0 1 2 3 4 5 6 7 8 9 10 11 12 13 14\n") {
		t.Fatalf("bootloader flash did not erase sectors 0-14: %v", b.flash.Calls)
	}
	want := "cdc " + b.stage.Demo + " COM9"
	if len(b.iap) != 1 || b.iap[0] != want {
		t.Fatalf("IAPTool calls %q, want [%q]", b.iap, want)
	}
	if !b.tested || !b.reset(t) || !b.rootGone() {
		t.Fatalf("tested %v reset %v root gone %v", b.tested, b.reset(t), b.rootGone())
	}
	if got := b.flash.At(stlink.Sector15, 64); !bytes.Equal(got, bytes.Repeat([]byte{0xCA}, 64)) {
		t.Fatal("calibration area not kept through the factory reset")
	}
	kept, _ := filepath.Glob(filepath.Join(b.calDir, uid, "calarea.readback.*.bin"))
	if len(kept) != 1 {
		t.Fatalf("calibration readback not saved on the PC: %v", kept)
	}
}

func TestStep6UploadFailureStillFactoryResets(t *testing.T) {
	b := newBench(t)
	b.iapErr = errors.New("exit status 1")
	res := b.run()
	if res.Outcome != ptreport.OutcomeError || !strings.Contains(res.Reason, "demo upload") ||
		!strings.Contains(res.Reason, "no answer") {
		t.Fatalf("outcome %s: %s", res.Outcome, res.Reason)
	}
	if b.tested || !b.reset(t) || !b.rootGone() {
		t.Fatalf("tested %v reset %v root gone %v", b.tested, b.reset(t), b.rootGone())
	}
}

func TestStep6TestFailureStillFactoryResets(t *testing.T) {
	b := newBench(t)
	b.test = ptreport.OutcomeFail
	res := b.run()
	if res.Outcome != ptreport.OutcomeFail || !strings.Contains(res.Reason, "demo echo failed") {
		t.Fatalf("outcome %s: %s", res.Outcome, res.Reason)
	}
	if !b.reset(t) || !b.rootGone() || strings.Contains(res.Reason, ptstages.RootLeft) {
		t.Fatalf("reset %v root gone %v reason %q", b.reset(t), b.rootGone(), res.Reason)
	}
}

func TestStep6ResetFailureSaysTheRootIsStillThere(t *testing.T) {
	b := newBench(t)
	b.flash.FailOn = "-e 1 2 3"
	res := b.run()
	if res.Outcome != ptreport.OutcomeError || !strings.Contains(res.Reason, ptstages.RootLeft) {
		t.Fatalf("outcome %s: %s", res.Outcome, res.Reason)
	}
	if b.rootGone() {
		t.Fatal("fake erased the root although the erase failed")
	}
}

func TestStep6WithAnInputMissingLeavesTheBoardAlone(t *testing.T) {
	b := newBench(t)
	b.stage.Demo = ""
	b.stage.UID = ""
	res := b.run()
	if res.Outcome != ptreport.OutcomeError || !strings.Contains(res.Reason, "board UID") {
		t.Fatalf("outcome %s: %s", res.Outcome, res.Reason)
	}
	if len(b.flash.Calls) != 0 || len(b.iap) != 0 {
		t.Fatalf("touched the board: %v %v", b.flash.Calls, b.iap)
	}
}

func TestStep6WaitsForTheUSBPortThenGivesUp(t *testing.T) {
	b := newBench(t)
	b.stage.PortWait = 1
	b.stage.Port = func() (string, error) { return "", errors.New("no 0483:5740 port") }
	res := b.run()
	if res.Outcome != ptreport.OutcomeError || !strings.Contains(res.Reason, "no 0483:5740 port") {
		t.Fatalf("outcome %s: %s", res.Outcome, res.Reason)
	}
	if len(b.iap) != 0 || !b.reset(t) {
		t.Fatalf("iap %v reset %v", b.iap, b.reset(t))
	}
}
