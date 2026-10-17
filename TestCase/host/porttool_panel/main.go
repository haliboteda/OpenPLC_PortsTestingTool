// Command porttool_panel clicks through the port tool panel in a real browser.
// Case T4-02 (and T4-03 with --wsl). From the repository root:
//
//	go run ./TestCase/host/porttool_panel --port COM12          the board's RS232 control port
//	go run ./TestCase/host/porttool_panel --port sim            the simulated board, no board needed
//	go run ./TestCase/host/porttool_panel --port COM12 --show   watch it happen in a visible window
//	go run ./TestCase/host/porttool_panel --wsl Debian --port /dev/ttyUSB1
//	                                                            case T4-03: the Linux build, run in WSL
//
// A browser, not the HTTP API: T4-01 and the Go tests cover the protocol and
// the API; only this sees whether the page renders right and its buttons do
// what they say. It never writes flash and never drives an output that needs
// wiring; it starts and stops sessions.
//
// Chrome is CHROME in tools/local.json (tools/build.sh find-tools).
// Criteria: $PROD/docs/engineering/HOW-TO-RUN-PORTTOOL-TESTS.md 「T4-02 的判据」.
//
// Exit 0 = every check held, 1 = a check failed, 2 = could not run at all.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/log"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"PortTool/tools/localcfg"
)

// ---------------------------------------------------------------- output

var (
	outMu    sync.Mutex
	failures []string
	colour   = func() bool {
		if os.Getenv("NO_COLOR") != "" {
			return false
		}
		st, err := os.Stdout.Stat()
		return err == nil && st.Mode()&os.ModeCharDevice != 0
	}()
)

func emit(t, code string) {
	outMu.Lock()
	defer outMu.Unlock()
	if colour {
		t = "\033[" + code + "m" + t + "\033[0m"
	}
	fmt.Println(t)
}

func Section(t string) { outMu.Lock(); fmt.Println(); outMu.Unlock(); emit("===== "+t, "36") }
func Ok(t string)      { emit(t, "32") }
func Warn(t string)    { emit(t, "33") }
func Fail(t string)    { emit(t, "31") }

func addFailure(what string) {
	outMu.Lock()
	failures = append(failures, what)
	outMu.Unlock()
}

// check prints one verdict line; cond is read with Python truthiness.
func check(cond any, what string, detail ...string) bool {
	ok := truthy(cond)
	if ok {
		Ok("PASS  " + what)
	} else {
		d := strings.Join(detail, "")
		if d != "" {
			d = "  --  " + d
		}
		Fail("FAIL  " + what + d)
		addFailure(what)
	}
	return ok
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case J:
		return x.Truthy()
	case string:
		return x != ""
	case []string:
		return len(x) > 0
	case nil:
		return false
	}
	return true
}

// cut is Python's s[:n] on characters.
func cut(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// tail is Python's s[-n:].
func tail(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[len(r)-n:])
	}
	return s
}

// ---------------------------------------------------------------- the panel

var (
	repo string
	wsl  string // --wsl: the distribution the Linux build runs in; the browser stays on Windows
)

func panelExe() string {
	var p string
	if wsl != "" {
		p = filepath.Join(repo, "Output", "bin", "linux", "PortTool")
	} else {
		plat := goruntime.GOOS
		if plat == "darwin" && goruntime.GOARCH == "arm64" {
			plat = "darwin-arm64"
		}
		name := "PortTool"
		if goruntime.GOOS == "windows" {
			name += ".exe"
		}
		p = filepath.Join(repo, "Output", "bin", plat, name)
	}
	if _, err := os.Stat(p); err != nil {
		Fail(fmt.Sprintf("no PortTool at %s - run tools/build.sh tool", p))
		os.Exit(2)
	}
	return p
}

var addrRe = regexp.MustCompile(`http://(127\.0\.0\.1:\d+)`)

// startPanel starts the panel and reads its address from its output. Output
// goes to a file, not a pipe: an unread pipe fills and blocks the panel.
func startPanel(exe string) (*exec.Cmd, string, string) {
	logPath := filepath.Join(os.TempDir(), "porttool_panel_test.log")
	fh, err := os.Create(logPath)
	if err != nil {
		Fail("cannot write " + logPath + ": " + err.Error())
		os.Exit(2)
	}
	cmd := exec.Command(exe, "--no-browser")
	if wsl != "" {
		cmd = exec.Command("wsl", "-d", wsl, "--cd", filepath.Dir(exe), "./"+filepath.Base(exe), "--no-browser")
	}
	cmd.Dir = filepath.Dir(exe)
	cmd.Stdout, cmd.Stderr = fh, fh
	if err := cmd.Start(); err != nil {
		Fail("the panel did not start: " + err.Error())
		os.Exit(2)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); fh.Close(); close(exited) }()
	end := time.Now().Add(20 * time.Second)
	for time.Now().Before(end) {
		select {
		case <-exited:
			Fail("the panel exited immediately - see " + logPath)
			os.Exit(2)
		default:
		}
		b, _ := os.ReadFile(logPath)
		if m := addrRe.FindSubmatch(b); m != nil {
			panelExited[cmd] = exited
			return cmd, "http://" + string(m[1]), logPath
		}
		time.Sleep(200 * time.Millisecond)
	}
	Fail("the panel never printed its address - see " + logPath)
	_ = cmd.Process.Kill()
	os.Exit(2)
	return nil, "", ""
}

var panelExited = map[*exec.Cmd]chan struct{}{}

// stopPanel ends the panel and does not wait forever for it to agree.
func stopPanel(cmd *exec.Cmd) {
	if wsl != "" {
		// Ending wsl.exe does not end the program it started inside WSL.
		_ = exec.Command("wsl", "-d", wsl, "-e", "pkill", "-x", "PortTool").Run()
	}
	exited := panelExited[cmd]
	terminate(cmd.Process)
	select {
	case <-exited:
		return
	case <-time.After(5 * time.Second):
	}
	_ = cmd.Process.Kill()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		Warn("the panel would not exit; leaving it to the OS")
	}
}

// ---------------------------------------------------------------- the browser

// pageErrorOf turns a page exception or a console / log error into a failure
// line and its tag; anything else is "". Chrome always asks for /favicon.ico.
func pageErrorOf(ev any) (string, string) {
	switch e := ev.(type) {
	case *cdpruntime.EventExceptionThrown:
		d := e.ExceptionDetails
		msg := d.Text
		if d.Exception != nil && d.Exception.Description != "" {
			msg = d.Exception.Description
		}
		return "the page threw: " + strings.SplitN(msg, "\n", 2)[0], "page error"
	case *cdpruntime.EventConsoleAPICalled:
		if e.Type != cdpruntime.APITypeError {
			return "", ""
		}
		var parts []string
		for _, a := range e.Args {
			var s string
			if len(a.Value) > 0 && json.Unmarshal(a.Value, &s) == nil {
				parts = append(parts, s)
			} else if len(a.Value) > 0 {
				parts = append(parts, string(a.Value))
			} else {
				parts = append(parts, a.Description)
			}
		}
		where := ""
		if e.StackTrace != nil && len(e.StackTrace.CallFrames) > 0 {
			where = e.StackTrace.CallFrames[0].URL
		}
		return consoleLine(strings.Join(parts, " "), where)
	case *log.EventEntryAdded:
		if e.Entry.Level != log.LevelError || e.Entry.Source == log.SourceWorker {
			return "", ""
		}
		return consoleLine(e.Entry.Text, e.Entry.URL)
	}
	return "", ""
}

func consoleLine(text, where string) (string, string) {
	if strings.Contains(where, "favicon") {
		return "", ""
	}
	return fmt.Sprintf("the page logged an error: %s  (%s)", cut(text, 200), where), "console error"
}

func chromePath() string {
	p := localcfg.Load(repo)["CHROME"]
	if p == "" {
		Fail("CHROME is not set in tools/local.json - run tools/build.sh find-tools, or --set CHROME=<path to chrome>")
		os.Exit(2)
	}
	if _, err := os.Stat(p); err != nil {
		Fail("CHROME in tools/local.json does not exist: " + p + " - run tools/build.sh find-tools")
		os.Exit(2)
	}
	return p
}

// newBrowser starts Chrome with a throwaway profile; stop ends it and its children.
func newBrowser(show bool) (*Page, func()) {
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.ExecPath(chromePath()), chromedp.WindowSize(1280, 720))
	if show {
		opts = append(opts, chromedp.Flag("headless", false))
	}
	actx, acancel := chromedp.NewExecAllocator(context.Background(), opts...)
	// cdproto for Go 1.23 predates some enum values today's Chrome sends (e.g.
	// IPAddressSpace "Loopback"); those events are dropped, nothing here reads them.
	ctx, cancel := chromedp.NewContext(actx, chromedp.WithErrorf(func(format string, a ...any) {
		if msg := fmt.Sprintf(format, a...); !strings.Contains(msg, "could not unmarshal event") {
			Warn(msg)
		}
	}))
	p := &Page{ctx: ctx}
	p.listen(func(msg, tag string) {
		Fail(msg)
		addFailure(tag)
	})
	// Playwright's default viewport; the layout checks compare column positions.
	if err := chromedp.Run(ctx, emulation.SetDeviceMetricsOverride(1280, 720, 1, false)); err != nil {
		Fail("Chrome did not start: " + err.Error())
		os.Exit(2)
	}
	if err := p.allowDownloads(); err != nil {
		Fail("Chrome refused downloads: " + err.Error())
		os.Exit(2)
	}
	var proc *os.Process
	if c := chromedp.FromContext(ctx); c != nil && c.Browser != nil {
		proc = c.Browser.Process()
	}
	return p, func() {
		killTree(proc)
		cancel()
		acancel()
		if p.dlDir != "" {
			_ = os.RemoveAll(p.dlDir)
		}
	}
}

// checkPageParses fails on a broken page before the panel is opened: a syntax
// error otherwise shows up only as every later check timing out on a selector.
func checkPageParses(p *Page) {
	src := filepath.Join(repo, "src", "ptpanel", "web", "index.html")
	b, err := os.ReadFile(src)
	if err != nil {
		Fail("no panel page at " + src)
		addFailure("panel page missing")
		return
	}
	page := string(b)
	var parts []string
	for _, m := range regexp.MustCompile(`(?s)<script>(.*?)</script>`).FindAllStringSubmatch(page, -1) {
		parts = append(parts, m[1])
	}
	for _, m := range regexp.MustCompile(`<script src="([^"]+)"></script>`).FindAllStringSubmatch(page, -1) {
		js, err := os.ReadFile(filepath.Join(filepath.Dir(src), m[1]))
		if err != nil {
			check(false, "the panel page parses", err.Error())
			return
		}
		parts = append(parts, string(js))
	}
	msg, err := p.compileOnly(strings.Join(parts, "\n"), "porttool_panel_page.js")
	if err != nil {
		Warn("Chrome could not compile the page - skipping the syntax check: " + err.Error())
		return
	}
	check(msg == "", "the panel page parses", cut(msg, 400))
}

// ---------------------------------------------------------------- main

func main() { os.Exit(run()) }

func run() int {
	port := flag.String("port", "", "the board's RS232 control port, or sim")
	show := flag.Bool("show", false, "visible browser window")
	flag.StringVar(&wsl, "wsl", "", "run the Linux build in this WSL distribution (T4-03)")
	flag.Parse()
	if *port == "" {
		fmt.Fprintln(os.Stderr, "usage: go run ./TestCase/host/porttool_panel --port COMx|sim [--show] [--wsl DISTRO]")
		return 2
	}
	var err error
	if repo, err = localcfg.FindRepo(); err != nil {
		Fail(err.Error())
		return 2
	}
	unlock, held := singleInstance()
	if !held {
		Fail("another T4-02 is already running on this machine - wait for it to finish")
		return 2
	}
	defer unlock()

	exe := panelExe()
	page, stopBrowser := newBrowser(*show)
	defer stopBrowser()

	Section("the page itself")
	checkPageParses(page)

	// The first half is the first-run state (nothing remembered, connect refused);
	// the mapping the panel writes is checked at the end.
	saved := filepath.Join(filepath.Dir(exe), "porttool_ports.json")
	_ = os.Remove(saved)
	// The bench's own configurations and page memory are put back afterwards;
	// the run starts from the defaults.
	restore := keepAside(filepath.Join(filepath.Dir(exe), "porttool_stagecfg.json"),
		filepath.Join(filepath.Dir(exe), "porttool_pagememo.json"))

	Section("panel")
	proc, base, logPath := startPanel(exe)
	fmt.Printf("  %s   control port %s\n", base, *port)
	fmt.Printf("  panel log %s\n", logPath)

	err = try(func() {
		page.Goto(base)
		runChecks(page, *port)
		checkRemembered(saved, *port)
		// Leave the page first so its SSE connection is dropped.
		page.Goto("about:blank")
	})
	stopPanel(proc)
	if err != nil {
		Fail("the run threw: " + strings.SplitN(err.Error(), "\n", 2)[0])
		addFailure("run threw")
		restore()
		return 1
	}

	checkConfigsSurviveRestart(exe)
	restore()

	Section("result")
	if len(failures) > 0 {
		Fail(fmt.Sprintf("%d check(s) failed", len(failures)))
		return 1
	}
	Ok("every check held")
	return 0
}

// keepAside removes the files and returns a function that puts back what was there.
func keepAside(paths ...string) func() {
	kept := map[string][]byte{}
	for _, p := range paths {
		if b, err := os.ReadFile(p); err == nil {
			kept[p] = b
		}
		_ = os.Remove(p)
	}
	return func() {
		for _, p := range paths {
			if b, ok := kept[p]; ok {
				_ = os.WriteFile(p, b, 0o644)
			} else {
				_ = os.Remove(p)
			}
		}
	}
}

// checkConfigsSurviveRestart: the configurations are remembered next to the
// exe, so a panel started again comes back with them (decision 97).
func checkConfigsSurviveRestart(exe string) {
	Section("what it remembered: configurations after a restart")
	if !check(stageCfgChanged, "the configuration checks ran") {
		return
	}
	proc, base, _ := startPanel(exe)
	var got J
	func() {
		defer stopPanel(proc)
		c := http.Client{Timeout: 10 * time.Second}
		r, err := c.Get(base + "/api/stagecfg")
		if err != nil {
			return
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		var v any
		_ = json.Unmarshal(b, &v)
		got = J{v}
	}()
	aging := got.K("aging")
	items := aging.K("items")
	fn := got.K("func").K("items")
	shown := cut(got.String(), 300)
	check(aging.K("duration").S() == "2", "the aging duration survived", shown)
	check(aging.K("ports_off").K("temp").B(), "the unticked port survived", shown)
	check(items.K("can.peer").K("peer").S() == "pc", "the chosen peer survived", shown)
	pts := items.K("aout.out1").K("points")
	check(pts.Len() == 3 && pts.I(0).N() == 2500 && pts.I(1).N() == 5000 && pts.I(2).N() == 9000,
		"the AO test points survived", shown)
	usbInfo := fn.K("usb.info").K("on")
	check(fn.K("usb.xfer").K("on").B() && !(usbInfo.v == false),
		"both USB items stay ticked after a restart (decision 128)", shown)
}

func checkRemembered(saved, com string) {
	Section("what it remembered")
	b, err := os.ReadFile(saved)
	if isSim(com) {
		// The simulated board must NOT be written down: recording it overwrites
		// a bench's real port mapping, which only a person can give.
		if err != nil {
			check(true, "nothing was written down for the simulated board")
			return
		}
		var got map[string]any
		_ = json.Unmarshal(b, &got)
		ctl, _ := got["control"].(string)
		check(strings.ToLower(ctl) != "sim", "the simulated board is not written down as a control port", string(b))
		check(got["peers"] != nil, "and whatever a bench had bound is left alone", string(b))
		return
	}
	// Remembered, so which adapter is which is worked out once, not on every open.
	if check(err == nil, "the control port is written down after connecting", saved) {
		var got map[string]any
		_ = json.Unmarshal(b, &got)
		ctl, _ := got["control"].(string)
		check(ctl == com, "the port written down is the one that was used", string(b))
	}
}

func isSim(com string) bool { return strings.ToLower(strings.TrimSpace(com)) == "sim" }
