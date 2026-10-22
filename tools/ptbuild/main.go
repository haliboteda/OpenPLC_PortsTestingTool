// Command ptbuild builds and packs PortTool: finds this machine's tools,
// builds PortTool, builds and flashes the fixture firmware, builds the
// simulated board and packs the delivery folder (decision 149 in $PROD).
//
//	go run ./tools/ptbuild                 menu
//	go run ./tools/ptbuild <item> [flags]  one item; see usage below
//
// Started through tools/build.sh, which runs it from the repository root.
//
// Exit 0 = done, 1 = an item failed or a tool it needs is missing, 2 = bad usage.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"PortTool/tools/localcfg"
)

const usage = `usage: tools/build.sh [item] [flags]

  (no item)                 menu
  find-tools [--set K=V] [--check]
                            find this machine's tools, write tools/local.json
  tool [--os P]             build PortTool into Output/bin/<platform>/ (P: windows,
                            linux, darwin, darwin-arm64, all; default all)
  fixture                   build the fixture firmware into Output/fixture/
  flash                     write Output/fixture/ to the board over SWD
  sim [--os P]              build the simulated board (default: this machine)
  deliver [--open] [--internal]
                            pack Output/delivery/; --internal packs even if a
                            version was not raised, not recorded as shipped
  all                       fixture + tool --os all + deliver
  get KEY                   print one tools/local.json value (for scripts)
`

// errUsage marks a bad command line, exit 2.
var errUsage = errors.New("bad usage")

// repo is the repository root; every path below is under it.
var repo string

func main() {
	var err error
	repo, err = findRepo()
	if err != nil {
		fail(err.Error())
		os.Exit(2)
	}
	if len(os.Args) < 2 {
		err = menu()
	} else {
		err = run(os.Args[1], os.Args[2:])
	}
	switch {
	case err == nil:
		os.Exit(0)
	case errors.Is(err, errUsage):
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	default:
		section("result")
		fail(err.Error())
		os.Exit(1)
	}
}

func findRepo() (string, error) { return localcfg.FindRepo() }

func run(item string, args []string) error {
	// get prints one tools/local.json value, for a shell step that needs a tool
	// path (TestCase/test.sh: the race detector's CC).
	if item == "get" {
		if len(args) != 1 {
			return errUsage
		}
		v := loadLocal()[args[0]]
		if v == "" {
			// stderr, so $(...) in a script captures nothing on a miss
			fmt.Fprintf(os.Stderr, "%s is not set in %s - run tools/build.sh find-tools\n", args[0], localPath())
			os.Exit(1)
		}
		fmt.Println(v)
		return nil
	}
	fs := flag.NewFlagSet(item, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	osFlag := fs.String("os", "", "platform")
	open := fs.Bool("open", false, "open the folder afterwards")
	internal := fs.Bool("internal", false, "pack even if a version was not raised")
	check := fs.Bool("check", false, "report only, write nothing")
	var sets multiFlag
	fs.Var(&sets, "set", "KEY=VALUE")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return errUsage
	}
	switch item {
	case "find-tools":
		return findTools(sets, *check)
	case "tool":
		plats, err := platforms(*osFlag, "all")
		if err != nil {
			return err
		}
		return buildTool(plats)
	case "fixture":
		return buildFixture()
	case "flash":
		return flash()
	case "sim":
		plats, err := platforms(*osFlag, hostPlatform())
		if err != nil {
			return err
		}
		for _, p := range plats {
			if err := buildSim(p, p != hostPlatform()); err != nil {
				return err
			}
		}
		return nil
	case "deliver":
		return deliver(*open, *internal)
	case "all":
		return all()
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	}
	return errUsage
}

func all() error {
	if err := cleanOutput(); err != nil {
		return err
	}
	if err := buildFixture(); err != nil {
		return err
	}
	if err := buildTool(allPlatforms); err != nil {
		return err
	}
	return deliver(false, false)
}

// cleanOutput empties Output/ before "All" (decision 149). The panel keeps its
// settings and records beside the exe, so from the menu it lists what goes and
// asks first.
func cleanOutput() error {
	out := filepath.Join(repo, "Output")
	ents, err := os.ReadDir(out)
	if os.IsNotExist(err) || len(ents) == 0 {
		return nil
	}
	if err != nil {
		return err
	}
	section("emptying Output")
	for _, e := range ents {
		fmt.Println("  " + filepath.Join(out, e.Name()))
	}
	if interactive {
		n, err := ask("Delete everything above, including the panel's saved settings and records beside the exe?",
			[]string{"Yes, delete it and build everything", "No, stop"}, 2)
		if err != nil {
			return err
		}
		if n == 2 {
			return errors.New("stopped; Output/ is untouched")
		}
	}
	for _, e := range ents {
		p := filepath.Join(out, e.Name())
		if err := os.RemoveAll(p); err != nil {
			return fmt.Errorf("cannot delete %s (open in Explorer, or a program in it running?): %v", p, err)
		}
	}
	ok("Output/ emptied")
	return nil
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// Output/bin/<platform>: GOOS, plus darwin-arm64 for Apple silicon
// ($PROD/maps/porttool-on-linux-and-macos/issues/XPT-02-whether-to-ship-arm64.md).
var allPlatforms = []string{"windows", "linux", "darwin", "darwin-arm64"}

func hostPlatform() string {
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		return "darwin-arm64"
	}
	return runtime.GOOS
}

func platforms(v, def string) ([]string, error) {
	if v == "" {
		v = def
	}
	if v == "all" {
		return allPlatforms, nil
	}
	for _, p := range allPlatforms {
		if v == p {
			return []string{p}, nil
		}
	}
	fmt.Fprintf(os.Stderr, "--os must be one of: %s, all\n", strings.Join(allPlatforms, ", "))
	return nil, errUsage
}

func goosArch(p string) (string, string) {
	if p == "darwin-arm64" {
		return "darwin", "arm64"
	}
	return p, "amd64"
}

func exeSuffix(p string) string {
	if p == "windows" {
		return ".exe"
	}
	return ""
}

func binDir(p string) string { return filepath.Join(repo, "Output", "bin", p) }

var stdin = bufio.NewReader(os.Stdin)

// interactive is set by the menu: only then may a build step stop and ask.
var interactive bool

// ask prints a numbered list and returns the 1-based pick; def on a bare Enter.
func ask(title string, items []string, def int) (int, error) {
	fmt.Println()
	fmt.Println("  " + title)
	for i, it := range items {
		fmt.Printf("    %d) %s\n", i+1, it)
	}
	fmt.Printf("\n  Choose [1-%d, Enter=%d]: ", len(items), def)
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		// Nobody there to ask: a script must name the item instead.
		fmt.Println()
		return 0, errUsage
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def, nil
	}
	var n int
	if _, err := fmt.Sscanf(line, "%d", &n); err != nil || n < 1 || n > len(items) {
		fmt.Println("  no such item: " + line)
		return 0, errUsage
	}
	return n, nil
}

var platformNames = []string{"Windows", "Linux", "macOS (Intel)", "macOS (Apple silicon)"}

func menu() error {
	interactive = true
	n, err := ask("What to do?", []string{
		"Find this machine's tools      (writes tools/local.json)",
		"Build PortTool",
		"Build the fixture firmware",
		"Flash the fixture firmware     (SWD, ST-Link connected)",
		"Build the simulated board",
		"Pack the delivery folder",
		"All                            (firmware + PortTool + delivery)",
	}, 2)
	if err != nil {
		return err
	}
	switch n {
	case 1:
		return findTools(nil, false)
	case 2:
		p, err := ask("Which platform?", append(append([]string{}, platformNames...), "All"), 5)
		if err != nil {
			return err
		}
		if p == 5 {
			return buildTool(allPlatforms)
		}
		return buildTool(allPlatforms[p-1 : p])
	case 3:
		return buildFixture()
	case 4:
		return flash()
	case 5:
		def := 1
		for i, p := range allPlatforms {
			if p == hostPlatform() {
				def = i + 1
			}
		}
		p, err := ask("Which platform? (this machine uses HOST_CC, the others zig)", platformNames, def)
		if err != nil {
			return err
		}
		return buildSim(allPlatforms[p-1], allPlatforms[p-1] != hostPlatform())
	case 6:
		return deliver(false, false)
	}
	return all()
}
