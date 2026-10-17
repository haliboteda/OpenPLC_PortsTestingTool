package main

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// checkFullTest: 工装完整测试: steps, the calibration gate, the safety card, the
// result. Steps 2 and 4 run from the saved configurations (PORTTOOL-FLOW.md
// C.3.6); step 6 says what it lacks. On the simulated board nothing is
// recorded - the Go tests cover records, progress, continuing, step 6's files
// and the gate on a scripted board.
func checkFullTest(p *Page, com string) {
	sim := isSim(com)
	Section("full fixture test")
	p.L(`.mi[data-menu="full"]`).Click()
	p.WaitSel(`#subtiles input[data-fullstep="5"]`, 0)
	// The tiles draw before /api/full answers, and until then every step looks pickable.
	p.WaitFunc("() => !!(fullData && fullData.steps)", nil, 0)
	step := func(s string) *Loc { return p.L(`#subtiles input[data-fullstep="` + s + `"]`) }
	for _, st := range []string{"2", "4", "5", "6"} {
		if !step(st).Disabled() {
			step(st).SetChecked(false)
		}
	}
	p.Wait(200)
	check(p.L("#shellstart").Disabled() && p.L("#rtop [data-fullplan]").Text() == "下面一项都没勾",
		"with nothing ticked, 「配置完成，开始测试」 cannot be pressed and says why")
	for _, st := range []string{"2", "4"} {
		box := step(st)
		_ = try(func() {
			p.WaitFunc("s => !document.querySelector('#subtiles input[data-fullstep=\"' + s + '\"]').disabled", st, 6000)
		})
		check(!box.Disabled(), "step "+st+" can be picked: it runs its configuration against the reference board",
			cut(p.L("#subtiles").Text(), 300))
	}
	check(step("6").Disabled(), "step 6 cannot be picked here")
	body := p.L("#subtiles").Text()
	why := "stage6"
	if sim {
		why = "模拟板"
	}
	check(strings.Contains(body, why), "and the page says why (simulated board / delivery files missing)", cut(body, 300))

	if sim {
		// Decision 97: no calibration on the board for an AI / AO the step tests.
		sendRaw(p, "sim.cal 0")
		step("2").Check()
		p.Wait(200)
		p.L("#shellstart").Click()
		note := p.L(`#fullbody [data-full="note"]`)
		_ = try(func() { note.WaitFor(8000) })
		text := ""
		if note.Count() > 0 {
			text = note.Text()
		}
		check(strings.Contains(text, "AI1") && strings.Contains(text, "AI2") && strings.Contains(text, "AO2") && strings.Contains(text, "校准值"),
			"with sim.cal 0, step 2 is refused and the uncalibrated channels it tests are listed (AI1 read is on, decision 134)", text)
		gotoRd := p.L(`#fullbody [data-flow="goto_rd"]`)
		check(gotoRd.Count() == 1, "and there is a 去研发测试 button")
		if gotoRd.Count() > 0 {
			gotoRd.Click()
			p.Wait(300)
			cur := p.L(".lport.cur")
			check(strings.Contains(p.L(`.mi[data-menu="rd"]`).Attr("class"), "cur") && cur.Count() == 1 && cur.Attr("data-lport") == "ain",
				"which opens R&D test on the AI port")
		}
		sendRaw(p, "sim.cal 1")
		p.L(`.mi[data-menu="full"]`).Click()
		p.WaitSel(`#subtiles input[data-fullstep="2"]`, 0)
		step("2").Uncheck()
	}

	step("5").Check()
	p.Wait(200)
	check(p.L("#rtop [data-fullplan]").Text() == "将依次跑：安规测试", "layer 1 says what will run", p.L("#rtop [data-fullplan]").Text())
	p.L("#shellstart").Click()
	if try(func() { p.WaitSel(`#fullbody [data-full="safety"]`, 5000) }) != nil {
		check(false, "starting step 5 brings up the safety card")
		return
	}
	check(true, "starting step 5 brings up the safety card")
	st := p.L(`#subtiles [data-substate="5"]`)
	check(st.Count() == 1 && st.Text() == "正在跑" && p.L("#shellstart").Text() == "停止" &&
		p.L(`#subtiles [data-substate="2"]`).Text() == "没勾，跳过" && strings.Contains(p.L("#itemcards").Attr("class"), "locked"),
		"while it runs: its tile says running, the unticked ones say skipped, Start says Stop, the cards are locked",
		cut(strings.ReplaceAll(p.L("#subtiles").Text(), "\n", " "), 300))
	p.L(`#fullbody input[data-full="sn"]`).Fill("SN-T402")
	p.L(`#fullbody button[data-full="submit"]`).Click()
	p.Wait(800)
	note := p.L(`#fullbody [data-full="note"]`)
	nt := ""
	if note.Count() > 0 {
		nt = note.Text()
	}
	check(note.Count() == 1 && strings.Contains(note.Text(), "合格"), "submitting without a verdict is refused", nt)
	check(p.L(`#fullbody input[data-full="sn"]`).Value() == "SN-T402", "and the SN typed survives the redraws")
	p.L(`#fullbody input[data-full="verdict=pass"]`).Check()
	p.L(`#fullbody button[data-full="submit"]`).Click()
	if try(func() { p.WaitSel(`#fullbody [data-full="last"]`, 5000) }) != nil {
		check(false, "the run finishes after Submit")
		return
	}
	res := p.L(`#subtiles [data-fullres="5"]`)
	rt := ""
	if res.Count() > 0 {
		rt = res.Text()
	}
	check(res.Count() == 1 && res.Text() == "通过" &&
		p.Eval("document.querySelector('#subtiles .subtile[data-sub=safety] .stbar i').style.width").S() == "100%",
		"step 5's tile shows 通过 and a full bar", rt)
	last := p.L(`#fullbody [data-full="last"]`).Text()
	check(strings.Contains(last, "全部通过") && strings.Contains(last, "模拟板"),
		"and the result says it passed and that the simulated board gets no record", last)
	check(p.L("#shellstart").Text() == "配置完成，开始测试", "after the run the button is 「配置完成，开始测试」 again")
	row := p.L(`#runres .rr[data-step="1"]`)
	rowText := ""
	if row.Count() > 0 {
		rowText = row.Text()
	} else {
		rowText = cut(p.L("#runres").Text(), 200)
	}
	check(row.Count() == 1 && strings.Contains(row.Text(), "安规测试") && strings.Contains(row.Text(), "问卷") && strings.Contains(row.Text(), "通过"),
		"the questionnaire is step #1 in the results and stays after the run", rowText)
	if sim {
		checkFullTestRelay(p)
		checkFullTestPorts(p)
	}
}

var stampRe = regexp.MustCompile(`^\d\d:\d\d:\d\d\.\d\d\d$`)

// checkRunView: the right column after a functional run (decision 128,
// PORTTOOL-FLOW.md C.3.6 「正在进行、结果、日志」 and 「步号」): every step numbered
// once, the same numbers in the result list, the log and the run's report; a
// result row locates its card and log; the filters.
func checkRunView(p *Page) {
	Section("full fixture test: results, log and step numbers")
	rows := p.L("#runres .rr")
	var nos []int
	for i := 0; i < rows.Count(); i++ {
		n, err := strconv.Atoi(rows.Nth(i).Attr("data-step"))
		if err != nil {
			throw("invalid literal for int(): %v", err)
		}
		nos = append(nos, n)
	}
	inOrder := len(nos) >= 6
	for i, n := range nos {
		inOrder = inOrder && n == i+1
	}
	check(inOrder, "the results number the steps #1, #2, ... in order", show(nos))
	var texts []string
	for i := 0; i < rows.Count(); i++ {
		texts = append(texts, rows.Nth(i).Text())
	}
	var di, diNames []string
	for _, t := range texts {
		if strings.Contains(t, "逐路读电平") {
			di = append(di, t)
			lines := strings.Split(t, "\n")
			if len(lines) < 2 {
				throw("list index out of range: %q", t)
			}
			parts := strings.Split(lines[1], " › ")
			diNames = append(diNames, parts[len(parts)-1])
		}
	}
	// Decision 129 「多路」: DI against the reference board is a step per input.
	check(eqStrings(diNames, "DI1", "DI2", "DI3", "DI4", "DI5", "DI6", "DI7", "DI8"),
		"DI against the reference board is one step per input, DI1-DI8", cut(show(di), 300))
	allPass := true
	for _, t := range texts {
		allPass = allPass && strings.Contains(t, "通过")
	}
	check(allPass, "every step passed", cut(show(texts), 300))
	report := p.Eval("(fullData.last || {}).items || []").Arr()
	same := len(report) == len(nos)
	for i, r := range report {
		no := int(r.K("no").N())
		same = same && i < len(nos) && no == nos[i] && strings.HasPrefix(r.K("where").S(), fmt.Sprintf("#%d ", no))
	}
	check(same, "the run's report numbers the same steps (what a board's record file holds)", cut(show(report), 300))
	lognos := p.Eval("[...new Set(runLines.filter(l => l.kind === 'end').map(l => l.no))]").Arr()
	sameLog := len(lognos) == len(nos)
	for i, n := range lognos {
		sameLog = sameLog && i < len(nos) && n.IsNum() && int(n.N()) == nos[i]
	}
	check(sameLog, "the log has one verdict line per step, under the same numbers", show(lognos))
	dots := p.Eval("[...document.querySelectorAll('.lport .rdot.ok')].map(d => d.dataset.rdot)").Strings()
	lit := true
	for _, d := range []string{"eth", "sd", "din", "temp"} {
		lit = lit && slices.Contains(dots, d)
	}
	check(lit, "the ports that ran have a green light", show(dots))

	// A result row locates its step: the middle shows that port with the card marked, the log only that step.
	di2 := -1
	for i, t := range texts {
		if strings.Contains(t, "逐路读电平") {
			di2 = nos[i]
			break
		}
	}
	if di2 < 0 {
		throw("StopIteration: no DI step in the results")
	}
	p.L(fmt.Sprintf(`#runres .rr[data-step="%d"]`, di2)).Click()
	p.Wait(300)
	check(p.L(".lport.cur").Attr("data-lport") == "din" && p.L("#itemcards [data-hl]").Count() == 1,
		"clicking DI's row opens the DI port with its card marked", p.L(".lport.cur").Attr("data-lport"))
	shown := p.Eval("[...document.querySelectorAll('#runlog .ln')].map(l => Number(l.dataset.no || 0))").Arr()
	only := len(shown) > 0
	for _, n := range shown {
		only = only && int(n.N()) == di2
	}
	check(p.L("#panelog").Visible() && only && p.L("#logchip .chip").Count() == 1,
		"and the log tab shows only that step, with a chip to clear it", show(shown))
	stamp := p.L("#runlog .ln .tm").First().Text()
	check(stampRe.MatchString(stamp), "log times carry milliseconds", stamp)
	p.L("#logchip .chip").Click()
	p.Wait(200)
	nAll := p.L("#runlog .ln").Count()
	check(nAll > len(shown) && p.L("#logchip .chip").Count() == 0, "clearing the chip shows every step again")
	head := p.L("#runlog .ln").First().Text()
	check(strings.Contains(head, "模拟板 UID"), "the log opens with the board's UID", head)
	tl := p.L("#runlog .ln").Last().Text()
	check(strings.Contains(tl, "全部跑完") && strings.Contains(tl, "没跑"), "and ends with the totals and where the record went", tl)
	check(p.L("#runlog .ln.raw").Count() == 0, "protocol lines are hidden by default")
	p.L("#lograw").Check()
	p.Wait(200)
	raw := p.L("#runlog .ln.raw")
	sent := int(p.Eval("[...document.querySelectorAll('#runlog .ln.raw')].map(l => l.textContent).filter(t => t.includes('→ pt.start')).length").N())
	rawFirst := ""
	if raw.Count() > 0 {
		rawFirst = raw.First().Text()
	}
	check(raw.Count() > 0 && sent > 0, "「显示协议原文」 shows them, the commands sent included", rawFirst)
	p.L("#lograw").Uncheck()
	p.L("#logbad").Check()
	p.Wait(200)
	lv := p.Eval("[...document.querySelectorAll('#runlog .ln')].map(l => l.dataset.lv)").Strings()
	onlyBad := true
	for _, x := range lv {
		onlyBad = onlyBad && (x == "fail" || x == "timeout")
	}
	check(onlyBad, "「只看不过 / 超时」 leaves only failed and timed-out lines", cut(show(lv), 200))
	p.L("#logbad").Uncheck()
	p.L("#tabres").Click()
	p.L("#resbad").Check()
	p.Wait(200)
	check(p.L("#runres .rr").Count() == 0, "「只看不过」 on an all-pass run lists nothing")
	p.L("#resbad").Uncheck()
}

const putStageCfg = "async c => { const j = await api('/api/stagecfg', {stage: 'func', cfg: c}); stageCfgs = j; return j.error || null; }"

// funcCfgWith copies the saved functional configuration, every port but keep switched off.
func funcCfgWith(p *Page, keep ...string) (kept J, cfg map[string]any) {
	kept = p.Eval("async () => (await api('/api/stagecfg')).func")
	b, _ := json.Marshal(kept.v)
	_ = json.Unmarshal(b, &cfg)
	if cfg == nil {
		cfg = map[string]any{}
	}
	off := map[string]any{}
	for _, port := range p.Eval("catalog.ports.map(p => p.port)").Strings() {
		if !slices.Contains(keep, port) {
			off[port] = true
		}
	}
	cfg["ports_off"] = off
	return kept, cfg
}

// checkFullTestRelay: the full test runs DO under load and then the relays from
// the functional configuration on the DO -> relay -> reference DI chain and
// asks nobody (decisions 123, 129, 130, 131). Only the DO and relay ports are
// ticked to keep it short; the configuration is put back afterwards. simboard
// plays the chain, so no real contact, bounce or load is seen.
func checkFullTestRelay(p *Page) {
	Section("full fixture test: DO and relays on the chain from the functional configuration")
	kept, cfg := funcCfgWith(p, "dout", "relay")
	items, _ := cfg["items"].(map[string]any)
	if items == nil {
		items = map[string]any{}
		cfg["items"] = items
	}
	items["relay.cycle"] = map[string]any{"on": true, "params": map[string]any{
		"ch": "1,2", "close": "1000", "open": "1000", "count": "1:2,2:1", "order": "par", "by": "gap"}}
	cfg["duration"] = "once"
	errv := p.Eval(putStageCfg, cfg)
	if !check(errv.Nil(), "a functional configuration with only the relays is saved", errv.String()) {
		return
	}
	defer p.Eval(putStageCfg, kept.v)
	if err := try(func() {
		p.L(`.mi[data-menu="full"]`).Click()
		p.WaitSel(`#subtiles input[data-fullstep="2"]`, 0)
		for _, st := range []string{"2", "4", "5"} {
			p.L(`#subtiles input[data-fullstep="` + st + `"]`).SetChecked(st == "2")
		}
		p.Wait(200)
		p.L("#shellstart").Click()
		restart := p.L(`#fullbody [data-full="restart"]`)
		p.Wait(500)
		if restart.Count() > 0 {
			restart.Click()
		}
		p.WaitFunc("() => fullData && !fullData.running && document.querySelector('#fullbody [data-full=\"last\"]')", nil, 30000)
		p.Eval("libRefresh()")
		p.Wait(500)
		st := p.Eval("(libData.states || {})['relay.cycle'] || {}")
		var seen []float64
		for _, c := range st.K("relay").K("chans").Arr() {
			seen = append(seen, c.K("seen").N())
		}
		check(st.K("outcome").S() == "PASS" && slices.Equal(seen, []float64{2, 1}) &&
			(p.L("#libask").Count() == 0 || p.L("#libask").Hidden()),
			"the fixture test asks nobody; the reference DI counted each relay's cycles (decision 133)", cut(st.String(), 300))
		do := p.Eval("(libData.states || {})['dout.load'] || {}")
		check(do.K("outcome").S() == "PASS" && !do.K("code").Truthy(),
			"DO under load passes on the chain, DO7 and DO8 straight to the reference DI (STG-36 A)", cut(do.String(), 300))
		// Decision 129 「多路」: one numbered result row per DO and per relay.
		p.L("#tabres").Click()
		p.Wait(300)
		rows := p.Eval("[...document.querySelectorAll('#runres .rr')].map(r => r.innerText)").Strings()
		var names []string
		for i := 1; i <= 8; i++ {
			names = append(names, fmt.Sprintf("DO%d", i))
		}
		names = append(names, "RY1", "RY2")
		per := 0
		for _, n := range names {
			hits := 0
			for _, r := range rows {
				lines := strings.Split(r, "\n")
				if len(lines) > 1 && strings.HasSuffix(lines[1], "› "+n) {
					hits++
				}
			}
			if hits == 1 {
				per++
			}
		}
		check(per == 10, "the results list a row of its own for DO1-DO8 and RY1, RY2", cut(strings.Join(rows, " | "), 400))
		res := p.L(`#subtiles [data-fullres="2"]`)
		rt := ""
		if res.Count() > 0 {
			rt = res.Text()
		} else {
			rt = cut(p.L("#fullbody").Text(), 300)
		}
		check(res.Count() == 1 && res.Text() == "通过", "and the functional step passes", rt)
	}); err != nil {
		check(false, "the full test runs the configured relays", err.Error())
	}
}

// checkFullTestPorts: the functional sub-test end to end on the simulated board
// for ETH link, USB enumeration, SD, SDRAM, temperature and DI, DI driven by
// the second simulated board as the reference, nobody asked (decision 129).
// The configuration is put back afterwards.
func checkFullTestPorts(p *Page) {
	Section("full fixture test: functional sub-test with the reference board, nobody asked")
	kept, cfg := funcCfgWith(p, "eth", "usb", "sd", "sdram", "din", "temp")
	cfg["items"] = map[string]any{"eth.xfer": map[string]any{"on": false}, "usb.info": map[string]any{"on": true},
		"usb.xfer": map[string]any{"on": false}, "din.level": map[string]any{"on": true, "params": map[string]any{"ch": "1,2"}}}
	cfg["duration"] = "once"
	errv := p.Eval(putStageCfg, cfg)
	if !check(errv.Nil(), "a functional configuration with those ports is saved", errv.String()) {
		return
	}
	defer p.Eval(putStageCfg, kept.v)
	if err := try(func() {
		p.L(`.mi[data-menu="full"]`).Click()
		p.WaitSel(`#subtiles input[data-fullstep="2"]`, 0)
		for _, st := range []string{"2", "4", "5"} {
			p.L(`#subtiles input[data-fullstep="` + st + `"]`).SetChecked(st == "2")
		}
		p.Wait(200)
		p.Eval("c => api('/api/command', {cmd: c})", "sim.din 0x00")
		p.L("#shellstart").Click()
		p.Wait(500)
		restart := p.L(`#fullbody [data-full="restart"]`)
		if restart.Count() > 0 {
			restart.Click()
		}
		heights := map[float64]bool{}
		p.WaitFunc("() => fullData && fullData.running", nil, 0)
		heights[math.RoundToEven(p.L("#runstat").Box().H)] = true
		p.WaitFunc("() => fullData && !fullData.running && document.querySelector('#fullbody [data-full=\"last\"]')", nil, 120000)
		res := p.L(`#subtiles [data-fullres="2"]`)
		rt := cut(p.L("#fullbody").Text(), 300)
		if res.Count() > 0 {
			rt = res.Text() + " " + rt
		}
		check(res.Count() == 1 && res.Text() == "通过",
			"ETH link, USB enumeration, SD, SDRAM, temperature and DI (reference board) pass", rt)
		check(p.Eval("runLines.filter(l => l.kind === 'ask').length").N() == 0, "nobody was asked anything during the run (decision 129)")
		heights[math.RoundToEven(p.L("#runstat").Box().H)] = true
		check(len(heights) == 1, "「正在进行」 keeps one height during and after the run", show(sortedKeys(func() map[string]bool {
			m := map[string]bool{}
			for h := range heights {
				m[strconv.FormatFloat(h, 'f', -1, 64)] = true
			}
			return m
		}())))
		checkRunView(p)
	}); err != nil {
		check(false, "the full test runs the functional configuration", err.Error())
	}
}
