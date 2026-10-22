package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"PortTool/src/stlink"
)

// The fixture image is the bootloader project built with one extra symbol and
// its own linker script, both on the command line only, so a build from inside
// the IDE stays a bootloader build. It runs on a copy of $BOOT under
// Output/build/fixture/; only the images and VERSION are kept, in
// Output/fixture/ (decision 86).

const (
	fixtureProject = "OpenPLC_BootLoader/Debug"
	toolLD         = "STM32H743IIKX_FLASH_PORTTOOL.ld"
	bootLD         = "STM32H743IIKX_FLASH.ld"
	// The #warning the fixture image must carry (decision 14).
	toolMarker = "PORTTOOL_ENABLE=1: this image is the hardware test tool"
)

var fixtureImages = []string{"OpenPLC_BootLoader.elf", "OpenPLC_BootLoader.bin",
	"OpenPLC_BootLoader.hex", "OpenPLC_BootLoader.map"}

// Warnings that follow from a deliberate design choice in $BOOT, matched on an
// exact substring. See $PROD/docs/modules/M1/FLASHBOOT.md
var knownWarnings = []string{"LOAD segment with RWX permissions"}

func fixtureDir() string { return filepath.Join(repo, "Output", "fixture") }

func cubePlug() string {
	switch runtime.GOOS {
	case "windows":
		return "win32"
	case "darwin":
		return "macos64"
	}
	return "linux64"
}

func cubeIDEExe(cfg map[string]string) string {
	if cfg["CUBEIDE"] == "" {
		return ""
	}
	name := "stm32cubeide"
	if runtime.GOOS == "windows" {
		name = "stm32cubeidec.exe"
	}
	return filepath.Join(cfg["CUBEIDE"], "STM32CubeIDE", name)
}

// programmerCLI: the one inside CubeIDE's versioned plugin (newest wins), else
// a standalone STM32CubeProgrammer where stlink.Find looks.
func programmerCLI(cfg map[string]string) string {
	if cfg["CUBEIDE"] != "" {
		name := "STM32_Programmer_CLI"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		pat := filepath.Join(cfg["CUBEIDE"], "STM32CubeIDE", "plugins",
			"com.st.stm32cube.ide.mcu.externaltools.cubeprogrammer."+cubePlug()+"_*", "tools", "bin", name)
		if hit := newest(pat); hit != "" {
			return hit
		}
	}
	p, _ := stlink.Find()
	return p
}

func buildFixture() error {
	cfg := loadLocal()
	if err := require("build the fixture firmware",
		cfgNeed(cfg, "BOOT_REPO"),
		need{"CUBEIDE", cubeIDEExe(cfg), "STM32CubeIDE install root: its headless launcher builds the firmware"},
		need{"git", lookPath("git"), "git on PATH: lists $BOOT's files to copy"}); err != nil {
		return err
	}
	section("fixture firmware")
	// A failed build must not leave the last image behind to be flashed.
	if err := os.RemoveAll(fixtureDir()); err != nil {
		return err
	}
	work := filepath.Join(repo, "Output", "build", "fixture")
	defer os.RemoveAll(work)
	project, err := copyBoot(cfg["BOOT_REPO"], work)
	if err != nil {
		return err
	}
	if err := compileFixture(cubeIDEExe(cfg), work, project); err != nil {
		return err
	}
	if err := keepImages(project); err != nil {
		return err
	}
	// The simulated board compiles the same sources: a firmware change otherwise
	// leaves the panel and T4-02 on a stale one. Only warns, since it needs a
	// host compiler the firmware build does not.
	if err := buildSim(hostPlatform(), false); err != nil {
		warn("the simulated board was not rebuilt (" + err.Error() + ") - T4-02 will use the old one")
	}
	return nil
}

// copyBoot copies $BOOT's working tree (tracked + untracked, minus ignored).
func copyBoot(boot, work string) (string, error) {
	if err := os.RemoveAll(work); err != nil {
		return "", err
	}
	out, err := exec.Command("git", "-C", boot, "ls-files", "-z", "-co", "--exclude-standard").Output()
	if err != nil {
		return "", fmt.Errorf("git ls-files in %s: %v", boot, err)
	}
	project := filepath.Join(work, "OpenPLC_BootLoader")
	for _, name := range strings.Split(string(out), "\x00") {
		if name == "" {
			continue
		}
		src := filepath.Join(boot, filepath.FromSlash(name))
		if st, err := os.Stat(src); err != nil || st.IsDir() {
			continue
		}
		dst := filepath.Join(project, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return "", err
		}
		if err := copyFile(src, dst); err != nil {
			return "", err
		}
	}
	return project, nil
}

var (
	makeErrRe  = regexp.MustCompile(`(?m)^make:.*Error \d+`)
	compileErr = regexp.MustCompile(`(?m):\d+:\d+: error:`)
	errLineRe  = regexp.MustCompile(`error:|Error \d+|overflowed`)
	finishedRe = regexp.MustCompile(`Build Finished\. (\d+) errors?, (\d+) warnings?`)
)

func compileFixture(ide, work, project string) error {
	argv := []string{"--launcher.suppressErrors", "-nosplash",
		"-application", "org.eclipse.cdt.managedbuilder.core.headlessbuild",
		"-data", filepath.Join(work, "workspace"), "-import", project,
		"-D", "PORTTOOL_ENABLE=1", "-E", "PLC_LD_SCRIPT=" + toolLD,
		// Clean: objects from a bootloader build are on disk and make does not
		// know the macro changed.
		"-cleanBuild", fixtureProject}
	fmt.Println("  building headless in " + work + " ...")
	b, _ := exec.Command(ide, argv...).CombinedOutput()
	out := string(b)

	if makeErrRe.MatchString(out) || compileErr.MatchString(out) {
		fail("the build failed")
		for _, line := range strings.Split(out, "\n") {
			if errLineRe.MatchString(line) {
				fmt.Println("    " + strings.TrimRight(line, "\r"))
			}
		}
		return fmt.Errorf("the fixture firmware did not build")
	}
	fin := finishedRe.FindAllStringSubmatch(out, -1)
	if fin == nil {
		tail := out
		if len(tail) > 2000 {
			tail = tail[len(tail)-2000:]
		}
		fmt.Println(tail)
		return fmt.Errorf("the build never reported finishing")
	}
	errs, warns := fin[len(fin)-1][1], fin[len(fin)-1][2]
	if errs != "0" {
		return fmt.Errorf("%s errors", errs)
	}
	if strings.Contains(strings.ReplaceAll(out, `"`, ""), "-T"+bootLD) || strings.Contains(out, " "+bootLD) {
		return fmt.Errorf("the linker was given %s, not %s - check the PLC_LD_SCRIPT default in .settings/org.eclipse.cdt.core.prefs", bootLD, toolLD)
	}
	if !strings.Contains(out, toolLD) {
		return fmt.Errorf("no linker line names %s - did PLC_LD_SCRIPT expand? See $PROD/docs/build/CUBEMX-RULES.md", toolLD)
	}
	ok("linked against " + toolLD)
	if !strings.Contains(out, toolMarker) {
		// The symbol did not reach the compiler: what was built is a bootloader.
		return fmt.Errorf("the PORTTOOL_ENABLE marker is not in the build log")
	}
	expected := 1
	for _, line := range strings.Split(out, "\n") {
		for _, k := range knownWarnings {
			if strings.Contains(line, k) {
				expected++
				break
			}
		}
	}
	if n, _ := strconv.Atoi(warns); n > expected {
		warn(fmt.Sprintf("%s warnings (expected %d)", warns, expected))
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "warning:") && !strings.Contains(line, toolMarker) && !containsAny(line, knownWarnings) {
				fmt.Println("    " + strings.TrimRight(line, "\r"))
			}
		}
	} else {
		ok(fmt.Sprintf("0 errors, %s warning(s) - as expected", warns))
	}
	var missing []string
	for _, n := range fixtureImages {
		if !exists(filepath.Join(project, "Debug", n)) {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("not produced: %s", strings.Join(missing, ", "))
	}
	return nil
}

func containsAny(s string, subs []string) bool {
	for _, k := range subs {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// keepImages moves the images and the firmware's version into Output/fixture/.
func keepImages(project string) error {
	dst := fixtureDir()
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, n := range fixtureImages {
		if err := copyFile(filepath.Join(project, "Debug", n), filepath.Join(dst, n)); err != nil {
			return err
		}
	}
	v := fixtureSourceVersion(project)
	if err := os.WriteFile(filepath.Join(dst, "VERSION"), []byte(v), 0o644); err != nil {
		return err
	}
	section("built")
	for _, n := range fixtureImages {
		product(filepath.Join(dst, n))
	}
	product(filepath.Join(dst, "VERSION"))
	// No size limit: ST-Link writes it whole, IAP never carries it.
	ok("fixture firmware " + v)
	return nil
}

// flash writes the last fixture build to the board over SWD.
func flash() error {
	cfg := loadLocal()
	elf := filepath.Join(fixtureDir(), "OpenPLC_BootLoader.elf")
	cli := programmerCLI(cfg)
	if err := require("flash the fixture firmware",
		need{"STM32_Programmer_CLI", cli, "from CUBEIDE in tools/local.json, or a standalone STM32CubeProgrammer"},
		need{"fixture image", elf, "Output/fixture/OpenPLC_BootLoader.elf - build the fixture firmware first"}); err != nil {
		return err
	}
	section("flash over SWD")
	stlink.CLI = cli
	probes, err := stlink.ListProbes()
	if err != nil {
		return fmt.Errorf("could not ask STM32_Programmer_CLI for ST-Links: %v", err)
	}
	if len(probes) == 0 {
		return fmt.Errorf("no ST-Link is connected - plug it in (and power the board), then flash again")
	}
	fmt.Println("  " + elf)
	cmd := exec.Command(cli, "-c", "port=SWD", "mode=UR", "-d", elf, "-v", "-rst")
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err = cmd.Run()
	os.Stdout.Write(buf.Bytes())
	if err != nil {
		return fmt.Errorf("flashing failed (see above) - ST-Link plugged in? board powered? SWD wires firm?")
	}
	ok("flashed; the board has restarted")
	return nil
}
