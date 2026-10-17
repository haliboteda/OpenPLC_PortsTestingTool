package ptstages

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"PortTool/src/calstore"
	"PortTool/src/ptreport"
	"PortTool/src/stlink"
)

// Stage6Dir is the folder beside the exe that holds step 6's inputs; a
// subfolder because start.cmd flashes "the only .hex" next to the exe.
// PORTTOOL-FLOW.md C.3.5, decision 103.
const Stage6Dir = "stage6"

// BootloaderName is the bootloader image inside Stage6Dir.
const BootloaderName = "bootloader.hex"

// DemoName is the demo inside Stage6Dir: Arduino's own output name for the
// FixtureDemo example, so the delivery copies it unrenamed (PORTTOOL-FLOW.md C.3.5 「第 6 步的输入」).
const DemoName = "FixtureDemo.ino.bin"

// IAPToolName is the IAPTool binary inside Stage6Dir.
func IAPToolName() string {
	if runtime.GOOS == "windows" {
		return "IAPTool.exe"
	}
	return "IAPTool"
}

// UserLevelFiles are step 6's inputs that ship in the delivery.
type UserLevelFiles struct {
	Bootloader string
	IAPTool    string
	Demo       string
}

// LocateUserLevelFiles finds the bootloader image, IAPTool and the demo in
// <dir>/stage6; dir "" means the folder of the running exe. The error names
// every file that is missing.
func LocateUserLevelFiles(dir string) (UserLevelFiles, error) {
	if dir == "" {
		exe, err := os.Executable()
		if err != nil {
			return UserLevelFiles{}, err
		}
		dir = filepath.Dir(exe)
	}
	f := UserLevelFiles{
		Bootloader: filepath.Join(dir, Stage6Dir, BootloaderName),
		IAPTool:    filepath.Join(dir, Stage6Dir, IAPToolName()),
		Demo:       filepath.Join(dir, Stage6Dir, DemoName),
	}
	missing := f.Missing()
	if len(missing) > 0 {
		return f, fmt.Errorf("step 6 files missing: %s", strings.Join(missing, ", "))
	}
	return f, nil
}

// Missing lists the paths of f that are not files.
func (f UserLevelFiles) Missing() []string {
	var missing []string
	for _, p := range []string{f.Bootloader, f.IAPTool, f.Demo} {
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			missing = append(missing, p)
		}
	}
	return missing
}

// Flasher is the ST-Link side of step 6; stlink.Programmer is the real one.
type Flasher interface {
	FlashBootloader(image string) error
	FactoryReset(keep func([]byte) error) error
}

// Stage6 is everything step 6 needs. Files, Demo, Port, UID and Test are
// the caller's; the rest default to the real thing.
type Stage6 struct {
	Files UserLevelFiles
	// Demo is the demo's .bin (PORTTOOL-FLOW.md C.3.5 「第 6 步的输入」).
	Demo string
	// Port names the board's USB CDC port once the bootloader runs.
	Port func() (string, error)
	// UID is the board's, from pt.id; the factory reset keeps the
	// calibration area read back under it.
	UID string
	// Test is the user-level test against the demo.
	Test Func

	// Flasher defaults to stlink.Open().
	Flasher Flasher
	// RunIAPTool runs IAPTool and returns what it printed; defaults to exec.
	RunIAPTool func(exe string, args ...string) (string, error)
	// PortWait bounds how long Port is retried after the bootloader is
	// flashed, for the board to enumerate; default 10 s.
	PortWait time.Duration
	Now      func() time.Time
}

// Missing says which of the caller's inputs are not there, or nil.
func (u Stage6) Missing() error {
	var miss []string
	for _, p := range []string{u.Files.Bootloader, u.Files.IAPTool, u.Demo} {
		if p == "" {
			miss = append(miss, "(path not given)")
		} else if st, err := os.Stat(p); err != nil || st.IsDir() {
			miss = append(miss, p)
		}
	}
	if u.Port == nil {
		miss = append(miss, "USB CDC port finder")
	}
	if _, err := calstore.ParseUID(u.UID); err != nil {
		miss = append(miss, "board UID ("+err.Error()+")")
	}
	if u.Test == nil {
		miss = append(miss, "user-level test")
	}
	if len(miss) == 0 {
		return nil
	}
	return fmt.Errorf("step 6 inputs missing: %s", strings.Join(miss, ", "))
}

func execIAPTool(exe string, args ...string) (string, error) {
	out, err := exec.Command(exe, args...).CombinedOutput()
	return string(out), err
}

// Step builds step 6. Nothing touches the board while an input is missing.
func (u Stage6) Step() Func {
	return func(ctx context.Context) ptreport.StepResult {
		if err := u.Missing(); err != nil {
			return ptreport.StepResult{Outcome: ptreport.OutcomeError, Reason: err.Error()}
		}
		fl := u.Flasher
		if fl == nil {
			p, err := stlink.Open()
			if err != nil {
				return ptreport.StepResult{Outcome: ptreport.OutcomeError, Reason: "ST-Link: " + err.Error()}
			}
			fl = p
		}
		run := u.RunIAPTool
		if run == nil {
			run = execIAPTool
		}
		now := u.Now
		if now == nil {
			now = time.Now
		}
		return UserLevelStep(UserLevelOps{
			FlashBootloader: func() error { return fl.FlashBootloader(u.Files.Bootloader) },
			UploadDemo: func() error {
				port, err := u.waitPort()
				if err != nil {
					return err
				}
				// Arduino IDE's default upload, CDC Transfer without Force flash:
				// $CORE_REPO/platform.txt tools.cdctransfer.upload.pattern.
				out, err := run(u.Files.IAPTool, "cdc", u.Demo, port)
				if err != nil {
					return fmt.Errorf("IAPTool cdc %s %s: %v\n%s", filepath.Base(u.Demo), port, err, tail(out, 6))
				}
				return nil
			},
			Test: u.Test,
			FactoryReset: func() error {
				return fl.FactoryReset(func(cal []byte) error {
					_, err := calstore.SaveReadback(u.UID, cal, now())
					return err
				})
			},
		})(ctx)
	}
}

func (u Stage6) waitPort() (string, error) {
	wait := u.PortWait
	if wait == 0 {
		wait = 10 * time.Second
	}
	deadline := time.Now().Add(wait)
	for {
		port, err := u.Port()
		if err == nil {
			return port, nil
		}
		if !time.Now().Before(deadline) {
			return "", errors.New("board USB port: " + err.Error())
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
