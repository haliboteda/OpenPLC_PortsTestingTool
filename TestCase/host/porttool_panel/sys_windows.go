package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/windows"
)

// singleInstance holds a lock for the life of this process, or reports false if
// another T4-02 holds it: two runs share the panel log and the files beside the exe.
func singleInstance() (func(), bool) {
	f, err := os.OpenFile(filepath.Join(os.TempDir(), "porttool_panel_test.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false
	}
	ol := new(windows.Overlapped)
	h := windows.Handle(f.Fd())
	if err := windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol); err != nil {
		f.Close()
		return nil, false
	}
	return func() { _ = windows.UnlockFileEx(h, 0, 1, 0, ol); f.Close() }, true
}

// terminate is what Python's Popen.terminate does on Windows.
func terminate(p *os.Process) { _ = p.Kill() }

// killTree ends Chrome and the renderers it started.
func killTree(p *os.Process) {
	if p == nil {
		return
	}
	_ = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(p.Pid)).Run()
}
