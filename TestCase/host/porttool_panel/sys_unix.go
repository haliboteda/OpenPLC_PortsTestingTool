//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
)

// singleInstance holds a lock for the life of this process, or reports false if
// another T4-02 holds it: two runs share the panel log and the files beside the exe.
func singleInstance() (func(), bool) {
	f, err := os.OpenFile(filepath.Join(os.TempDir(), "porttool_panel_test.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, false
	}
	return func() { f.Close() }, true
}

func terminate(p *os.Process) { _ = p.Signal(syscall.SIGTERM) }

// killTree: cancelling the chromedp context closes Chrome, and its renderers go with it.
func killTree(p *os.Process) {}
