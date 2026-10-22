package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"PortTool/tools/firmhost"
)

// buildTool builds PortTool for each platform into Output/bin/<platform>/ and
// copies the shipped plans beside it: the panel reads plans/ next to the exe.
func buildTool(plats []string) error {
	if err := require("build PortTool", need{"go", lookPath("go"), "the Go toolchain on PATH"}); err != nil {
		return err
	}
	section("PortTool")
	plans, _ := filepath.Glob(filepath.Join(repo, "src", "plans", "*.json"))
	var built []string
	for _, p := range plats {
		goos, goarch := goosArch(p)
		out := filepath.Join(binDir(p), "PortTool"+exeSuffix(p))
		fmt.Printf("building PortTool for %s ...\n", p)
		if err := removeFile(out); err != nil {
			return err
		}
		cmd := exec.Command("go", "build", "-o", out, "./src/cmd/porttool")
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("PortTool for %s did not build (see above)", p)
		}
		// Shipped plans are overwritten; a plan written on the line is left alone.
		dst := filepath.Join(binDir(p), "plans")
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
		for _, f := range plans {
			if err := copyFile(f, filepath.Join(dst, filepath.Base(f))); err != nil {
				return err
			}
		}
		built = append(built, out, dst+string(filepath.Separator))
	}
	section("built")
	for _, b := range built {
		product(b)
	}
	ok(fmt.Sprintf("PortTool %s, %d plan(s) beside each", toolVersion(), len(plans)))
	return nil
}

// ---------------------------------------------------------------- simulated board

// zigTargets: -static on Linux so the board runs on any distribution (musl).
var zigTargets = map[string][]string{
	"linux":        {"-target", "x86_64-linux-musl", "-pthread", "-static"},
	"darwin":       {"-target", "x86_64-macos", "-pthread"},
	"darwin-arm64": {"-target", "aarch64-macos", "-pthread"},
}

// removeFile deletes what a build item is about to rebuild, so a failed build
// leaves nothing stale behind (decision 149).
func removeFile(p string) error {
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("cannot delete %s before rebuilding it (is it running?): %v", p, err)
	}
	return nil
}

func simOut(p string) string {
	return filepath.Join(binDir(p), "porttool_simboard"+exeSuffix(p))
}

// buildSim compiles the fixture firmware with the stubs and sim_main.c into
// Output/bin/<platform>/, beside PortTool: this machine's with HOST_CC, the
// others cross-compiled with zig.
func buildSim(p string, cross bool) error {
	cfg := loadLocal()
	if cross && p == "windows" {
		return fmt.Errorf("the Windows simulated board is only built on Windows (with HOST_CC)")
	}
	compiler := cfgNeed(cfg, "HOST_CC")
	if cross {
		compiler = cfgNeed(cfg, "ZIG")
	}
	if err := require("build the simulated board ("+p+")", cfgNeed(cfg, "BOOT_REPO"), compiler); err != nil {
		return err
	}
	boot := cfg["BOOT_REPO"]
	pt := filepath.Join(boot, "TestCase", "porttool")
	section("simulated board: " + p)
	out := simOut(p)
	args, err := firmhost.Args(repo, boot, filepath.Join(firmhost.CDir(repo), "sim_main.c"), out)
	if err != nil {
		return err
	}
	if err := removeFile(out); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	var argv []string
	if cross {
		argv = []string{compiler.path, "cc"}
		argv = append(argv, zigTargets[p]...)
	} else {
		argv = []string{compiler.path}
	}
	head := strings.Join(argv, " ")
	argv = append(argv, args...)
	if !cross {
		argv = append(argv, firmhost.SimLink()...)
	}
	fmt.Printf("  compiler   %s\n", head)
	fmt.Printf("  firmware   %s  (version %s)\n", pt, fixtureSourceVersion(boot))
	cmd := exec.Command(argv[0], argv[1:]...)
	outb, err := cmd.CombinedOutput()
	if len(outb) > 0 {
		os.Stdout.Write(outb)
	}
	if err != nil {
		return fmt.Errorf("the %s simulated board did not compile (see above; a running one cannot be overwritten either)", p)
	}
	if len(outb) > 0 {
		warn("compiled, with the compiler output above (-Wall -Wextra)")
	} else {
		ok("compiled clean (-Wall -Wextra)")
	}
	product(out)
	fmt.Println("  Every reading it gives is invented: it stands in for the PC-side wiring, never for a board.")
	return nil
}

var fwVersionRe = regexp.MustCompile(`#define\s+PORTTOOL_VERSION\s+"([^"]+)"`)

func fixtureSourceVersion(boot string) string {
	b, _ := os.ReadFile(filepath.Join(boot, "TestCase", "porttool", "porttool.c"))
	if m := fwVersionRe.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return "unknown"
}

var toolVersionRe = regexp.MustCompile(`const version = "([^"]+)"`)

// toolVersion is the `version` constant in src/cmd/porttool/main.go.
func toolVersion() string {
	b, _ := os.ReadFile(filepath.Join(repo, "src", "cmd", "porttool", "main.go"))
	if m := toolVersionRe.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return "unknown"
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, st.Mode().Perm()|0o200)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chtimes(dst, st.ModTime(), st.ModTime())
}
