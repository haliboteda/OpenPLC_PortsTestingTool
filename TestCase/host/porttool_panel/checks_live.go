package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// waitJS checks that js turns truthy within 10 s; on failure the detail is detailJS's value.
func waitJS(p *Page, js string, arg any, what, detailJS string) bool {
	if try(func() { p.WaitFunc(js, arg, 10000) }) == nil {
		return check(true, what)
	}
	return check(false, what, p.Eval(detailJS, arg).String())
}

// sendRaw sends one line from the log column's raw-command box; on the full
// test's page, where that column gives way to the run status (decision 128),
// the same request the box makes.
func sendRaw(p *Page, cmd string) {
	if p.L("#raw").Visible() {
		p.L("#raw").Fill(cmd)
		p.L("#send").Click()
	} else {
		p.Eval("c => run(c)", cmd)
	}
	p.Wait(500)
}

func trimmed(ss []string) []string {
	var out []string
	for _, s := range ss {
		out = append(out, strings.TrimSpace(s))
	}
	return out
}

// checkDoutBlink: the R&D DO card (decision 137): no 开始, each table one 输出 /
// 停止输出 for its ticked rows. PWM duty is checked on the page; the toggle count
// stops by itself after the plan's toggles and is judged by the count typed
// (STG-33). The simulated board has no LEDs: the count typed is the board's own.
func checkDoutBlink(p *Page) {
	Section("DO card: ticked rows, one 输出 per table, no 开始 (decision 137)")
	p.L(`.mi[data-menu="rd"]`).Click()
	p.L(lport("dout")).Click()
	p.Wait(300)
	card := p.L(`#itemcards [data-diocard="dout"]`)
	check(card.L("select[data-diosrc]").Count() == 0, "the DO card has no data source")
	check(!slices.Contains(trimmed(card.L("button").AllTexts()), "开始"), "the DO card has no 开始 (decision 137)")
	allOut := true
	for _, b := range []string{"drop", "blink", "pwm"} {
		allOut = allOut && card.L(`button[data-rdout="dout:`+b+`"]`).Count() == 1
	}
	check(allOut && card.L(`[data-dioblk="drop"] input[data-rdtick="drop|1"]`).Count() == 1,
		"each DO table has a tick per row and one 输出 on top")
	check(p.Eval("!libRunning()").Truthy(), "opening the DO page outputs nothing")
	// PWM: several outputs, each with its own frequency and duty, checked on the page.
	p.L(`#itemcards input[data-diopwm="2"]`).SetChecked(true)
	p.L(`#itemcards input[data-diopwmduty="2"]`).Fill("300")
	p.L(`#itemcards input[data-diopwmduty="2"]`).Dispatch("change")
	p.Wait(200)
	// The cell itself goes red with the range under it and is not kept (C.3.6 「填数超量程」).
	duty := p.L(`#itemcards input[data-diopwmduty="2"]`)
	hint := p.L(`#itemcards [data-dioblk="pwm"] tr[data-ch="2"] .cellhint:not([hidden])`)
	ht := "no red cell"
	if hint.Count() > 0 {
		ht = hint.Text()
	}
	check(strings.Contains(duty.Attr("class"), "badcell") && hint.Count() == 1 && strings.Contains(hint.Text(), "255"),
		"a duty above 255 at 8 bits is refused on the page before anything is sent", ht)
	p.L(`#itemcards input[data-diopwmduty="2"]`).Fill("64")
	p.L(`#itemcards input[data-diopwmduty="2"]`).Dispatch("change")
	p.Wait(200)
	check(!strings.Contains(p.L(`#itemcards input[data-diopwmduty="2"]`).Attr("class"), "badcell") &&
		p.L("#itemcards [data-diopwmbad]").Count() == 0, "and accepted once it fits")
	// Toggle count: only the ticked DO1 blinks, then stops by itself.
	p.L(`#itemcards input[data-rdtick="blink|1"]`).SetChecked(true)
	// Which outputs the board reports on while it blinks (its frames, not the command).
	p.Eval("() => { window.__doSeen = new Set(); window.__doT = setInterval(() => { const lv = rdLive('dout');" +
		" if (lv && lv.mode === 'blink' && lv.duty) { lv.duty.forEach((d, i) => { if (d > 0) { __doSeen.add(i + 1); } }); } }, 20); }")
	p.L(`#itemcards button[data-rdout="dout:blink"]`).Click()
	if !waitJS(p, "() => { const lv = rdLive('dout'); return !!lv && lv.blink_of > 0 && lv.blink >= lv.blink_of; }", nil,
		"the ticked DO toggles the plan's count and stops by itself", "() => libState('dout.manual')") {
		p.Eval("libStop()")
		return
	}
	n := int(p.Eval("rdLive('dout').blink_of").N())
	seen := p.Eval("() => { clearInterval(__doT); return [...__doSeen]; }")
	check(seen.Len() == 1 && seen.I(0).N() == 1, "only DO1 was asked to blink", seen.String())
	v := p.L(`#itemcards input[data-diostd="blink|1"]`)
	v.Fill(strconv.Itoa(n))
	v.Dispatch("change")
	waitJS(p, "() => (document.querySelector('[data-dioblk=\"blink\"] [data-cmp=\"1\"]') || {dataset: {}}).dataset.pass === '1'", nil,
		fmt.Sprintf("the %d toggles typed are judged right as they are typed", n), "() => libState('dout.manual')")
	p.L(`#itemcards button[data-rdout="dout:blink"]`).Click()
	waitJS(p, "() => { const s = libState('dout.blink') || {}; return !libRunning() && (s.chans || []).length === 1"+
		" && s.chans[0].ch === 1 && s.chans[0].outcome === 'PASS'; }", nil,
		"停止输出 records only the ticked DO1 on the toggle count, passed", "() => libState('dout.blink')")
}

// checkLiveCards: the R&D DI and DO cards (decisions 108, 137; PORTTOOL-FLOW.md
// C.3.6 「研发四张卡的会话」「离开端口页」): opening the DI page sends no pt.start,
// 开始读取 starts the reading, lamps and counts follow the inputs live, all eight
// levels and the ticked encoder groups are judged as values are typed and
// recorded on 停止读取 or on leaving (decision 151); coming back shows the last
// test time, the kept result and the typed values and reads nothing. DO outputs
// only the ticked rows on 输出. The person's signals are sim.din / sim.quad, so
// this proves the card and the round trip, not real 24 V inputs or a meter.
func checkLiveCards(p *Page, com string) {
	Section("R&D DI / DO cards: 开始读取 / 停止读取, ticked rows, leave and come back")
	if !isSim(com) {
		check(true, "not on a simulated board - the inputs need a person, checked by hand")
		return
	}

	p.L(`.mi[data-menu="rd"]`).Click()
	dinStarts := func() int { return strings.Count(p.L("#log").Text(), "> pt.start din") }
	n0 := dinStarts()
	p.L(lport("din")).Click()
	card := p.L(`#itemcards [data-diocard="din"]`)
	p.Wait(1500)
	n1 := dinStarts()
	check(!p.Eval("!!rdLive('din')").B() && n1 == n0 && card.L(`[data-rdnote="din"]`).Count() == 1,
		"opening the DI page sends no pt.start: it waits for 开始读取 (decision 137)", fmt.Sprintf("%d -> %d", n0, dinStarts()))
	card.L(`button[data-rdread="din"]`).Click()
	waitJS(p, "() => !!rdLive('din')", nil, "开始读取 starts the DI reading", "() => [libData.running, rdReadWhy]")
	waitJS(p, `n => (document.querySelector('#log').innerText.match(/> pt\.start din /g) || []).length > n`, n0,
		"the log shows the pt.start the panel sent, word for word (C.3.6 「研发测试」)",
		"() => document.querySelector('#log').innerText.slice(-300)")
	check(strings.TrimSpace(card.L(`button[data-rdread="din"]`).Text()) == "停止读取", "while it reads the button is 停止读取")
	check(!slices.Contains(trimmed(card.L("button").AllTexts()), "开始") && card.L(`[data-libparam="din.level.ch"]`).Count() == 0,
		"the DI card has no 开始 and no channel parameter: the rows are ticked")
	lamp := "c => (document.querySelector('[data-dioblk=\"level\"] [data-di=\"' + c + '\"]') || {dataset: {}}).dataset.on"
	sendRaw(p, "sim.din 0x05")
	waitJS(p, "() => ("+lamp+")(1) === '1' && ("+lamp+")(2) === '0'", nil, "the lamps follow the inputs live", "() => (rdLive('din') || {}).bits")
	cmp := "([b, c]) => (document.querySelector('[data-dioblk=\"' + b + '\"] [data-cmp=\"' + c + '\"]') || {dataset: {}}).dataset.pass"
	check(card.L(`input[data-rdtick^="level|"]`).Count() == 0 && card.L(`[data-dioblk="level"] tr[data-ch]`).Count() == 8,
		"逐路电平 has no ticks: all eight rows are read (decision 151 item 4)")
	quad := card.L(`[data-dioblk="quad"]`)
	check(strings.Contains(quad.Text(), "编码器计数") && !strings.Contains(card.Text(), "正交") &&
		quad.L("[data-quadwhat]").Count() == 1 && strings.Contains(quad.L("[data-quadwhat]").Text(), "DI1+DI2"),
		"the block is called 编码器计数 and says two neighbouring DI form one A / B pair (decision 151 item 4)")
	for _, ch := range []int{1, 2} {
		p.L(fmt.Sprintf(`#itemcards select[data-diostd="level|%d"]`, ch)).Select("1")
	}
	waitJS(p, "() => ("+cmp+")(['level', 1]) === '1' && ("+cmp+")(['level', 2]) === '0'", nil,
		"DI1 high matches the standard high, DI2 low does not, marked as chosen", "() => (rdLive('din') || {}).blocks")

	counts := "() => [...document.querySelectorAll('[data-dioblk=\"quad\"] tr[data-group]')].map(r => r.children[3].textContent).join(',')"
	// sim.quad steps every pair from A = B = 0, so the inputs start there.
	sendRaw(p, "sim.din 0x00")
	p.Wait(500)
	p.L(`#itemcards button[data-livereset="din.quad"]`).Click()
	waitJS(p, "() => ("+counts+")() === '0,0,0,0'", nil, "清零 puts every count back to 0", counts)
	// 27 steps end each pair on A high, B low: DI1 high, DI2 low again for the level
	// rows judged at 停止读取, with no jump the decoder would count as an error.
	sendRaw(p, "sim.quad 27")
	waitJS(p, "() => ("+counts+")() === '27,27,27,27'", nil, "27 steps sent count 27 in every group", counts)
	p.L(`#itemcards input[data-rdtick="quad|1"]`).SetChecked(true)
	ref := p.L(`#itemcards [data-liveref="din.quad|1"]`)
	ref.Fill("27")
	ref.Dispatch("change")
	waitJS(p, "() => ("+cmp+")(['quad', 1]) === '1'", nil, "the 27 steps typed for group 1 match", "() => rdLive('din')")

	// 停止读取 records all eight levels (no standard: stopped) and the ticked groups.
	card.L(`button[data-rdread="din"]`).Click()
	got := "() => [libState('din.level'), libState('din.quad')].map(s => ((s || {}).chans || []).map(c => c.name + ':' + c.outcome).join(',')).join(' | ')"
	var stopped []string
	for c := 3; c <= 8; c++ {
		stopped = append(stopped, fmt.Sprintf("DI%d:STOPPED", c))
	}
	want := "DI1:PASS,DI2:FAIL," + strings.Join(stopped, ",") + " | DI1/DI2:PASS"
	waitJS(p, "w => !libRunning() && ("+got+")() === w", want,
		"停止读取 records DI1 passed, DI2 failed, DI3-8 stopped (no standard) and the ticked group 1 passed", got)
	p.L(lport("temp")).Click()
	p.Wait(300)
	n0 = dinStarts()
	p.L(lport("din")).Click()
	p.Wait(1000)
	check(!p.Eval("!!rdLive('din')").B() && dinStarts() == n0, "coming back does not start the reading")
	last := p.L(`#rtop [data-lasttime="din"]`).Text()
	res := p.L(`#rtop [data-lastres="din"]`)
	check(strings.HasPrefix(last, "20") && res.Count() == 1 && strings.Contains(res.Text(), "DI2"),
		"the DI page shows its last test time and the kept results", last)
	check(p.L("#rtop h2").Count() == 1 && p.L("#itemcards [data-lasttest]").Count() == 0,
		"the last test sits in the one header block with the port name, not above the cards (decision 139)")
	keys := p.Eval("async () => { const m = await api('/api/memo'); return [rdUid, Object.keys(m.rd || {}).filter(k => k.startsWith('snap|'))]; }")
	uid, snaps := keys.I(0), keys.I(1).Strings()
	check(uid.Truthy() && slices.Contains(snaps, "snap|"+uid.S()+"|din") && !slices.Contains(snaps, "snap|din"),
		"the kept round is filed under the board's UID (snap|<UID>|din, STG-39)", keys.String())
	check(p.L(`#itemcards select[data-diostd="level|1"]`).Value() == "1" && p.L(`#itemcards input[data-rdtick="quad|1"]`).Checked() &&
		p.L(`#itemcards [data-liveref="din.quad|1"]`).Value() == "27",
		"the standard values, ticks and typed steps are still there")
	sendRaw(p, "sim.din 0xFF")
	card.L(`button[data-rdread="din"]`).Click()
	waitJS(p, "() => !!rdLive('din')", nil, "开始读取 once more", "() => [libData.running, rdReadWhy]")
	for _, ch := range []int{1, 2} {
		p.L(fmt.Sprintf(`#itemcards select[data-diostd="level|%d"]`, ch)).Select("")
	}

	// DO: 输出 turns on only the ticked rows; 停止输出 records them.
	p.L(lport("dout")).Click()
	waitJS(p, "() => !libRunning()", nil, "leaving DI for DO ends the reading; DO outputs nothing yet", "() => libData.states")
	lv8 := "() => ((libState('din.level') || {}).chans || []).map(c => c.name + ':' + c.outcome).join(',')"
	var all8 []string
	for c := 1; c <= 8; c++ {
		all8 = append(all8, fmt.Sprintf("DI%d:STOPPED", c))
	}
	waitJS(p, "w => ("+lv8+")() === w", strings.Join(all8, ","),
		"no standard chosen on any DI: leaving still records all eight stopped (STG-40 row 1)", lv8)
	p.L(`#itemcards input[data-rdtick="drop|3"]`).SetChecked(true)
	p.L(`#itemcards button[data-rdout="dout:drop"]`).Click()
	waitJS(p, "() => { const lv = rdLive('dout'); return !!lv && !!lv.duty && lv.duty[2] > 0 && lv.duty[3] === 0 && lv.duty[0] === 0; }", nil,
		"输出 on 带负载 turns on only the ticked DO3", "() => (rdLive('dout') || {}).duty")
	duty := p.Eval("(rdLive('dout') || {}).duty || []").Arr()
	only3 := len(duty) == 8
	for i, d := range duty {
		only3 = only3 && ((d.N() > 0) == (i == 2))
	}
	check(only3, "the board is told the eight duties in one line", show(duty))
	check(strings.TrimSpace(p.L(`#itemcards button[data-rdout="dout:drop"]`).Text()) == "停止输出" &&
		p.L(`#itemcards button[data-rdout="dout:pwm"]`).Disabled(),
		"the button turns to 停止输出 and the other tables wait")
	heads := func(b string) string { return strings.Join(p.L(`#itemcards [data-dioblk="`+b+`"] th`).AllTexts(), " ") }
	check(strings.Contains(heads("drop"), "测量值") && !strings.Contains(heads("drop"), "标准值") &&
		strings.Contains(heads("blink"), "实际次数") && !strings.Contains(heads("blink"), "标准值"),
		"带负载 asks for 测量值, 翻转计数 for 实际次数 (decision 151 item 9)", heads("drop")+" / "+heads("blink"))
	v := p.L(`#itemcards input[data-diostd="load|3"]`)
	v.Fill("0.5")
	v.Dispatch("change")
	waitJS(p, "() => ("+cmp+")(['drop', 3]) === '1'", nil, "a 0.5 V drop is within the criterion", "() => (rdLive('dout') || {}).blocks")
	// Ticking DO4 while it outputs turns DO4 on at once; left blank, it is not judged.
	p.L(`#itemcards input[data-rdtick="drop|4"]`).SetChecked(true)
	waitJS(p, "() => { const lv = rdLive('dout'); return !!lv && !!lv.duty && lv.duty[3] > 0 && lv.duty[4] === 0; }", nil,
		"ticking DO4 during 输出 turns it on too: only ticked rows output (decision 151 item 9)", "() => (rdLive('dout') || {}).duty")
	p.L(`#itemcards button[data-rdout="dout:drop"]`).Click()
	waitJS(p, "() => { const s = libState('dout.load') || {}; return !libRunning() && (s.chans || []).map(c => c.name + ':' + c.outcome).join(',') === 'DO3:PASS'; }",
		nil, "停止输出 ends the output and records DO3 on DO 带负载; DO4 had no 测量值 and is not judged", "() => libState('dout.load')")
	p.L(`#itemcards input[data-rdtick="drop|4"]`).SetChecked(false)
}
