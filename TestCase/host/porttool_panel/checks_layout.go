package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

func runChecks(p *Page, com string) {
	// ---------------------------------------------------- before connecting
	Section("before a control port is chosen")
	// Step one is a dropdown, the simulated board its last option (decision 128).
	p.WaitSelState("#ctlport option[data-port]", "attached", 0)
	check(p.L("#connbox .step").First().Text() == "第一步：连接 RS232 串口",
		"step one says to connect the RS232 serial port", p.L("#connbox .step").First().Text())
	opts := p.L("#ctlport option[data-port]")
	var names, texts []string
	for i := 0; i < opts.Count(); i++ {
		names = append(names, opts.Nth(i).Attr("data-port"))
		texts = append(texts, strings.TrimSpace(opts.Nth(i).Text()))
	}
	check(opts.Count() > 0, "the control port is picked from a dropdown", fmt.Sprintf("%d options", opts.Count()))
	// R4-03: a port name alone does not say which adapter it is. An option with
	// USB ids must also say what the device calls itself or which driver took it.
	var unnamed []string
	for _, t := range texts {
		if strings.Contains(t, "[") && !strings.Contains(t, " - ") && !strings.Contains(t, " (") {
			unnamed = append(unnamed, t)
		}
	}
	check(len(unnamed) == 0, "every USB port option says what the adapter is", strings.Join(unnamed, "; "))
	check(p.L("#connect").Disabled(), "connect is refused until a port is picked")
	check(p.L("#ctlport").Value() == "", "nothing is preselected on a first run")
	if slices.Contains(names, "sim") {
		// It is invented data: last, after every real port, and marked as such.
		check(names[len(names)-1] == "sim", "the simulated board is the last option", show(names))
		check(strings.Contains(texts[len(texts)-1], "模拟板"), "and says it is the simulated board", texts[len(texts)-1])
	} else {
		check(true, "no simulated board built on this machine - nothing to check")
	}
	check(!strings.Contains(p.L("#menus").Attr("class"), "live"), "the test types are gated off before connecting too")

	// The whole rest of the page is inert: everything on it is a reading that
	// arrives over the control port.
	gate := p.L("#afterconn")
	check(!strings.Contains(gate.Attr("class"), "live"), "the rest of the page is gated off before connecting")

	// ---------------------------------------------------- pick and connect
	Section("choosing the control port")
	if !check(slices.Contains(names, com), "the control port "+com+" is in the dropdown", show(names)) {
		return
	}
	p.L("#ctlport").Select(com)
	check(!p.L("#connect").Disabled(), "connect becomes available once a port is picked")
	check(strings.Contains(p.L("#connhint").Text(), com), "the hint names the port that was picked")

	p.L("#connect").Click()
	p.WaitSel("#afterconn.live", 0)
	check(true, "connecting opens the rest of the page")
	check(p.L("#ctlport").Disabled() && p.L("#disconnect").Visible() && strings.Contains(p.L("#menus").Attr("class"), "live"),
		"connected: the dropdown is locked, 断开 is offered, the test types open up")
	status := p.L("#status").Text()
	check(strings.Contains(strings.ToLower(status), "porttool") || regexp.MustCompile(`\d+\.\d+\.\d+`).MatchString(status),
		"the header shows the firmware version the board reported", status)

	checkFiveBlocks(p)
	checkLeftResize(p)
	checkConnZone(p)
	checkCfgItemNotes(p)
	checkRdHasCfgItems(p)
	checkStageConfigs(p)
	checkItemRunner(p)
	checkRelayRd(p)
	checkEveryPort(p, com)
	checkCtrlCounts(p)
	checkNothingInEnglish(p)
	checkRunLogShown(p) // after the sweep: it leaves temp's card STOPPED
	checkTimedReadable(p)
	checkLogPane(p)
	checkWhichLimits(p)

	checkAutoechoToggle(p)
	checkPeerBinding(p)
	checkPeerErrorLine(p)
	checkAbsentField(p)
	checkCanPeer(p)
	checkDoutBlink(p)
	checkCanWindow(p, com)
	checkAnalogCards(p)
	// The configurations run AI / AO / DI / DO against the reference board (decision 129).
	sim := isSim(com)
	if sim {
		check(p.Eval("api('/api/ref', {port: 'sim'})").K("connected"),
			"a second simulated board connects as the reference for the full test")
	}
	checkFullTest(p, com)
	if sim {
		p.Eval("api('/api/ref', {port: ''})")
	}
	checkKnxFrames(p)
	checkKnxPortVerdict(p)
	checkKnxWindow(p, com)
	checkRs232Baud(p)
	checkRecoverCards(p, com)
	checkLiveCards(p, com)

	// ---------------------------------------------------- disconnect
	Section("disconnecting")
	p.L("#disconnect").Click()
	p.WaitSel("#afterconn:not(.live)", 0)
	check(true, "disconnecting gates the page again")
}

func checkLogPane(p *Page) {
	Section("the log pane")
	p.L("#pause").Click()
	check(p.L("#paused").Visible(), "pausing the log says so - it pauses drawing, not reading")

	// Timed: resuming rebuilds the whole pane, and a layout per line freezes the
	// page for seconds. The bound is loose; it catches per-line layout, not milliseconds.
	t0 := time.Now()
	p.L("#pause").Click()
	check(!p.L("#paused").Visible(), "resuming clears that")
	resume := time.Since(t0).Milliseconds()
	check(resume < 3000, "resuming the log redraws in one go, not a layout per line",
		fmt.Sprintf("took %d ms - see redrawLog()", resume))

	p.L("#clear").Click()
	_ = p.L("#log").Text()
	check(true, "clearing the log does not throw")

	for _, box := range p.L(".filt").All() {
		box.Uncheck()
		box.Check()
	}
	check(true, "every log filter can be switched off and on")

	// Exporting the log: .txt, so it opens by double-click and mails as an attachment.
	if err := try(func() {
		name, path := p.ExpectDownload(10000, func() { p.L("#save").Click() })
		check(strings.HasSuffix(name, ".txt"), "the log exports as .txt", name)
		saved := filepath.Join(os.TempDir(), "porttool_export_check.txt")
		b, err := os.ReadFile(path)
		if err != nil {
			throw("reading the download: %v", err)
		}
		_ = os.WriteFile(saved, b, 0o644)
		body := string(b)
		first := "(empty)"
		if body != "" {
			first = strings.SplitN(body, "\n", 2)[0]
		}
		check(strings.HasPrefix(body, "PortTool log"), "the exported file says what it is on the first line", first)
		// A log exported to send somebody has to carry which port and which
		// firmware produced it, or it cannot be matched to a board later.
		check(strings.Contains(body, "control port:") && strings.Contains(body, "firmware:"),
			"the export records the port and the firmware it came from")
		n := len(splitLines(body))
		check(n > 4, "the export has the log lines under that header", fmt.Sprintf("%d lines", n))
	}); err != nil {
		Fail("FAIL  exporting the log threw: " + strings.SplitN(err.Error(), "\n", 2)[0])
		addFailure("log export")
	}
}

// splitLines is Python's str.splitlines for \n and \r\n text.
func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// checkWhichLimits: a tick must say which limit set produced it, and limits
// switch only by choosing a named plan, never by editing a number.
func checkWhichLimits(p *Page) {
	Section("which limits the verdicts came from")
	crit := p.L("#critbox")
	check(crit.Count() > 0 && crit.Visible(), "the page says which limits it is judging by, in the header")
	sel := p.L("#critplan")
	strict := sel.Value()
	check(strings.HasSuffix(strict, ".json"), "a plan file supplies the limits", strict)
	verBefore := p.L("#critver").Text()
	check(strings.Contains(verBefore, "限值"), "and shows that plan's limit_version, not just its name", verBefore)

	options := sel.L("option").AllTextContents()
	relaxed := ""
	for _, o := range options {
		if strings.Contains(o, "relaxed") {
			relaxed = o
			break
		}
	}
	if check(relaxed != "", "a relaxed limit set is offered next to the strict one", strings.Join(options, ", ")) {
		before := int(p.Eval("Object.keys(libData.states || {}).length").N())
		check(before > 0, "the cards hold verdicts from the sweep before switching", strconv.Itoa(before))
		sel.Select(relaxed)
		p.Wait(800)
		check(p.Eval("Object.keys(libData.states || {}).length + Object.keys(libData.ports || {}).length").N() == 0,
			"the verdicts from the old limits are cleared, not left looking current (decision 30)")
		check(sel.Value() == relaxed, "choosing the relaxed set switches to it", sel.Value())
		verAfter := p.L("#critver").Text()
		check(verAfter != verBefore, "the limit_version on screen changes with it", verBefore+" -> "+verAfter)
		check(strings.Contains(p.L("#log").Text(), "判据换成"), "and the log records the change, so a report can be traced to it")
		sel.Select(strict)
		p.Wait(600)
		check(sel.Value() == strict, "and it switches back")
	}
	// No editable limit anywhere: that is the property being protected.
	check(p.L("#itemcards input[data-limit]").Count() == 0, "no limit is editable on the item cards")
}

// openSub opens 工装完整测试 on sub-test k (decision 125): the type menu, then its tile.
func openSub(p *Page, k string) {
	tile := `#subtiles .subtile[data-sub="` + k + `"]`
	if p.L(tile).Count() == 0 {
		p.L(`.mi[data-menu="full"]`).Click()
		p.WaitSel(tile, 0)
	}
	p.L(tile + " .nm").Click()
	p.Wait(250)
}

// startedLines are the log lines of a session start: the command sent and the board's answer.
func startedLines(log string) []string {
	var out []string
	for _, l := range splitLines(log) {
		if strings.Contains(l, "> pt.start ") || strings.Contains(l, "OK started") {
			out = append(out, l)
		}
	}
	return out
}

func lport(port string) string { return `.lport[data-lport="` + port + `"]` }

// attrs reads one attribute of every match, in order.
func attrs(l *Loc, name string) []string {
	var out []string
	for i := 0; i < l.Count(); i++ {
		out = append(out, l.Nth(i).Attr(name))
	}
	return out
}

func eqStrings(a []string, b ...string) bool { return slices.Equal(a, b) }

// checkFiveBlocks: the redesigned shell (decision 97, STG-10; PORTTOOL-FLOW.md
// C.3.6). Left menus only choose what is shown; clicking every menu sends no pt.start.
func checkFiveBlocks(p *Page) {
	Section("layout (decision 128)")
	check(p.L(".tab").Count() == 0, "no tab bar for the main flow")
	for _, b := range [][2]string{{"#connzone", "connection"}, {"#menus", "test types"},
		{"#mid", "port list"}, {"#right", "test items"}, {"#logcol", "log"}} {
		check(p.L(b[0]).Visible(), "R&D: the "+b[1]+" block is on screen")
	}
	check(!p.L("#statcol").Visible(), "R&D: no run-status column")
	check(eqStrings(attrs(p.L("#typemenu .mi"), "data-menu"), "rd", "full") && p.L(`.mi[data-menu="ref"]`).Count() == 0,
		"two test types, R&D and the full fixture test; no separate 制作标准机 (decision 128)")
	// The test types sit at the bottom of the left column, under the other connections.
	mb, rb, cb := p.L("#menus").Box(), p.L("#connrows").Box(), p.L("#connzone").Box()
	check(mb != nil && rb != nil && cb != nil && mb.Y >= rb.Y+rb.H-1 && cb.X <= mb.X && mb.X < cb.X+cb.W,
		"the test types sit in the left column, under the other connections", show([]any{mb, rb, cb}))

	p.L("#clear").Click()
	for _, k := range []string{"rd", "full", "rd"} {
		p.L(`.mi[data-menu="` + k + `"]`).Click()
		p.Wait(150)
		check(strings.Contains(p.L(`.mi[data-menu="`+k+`"]`).Attr("class"), "cur"), "clicking "+k+" marks it as the current menu")
	}
	for _, k := range []string{"func", "aging", "safety", "user"} {
		openSub(p, k)
		check(strings.Contains(p.L(`#subtiles .subtile[data-sub="`+k+`"]`).Attr("class"), "cur") &&
			strings.Contains(p.L(`.mi[data-menu="full"]`).Attr("class"), "cur"),
			"clicking the "+k+" tile shows it, under 工装完整测试")
	}
	p.L(`.mi[data-menu="rd"]`).Click()
	p.WaitSel(".lport", 0)
	rows := p.L(".lport:not(.dim)")
	for i := 0; i < rows.Count(); i++ {
		rows.Nth(i).Click()
	}
	p.Wait(300)
	p.WaitFunc("() => !libRunning() && !rdStarting && !rdBusy", nil, 15000)
	// The port header in two halves (PORTTOOL-FLOW.md C.3.6 「页头和日志」); nothing tested yet.
	hl, hr := p.L("#rtop .phl").Box(), p.L("#rtop .phr").Box()
	check(p.L("#rtop .phl h2").Count() == 1 && strings.Contains(p.L("#rtop .phl .sub").Text(), "端子") &&
		hl != nil && hr != nil && hr.X >= hl.X+hl.W,
		"R&D port header: name and terminals on the left half, the rest on the right", show([]any{hl, hr}))
	check(strings.TrimSpace(p.L("#rtop .phr").Text()) == "还没测过。",
		"a port not tested yet says only 还没测过。 on the right", p.L("#rtop .phr").Text())
	// Decision 137: the AI and DI pages read only after 开始读取.
	starts := startedLines(p.L("#log").Text())
	check(len(starts) == 0, "menus and the port list send no pt.start, the AI / DI pages included", cut(strings.Join(starts, " "), 200))

	names := attrs(p.L(".lport"), "data-lport")
	check(!slices.Contains(names, "rtc") && slices.Contains(names, "eth") && slices.Contains(names, "din") &&
		slices.Contains(names, "can") && slices.Contains(names, "aout"),
		"the port list comes from the catalogue (rtc is not a test item)", strings.Join(names, " "))
	p.L(lport("can")).Click()
	p.Wait(200)
	ids := attrs(p.L("#itemcards .item"), "data-item")
	check(eqStrings(ids, "can.bus", "can.recover", "can.peer", "can.loop", "can.ext"),
		"R&D shows every CAN item as a card, the analyser window and disconnect-and-recover in the once group", show(ids))
	check(p.L(`#itemcards button[data-start="repeat"]`).Count() == 1 && p.L(`#itemcards button[data-start="once"]`).Count() == 0,
		"R&D: the repeatable group has its Start, the once group none (decisions 115, 116)")
	check(p.L(`#itemcards .item[data-item="can.bus"] .ih button[data-startitem="can.bus"]`).Count() == 1 &&
		p.L(`#itemcards input[data-pick="can.bus"]`).Count() == 0,
		"a once card has its own Start beside its name and no tick (decision 116)")
	var ticked []string
	for _, i := range ids {
		b := p.L(`#itemcards input[data-pick="` + i + `"]`)
		if b.Count() > 0 && b.Checked() {
			ticked = append(ticked, i)
		}
	}
	check(eqStrings(ticked, "can.peer"), "of one exclusive group only the first starts ticked", show(ticked))
	p.L(`#itemcards input[data-pick="can.ext"]`).Check()
	p.Wait(100)
	check(!p.L(`#itemcards input[data-pick="can.peer"]`).Checked(), "ticking one CAN mode unticks the others in its group")

	openSub(p, "func")
	p.L(lport("can")).Click()
	p.Wait(200)
	ids = attrs(p.L("#itemcards .item"), "data-item")
	check(eqStrings(ids, "can.peer"), "the functional test offers only the production CAN item", show(ids))
	checkSubTestPage(p)
	p.L(`.mi[data-menu="rd"]`).Click()

	// Decision 133: R&D is done by hand; the DI card has no data source.
	p.L(lport("din")).Click()
	p.Wait(200)
	check(p.L("#itemcards .diocard").Count() == 1 && p.L("#itemcards .diocard select[data-diosrc]").Count() == 0,
		"the R&D DI card has no data-source choice")
	check(p.Eval("(libPicks().find(p => p.id === 'din.level') || {}).peer").S() == "human",
		"and Start would send a person as the peer")
}

// checkLeftResize: the connection column is dragged wider and keeps that width
// across a reload (decision 112); the stored width is cleared afterwards.
func checkLeftResize(p *Page) {
	Section("widening the connection column")
	grip := p.L("#leftgrip")
	if !check(grip.Count() == 1 && grip.Visible(), "there is a handle on the connection column's edge") {
		return
	}
	before := p.L("#connzone").Box().W
	g := grip.Box()
	x, y := g.X+g.W/2, g.Y+200
	p.MouseMove(x, y, 1, false)
	p.MouseDown()
	p.MouseMove(x+60, y, 5, true)
	p.MouseMove(x+120, y, 5, true)
	p.MouseUp()
	after := p.L("#connzone").Box().W
	check(after > before+80, "dragging the handle widens the column", fmt.Sprintf("%.0f -> %.0f", before, after))
	p.Reload()
	p.WaitSel("#afterconn.live", 20000)
	kept := p.L("#connzone").Box().W
	check(math.Abs(kept-after) < 3, "the width is the same after a reload", fmt.Sprintf("%.0f vs %.0f", kept, after))
	p.Eval("() => { try { localStorage.removeItem('porttool.leftw'); } catch (e) {} }")
	p.Reload()
	p.WaitSel("#afterconn.live", 20000)
}

// checkSubTestPage: 工装完整测试's three layers (decision 125, PORTTOOL-FLOW.md
// C.3.6): one start button on top, four tiles with their own times, cards with
// criteria only - no Start, no group duration, no result.
func checkSubTestPage(p *Page) {
	Section("full fixture test: three layers")
	// Decision 128: sub-tests and ports | cards in the middle, run status on the right.
	check(p.L("#statcol").Visible() && !p.L("#logcol").Visible(), "the full test shows the run-status column instead of the R&D log")
	sb, rb := p.L("#statcol").Box(), p.L("#right").Box()
	check(sb != nil && rb != nil && sb.X >= rb.X+rb.W-1, "「正在进行」 is the rightmost column", show([]any{sb, rb}))
	var tabs []string
	for i := 0; i < p.L(".rtabs button").Count(); i++ {
		tabs = append(tabs, p.L(".rtabs button").Nth(i).Text())
	}
	check(eqStrings(tabs, "结果", "日志"), "under it two tabs, 结果 and 日志")
	check(p.L("#midtitle").Text() == "功能测试 · 端口", "the port list is headed by its sub-test", p.L("#midtitle").Text())
	cnt := p.L(`.lport[data-lport="can"] [data-ticked]`)
	cntText := "none"
	if cnt.Count() > 0 {
		cntText = cnt.Text()
	}
	check(cnt.Count() == 1 && regexp.MustCompile(`^\d+/\d+$`).MatchString(cnt.Text()), "each port says ticked / total", cntText)
	starts := p.L("#afterconn button").HasText("开始")
	var st []string
	for i := 0; i < starts.Count(); i++ {
		st = append(st, starts.Nth(i).Text())
	}
	check(starts.Count() == 1 && starts.First().Attr("id") == "shellstart" && p.L("#shellstart").Text() == "配置完成，开始测试",
		"the only start button is 「配置完成，开始测试」 on top", strings.Join(st, " | "))
	check(eqStrings(attrs(p.L("#subtiles .subtile"), "data-sub"), "func", "aging", "safety", "user"),
		"layer 2 has the four sub-tests side by side")
	opts := func(k string) []string {
		return p.Eval("k => [...document.querySelectorAll(`#subtiles select[data-subdur='${k}'] option`)].map(o => o.textContent)", k).Strings()
	}
	// Decision 139: the functional test runs once, no time picker.
	only := p.L(`#subtiles [data-subonly="func"]`)
	check(len(opts("func")) == 0 && only.Count() == 1 && only.Text() == "单次",
		"functional shows 单次 with no time picker (decision 139)", show(opts("func")))
	check(eqStrings(opts("user"), "单次", "10 分钟", "30 分钟", "1 小时"), "user-level offers once / 10 min / 30 min / 1 h", show(opts("user")))
	check(eqStrings(opts("aging"), "1 小时", "2 小时", "4 小时", "8 小时"), "aging offers 1 / 2 / 4 / 8 h", show(opts("aging")))
	check(p.L(`#subtiles select[data-subdur="safety"]`).Count() == 0 &&
		strings.Contains(p.L(`#subtiles .subtile[data-sub="safety"]`).Text(), "扫码"),
		"safety has no time and says it is done by hand")
	check(p.L(`#subtiles [data-subn="func"]`).Count() == 1, "each configurable tile says how many items are picked")
	hd := ""
	if p.L("#itemcards [data-sechd]").Count() > 0 {
		hd = p.L("#itemcards [data-sechd]").Text()
	}
	check(p.L("#itemcards [data-sechd]").Count() == 1 && p.L("#itemcards [data-sechd]").Text() == "功能测试 · CAN",
		"layer 3 is headed 子测试 · 端口", hd)
	check(p.L("#itemcards button[data-start]").Count() == 0 && p.L(`#itemcards select[data-shell="duration"]`).Count() == 0 &&
		p.L("#itemcards .pill").Count() == 0,
		"the cards have no Start, no group duration and no result badge")
	heads := p.Eval("[...document.querySelectorAll('#itemcards table.crit tr:first-child')].map(r => r.children.length)")
	allTwo := heads.Len() > 0
	for _, n := range heads.Arr() {
		allTwo = allTwo && n.N() == 2
	}
	check(allTwo, "the criteria tables have 项目 / 预期 only, no 结果", heads.String())
	plan := p.L("#rtop [data-fullplan]").Text()
	check(strings.HasPrefix(plan, "将依次跑：") || plan == "下面一项都没勾", "layer 1 says what will run", plan)
}

var readItemRe = regexp.MustCompile(`^(ain\.read|aout\.out)\d$`)

// checkCfgItemNotes: layer 3 of 工装完整测试 says, for every item of every port,
// what it tests, the peer, what a person fills, the criteria and how it runs
// in this sub-test (PORTTOOL-FLOW.md C.3.6). Default configurations, no
// reference board connected.
func checkCfgItemNotes(p *Page) {
	Section("sub-test cards say what, peer, criteria and how each item runs")
	order := []string{"eth", "usb", "sd", "sdram", "din", "ain", "aout", "rs232", "rs485", "can", "dout", "relay", "temp"}
	for _, stage := range []string{"func", "aging", "user"} {
		openSub(p, stage)
		for _, port := range order {
			row := p.L(lport(port))
			ids := p.Eval(`p => itemsOf('`+stage+`', p).filter(it => !/\.cal\d$/.test(it.id)).map(it => it.id)`, port).Strings()
			if len(ids) == 0 {
				continue
			}
			row.Click()
			p.Wait(250)
			// AI / AO readings and outputs carry one note of method and criterion instead (decision 139).
			var reads, missing []string
			for _, i := range ids {
				if readItemRe.MatchString(i) {
					reads = append(reads, i)
				}
			}
			for _, i := range ids {
				if slices.Contains(reads, i) {
					continue
				}
				if strings.TrimSpace(p.L(`#itemcards [data-cfgrun="`+i+`"]`).Text()) == "" ||
					p.L(`#itemcards [data-cfgin="`+i+`"]`).Count() != 1 {
					missing = append(missing, i)
				}
			}
			check(len(missing) == 0, stage+" · "+port+": every item says how it runs here and which sub-tests have it", strings.Join(missing, ", "))
			if len(reads) > 0 {
				note := p.L(`#itemcards [data-cfgananote="` + port + `"]`)
				text := ""
				if note.Count() > 0 {
					text = note.Text()
				}
				check(note.Count() == 1 && strings.Contains(text, "测法") && strings.Contains(text, "判据") && strings.Contains(text, "1 %") &&
					p.L(`#itemcards [data-cfgrun="`+reads[0]+`"]`).Count() == 0,
					stage+" · "+port+": one short note, method and criterion only (decision 139)", cut(text, 200))
			}
			if stage == "user" {
				check(p.L("#itemcards [data-userdemo]").Count() == 1, "user · "+port+": the page says the real board runs the demo")
			}
		}
	}
	run := func(i string) string { return p.L(`#itemcards [data-cfgrun="` + i + `"]`).Text() }

	openSub(p, "user")
	// Decision 139: SDRAM read/write is functional only; the demo has none. The window CRC is gone (decision 141).
	funcOnly := map[string][]string{"sdram": {"sdram.hold"}, "rs232": {}}
	for _, port := range []string{"sdram", "rs232"} {
		same := p.Eval("p => [itemsOf('user', p).map(it => it.id), itemsOf('func', p).map(it => it.id)]", port)
		user, fn := same.I(0).Strings(), same.I(1).Strings()
		var less []string
		for _, i := range fn {
			if !slices.Contains(funcOnly[port], i) {
				less = append(less, i)
			}
		}
		allIn := true
		for _, i := range funcOnly[port] {
			allIn = allIn && slices.Contains(fn, i)
		}
		check(len(user) > 0 && slices.Equal(user, less) && allIn,
			"user-level has the functional test's "+port+" items, less the functional-only ones (decisions 127, 139)", same.String())
	}

	openSub(p, "func")
	p.L(lport("eth")).Click()
	p.Wait(250)
	check(strings.Contains(run("eth.link"), "单次"), "func once: ETH link runs one time", run("eth.link"))
	p.L(lport("usb")).Click()
	p.Wait(250)
	check(p.L(`#itemcards .item[data-item="usb.info"] table.crit tr`).Count() >= 2,
		"USB enumeration has a criterion now (state configured, STG-03)")
	p.L(lport("din")).Click()
	p.Wait(250)
	// Decision 129: outside R&D the AI / AO / DI / DO peer is the reference board, nobody is asked.
	src := p.L(`#itemcards [data-cfgsrc="din"]`)
	srcText := "no source"
	if src.Count() > 0 {
		srcText = src.Text()
	}
	check(src.Count() == 1 && strings.Contains(src.Text(), "标准机") && p.L("#itemcards select[data-cfgsrc]").Count() == 0,
		"DI: the data source is the reference board, with no choice of a person (decision 129)", srcText)
	check(p.L(`#itemcards [data-cfghuman="din.level"]`).Count() == 0 &&
		p.L(`#itemcards [data-cfginfo="din.level"] table.crit tr`).Count() >= 2,
		"DI: nothing for a person to do, the criteria are on the card",
		cut(p.L(`#itemcards [data-cfginfo="din.level"]`).Text(), 300))
	p.L(lport("ain")).Click()
	p.Wait(250)
	// Decision 134: the default wiring is same-number, reference AO1 -> AI1, so AI1 read starts ticked.
	check(p.L(`#itemcards [data-cfgnoauto="ain.read1"]`).Count() == 0 && p.L(`#itemcards input[data-pick="ain.read1"]`).Checked(),
		"AI1 read starts ticked: the default wiring gives it the reference AO1 (decision 134)")
	// Decisions 136, 139: the range is a dropdown; the details stay in a summary that opens to the whole block.
	rs := p.L(`#itemcards [data-rngsum="AI1"]`)
	rsText := "no summary"
	if rs.Count() > 0 {
		rsText = cut(rs.First().Text(), 200)
	}
	check(rs.Count() >= 1 && rs.First().L("select").Count() == 0 && rs.First().L(`[data-rng="AI1"]`).Count() == 1 &&
		p.L(`#itemcards select[data-cfgrange="AI1"]`).Count() == 1,
		"the configuration card has AI1's range dropdown and a summary holding the range block (decisions 136, 139)", rsText)
	check(p.L(`#itemcards [data-cfghuman="ain.read2"]`).Count() == 0 &&
		strings.Count(p.L(`#itemcards [data-cfgananote="ain"]`).Text(), "1 %") >= 1,
		"AI2 read: nobody types anything, the ±1 % criterion is on the card",
		cut(p.L(`#itemcards [data-cfgananote="ain"]`).Text(), 200))
	checkSharedRange(p)
	p.L(lport("can")).Click()
	p.Wait(250)
	check(p.L(`#itemcards select[data-peer="can.peer"]`).Value() == "pc", "CAN defaults to the PC with no reference board connected")
	check(p.L(`#itemcards .item[data-item="can.peer"] table.crit tr`).Count() == 4, "and lists only the PC peer's three criteria")
	p.L(lport("dout")).Click()
	p.Wait(250)
	check(p.L(`#itemcards [data-cfghuman="dout.load"]`).Count() == 0, "DO: nobody measures anything, the reference board reads it (decision 129)")
	p.L(lport("relay")).Click()
	p.Wait(250)
	check(p.L(`#itemcards [data-cfginfo="relay.cycle"]`).Count() == 1, "relays: what, criterion and how it runs under the card")

	openSub(p, "aging")
	p.L(lport("eth")).Click()
	p.Wait(250)
	check(strings.Contains(run("eth.xfer"), "同时跑满") && strings.Contains(run("eth.link"), "在开头跑一遍"),
		"aging: ETH data runs for the time, the link check once", run("eth.xfer"))
	check(!strings.Contains(p.L("#itemcards").Text(), "只测一次"), "aging: no 「只测一次」 heading, badge or hint (decision 151)")
	p.L(lport("aout")).Click()
	p.Wait(250)
	check(strings.Contains(run("aout.load"), "一直输出") && strings.Contains(run("aout.load"), "量一次并判"),
		"aging: the AO load holds its current to the end and is read and judged once then (decision 128)", run("aout.load"))
	crit := p.L(`#itemcards [data-cfginfo="aout.load"] table.crit`)
	critText := ""
	if crit.Count() > 0 {
		critText = crit.Text()
	} else {
		critText = cut(p.L(`#itemcards [data-cfginfo="aout.load"]`).Text(), 200)
	}
	check(crit.Count() == 1 && crit.L("tr").Count() >= 3, "the AO load lists its criteria, one per channel, like AO output", critText)
	p.L(`.mi[data-menu="rd"]`).Click()
}

// checkRdHasCfgItems: R&D is the fullest test type: an item a configuration
// offers is on its R&D card too, with the same parameters (TODO-fixture 82).
func checkRdHasCfgItems(p *Page) {
	Section("R&D has every configuration item, with the same parameters")
	p.L(`.mi[data-menu="rd"]`).Click()
	p.L(lport("aout")).Click()
	p.Wait(500)
	// Decisions 137, 138: in R&D the AO load is part of 实测, output from its one 开始 / 停止.
	blk := p.L(`#itemcards [data-anablk="meas"]`)
	blkText := "no block"
	if blk.Count() > 0 {
		blkText = cut(blk.Text(), 300)
	}
	check(p.L(`#itemcards [data-anablk="load"]`).Count() == 0 && blk.Count() == 1 && blk.L(`button[data-rdout="aout"]`).Count() == 1,
		"the R&D AO card has 带负载 merged into 实测, with one 开始 / 停止 (decisions 137, 138)", blkText)
	for _, pi := range [][2]string{{"din", "din.level"}, {"dout", "dout.load"}} {
		p.L(lport(pi[0])).Click()
		p.Wait(300)
		got := []int{p.L(`#itemcards [data-libparam="` + pi[1] + `.ch"]`).Count(), p.L(`#itemcards [data-libparam="` + pi[1] + `.period"]`).Count()}
		check(got[0] == 0 && got[1] == 1, "the R&D "+pi[1]+" block sets the period; channels are the rows' ticks (decision 137)", show(got))
	}
	p.L(lport("relay")).Click()
	p.Wait(300)
	check(strings.Contains(p.L("#itemcards .relaycard").Text(), "负载反馈的实际开合次数"), "the R&D relay card states its criterion")
}

// checkSharedRange (decision 139): AI2's range set on the functional page is the
// aging page's too, and its test points go back to the new range's defaults.
func checkSharedRange(p *Page) {
	sel := p.L(`#itemcards select[data-cfgrange="AI2"]`)
	selText := "no dropdown"
	if sel.Count() > 0 {
		selText = sel.Value()
	}
	if !check(sel.Count() == 1 && sel.Value() == "i", "AI2's range dropdown starts on current (decision 134)", selText) {
		return
	}
	pts := p.L(`#itemcards input[data-ana-points="ain.read2"]`)
	pts.Fill("4, 12, 18")
	pts.Dispatch("change")
	p.Wait(400)
	sel.Select("v")
	p.Wait(500)
	got := p.L(`#itemcards input[data-ana-points="ain.read2"]`).Value()
	allOver := true
	for _, x := range strings.Split(strings.ReplaceAll(got, " ", ""), ",") {
		if x == "" {
			continue
		}
		f, err := strconv.ParseFloat(x, 64)
		allOver = allOver && err == nil && f > 20
	}
	check(got != "4, 12, 18" && allOver, "changing AI2 to voltage puts its test points back to the voltage defaults", got)
	openSub(p, "aging")
	p.L(lport("ain")).Click()
	p.Wait(300)
	other := p.L(`#itemcards select[data-cfgrange="AI2"]`)
	otherText := "no dropdown"
	if other.Count() > 0 {
		otherText = other.Value()
	}
	check(other.Count() == 1 && other.Value() == "v", "the aging page shows the same AI2 range", otherText)
	other.Select("i")
	p.Wait(500)
	openSub(p, "func")
	p.L(lport("ain")).Click()
	p.Wait(300)
}

// checkConnZone: the connection block and the log column (STG-10;
// PORTTOOL-FLOW.md C.3.6 "连接区").
func checkConnZone(p *Page) {
	Section("connection block")
	check(p.L("#connzone select#ctlport").Count() == 1 && p.L("#connzone #boardclock").Count() == 1,
		"the control port chooser and the board time sit in the connection block")
	p.WaitSel("#connrows .crow", 0)
	keys := attrs(p.L("#connrows .crow"), "data-conn")
	check(eqStrings(keys, "usb", "eth", "rs232", "rs485", "can", "knxusb", "ref", "stlink", "instrument"),
		"USB, network, RS232 / RS485 / CAN, KNX USB interface, reference board, ST-Link, instrument rows", show(keys))
	check(p.L(`.crow[data-conn="instrument"] select`).Disabled(), "the instrument row is reserved and cannot be picked")
	check(p.L(`.crow[data-conn="ref"] button`).Count() >= 1, "the reference board row can be connected from here")

	clock := p.L("#boardclock")
	_ = try(func() {
		p.WaitFunc(`/\d\d:\d\d:\d\d/.test(document.querySelector('#boardclock').textContent)`, nil, 5000)
	})
	first := clock.Text()
	if check(regexp.MustCompile(`\d\d:\d\d:\d\d`).MatchString(first), "the board time shows under the control port", first) {
		t0 := time.Now()
		_ = try(func() {
			p.WaitFunc("t => document.querySelector('#boardclock').textContent !== t", first, 2500)
		})
		second := clock.Text()
		check(second != first, fmt.Sprintf("the board time moves (%.1f s)", time.Since(t0).Seconds()), first+" -> "+second)
	}

	ver := p.L("#toolver").Text()
	check(regexp.MustCompile(`上位机 \d+\.\d+\.\d+`).MatchString(ver), "the header names the panel's own version (decision 112)", ver)
	// The 网口 row finds the board's address without a network test; the
	// simulated board's is this PC's 127.0.0.1 and says so (C.3.6 「网口」).
	ok := try(func() { p.WaitSel(`#connrows .crow[data-conn="eth"] [data-ethip="127.0.0.1"]`, 20000) }) == nil
	eth := p.L(`#connrows .crow[data-conn="eth"]`).Text()
	check(ok && strings.Contains(eth, "模拟板"), "the network row shows the board's address right after connecting", eth)

	log := p.L("#logcol").Box()
	right := log != nil
	for _, s := range []string{"#connzone", "#mid", "#right"} {
		b := p.L(s).Box()
		right = right && b != nil && log.X >= b.X+b.W-1
	}
	check(right, "the log is the rightmost column")
	p.L("#clear").Click()
	p.Wait(100)
	n := p.L("#log > *").Count()
	check(n <= 3, "clearing empties the log", fmt.Sprintf("%d lines left", n))
}
