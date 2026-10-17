package main

// The R&D AI / AO card on the simulated board (decisions 97, 98, 99, 102, 117,
// 137, 138; PORTTOOL-FLOW.md C.3.2, C.3.6 「AI / AO 卡片」). The simulated board
// has no real UID: store and burn are only seen refusing; writing them is
// covered by TestCase/host/porttool_plan/calibration_test.go.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
)

// ai1PinMV is the pin millivolts the simulated AI1 needs to read v terminal mV
// on the voltage range (90.6k / 22.6k divider; C.3.2 "AI 的标称怎么算").
func ai1PinMV(v float64) int { return int(math.RoundToEven(v * 22.6 / 90.6)) }

func anaWaitIdle(p *Page) { p.WaitFunc("!anaCardBusy", nil, 20000) }

func anaDone(p *Page, item string, secs int) string {
	_ = try(func() {
		p.WaitFunc("id => !libData.running && (libData.states[id] || {}).outcome", item, secs*1000)
	})
	return p.Eval("id => (libData.states[id] || {}).outcome || ''", item).S()
}

func anaPick(p *Page, port string) {
	p.L(`.mi[data-menu="rd"]`).Click()
	p.L(lport(port)).Click()
	p.Wait(300)
}

func anaWait(p *Page, js, what, detailJS string, arg any, ms int) bool {
	if ms == 0 {
		ms = 15000
	}
	if try(func() { p.WaitFunc(js, arg, ms) }) == nil {
		return check(true, what)
	}
	return check(false, what, cut(p.Eval(detailJS, arg).String(), 300))
}

func anaType(p *Page, sel, value string) {
	f := p.L(sel)
	f.Fill(value)
	f.Dispatch("change")
	p.Wait(200)
}

// cls is the class list of the first match, "" when there is none.
func cls(p *Page, sel string) string {
	l := p.L(sel)
	if l.Count() == 0 {
		return ""
	}
	return l.First().Attr("class")
}

// anaStarts counts the sessions on port the board started, by its replies in
// the log: the commands the server sends do not appear there.
func anaStarts(p *Page, port string) int {
	return strings.Count(p.L("#log").Text(), "OK started "+port)
}

func checkAnalogCards(p *Page) {
	Section("AI / AO card: calibration table, 实测 on both channels, 开始 / 停止 per block (decisions 117, 137, 138)")
	base := strings.TrimRight(strings.SplitN(p.url, "#", 2)[0], "/")
	sim := func(cmd string) {
		b, _ := json.Marshal(map[string]string{"cmd": cmd})
		c := http.Client{Timeout: 30 * time.Second}
		if r, err := c.Post(base+"/api/command", "application/json", bytes.NewReader(b)); err == nil {
			r.Body.Close()
		} else {
			throw("POST /api/command: %v", err)
		}
		p.Wait(300)
	}

	// ---- AO: one card, by hand (decision 133), 校准 and 实测 blocks
	anaPick(p, "aout")
	card := p.L(`#itemcards [data-anacard="aout"]`)
	if !check(card.Count() == 1, "R&D shows one AO card") {
		return
	}
	check(card.L("select[data-anasrc]").Count() == 0, "R&D has no data-source choice: a person measures")
	check(card.L(`[data-anablk="cal"]`).Count() == 1 && card.L(`[data-anablk="meas"]`).Count() == 1 && card.L(`[data-anablk="load"]`).Count() == 0,
		"the card has a 校准 block and a 实测 block, 带负载 merged into 实测")
	check(card.L("button[data-rdread]").Count() == 0 && card.L(`button[data-rdout="aout"]`).Count() == 1 &&
		strings.TrimSpace(card.L(`[data-anablk="meas"] button[data-rdout="aout"]`).Text()) == "开始" &&
		strings.Contains(cls(p, `#itemcards button[data-rdout="aout"]`), "primary"),
		"the AO card has one 开始 on 实测, in the start colour, and no card-wide 开始读取 (decision 151)")
	check(card.L(`input[data-rdtick^="aout|"]`).Count() == 0, "AO 实测 has no ticks: both channels (decision 151)")
	check(p.Eval("!libRunning()").Truthy(), "opening the AO page outputs nothing")
	if try(func() { p.WaitSel(`select[data-range="AO1"]`, 10000) }) != nil {
		check(false, "the AO card shows the registered range", "no range select")
		return
	}
	sel := p.L(`select[data-range="AO1"]`).First()
	check(sel.Value() == "v", "AO1 starts on the default voltage range (decision 134)", sel.Value())
	// Decision 136: the range block on top of the card shows jumpers, formula and the warning.
	blk := card.L(`[data-anablk="range"] [data-rng="AO1"]`)
	// Decision 137: the details are collapsed; the toggle right of the dropdown opens them.
	if blk.Count() == 1 {
		check(blk.L(".rngtab").Hidden(), "the AO1 range block starts collapsed (decision 137)")
		blk.L(`button[data-rngtoggle="AO1"]`).Click()
	}
	text := ""
	if blk.Count() == 1 {
		text = blk.Text()
	}
	check(strings.Contains(text, "JP1") && strings.Contains(text, "V_DAC") && strings.Contains(text, "2.5 V"),
		"the AO1 range block shows its jumpers, formula and the 2.5 V warning", cut(text, 300))
	sel.Select("i20") // the calibration checks below are in mA
	p.Wait(800)
	text = card.L(`[data-anablk="range"] [data-rng="AO1"]`).Text()
	check(strings.Contains(text, "1024") && strings.Contains(text, "开（电流档）"), "choosing 0-20 mA switches the jumpers and the formula", cut(text, 300))
	var pts []string
	for i := 0; i < 6; i++ {
		pts = append(pts, p.L(fmt.Sprintf(`input[data-anapt="aout.cal1|%d"]`, i)).Value())
	}
	check(eqStrings(pts, "0", "10", "20", "", "", ""), "AO1 calibration: six point rows, the default three filled", show(pts))
	store := card.L(`button[data-ana-store="aout.cal1"]`)
	burn := card.L(`button[data-ana-burn="aout.cal1"]`)
	check(store.Count() == 1 && burn.Count() == 1 && store.Disabled() && burn.Disabled(), "存到本地 and 烧进板子 are there and off before a fit")
	check(strings.Contains(card.L(`[data-anachan="aout.cal1"] [data-ana-why="burn"]`).Text(), "模拟板"), "烧进板子 says why: the simulated board")

	// 输出该点: the board outputs the point, the person types the meter; one point in effect at a time.
	check(strings.TrimSpace(p.L(`button[data-anaset="aout.cal1|0"]`).Text()) == "输出该点" &&
		strings.Contains(cls(p, `button[data-anaset="aout.cal1|0"]`), "act"),
		"the AO point button is 输出该点, in the per-row action colour (decision 151)")
	for i, typed := range []string{"0.05", "10.10", "19.90"} {
		b := fmt.Sprintf(`button[data-anaset="aout.cal1|%d"]`, i)
		p.L(b).Click()
		anaWaitIdle(p)
		check(p.L(b).Attr("data-active") == "1" && strings.Contains(cls(p, b), " on") && p.L(b).Enabled(),
			fmt.Sprintf("point %d's 输出该点 shows it is in effect and can be pressed again", i+1))
		if i > 0 {
			check(p.L(fmt.Sprintf(`button[data-anaset="aout.cal1|%d"]`, i-1)).AttrNil("data-active"), "and the previous point's button is released")
		}
		got := p.L(fmt.Sprintf(`input[data-anagot="aout.cal1|%d"]`, i))
		got.Fill(typed)
		got.Dispatch("change")
		p.Wait(200)
		check(p.L(fmt.Sprintf(`[data-anachan="aout.cal1"] tr[data-row="%d"]`, i)).Attr("data-done") == "1",
			fmt.Sprintf("point %d with its meter reading is marked recorded", i+1))
	}
	// Pressing the point in effect again cancels it: the channel goes back to 0 (decision 151).
	p.L(`button[data-anaset="aout.cal1|2"]`).Click()
	anaWaitIdle(p)
	check(p.L(`button[data-anaset="aout.cal1|2"]`).AttrNil("data-active") && p.Eval("!anaAoOn[1]").Truthy() &&
		strings.Contains(p.L("#log").Text(), "aout mv=1:0"),
		"pressing the point in effect again cancels it: AO1 to 0, nothing in effect")
	row := p.L(`[data-anachan="aout.cal1"] tr[data-row="1"]`).Text()
	check(strings.Contains(row, "%"), "the error column fills in by itself", row)
	// STG-12: the error is marked against ±1 % FS for AO as for AI, as a reference only.
	head := p.L(`[data-anachan="aout.cal1"] table.anapts tr`).First().Text()
	check(strings.Contains(head, "±1 %") && strings.Contains(row, "✅"), "the AO error column is judged against ±1 % FS (10.10 mA: +0.5 %)", head+" | "+row)
	got := p.L(`input[data-anagot="aout.cal1|2"]`)
	got.Fill("19.70")
	got.Dispatch("change")
	p.Wait(200)
	cell := p.L(`[data-anaerr="aout.cal1|2"]`).Text()
	check(strings.Contains(cell, "❌") && strings.Contains(cell, "-1.50"), "19.70 mA at 20 mA is -1.5 % FS: marked over", cell)
	p.L(`button[data-anafit="aout.cal1"]`).Click()
	p.Wait(800)
	box := p.L(`[data-anachan="aout.cal1"]`).Text()
	check(strings.Contains(box, "k = ") && strings.Contains(box, "b = ") && strings.Contains(box, "最小二乘"), "拟合 gives k and b and names the method", tail(box, 300))
	check(p.L(`button[data-ana-store="aout.cal1"]`).Disabled() &&
		strings.Contains(p.L(`[data-anachan="aout.cal1"] [data-ana-why="store"]`).Text(), "模拟板"),
		"after the fit, 存到本地 is still off on the simulated board, saying why")

	// ---- AO 实测 (decisions 137, 138): both channels at their own targets, k, b shown,
	// a target changed while running is output again, the meter typed is judged live
	anaType(p, `input[data-anatarget="aout.out1"]`, "10")
	anaType(p, `input[data-anatarget="aout.out2"]`, "")
	p.L(`#itemcards button[data-rdout="aout"]`).Click()
	p.Wait(500)
	check(!p.Eval("!!rdLive('aout')").B(), "开始 refuses while AO2 has no target")
	anaType(p, `input[data-anatarget="aout.out2"]`, "5")
	p.L(`#itemcards button[data-rdout="aout"]`).Click()
	anaWait(p, "() => { const an = ((rdLive('aout') || {}).analog) || []; const a = an.find(x => x.ch === 1), b = an.find(x => x.ch === 2);"+
		" return !!a && !!b && a.set === 10 && b.set === 5; }",
		"开始 drives AO1 at 10 mA and AO2 at 5 mA, each its own target", "() => libState('aout.manual')", nil, 0)
	check(strings.TrimSpace(p.L(`#itemcards button[data-rdout="aout"]`).Text()) == "停止" &&
		strings.Contains(cls(p, `#itemcards button[data-rdout="aout"]`), "stop"),
		"while it outputs the button is 停止, in the stop colour")
	kb := p.L(`#itemcards [data-anakb="1"]`).Text()
	check(strings.Contains(kb, "k=") && strings.Contains(kb, "b="), "实测 shows the k, b read from the board at the start", kb)
	anaType(p, `input[data-anatarget="aout.out1"]`, "12")
	anaWait(p, "() => { const a = (((rdLive('aout') || {}).analog) || []).find(x => x.ch === 1); return !!a && a.set === 12; }",
		"changing AO1's target to 12 mA while running outputs it at once", "() => libState('aout.manual')", nil, 0)
	aoRow := "() => (document.querySelector('[data-anablk=\"meas\"] tr[data-item=\"aout.out1\"]') || {}).innerText"
	aoCmp := "() => (document.querySelector('[data-anablk=\"meas\"] tr[data-item=\"aout.out1\"] [data-cmp=\"1\"]') || {dataset: {}}).dataset.pass"
	anaType(p, `input[data-anaactual="aout.out1"]`, "12.02")
	anaWait(p, aoCmp+" === '1'", "the meter's 12.02 mA is judged within ±1 % FS as it is typed", aoRow, nil, 0)
	anaType(p, `input[data-anaactual="aout.out1"]`, "12.50")
	anaWait(p, aoCmp+" === '0'", "changing the meter's value to 12.50 mA re-judges at once: +2.5 % FS is over", aoRow, nil, 0)
	anaType(p, `input[data-anaactual="aout.out1"]`, "12.02")
	p.Wait(500)
	p.L(`#itemcards button[data-rdout="aout"]`).Click()
	anaWait(p, "() => { const s = libState('aout.manual') || {}; const c = s.chans || [];"+
		" return !s.running && c.length === 2 && c.find(x => x.ch === 1).outcome === 'PASS'"+
		" && c.find(x => x.ch === 2).outcome === 'STOPPED'; }",
		"停止 records both channels: AO1 passed, AO2 without a meter reading stopped (decision 129)",
		"() => libState('aout.manual')", nil, 0)
	p.L(`select[data-range="AO1"]`).First().Select("v") // back to the default
	p.Wait(800)

	// ---- AI: nothing reads until a block's 开始 (decisions 137, 138)
	n0 := anaStarts(p, "ain")
	anaPick(p, "ain")
	card = p.L(`#itemcards [data-anacard="ain"]`)
	p.Wait(1500)
	n1 := anaStarts(p, "ain")
	check(!p.Eval("!!rdLive('ain')").B() && n1 == n0 && card.L(`[data-rdnote="ain|cal"]`).Count() == 1 && card.L(`[data-rdnote="ain|meas"]`).Count() == 1,
		"opening the AI page sends no pt.start: each block waits for its 开始", fmt.Sprintf("%d -> %d", n0, anaStarts(p, "ain")))
	check(card.L("button[data-rdread]").Count() == 0 &&
		strings.TrimSpace(card.L(`[data-anablk="cal"] button[data-anaread="cal"]`).Text()) == "开始" &&
		strings.TrimSpace(card.L(`[data-anablk="meas"] button[data-anaread="meas"]`).Text()) == "开始",
		"the AI card has a 开始 on 校准 and one on 实测, no card-wide 开始读取 (decision 151)")
	check(card.L(`input[data-rdtick^="ain|"]`).Count() == 0, "AI 实测 has no ticks: both channels (decision 151)")
	check(p.L(`button[data-anaset="ain.cal1|1"]`).Disabled() && strings.TrimSpace(p.L(`button[data-anaset="ain.cal1|1"]`).Text()) == "记录该点" &&
		card.L("[data-calneedread]").Count() == 1,
		"记录该点 is off while the calibration block does not read, saying why")

	// ---- AI read item, point by point (the configurations' path): a person sets the
	// source and types its value; each point lists raw, converted, standard and error
	p.Eval("() => { anaPtsText['ain.read1'] = '2500, 5000, 7500'; libStartOnly(['ain.read1']); }")
	var prev any
	for _, v := range []int{2500, 5000, 7500} { // 3-6 points (PORTTOOL-FLOW.md C.3.6)
		if try(func() {
			p.WaitFunc("id => libData.ask && libData.ask.id !== id && !document.querySelector('#libask').hidden", prev, 15000)
		}) != nil {
			check(false, fmt.Sprintf("the AI1 read asks for the %d mV point", v), cut(p.Eval("libData.states['ain.read1'] || null").String(), 300))
			p.Eval("libStop()")
			break
		}
		prev = p.Eval("libData.ask.id").v
		sim(fmt.Sprintf("sim.ain 1 %d", ai1PinMV(float64(v))))
		p.L(`#libask input[data-ask="value"]`).Fill(fmt.Sprint(v))
		p.L(`#libask [data-ask="ok"]`).Click()
	}
	out := anaDone(p, "ain.read1", 60)
	rows := p.Eval("((libData.states['ain.read1'] || {}).analog || {}).rows || []").Arr()
	good := out == "PASS" && len(rows) == 3
	for _, r := range rows {
		for _, k := range []string{"raw", "shown", "got", "dev"} {
			good = good && r.K(k).IsNum()
		}
		good = good && r.K("pass").Truthy()
	}
	check(good, "AI1 read at 2500, 5000 and 7500 mV passes, each point with raw, converted, standard and error", out+" "+cut(show(rows), 300))

	// ---- each block's 开始 / 停止, one at a time
	p.L(`#itemcards button[data-anaread="meas"]`).Click()
	anaWait(p, "() => !!rdLive('ain') && anaAiBlock === 'meas'", "实测's 开始 starts the AI reading", "() => [libData.running, rdReadWhy]", nil, 0)
	check(strings.TrimSpace(p.L(`#itemcards button[data-anaread="meas"]`).Text()) == "停止" &&
		strings.Contains(cls(p, `#itemcards button[data-anaread="meas"]`), "stop") &&
		strings.TrimSpace(p.L(`#itemcards button[data-anaread="cal"]`).Text()) == "开始",
		"while 实测 reads its button is 停止 in the stop colour, 校准's stays 开始")
	check(p.L(`button[data-anaset="ain.cal1|1"]`).Disabled(), "记录该点 stays off while 实测 reads")
	p.L(`#itemcards button[data-anaread="cal"]`).Click()
	anaWait(p, "() => !!rdLive('ain') && anaAiBlock === 'cal'", "校准's 开始 takes the reading over from 实测",
		"() => [libData.running, anaAiBlock, rdReadWhy]", nil, 0)
	check(strings.TrimSpace(p.L(`#itemcards button[data-anaread="meas"]`).Text()) == "开始" &&
		p.Eval("(libState('ain.live') || {}).outcome !== 'PASS'").Truthy(),
		"and 实测 is stopped")
	p.L(`#itemcards button[data-anaread="cal"]`).Click()
	anaWait(p, "() => !libRunning() && !rdLive('ain')", "校准's 停止 ends the reading", "() => libState('ain.live')", nil, 0)

	// ---- range registration persists per UID on the simulated board
	p.WaitSel(`select[data-range="AI1"]`, 10000)
	opts := p.L(`select[data-range="AI1"]`).First().L("option").EvalAll("os => os.map(o => o.value)").Strings()
	check(eqStrings(opts, "v", "i"), "AI1 offers voltage or current only: an unsoldered board is a bad board (decision 106)", show(opts))
	sel = p.L(`select[data-range="AI1"]`).First()
	check(sel.Value() == "v", "AI1 starts as the product default, voltage", sel.Value())
	sel.Select("i")
	p.Wait(800)
	st := p.Eval("fetch('/api/analog').then(r => r.json())")
	check(st.K("registered").v == true && st.K("channels").K("AI1").K("unit").S() == "mA",
		"registering AI1 as current switches it to mA", st.K("channels").K("AI1").String())
	p.Reload()
	p.WaitSel(".lport", 15000)
	anaPick(p, "ain")
	p.WaitSel(`select[data-range="AI1"]`, 10000)
	sel = p.L(`select[data-range="AI1"]`).First()
	check(sel.Value() == "i", "the registration survives a reload (kept by the board's UID)")
	check(strings.Contains(p.L(`[data-anachan="ain.cal1"]`).Text(), "mA"), "and the AI1 table now speaks mA")
	check(!p.Eval("!!rdLive('ain')").B(), "a reload does not start the reading either")
	p.L(`#itemcards button[data-anaread="cal"]`).Click()
	anaWait(p, "() => !!rdLive('ain')", "校准's 开始 again", "() => [libData.running, rdReadWhy]", nil, 0)
	n0 = anaStarts(p, "ain")
	sel.Select("v") // back to the default for the checks below and later runs
	p.Wait(800)
	anaWait(p, "n => !!rdLive('ain') && document.querySelector('#log').innerText.split('OK started ain').length - 1 > n",
		"a range change while reading starts the reading again", "() => [libData.running, rdReadWhy]", n0, 0)

	// ---- AI calibration point: the source is set, 记录该点 takes the running reading
	sim(fmt.Sprintf("sim.ain 1 %d", ai1PinMV(2500)))
	check(p.L(`button[data-anaset="ain.cal1|1"]`).Enabled() && strings.Contains(cls(p, `button[data-anaset="ain.cal1|1"]`), "act"),
		"记录该点 is on while 校准 reads, in the per-row action colour")
	p.L(`button[data-anaset="ain.cal1|1"]`).Click()
	anaWaitIdle(p)
	tr := p.L(`[data-anachan="ain.cal1"] tr[data-row="1"]`)
	row = tr.Text()
	check(strings.Contains(row, "%") && tr.Attr("data-done") == "1" && tr.L("b.gotval").Count() == 1,
		"记录该点 on AI1's 2500 mV point shows the reading in bold, its error, the row marked recorded (decision 151)", row)
	check(p.L(`[data-anachan="ain.cal1"] tr[data-row="0"]`).AttrNil("data-done"), "a point not recorded is not marked")

	// ---- AI 实测: both channels, k, b shown, the target typed, judged live and again when changed
	p.L(`#itemcards button[data-anaread="meas"]`).Click()
	anaWait(p, "() => !!rdLive('ain') && anaAiBlock === 'meas'", "实测's 开始 takes the reading over from 校准",
		"() => [libData.running, anaAiBlock, rdReadWhy]", nil, 0)
	anaWait(p, "() => /k=.*b=/.test((document.querySelector('[data-anakb=\"1\"]') || {}).innerText || '')",
		"实测 shows AI1's k, b read from the board at the start",
		"() => (document.querySelector('[data-anablk=\"meas\"]') || {}).innerText", nil, 0)
	aiRow := "() => (document.querySelector('[data-anablk=\"meas\"] tr[data-item=\"ain.read1\"]') || {}).innerText"
	aiCmp := "() => (document.querySelector('[data-anablk=\"meas\"] tr[data-item=\"ain.read1\"] [data-cmp=\"1\"]') || {dataset: {}}).dataset.pass"
	anaType(p, `input[data-anatarget="ain.read1"]`, "5000")
	sim(fmt.Sprintf("sim.ain 1 %d", ai1PinMV(5000)))
	anaWait(p, aiCmp+" === '1'", "AI1 at 5000 mV against a 5000 mV target is judged within ±1 % FS as it reads", aiRow, nil, 0)
	anaType(p, `input[data-anatarget="ain.read1"]`, "6000")
	anaWait(p, aiCmp+" === '0'", "changing the target to 6000 mV re-judges at once: -10 % FS is over", aiRow, nil, 0)
	anaType(p, `input[data-anatarget="ain.read1"]`, "5000")
	anaWait(p, aiCmp+" === '1'", "and back to 5000 mV it passes again", aiRow, nil, 0)

	// ---- leaving and coming back (decision 137): leaving stops and records both
	// channels, the round is kept, the values stay, nothing reads until a 开始
	anaPick(p, "aout")
	anaWait(p, "() => { const s = libState('ain.live') || {}; const c = s.chans || []; return !s.running && c.length === 2"+
		" && c.some(x => x.ch === 1 && x.outcome === 'PASS') && c.some(x => x.ch === 2 && x.outcome === 'STOPPED'); }",
		"leaving the AI page stops 实测 and records both channels: AI1 passed, AI2 without a target stopped",
		"() => libState('ain.live')", nil, 0)
	n0 = anaStarts(p, "ain")
	anaPick(p, "ain")
	p.Wait(1000)
	check(!p.Eval("!!rdLive('ain')").B() && anaStarts(p, "ain") == n0, "coming back does not start the reading")
	last := p.L(`#rtop [data-lasttime="ain"]`).Text()
	check(strings.HasPrefix(last, "20") && strings.Contains(p.L(`#rtop [data-lastres="ain"]`).Text(), "AI1"),
		"the AI page shows its last test time and the kept result", last)
	check(p.L(`input[data-anatarget="ain.read1"]`).Value() == "5000", "the target typed is still there")

	// ---- a blank calibration area: not calibrated, no verdict (the item still runs for the configurations)
	sim("sim.cal 0")
	anaPick(p, "aout")
	p.Eval("() => libStartOnly(['aout.out1'])")
	out = anaDone(p, "aout.out1", 60)
	stj := p.Eval("libData.states['aout.out1'] || {}")
	check(out == "ERROR" && stj.K("code").S() == "not_calibrated",
		`with the calibration area erased the AO output is "not calibrated"`, out+" "+cut(stj.String(), 300))
	sim("sim.cal 1")

	// ---- decision 136: a 4-20 mA AO is refused under what code 0 puts out (about 4.86 mA)
	anaPick(p, "aout")
	p.L(`select[data-range="AO1"]`).First().Select("i420")
	p.Wait(800)
	p.Eval("() => { anaPtsText['aout.out1'] = '4, 12, 18'; libStartOnly(['aout.out1']); }")
	// The not-calibrated result above is still there until this run reports.
	_ = try(func() {
		p.WaitFunc("() => ((libData.states['aout.out1'] || {}).code || '') !== 'not_calibrated'", nil, 15000)
	})
	out = anaDone(p, "aout.out1", 60)
	stj = p.Eval("libData.states['aout.out1'] || {}")
	check(out == "ERROR" && stj.K("code").S() == "below_min" && strings.Contains(stj.K("reason").String(), "4.86"),
		"AO1 on 4-20 mA: a 4 mA point is refused, naming the 4.86 mA minimum (decision 136)", out+" "+cut(stj.String(), 300))
	p.L(`select[data-range="AO1"]`).First().Select("v") // back to the default
	p.Wait(800)

	// ---- decision 129: an R&D reading left blank is "not measured to the end":
	// the question refuses a blank answer, so the way out is Stop, and the item is STOPPED.
	// The AO load is not a block of the R&D card (decision 137); its run is started through the page.
	anaPick(p, "aout")
	// Each target in its range's unit: AO1 mV, AO2 mA (STG-38).
	p.Eval("() => { libEdits['aout.load'] = { out1: '1000', out2: '10' }; libStartOnly(['aout.load']); }")
	if try(func() {
		p.WaitFunc("() => libData.ask && !document.querySelector('#libask').hidden", nil, 15000)
		first := p.Eval("libData.ask.id").v
		p.L(`#libask input[data-ask="value"]`).Fill("1000")
		p.L(`#libask [data-ask="ok"]`).Click()
		// AO2's question follows at once: wait for its id, not for the box to close.
		p.WaitFunc("id => libData.ask && libData.ask.id !== id && !document.querySelector('#libask').hidden", first, 15000)
	}) != nil {
		check(false, "the AO load asks for AO1's and AO2's readings",
			p.Eval("[libData.states['aout.load'] || null, itemPeer['rd|aout.load'] || '', "+
				"(document.querySelector('#msgbox') || {}).innerText || '']").String())
		p.Eval("libStop()")
		return
	}
	p.L(`#libask input[data-ask="value"]`).Fill("")
	p.L(`#libask [data-ask="ok"]`).Click()
	p.Wait(800)
	check(p.L("#libask").Visible(), "a blank AO2 reading is not taken: the question stays open")
	p.L(`#libask [data-ask="stop"]`).Click()
	out = anaDone(p, "aout.load", 60)
	code := p.Eval("(libData.states['aout.load'] || {}).code || ''").S()
	check(out == "STOPPED" && code == "stopped",
		"R&D: AO load left without AO2's reading is STOPPED, not an error (decision 129)", out+" "+code)
}
