// Package firmhost is the fixture firmware's host build: the C sources of
// src/simboard/c/sources.txt and the flags they compile with. The simulated
// board (tools/ptbuild, with sim_main.c), T4-01 (with test_main.c) and T4-05
// all compile through it, so the three cannot drift apart.
package firmhost

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// CDir is the simulated board's C shell: sim_main.c, stubs/, sources.txt.
func CDir(repo string) string { return filepath.Join(repo, "src", "simboard", "c") }

// Sources reads sources.txt: $BOOT/... from the bootloader repo, the rest from CDir.
func Sources(repo, boot string) ([]string, error) {
	cdir := CDir(repo)
	b, err := os.ReadFile(filepath.Join(cdir, "sources.txt"))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "$BOOT/"); ok {
			out = append(out, filepath.Join(boot, filepath.FromSlash(rest)))
		} else {
			out = append(out, filepath.Join(cdir, filepath.FromSlash(line)))
		}
	}
	return out, nil
}

// Args is everything after the compiler: flags, the sources, mainC as the
// outermost file, and -o out. Link flags are the caller's (see SimLink).
func Args(repo, boot, mainC, out string) ([]string, error) {
	srcs, err := Sources(repo, boot)
	if err != nil {
		return nil, err
	}
	cdir := CDir(repo)
	args := []string{
		"-std=c11", "-Wall", "-Wextra", "-O0", "-g",
		"-DPORTTOOL_ENABLE=1",
		// testcase_hal_guard.h's CubeMX check is meaningless here, and mingw's
		// own <adc.h> would trip it.
		"-DPORTTOOL_HOST_TEST=1",
		"-I", filepath.Join(cdir, "stubs"),
		"-I", filepath.Join(boot, "TestCase", "porttool"),
		"-I", filepath.Join(boot, "TestCase"),
		"-I", filepath.Join(boot, "TestCase", "common"),
		// Only for IAP_boot_handoff.h (pt.run reset.cause): the real header, so a
		// signature change breaks this build instead of drifting.
		"-I", filepath.Join(boot, "IAPServer"),
		"-I", filepath.Join(boot, "Core", "Inc"),
	}
	args = append(args, srcs...)
	return append(args, mainC, "-o", out), nil
}

// SimLink is what sim_main.c links with on this machine's compiler.
// -static: a 32-bit libwinpthread-1.dll on PATH kills a double-clicked panel's
// sim ($PROD/docs/engineering/HOW-TO-RUN-PORTTOOL-TESTS.md "模拟板").
func SimLink() []string {
	l := []string{"-pthread", "-static"}
	if runtime.GOOS == "windows" {
		l = append(l, "-lws2_32") // the eth bridge's sockets
	}
	return l
}
