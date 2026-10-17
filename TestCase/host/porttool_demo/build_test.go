package porttool_demo

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"PortTool/tools/firmhost"
	"PortTool/tools/localcfg"
)

var buildFlag = flag.Bool("build", false,
	"compile the fixture's sim board and the demo's host build, then run T4-05 against both")

// fixtureSim and demoSim are the two boards; empty without -build, and the tests skip.
var fixtureSim, demoSim string

func TestMain(m *testing.M) {
	flag.Parse()
	if *buildFlag {
		if err := build(); err != nil {
			fmt.Fprintln(os.Stderr, "T4-05 could not build: "+err.Error())
			os.Exit(2)
		}
	}
	os.Exit(m.Run())
}

func needBuild(t *testing.T) {
	if fixtureSim == "" || demoSim == "" {
		t.Skip("needs -build: go test ./TestCase/host/porttool_demo/ -args -build (TestCase/test.sh t4-05)")
	}
}

// resolveCXX: $CXX, the g++ beside HOST_CC, then g++ or clang++ on PATH.
func resolveCXX(cfg map[string]string, exe string) string {
	beside := ""
	if cfg["HOST_CC"] != "" {
		beside = filepath.Join(filepath.Dir(cfg["HOST_CC"]), "g++"+exe)
	}
	for _, c := range []string{os.Getenv("CXX"), beside, "g++", "clang++"} {
		if c == "" {
			continue
		}
		if _, err := os.Stat(c); err == nil {
			return c
		}
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	return ""
}

func compile(what, compiler string, args []string) error {
	out, err := exec.Command(compiler, args...).CombinedOutput()
	os.Stdout.Write(out)
	if err != nil {
		return fmt.Errorf("%s did not compile (see above): %v", what, err)
	}
	return nil
}

// build compiles both boards into Output/build/porttool_demo/, leaving
// Output/bin/ alone: a running panel holds the sim board there open.
func build() error {
	repo, err := localcfg.FindRepo()
	if err != nil {
		return err
	}
	cfg := localcfg.Load(repo)
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	core := os.Getenv("CORE_REPO")
	if core == "" {
		core = cfg["CORE_REPO"]
	}
	demoDir := filepath.Join(core, "libraries", "OpenPLC_Ports", "examples", "FixtureDemo")
	cc, boot := cfg["HOST_CC"], cfg["BOOT_REPO"]
	cxx := resolveCXX(cfg, exe)
	fmt.Println("===== T4-05 build")
	fmt.Printf("  demo       %s\n  firmware   %s\n  compilers  %s, %s\n", demoDir, boot, cc, cxx)
	if _, err := os.Stat(filepath.Join(demoDir, "fixture_proto.cpp")); err != nil || core == "" {
		return fmt.Errorf("no FixtureDemo at %s (set CORE_REPO)", demoDir)
	}
	if boot == "" || cc == "" {
		return fmt.Errorf("BOOT_REPO and HOST_CC must be set in %s (tools/build.sh find-tools)", localcfg.Path(repo))
	}
	if cxx == "" {
		return fmt.Errorf("no C++ compiler (set CXX)")
	}
	dir := filepath.Join(repo, "Output", "build", "porttool_demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	fixture := filepath.Join(dir, "porttool_simboard"+exe)
	args, err := firmhost.Args(repo, boot, filepath.Join(firmhost.CDir(repo), "sim_main.c"), fixture)
	if err != nil {
		return err
	}
	if err := compile("the fixture's sim board", cc, append(args, firmhost.SimLink()...)); err != nil {
		return err
	}

	demo := filepath.Join(dir, "demo_sim"+exe)
	dargs := []string{"-std=c++17", "-Wall", "-Wextra", "-O0", "-g", "-I", demoDir,
		filepath.Join(demoDir, "fixture_proto.cpp"),
		filepath.Join(repo, "TestCase", "host", "porttool_demo", "harness", "demo_sim.cpp"),
		"-o", demo, "-pthread", "-static"}
	if runtime.GOOS == "windows" {
		dargs = append(dargs, "-lws2_32")
	}
	if err := compile("the demo source "+filepath.Join(demoDir, "fixture_proto.cpp")+" (with harness/demo_sim.cpp; CORE_REPO picks the board package)", cxx, dargs); err != nil {
		return err
	}
	fmt.Printf("  built: %s, %s\n", fixture, demo)
	fixtureSim, demoSim = fixture, demo
	return nil
}
