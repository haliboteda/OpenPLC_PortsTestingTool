// Case T4-01, firmware half: with -build, TestMain compiles the real
// porttool.c natively with test_main.c, runs it, writes the transcript to
// caps_golden.txt and TestFirmwareTranscript checks it line by line; the Go
// half (ptproto / ptboard / ptpanel tests) then reads those very bytes, so the
// two cannot drift apart. Without -build only the Go half runs, on the golden
// file as committed.
//
//	CGO_ENABLED=1 CC=<HOST_CC> go test -race -count=1 -v ./TestCase/host/porttool_caps/ -args -build
//
// Compiler, first match wins: $CC, HOST_CC from tools/local.json, gcc or clang
// on PATH. What is checked: PORTTOOL-CAPS-TEST.md beside this file.
package testcase

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"PortTool/tools/firmhost"
	"PortTool/tools/localcfg"
)

var buildFlag = flag.Bool("build", false,
	"compile the firmware host build, rewrite caps_golden.txt and check its transcript (T4-01)")

// fw is the transcript just produced; nil without -build.
var fw *firmware

type section struct {
	cmd  string
	body []string
}

type firmware struct {
	raw      string
	sections []section
	lineMax  int
	version  string
}

func TestMain(m *testing.M) {
	flag.Parse()
	if *buildFlag {
		f, err := buildFirmware()
		if err != nil {
			fmt.Fprintln(os.Stderr, "T4-01 could not build or run the firmware host build: "+err.Error())
			os.Exit(2)
		}
		fw = f
		if !raceEnabled {
			fmt.Println("WARN  without -race: ptboard's reader, sender and subscribers are not checked for races")
		}
	}
	os.Exit(m.Run())
}

func resolveCC(cfg map[string]string) string {
	for _, cand := range []string{os.Getenv("CC"), cfg["HOST_CC"], "gcc", "clang"} {
		if cand == "" {
			continue
		}
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
		if p, err := exec.LookPath(cand); err == nil {
			return p
		}
	}
	return ""
}

func constFrom(path, pattern string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	m := regexp.MustCompile(pattern).FindStringSubmatch(string(b))
	if m == nil {
		return "", fmt.Errorf("no match for %s in %s", pattern, path)
	}
	return m[1], nil
}

func buildFirmware() (*firmware, error) {
	repo, err := localcfg.FindRepo()
	if err != nil {
		return nil, err
	}
	cfg := localcfg.Load(repo)
	boot := cfg["BOOT_REPO"]
	if boot == "" {
		return nil, fmt.Errorf("BOOT_REPO is not set in %s (tools/build.sh find-tools)", localcfg.Path(repo))
	}
	cc := resolveCC(cfg)
	if cc == "" {
		return nil, fmt.Errorf("no C compiler: point HOST_CC in %s at a gcc (tools/build.sh find-tools)", localcfg.Path(repo))
	}
	pt := filepath.Join(boot, "TestCase", "porttool")
	lm, err := constFrom(filepath.Join(pt, "porttool.h"), `#define\s+PORTTOOL_REPLY_MAX\s+(\d+)`)
	if err != nil {
		return nil, err
	}
	lineMax, _ := strconv.Atoi(lm)
	version, err := constFrom(filepath.Join(pt, "porttool.c"), `#define\s+PORTTOOL_VERSION\s+"([^"]+)"`)
	if err != nil {
		return nil, err
	}
	fmt.Println("===== build")
	fmt.Printf("  compiler   %s\n  firmware   %s\n  version    %s   line limit %d\n", cc, pt, version, lineMax)

	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	out := filepath.Join(repo, "Output", "build", "porttool_caps", "porttool_hosttest"+exe)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return nil, err
	}
	// test_main.c sits in harness/: with cgo on, Go refuses a package whose
	// directory holds a .c file. It #includes porttool.c.
	mainC := filepath.Join(repo, "TestCase", "host", "porttool_caps", "harness", "test_main.c")
	args, err := firmhost.Args(repo, boot, mainC, out)
	if err != nil {
		return nil, err
	}
	cout, err := exec.Command(cc, args...).CombinedOutput()
	os.Stdout.Write(cout)
	if err != nil {
		return nil, fmt.Errorf("compile failed: %v", err)
	}
	if len(cout) == 0 {
		fmt.Println("  compiled clean (-Wall -Wextra)")
	}

	raw, err := exec.Command(out).CombinedOutput()
	if err != nil {
		os.Stdout.Write(raw)
		return nil, fmt.Errorf("harness: %v", err)
	}
	if err := os.WriteFile(goldenPath, raw, 0o644); err != nil {
		return nil, err
	}
	f := &firmware{raw: string(raw), lineMax: lineMax, version: version}
	for _, chunk := range strings.Split(f.raw, ">>> ")[1:] {
		lines := strings.Split(chunk, "\n")
		s := section{cmd: strings.TrimRight(lines[0], "\r")}
		for _, l := range lines[1:] {
			if l = strings.TrimRight(l, "\r"); l != "" {
				s.body = append(s.body, l)
			}
		}
		f.sections = append(f.sections, s)
	}
	fmt.Printf("  transcript written to %s (%d sections)\n", goldenPath, len(f.sections))
	return f, nil
}

// ---------------------------------------------------------------- helpers

type kvPair struct{ k, v string }

// parseKV splits "OK port=din kind=session ..." into ordered pairs: not a map,
// because a duplicate key is the bug being looked for.
func parseKV(line string) []kvPair {
	var out []kvPair
	for _, tok := range strings.Fields(line) {
		if k, v, ok := strings.Cut(tok, "="); ok {
			out = append(out, kvPair{k, v})
		}
	}
	return out
}

func kvMap(line string) map[string]string {
	m := map[string]string{}
	for _, p := range parseKV(line) {
		m[p.k] = p.v
	}
	return m
}

func has(m map[string]string, k string) bool { _, ok := m[k]; return ok }

// pyGet formats an absent key the way the checks' names always have.
func pyGet(m map[string]string, k string) string {
	if v, ok := m[k]; ok {
		return v
	}
	return "None"
}

func atoiOr(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func head(s []string, n int) []string {
	if len(s) < n {
		return s
	}
	return s[:n]
}

func cut(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// checker makes each check a subtest named by what it proves.
type checker struct{ t *testing.T }

func (c checker) check(cond bool, what string, detail ...string) bool {
	c.t.Helper()
	c.t.Run(what, func(t *testing.T) {
		if !cond {
			t.Error(strings.Join(append([]string{"FAIL"}, detail...), "  --  "))
		}
	})
	return cond
}

func (c checker) section(name string) { c.t.Logf("===== %s", name) }

// bodies: the replies of every section sent as cmd.
func (f *firmware) bodies(cmd string) [][]string {
	var out [][]string
	for _, s := range f.sections {
		if s.cmd == cmd {
			out = append(out, s.body)
		}
	}
	return out
}

// replyTo: the first line of the first section sent as cmd.
func (f *firmware) replyTo(cmd string) string {
	b := f.bodies(cmd)
	if len(b) > 0 && len(b[0]) > 0 {
		return b[0][0]
	}
	return "(nothing)"
}

// firstReply: the first line of the first section sent as cmd that has one.
func (f *firmware) firstReply(cmd string) string {
	for _, b := range f.bodies(cmd) {
		if len(b) > 0 {
			return b[0]
		}
	}
	return "(nothing)"
}

func (f *firmware) index(cmd string) int {
	for i, s := range f.sections {
		if s.cmd == cmd {
			return i
		}
	}
	return -1
}

// lines: every reply line, TEST observations included, that starts with prefix.
func (f *firmware) lines(prefix string) []string {
	var out []string
	for _, s := range f.sections {
		for _, l := range s.body {
			if strings.HasPrefix(l, prefix) {
				out = append(out, l)
			}
		}
	}
	return out
}

func (f *firmware) frames(prefix string) []map[string]string {
	var out []map[string]string
	for _, l := range f.lines(prefix) {
		out = append(out, kvMap(l))
	}
	return out
}

// testLine: the first TEST observation starting with prefix, its pairs and the line.
func (f *firmware) testLine(prefix string) (map[string]string, string) {
	for _, l := range f.lines(prefix) {
		return kvMap(l[len("TEST "):]), l
	}
	return nil, ""
}

func (f *firmware) rawLines(prefix string) []string {
	var out []string
	for _, l := range strings.Split(f.raw, "\n") {
		if l = strings.TrimRight(l, "\r"); strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return out
}

func okLine(body []string) string {
	for _, l := range body {
		if strings.HasPrefix(l, "OK ") {
			return l
		}
	}
	return "(no OK line)"
}

func okLines(body []string) []string {
	var out []string
	for _, l := range body {
		if strings.HasPrefix(l, "OK ") {
			out = append(out, l)
		}
	}
	return out
}

func last(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[len(s)-1]
}
