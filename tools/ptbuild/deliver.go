package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// deliver packs what the hardware engineer needs into Output/delivery/: the
// fixture image, PortTool and its plans, the simulated board, step 6's inputs
// in stage6/, start.cmd / start.sh and the guide as HTML. Windows and Linux
// share the folder (PORTTOOL-FLOW.md C.4 in $PROD).

// Step 6's inputs; names must match src/ptstages/userlevel.go.
const (
	stage6Dir          = "stage6"
	stage6Boot         = "bootloader.hex"
	stage6IAPTool      = "IAPTool.exe"
	stage6IAPToolLinux = "IAPTool"
	stage6Demo         = "FixtureDemo.ino.bin" // ptstages.DemoName: Arduino's own output name
)

// A fixture image in $BOOT/Debug is not a bootloader (same marks as
// $TEST/tools/flash_bootloader.py).
var (
	fixtureMark    = []byte("pt.caps")
	bootloaderMark = []byte("openplc_server_where_r_y")
)

// shipped is tools/shipped_fixture.json: the last packed versions and hashes,
// in git so every machine that packs sees what was last shipped.
type shipped struct {
	Version  string   `json:"version"`
	SHA256   string   `json:"sha256"`
	PortTool *verHash `json:"porttool,omitempty"`
}

type verHash struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

func shippedPath() string { return filepath.Join(repo, "tools", "shipped_fixture.json") }

func fileSHA(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:]), nil
}

// toolHash is SHA-256 over PortTool's shipped sources in path order, line
// endings normalised: go.mod, go.sum and src/ without tests, plans and the
// simulated board's C. Not the exe: a Go exe embeds its build path.
func toolHash() (string, error) {
	files := []string{"go.mod", "go.sum"}
	err := filepath.WalkDir(filepath.Join(repo, "src"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(repo, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "src/plans" || rel == "src/simboard/c" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, "_test.go") {
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	h := sha256.New()
	for _, rel := range files {
		b, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		h.Write([]byte(rel + "\x00"))
		h.Write(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// sameSet: panel and firmware are one set when major.minor agree (decision 120).
func sameSet(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	if len(pa) < 2 || len(pb) < 2 {
		return a == b
	}
	return pa[0] == pb[0] && pa[1] == pb[1]
}

// staleVersion: the thing changed but its version did not.
func staleVersion(last *verHash, version, sha string) bool {
	return last != nil && last.SHA256 != sha && last.Version == version
}

var fqbnRe = regexp.MustCompile(`(?s)\nFQBN\s*=\s*\((.*?)\)`)
var pyStrRe = regexp.MustCompile(`"([^"]*)"`)

// boardFQBN reads FQBN from $CORE_REPO/tests/_common.py, the board package's
// own example build, so the demo is compiled the way the IDE would.
func boardFQBN(core string) (string, error) {
	p := filepath.Join(core, "tests", "_common.py")
	b, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("no %s (the board package repo, for its FQBN)", p)
	}
	m := fqbnRe.FindSubmatch(b)
	if m == nil {
		return "", fmt.Errorf("no FQBN = (...) in %s", p)
	}
	var s strings.Builder
	for _, part := range pyStrRe.FindAllSubmatch(m[1], -1) {
		s.Write(part[1])
	}
	return s.String(), nil
}

func arduino15() string {
	if v := os.Getenv("ARDUINO15"); v != "" {
		return v
	}
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(os.Getenv("LOCALAPPDATA"), "Arduino15")
	case "darwin":
		return filepath.Join(home(), "Library", "Arduino15")
	}
	return filepath.Join(home(), ".arduino15")
}

var versionPart = regexp.MustCompile(`[.\-]`)

// versionLess compares dotted versions numerically part by part.
func versionLess(a, b string) bool {
	pa, pb := versionPart.Split(a, -1), versionPart.Split(b, -1)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		var na, nb int
		_, ea := fmt.Sscanf(pa[i], "%d", &na)
		_, eb := fmt.Sscanf(pb[i], "%d", &nb)
		if ea == nil && eb == nil {
			if na != nb {
				return na < nb
			}
		} else if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return len(pa) < len(pb)
}

// coreLive is the installed board package: $CORE_LIVE, else the newest version.
func coreLive() string {
	if v := os.Getenv("CORE_LIVE"); v != "" {
		if st, err := os.Stat(v); err == nil && st.IsDir() {
			return v
		}
		return ""
	}
	root := filepath.Join(arduino15(), "packages", "OpenPLC_Alpha", "hardware", "stm32")
	ents, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	var vs []string
	for _, e := range ents {
		if e.IsDir() {
			vs = append(vs, e.Name())
		}
	}
	if len(vs) == 0 {
		return ""
	}
	sort.Slice(vs, func(i, j int) bool { return versionLess(vs[i], vs[j]) })
	return filepath.Join(root, vs[len(vs)-1])
}

// buildDemo compiles FixtureDemo from the installed board package.
func buildDemo(cfg map[string]string) (string, error) {
	fqbn, err := boardFQBN(cfg["CORE_REPO"])
	if err != nil {
		return "", err
	}
	live := coreLive()
	if live == "" {
		return "", fmt.Errorf("no installed OpenPLC board package under %s", arduino15())
	}
	sketch := filepath.Join(live, "libraries", "OpenPLC_Ports", "examples", "FixtureDemo")
	if !exists(filepath.Join(sketch, "FixtureDemo.ino")) {
		return "", fmt.Errorf("no FixtureDemo in the installed board package %s", live)
	}
	work := filepath.Join(repo, "Output", "build", "demo")
	_ = os.RemoveAll(work)
	out := filepath.Join(work, "out")
	argv := []string{"compile"}
	if cfg["ARDUINO_CLI_CONFIG"] != "" {
		argv = append(argv, "--config-file", cfg["ARDUINO_CLI_CONFIG"])
	}
	argv = append(argv, "--fqbn", fqbn, "--build-path", filepath.Join(work, "build"), "--output-dir", out, sketch)
	log, rerr := exec.Command(cfg["ARDUINO_CLI"], argv...).CombinedOutput()
	demo := filepath.Join(out, stage6Demo)
	if rerr != nil || !exists(demo) {
		var lines []string
		for _, l := range strings.Split(string(log), "\n") {
			if strings.TrimSpace(l) != "" {
				lines = append(lines, strings.TrimRight(l, "\r"))
			}
		}
		if len(lines) > 8 {
			lines = lines[len(lines)-8:]
		}
		return "", fmt.Errorf("FixtureDemo did not compile:\n    %s", strings.Join(lines, "\n    "))
	}
	return demo, nil
}

func deliver(open, internal bool) error {
	cfg := loadLocal()
	needs := []need{
		cfgNeed(cfg, "BOOT_REPO"), cfgNeed(cfg, "TOOL_REPO"), cfgNeed(cfg, "CORE_REPO"),
		cfgNeed(cfg, "ARDUINO_CLI"), cfgNeed(cfg, "HOST_CC"), cfgNeed(cfg, "ZIG"),
	}
	if runtime.GOOS != "windows" {
		needs = append(needs, need{"Windows", "", "the Windows simulated board is built with HOST_CC on Windows, so the delivery is packed there"})
	}
	if err := require("pack the delivery folder", needs...); err != nil {
		return err
	}

	hexfile := filepath.Join(fixtureDir(), "OpenPLC_BootLoader.hex")
	binfile := filepath.Join(fixtureDir(), "OpenPLC_BootLoader.bin")
	panel := filepath.Join(binDir("windows"), "PortTool.exe")
	panelLinux := filepath.Join(binDir("linux"), "PortTool")
	plansDir := filepath.Join(repo, "src", "plans")

	section("checking what goes in")
	var missing []string
	for _, p := range []string{hexfile, binfile, panel, panelLinux, plansDir} {
		if !exists(p) {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing: %s\n  build first: tools/build.sh fixture, then tools/build.sh tool", strings.Join(missing, ", "))
	}
	plans, _ := filepath.Glob(filepath.Join(plansDir, "*.json"))
	sort.Strings(plans)
	fwVer := "unknown"
	if b, err := os.ReadFile(filepath.Join(fixtureDir(), "VERSION")); err == nil {
		fwVer = strings.TrimSpace(string(b))
	}
	tVer := toolVersion()
	product(hexfile)
	product(panel)
	product(panelLinux)
	fmt.Printf("  %d plan(s) in %s\n", len(plans), plansDir)

	// Decision 120, PORTTOOL-FLOW.md C.4 "版本号".
	if !sameSet(tVer, fwVer) {
		return fmt.Errorf("PortTool %s and the fixture firmware %s differ in the first two numbers, so they are not one set.\n"+
			"  Make the version in src/cmd/porttool/main.go and PORTTOOL_VERSION in porttool.c agree there, rebuild, pack again.", tVer, fwVer)
	}
	var last *shipped
	if b, err := os.ReadFile(shippedPath()); err == nil {
		last = &shipped{}
		if err := json.Unmarshal(b, last); err != nil {
			return fmt.Errorf("%s: %v", shippedPath(), err)
		}
	}
	sha, err := fileSHA(binfile)
	if err != nil {
		return err
	}
	var stale []string
	if last != nil && staleVersion(&verHash{last.Version, last.SHA256}, fwVer, sha) {
		stale = append(stale, fmt.Sprintf("the fixture firmware differs from the last packed one, but its version is still %s.\n"+
			"  To release: raise it (third number for a small fix, second together with PortTool when capabilities\n"+
			"  change) in every place below, then tools/build.sh fixture and pack again.\n%s", fwVer,
			versionSpots(fwVer,
				filepath.Join(cfg["BOOT_REPO"], "TestCase", "porttool", "porttool.c"),
				filepath.Join(coreLive(), demoDir),
				filepath.Join(cfg["CORE_REPO"], demoDir),
				filepath.Join(repo, "TestCase", "host"))+
				"\n  Change the installed board package first, then copy to open_plc_arduino; TestCase/test.sh t4-01\n"+
				"  rewrites caps_golden.txt with the new version."))
	}
	tsha, err := toolHash()
	if err != nil {
		return err
	}
	if last != nil && staleVersion(last.PortTool, tVer, tsha) {
		stale = append(stale, fmt.Sprintf("PortTool's sources differ from the last packed ones, but its version is still %s.\n"+
			"  To release: raise it (third number for a small fix, second together with the firmware when capabilities\n"+
			"  change) in every place below, then tools/build.sh tool and pack again.\n%s", tVer,
			versionSpots(tVer, filepath.Join(repo, "src", "cmd", "porttool", "main.go"))))
	}
	if len(stale) > 0 {
		if internal, err = packInternal(stale, internal); err != nil {
			return err
		}
	}

	// Step 6: bootloader and IAPTool from their last builds.
	bootDir := filepath.Join(cfg["BOOT_REPO"], "Debug")
	bootHex, bootElf := filepath.Join(bootDir, "OpenPLC_BootLoader.hex"), filepath.Join(bootDir, "OpenPLC_BootLoader.elf")
	iap := filepath.Join(cfg["TOOL_REPO"], "Output", "windows", stage6IAPTool)
	iapLinux := filepath.Join(cfg["TOOL_REPO"], "Output", "linux", stage6IAPToolLinux)
	missing = nil
	for _, p := range []string{bootHex, bootElf, iap, iapLinux} {
		if !exists(p) {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("step 6 bootloader / IAPTool cannot be packed: missing %s\n"+
			"  build the bootloader in $BOOT (CubeIDE, or $TEST/tools/build_image.py) and IAPTool in $TOOL (build.py)", strings.Join(missing, ", "))
	}
	image, err := os.ReadFile(bootElf)
	if err != nil {
		return err
	}
	if bytes.Contains(image, fixtureMark) || !bytes.Contains(image, bootloaderMark) {
		return fmt.Errorf("%s is not a bootloader image (fixture build left in Debug/?) - rebuild the bootloader", bootElf)
	}
	product(bootHex)
	product(iap)
	product(iapLinux)
	fmt.Println("  compiling FixtureDemo for step 6 ...")
	demo, err := buildDemo(cfg)
	if err != nil {
		return fmt.Errorf("step 6 demo cannot be packed: %v", err)
	}
	product(demo)

	// The simulated board ships beside the exe so a copied package can still
	// pick "sim" (decision 112); built now so it speaks this firmware's protocol.
	if err := buildSim("windows", false); err != nil {
		return err
	}
	// Linux: zig, static musl, so it runs on any distribution.
	if err := buildSim("linux", true); err != nil {
		return err
	}

	section("packing")
	out := filepath.Join(repo, "Output", "delivery")
	if err := emptyDir(out); err != nil {
		return err
	}
	copies := [][2]string{
		{hexfile, "openplc_fixture_" + fwVer + ".hex"},
		{panel, "PortTool.exe"},
		{simOut("windows"), "porttool_simboard.exe"},
		{panelLinux, "PortTool"},
		{simOut("linux"), "porttool_simboard"},
		{bootHex, filepath.Join(stage6Dir, stage6Boot)},
		{iap, filepath.Join(stage6Dir, stage6IAPTool)},
		{iapLinux, filepath.Join(stage6Dir, stage6IAPToolLinux)},
		{demo, filepath.Join(stage6Dir, stage6Demo)},
	}
	// The plans hold the criteria; the panel looks for plans/ beside the exe.
	for _, p := range plans {
		copies = append(copies, [2]string{p, filepath.Join("plans", filepath.Base(p))})
	}
	for _, c := range copies {
		dst := filepath.Join(out, c[1])
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := copyFile(c[0], dst); err != nil {
			return err
		}
	}
	if err := writeStartScripts(out); err != nil {
		return err
	}
	for _, name := range []string{"README.zh-CN", "README.en"} {
		if err := mdToHTML(filepath.Join(repo, "tools", "delivery", name+".md"), filepath.Join(out, name+".html")); err != nil {
			return err
		}
	}
	listFolder(out)

	// An internal package is not a release: the next release still has to raise the version.
	if !internal {
		b, _ := json.MarshalIndent(shipped{fwVer, sha, &verHash{tVer, tsha}}, "", " ")
		if err := os.WriteFile(shippedPath(), append(b, '\n'), 0o644); err != nil {
			return err
		}
	}

	section("result")
	if internal {
		ok("internal test package packed (not recorded in tools/shipped_fixture.json): " + out)
	} else {
		ok("delivery folder packed: " + out)
	}
	fmt.Println("  Send the whole folder to the hardware engineer. He installs only STM32CubeProgrammer,")
	fmt.Println("  then double-clicks start.cmd (Windows) or runs bash start.sh (Linux).")
	if open {
		openFolder(out)
	}
	return nil
}

// demoDir is FixtureDemo inside a board package; it states the protocol version too.
var demoDir = filepath.Join("libraries", "OpenPLC_Ports", "examples", "FixtureDemo")

// versionSpots lists every file:line under roots that holds ver, so a version
// raise finds the copies by search instead of by a list that would drift.
func versionSpots(ver string, roots ...string) string {
	var out []string
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			// Golden files are rewritten by their test (T4-01 -build), not by hand.
			if err != nil || d.IsDir() || strings.Contains(d.Name(), "_golden") {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			for i, l := range strings.Split(string(b), "\n") {
				if strings.Contains(l, ver) {
					out = append(out, fmt.Sprintf("    %s:%d", p, i+1))
				}
			}
			return nil
		})
	}
	if len(out) == 0 {
		return "    (no file holds " + ver + ")"
	}
	return strings.Join(out, "\n")
}

// packInternal decides what happens when a version was not raised (decision 120):
// --internal packs, the menu asks, a script without --internal stops.
func packInternal(stale []string, internal bool) (bool, error) {
	msg := strings.Join(stale, "\n")
	if internal {
		warn(msg)
		warn("packing anyway for internal testing (--internal)")
		return true, nil
	}
	if !interactive {
		return false, fmt.Errorf("%s\n  For an internal test package run tools/build.sh deliver --internal.", msg)
	}
	warn(msg)
	n, err := ask("Pack anyway?", []string{
		"Pack for internal testing    (version unchanged, not recorded as shipped)",
		"Stop                         (I will raise the version first)",
	}, 1)
	if err != nil {
		return false, err
	}
	if n == 2 {
		return false, errors.New("stopped before packing; raise the version as above, then pack again")
	}
	return true, nil
}

// emptyDir empties rather than removes: removing fails while Explorer has it open.
func emptyDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return fmt.Errorf("cannot delete %s - is it open? Close it and pack again", e.Name())
		}
	}
	return nil
}

func readTemplate(name string) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(repo, "tools", "delivery", name))
	return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")), err
}

func writeStartScripts(out string) error {
	cmdText, err := readTemplate("start.cmd")
	if err != nil {
		return err
	}
	// GBK and CRLF: cmd.exe reads a batch file in the console code page, 936 on
	// Chinese Windows.
	gbk, err := simplifiedchinese.GBK.NewEncoder().Bytes(bytes.ReplaceAll(cmdText, []byte("\n"), []byte("\r\n")))
	if err != nil {
		return fmt.Errorf("start.cmd: %v", err)
	}
	if err := os.WriteFile(filepath.Join(out, "start.cmd"), gbk, 0o644); err != nil {
		return err
	}
	// UTF-8 and LF: bash reads CR as part of each command.
	sh, err := readTemplate("start.sh")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, "start.sh"), sh, 0o755)
}

// mdToHTML turns one guide into a self-contained page: tools/delivery/page.html
// with the title and body filled in.
func mdToHTML(src, dst string) error {
	md, err := readTemplate(filepath.Base(src))
	if err != nil {
		return err
	}
	page, err := readTemplate("page.html")
	if err != nil {
		return err
	}
	title := strings.TrimSuffix(filepath.Base(src), ".md")
	for _, line := range strings.Split(string(md), "\n") {
		if strings.HasPrefix(line, "# ") {
			title = strings.TrimSpace(line[2:])
			break
		}
	}
	var body bytes.Buffer
	if err := goldmark.New(goldmark.WithExtensions(extension.Table)).Convert(md, &body); err != nil {
		return fmt.Errorf("%s: %v", src, err)
	}
	html := strings.Replace(string(page), "{{TITLE}}", title, 1)
	html = strings.Replace(html, "{{BODY}}", strings.TrimRight(body.String(), "\n"), 1)
	return os.WriteFile(dst, []byte(html), 0o644)
}

func listFolder(out string) {
	ents, _ := os.ReadDir(out)
	for _, e := range ents {
		p := filepath.Join(out, e.Name())
		if e.IsDir() {
			sub, _ := os.ReadDir(p)
			fmt.Printf("  %-32s %d file(s)\n", e.Name()+string(filepath.Separator), len(sub))
			continue
		}
		if st, err := os.Stat(p); err == nil {
			fmt.Printf("  %-32s %10d bytes\n", e.Name(), st.Size())
		}
	}
}

func openFolder(dir string) {
	switch runtime.GOOS {
	case "windows":
		_ = exec.Command("explorer", dir).Run()
	case "darwin":
		_ = exec.Command("open", dir).Run()
	default:
		_ = exec.Command("xdg-open", dir).Run()
	}
}
