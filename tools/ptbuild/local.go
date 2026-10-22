package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"PortTool/tools/localcfg"
)

// tools/local.json holds this machine's tool paths, written by find-tools and
// gitignored (decision 144); nothing else spells out a path on this machine.

type setting struct {
	key      string
	required bool
	what     string
	detect   func(found map[string]string) string
}

var settings = []setting{
	{"BOOT_REPO", true, "OpenPLC_BootLoader: the fixture firmware (TestCase/porttool/), the CubeIDE project, the step-6 bootloader in Debug/",
		func(map[string]string) string { return detectRepo("OpenPLC_BootLoader") }},
	{"TOOL_REPO", false, "IAPTranfer_Tool: its Output/windows and Output/linux IAPTool go into the delivery for step 6",
		func(map[string]string) string { return detectRepo("IAPTranfer_Tool") }},
	{"CORE_REPO", false, "open_plc_arduino: the FQBN the step-6 demo is compiled with (tests/_common.py)",
		func(map[string]string) string { return detectRepo("open_plc_arduino") }},
	{"CUBEIDE", false, "STM32CubeIDE install root: headless fixture build and STM32_Programmer_CLI",
		func(map[string]string) string { return detectCubeIDE() }},
	{"WORKSPACE", false, "Eclipse workspace holding the bootloader project",
		func(f map[string]string) string {
			if f["BOOT_REPO"] == "" {
				return ""
			}
			return filepath.Dir(f["BOOT_REPO"])
		}},
	{"HOST_CC", false, "a modern gcc/clang (GCC 5+): the simulated board for this machine, T4-01, T4-05",
		func(map[string]string) string { return detectHostCC() }},
	{"ZIG", false, "zig: cross-compiles the simulated board for the other systems",
		func(map[string]string) string { return detectZig() }},
	{"ARDUINO_CLI", false, "arduino-cli: compiles the step-6 demo FixtureDemo for the delivery",
		func(map[string]string) string { return detectArduinoCLI() }},
	{"ARDUINO_CLI_CONFIG", false, "arduino-cli's --config-file (Arduino IDE 2's own), so it finds the installed board package",
		func(map[string]string) string { return detectArduinoCLIConfig() }},
	{"CHROME", false, "Google Chrome: T4-02 clicks the panel through in it",
		func(map[string]string) string { return detectChrome() }},
}

func localPath() string { return localcfg.Path(repo) }

func loadLocal() map[string]string { return localcfg.Load(repo) }

func exists(p string) bool {
	_, err := os.Stat(p)
	return p != "" && err == nil
}

// findTools keeps every value that still exists on disk and detects the rest.
func findTools(sets []string, check bool) error {
	section("find this machine's tools")
	known := map[string]bool{}
	for _, s := range settings {
		known[s.key] = true
	}
	forced := map[string]string{}
	for _, kv := range sets {
		k, v, _ := strings.Cut(kv, "=")
		if !known[k] {
			return fmt.Errorf("unknown setting %s", k)
		}
		forced[k] = v
	}
	old := loadLocal()
	found := map[string]string{}
	var missing []string
	for _, s := range settings {
		v, ok := forced[s.key]
		if !ok {
			if exists(old[s.key]) {
				v = old[s.key]
			} else {
				v = s.detect(found)
			}
		}
		found[s.key] = v
		state := "ok     "
		if v == "" {
			state = "empty  "
			if s.required {
				state = "MISSING"
				missing = append(missing, s.key)
			}
		}
		fmt.Printf("  %s %-18s %s\n", state, s.key, v)
	}
	if !check {
		var b bytes.Buffer
		b.WriteString("{\n")
		for i, s := range settings {
			k, _ := json.Marshal(s.key)
			v, _ := json.Marshal(found[s.key])
			b.WriteString("  " + string(k) + ": " + string(v))
			if i < len(settings)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString("}\n")
		if err := os.WriteFile(localPath(), b.Bytes(), 0o644); err != nil {
			return err
		}
		ok("wrote " + localPath())
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing: %s - set with tools/build.sh find-tools --set KEY=VALUE", strings.Join(missing, ", "))
	}
	return nil
}

// need is one tool an item needs: its local.json key (or "" for a tool found
// another way), a path when found, and what it is for.
type need struct {
	name, path, what string
}

// require stops an item before it starts when a tool is missing, naming each one.
func require(item string, needs ...need) error {
	var miss []need
	for _, n := range needs {
		if !exists(n.path) {
			miss = append(miss, n)
		}
	}
	if len(miss) == 0 {
		return nil
	}
	section(item + ": missing tools")
	for _, n := range miss {
		fail(fmt.Sprintf("  %-18s %s", n.name, n.what))
	}
	if _, err := os.Stat(localPath()); err != nil {
		warn("  tools/local.json does not exist yet - run tools/build.sh find-tools first")
	} else {
		warn("  run tools/build.sh find-tools, or set one with --set KEY=VALUE")
	}
	return fmt.Errorf("%s cannot run: %d tool(s) missing (listed above)", item, len(miss))
}

// cfgNeed is a local.json value as a need.
func cfgNeed(cfg map[string]string, key string) need {
	for _, s := range settings {
		if s.key == key {
			return need{key, cfg[key], s.what}
		}
	}
	return need{key, cfg[key], ""}
}

// ---------------------------------------------------------------- detectors

func newest(pattern string) string {
	hits, _ := filepath.Glob(pattern)
	if len(hits) == 0 {
		return ""
	}
	sort.Strings(hits)
	return hits[len(hits)-1]
}

// detectRepo: repos sit next to each other; one level further out is the fallback.
func detectRepo(name string) string {
	for _, base := range []string{filepath.Dir(repo), filepath.Dir(filepath.Dir(repo))} {
		for _, pat := range []string{name, filepath.Join("*", name)} {
			if hit := newest(filepath.Join(base, pat)); hit != "" && exists(filepath.Join(hit, ".git")) {
				return hit
			}
		}
	}
	return ""
}

func home() string { h, _ := os.UserHomeDir(); return h }

func detectCubeIDE() string {
	var roots []string
	switch runtime.GOOS {
	case "windows":
		roots = []string{`C:\ST\STM32CubeIDE*`, `D:\ST\STM32CubeIDE*`, `E:\ST\STM32CubeIDE*`,
			`C:\Program Files\STMicroelectronics\STM32CubeIDE*`,
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "STM32CubeIDE*")}
	case "darwin":
		roots = []string{"/Applications/STM32CubeIDE*", filepath.Join(home(), "Applications", "STM32CubeIDE*")}
	default:
		roots = []string{"/opt/st/stm32cubeide*", "/opt/stm32cubeide*", filepath.Join(home(), "st", "stm32cubeide*")}
	}
	for _, r := range roots {
		hits, _ := filepath.Glob(r)
		sort.Strings(hits)
		for _, h := range hits {
			// The install root is the directory holding STM32CubeIDE/plugins.
			if st, err := os.Stat(filepath.Join(h, "STM32CubeIDE", "plugins")); err == nil && st.IsDir() {
				return h
			}
		}
	}
	return ""
}

var versionRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// gccIsModern: GCC older than 5 (Dev-C++ ships 3.4.2) rejects -std=c11.
func gccIsModern(exe string) bool {
	cmd := exec.Command(exe, "--version")
	done := make(chan []byte, 1)
	go func() { out, _ := cmd.Output(); done <- out }()
	select {
	case out := <-done:
		m := versionRe.FindStringSubmatch(string(out))
		if m == nil {
			return false
		}
		major, _ := strconv.Atoi(m[1])
		return major >= 5
	case <-time.After(20 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return false
	}
}

func lookPath(name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

func detectHostCC() string {
	cands := []string{lookPath("gcc")}
	if cands[0] == "" {
		cands[0] = lookPath("clang")
	}
	if runtime.GOOS == "windows" {
		cands = append(cands, `C:\mingw64\bin\gcc.exe`, `D:\Soft\mingw64\bin\gcc.exe`, `C:\msys64\mingw64\bin\gcc.exe`)
	}
	for _, c := range cands {
		if exists(c) && gccIsModern(c) {
			return c
		}
	}
	return ""
}

func detectZig() string {
	if p := lookPath("zig"); p != "" {
		return p
	}
	if runtime.GOOS == "windows" && exists(`D:\Soft\zig\zig.exe`) {
		return `D:\Soft\zig\zig.exe`
	}
	return ""
}

// detectArduinoCLI: arduino-cli on PATH, else the one Arduino IDE 2 ships with.
func detectArduinoCLI() string {
	if p := lookPath("arduino-cli"); p != "" {
		return p
	}
	name := "arduino-cli"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	rel := filepath.Join("resources", "app", "lib", "backend", "resources", name)
	var roots []string
	switch runtime.GOOS {
	case "windows":
		roots = []string{`C:\Program Files\Arduino IDE`, `D:\Soft\arduino-2`,
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Arduino IDE")}
	case "darwin":
		roots = []string{"/Applications/Arduino IDE.app/Contents"}
		rel = filepath.Join("Resources", "app", "lib", "backend", "resources", name)
	default:
		roots = []string{filepath.Join(home(), "arduino-ide"), "/opt/arduino-ide"}
	}
	for _, r := range roots {
		if p := filepath.Join(r, rel); exists(p) {
			return p
		}
	}
	return ""
}

func detectArduinoCLIConfig() string {
	p := filepath.Join(home(), ".arduinoIDE", "arduino-cli.yaml")
	if exists(p) {
		return p
	}
	return ""
}

func detectChrome() string {
	var cands []string
	switch runtime.GOOS {
	case "windows":
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LOCALAPPDATA"} {
			if d := os.Getenv(env); d != "" {
				cands = append(cands, filepath.Join(d, "Google", "Chrome", "Application", "chrome.exe"))
			}
		}
	case "darwin":
		cands = []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"}
	default:
		for _, n := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
			cands = append(cands, lookPath(n))
		}
	}
	for _, c := range cands {
		if exists(c) {
			return c
		}
	}
	return ""
}

// ---------------------------------------------------------------- output

var colour = func() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	st, err := os.Stdout.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}()

func paint(t, code string) string {
	if !colour {
		return t
	}
	return "\033[" + code + "m" + t + "\033[0m"
}

func section(t string) { fmt.Println(); fmt.Println(paint("===== "+t, "36")) }
func ok(t string)      { fmt.Println(paint(t, "32")) }
func warn(t string)    { fmt.Println(paint(t, "33")) }
func fail(t string)    { fmt.Println(paint(t, "31")) }

// product prints one built file with its full path and size.
func product(p string) {
	abs, _ := filepath.Abs(p)
	if st, err := os.Stat(abs); err == nil {
		if st.IsDir() {
			ents, _ := os.ReadDir(abs)
			fmt.Printf("  %s  (%d files)\n", abs, len(ents))
			return
		}
		fmt.Printf("  %s  (%d bytes)\n", abs, st.Size())
		return
	}
	fmt.Printf("  %s\n", abs)
}

var errFailed = errors.New("failed")
