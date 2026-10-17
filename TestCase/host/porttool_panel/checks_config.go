package main

import (
	"fmt"
	"strings"
)

// Set once checkStageConfigs has changed the configurations; the restart check
// then looks for exactly those changes.
var stageCfgChanged bool

// setRefAI1 sets the reference board's AI1 range (decision 134). The board under
// test is on the configurations' shared ranges (decision 139, AO1 voltage by
// default): with the default same-number wiring "i" mismatches AO1 -> AI1, "v" matches.
func setRefAI1(p *Page, rng string) J {
	j := p.Eval("async r => { const st = await api('/api/refboard');"+
		" return api('/api/refboard', {wiring: st.wiring, ranges: {AI1: r}}); }", rng)
	p.Eval("loadStageCfgs()")
	p.Wait(500)
	return j
}

// checkRefPage: the reference board's settings page (decision 135): opened from
// the connection area, analog and digital rows, no range match against the
// board under test (decision 140), the impact worked out again as a setting
// changes, refused when a reference DI is wired twice, closed without saving.
func checkRefPage(p *Page) {
	Section("reference board settings page")
	if try(func() { p.WaitSel("#connrows [data-refopen]", 8000) }) != nil {
		check(false, "the reference board row has 「设置标准机」")
		return
	}
	p.L("#connrows [data-refopen]").Click()
	if try(func() { p.WaitSel("#refpage [data-refimpact] tr[data-impact]", 8000) }) != nil {
		msg := ""
		if p.L("#msgbox").Count() > 0 {
			msg = p.L("#msgbox").Text()
		}
		check(false, "the settings page opens", msg)
		return
	}
	check(p.L("#refpage [data-refana]").Count() == 4 && p.L("#refpage [data-refdig]").Count() == 16,
		"four analog rows (AO1, AO2, AI1, AI2) and sixteen digital rows (DO1-8, DI1-8)")
	// Decision 135 「状态」: refreshed every second while nothing runs.
	if try(func() {
		p.WaitFunc("() => { const d = document.querySelector('#refpage [data-refnow=\"DO1\"]');"+
			" const t = document.querySelector('#refpage [data-reftemp]');"+
			" return d && ['高', '低'].some(x => d.textContent.startsWith(x)) && t && t.textContent.includes('°C'); }", nil, 6000)
	}) == nil {
		check(true, "the reference DI levels and temperatures show and refresh while nothing runs")
	} else {
		cell := "no cell"
		if p.L(`#refpage [data-refnow="DO1"]`).Count() > 0 {
			cell = p.L(`#refpage [data-refnow="DO1"]`).Text()
		}
		check(false, "the reference DI levels and temperatures show and refresh while nothing runs", cell)
	}
	// Decision 136: a row opens its range block; each analog row has a state.
	var states []int
	for _, c := range []string{"AO1", "AO2", "AI1", "AI2"} {
		states = append(states, p.L(`#refpage [data-refstate="`+c+`"]`).Count())
	}
	check(fmt.Sprint(states) == "[1 1 1 1]", "every analog row has a state", show(states))
	// Decision 140: the page leaves the board under test's range to the wiring.
	heads := p.L(`#refpage tr[data-refana="AO1"]`).L("xpath=..").L("tr").First().L("th").Count()
	ai1 := p.L(`#refpage [data-refstate="AI1"]`).Text()
	out1 := p.Eval("() => (document.querySelector('#refpage tr[data-impact=\"aout.out1|AO1\"]') || {}).innerText || ''").S()
	hint := p.L("#refpage [data-refana]").First().L("xpath=ancestor::div[contains(@class,'rpcard')]").Text()
	check(heads == 5 && !strings.Contains(ai1, "档位不匹配") && !strings.Contains(out1, "档位不匹配") && strings.Contains(hint, "同是电压档或同是电流档"),
		"the analog table has no range of the board under test and no 档位不匹配, only the hint to wire both alike (decision 140)",
		fmt.Sprintf("%d columns; AI1: %s; AO1 impact: %s", heads, ai1, out1))
	p.L(`#refpage tr[data-refana="AO1"] td`).First().Click()
	p.Wait(300)
	xr := p.L(`#refpage tr[data-refrange="AO1"]`)
	if xr.Count() == 1 {
		xr.L(`button[data-rngtoggle="AO1"]`).Click() // details are collapsed (decision 137)
	}
	xrText := "not opened"
	if xr.Count() > 0 {
		xrText = cut(xr.Text(), 200)
	}
	check(xr.Count() == 1 && xr.L(`[data-rng="AO1"]`).Count() == 1 && strings.Contains(xr.Text(), "JP1"),
		"clicking the AO1 row opens its range block below it (decision 136)", xrText)
	p.L(`#refpage tr[data-refana="AO1"] td`).First().Click()
	p.Wait(300)
	p.L(`#refpage select[data-ref="to|AI1"]`).Select("")
	p.Wait(300)
	st := p.L(`#refpage [data-refstate="AI1"]`)
	stText := "no state"
	if st.Count() > 0 {
		stText = st.Text()
	}
	check(st.Count() == 1 && strings.Contains(st.Text(), "不接"), "the reference AI1 wired to nothing: its state says 不接", stText)
	p.L("#refpage [data-refrestore]").Click()
	p.Wait(500)
	check(p.L(`#refpage select[data-ref="via|DO3"]`).Value() == "3" && p.L(`#refpage select[data-ref="via|DO8"]`).Value() == "0",
		"by default DO3 runs through RY3 and DO8 is direct")
	imp := "() => (document.querySelector('#refpage tr[data-impact=\"dout.load|DO1\"]') || {}).innerText || ''"
	p.L(`#refpage select[data-ref="to|DO1"]`).Select("0")
	_ = try(func() { p.WaitFunc("() => ("+imp+")().includes('没接标准机')", nil, 8000) })
	text := p.Eval(imp).S()
	check(strings.Contains(text, "没接标准机"), "DO1 wired to nothing: the impact says it cannot be tested, before saving", text)
	p.L(`#refpage select[data-ref="to|DO1"]`).Select("2")
	p.L("#refpage [data-refsave]").Click()
	p.Wait(800)
	check(p.L(`#refpage [data-refmsg="fail"]`).Count() == 1 && p.L("#refpage [data-refproblem]").Count() >= 1,
		"the reference DI2 wired twice is refused and not saved")
	p.L("#refpage [data-refrestore]").Click()
	p.Wait(500)
	check(p.L(`#refpage select[data-ref="to|DO1"]`).Value() == "1", "「恢复上次保存」 puts DO1 back on DI1")
	p.L("#refpage [data-refclose]").Click()
	check(p.L("#refpage").Count() == 0, "「关闭」 leaves the page")
}

// checkStageConfigs: functional / aging / user-level are configurations
// (decision 97, STG-26): every change is remembered at once, an exclusive pair
// cannot both be ticked, and clicking any of it sends no pt.start.
func checkStageConfigs(p *Page) {
	Section("stage configurations")
	p.L("#clear").Click()
	openSub(p, "aging")
	p.L(lport("temp")).Click()
	p.Wait(300)
	dur := p.L(`#subtiles select[data-subdur="aging"]`) // the time sits on the sub-test's tile
	dur.Select("2")
	p.Wait(400)
	check(p.L(`#subtiles select[data-subdur="aging"]`).Value() == "2", "the aging duration stays as picked")
	box := p.L(`.lport input[data-cfgport="temp"]`)
	if check(box.Count() == 1, "a configuration page has a tick per port") {
		box.Uncheck()
		p.Wait(400)
		check(!p.L(`.lport input[data-cfgport="temp"]`).Checked(), "unticking a port sticks")
		check(p.L(`#itemcards [data-portoff="temp"]`).Count() == 1 && p.L("#itemcards .item").Count() == 0,
			"and layer 3 says the port is not ticked instead of showing its cards")
	}
	p.L(lport("can")).Click()
	p.Wait(300)
	peer := p.L(`#itemcards select[data-peer="can.peer"]`)
	if check(peer.Count() == 1, "the production CAN item offers a choice of peer") {
		peer.Select("pc")
		p.Wait(400)
	}
	p.L(lport("aout")).Click()
	p.Wait(300)
	pts := p.L(`#itemcards input[data-ana-points="aout.out1"]`)
	if check(pts.Count() == 1, "the AO test points can be set on a configuration page") {
		pts.Fill("2500, 5000, 9000") // AO1 defaults to voltage, mV (decision 134)
		pts.Dispatch("change")
		p.Wait(400)
		warn := p.L(`#itemcards [data-ana-overlap="aout.out1"]`)
		wt := "no warning"
		if warn.Count() > 0 {
			wt = warn.Text()
		}
		check(warn.Count() == 1 && warn.Visible() && strings.Contains(warn.Text(), "5000"),
			"a configured AO test point on the 5000 mV calibration point is pointed out", wt)
	}
	// Decision 129: outside R&D the AO load targets are filled in before the run;
	// blank, step 4 cannot be picked and its tile says why. One per AO (STG-38).
	out1 := p.L(`#itemcards input[data-cfgparam="aout.load|out1"]`)
	out2 := p.L(`#itemcards input[data-cfgparam="aout.load|out2"]`)
	simRef := p.Eval("api('/api/ref', {port: 'sim'})").K("connected").Truthy()
	tileHas := func(s string) {
		_ = try(func() {
			p.WaitFunc("() => (document.querySelector('#subtiles .subtile[data-sub=\"aging\"]') || {}).innerText.includes('"+s+"')", nil, 8000)
		})
	}
	step4 := p.L(`#subtiles input[data-fullstep="4"]`)
	if simRef {
		// Decision 134: AI1 on current against AO1 on voltage cannot test.
		setRefAI1(p, "i")
		tile := p.L(`#subtiles .subtile[data-sub="aging"]`)
		tileHas("档位不匹配")
		check(strings.Contains(tile.Text(), "档位不匹配") && step4.Disabled(),
			"ranges that do not match: step 4 cannot be picked and says so (decision 134)", tile.Text())
		checkRefPage(p)
		setRefAI1(p, "v")
	}
	if check(out1.Count() == 1 && out2.Count() == 1 && out1.Value() == "" && out2.Value() == "",
		"aging: the AO load has one target per AO, both blank (STG-38)") && simRef {
		tile := p.L(`#subtiles .subtile[data-sub="aging"]`)
		tileHas("目标值没填")
		check(strings.Contains(tile.Text(), "目标值没填") && step4.Disabled(),
			"and step 4 cannot be picked, its tile saying the target is not filled in", tile.Text())
		out1.Fill("20")
		out1.Dispatch("change")
		p.Wait(800)
		check(step4.Disabled(), "with only AO1's target filled in, step 4 still cannot be picked (STG-38)", tile.Text())
		out2.Fill("20")
		out2.Dispatch("change")
		_ = try(func() {
			p.WaitFunc("() => !document.querySelector('#subtiles input[data-fullstep=\"4\"]').disabled", nil, 8000)
		})
		check(step4.Enabled(), "with both AO targets filled in, step 4 can be picked again", tile.Text())
		// Decision 129: the reference board runs AI / AO / DI / DO here; without it the step is refused.
		p.Eval("api('/api/ref', {port: ''})")
		tileHas("要标准机")
		check(strings.Contains(tile.Text(), "要标准机") && step4.Disabled(),
			"without the reference board, step 4 cannot be picked and says it needs one", tile.Text())
	}
	if simRef {
		p.Eval("api('/api/ref', {port: ''})")
		p.Eval("api('/api/analog/ranges', {ranges: {AI1: 'v'}})") // the shared UID back to the product default
	}
	checkRelayConfigs(p)

	openSub(p, "func")
	p.L(lport("usb")).Click()
	p.Wait(300)
	// Decision 128: a once-only item and a repeatable one of the same group run
	// one after the other, so both start ticked and stay ticked together.
	info, xfer := p.L(`#itemcards input[data-pick="usb.info"]`), p.L(`#itemcards input[data-pick="usb.xfer"]`)
	check(info.Checked() && xfer.Checked(), "USB enumeration (once) and transfer (repeatable) are both ticked by default")
	xfer.Uncheck()
	p.Wait(400)
	xfer.Check()
	p.Wait(500)
	check(info.Checked() && xfer.Checked() && p.L("#msgbox .msg.bad").Count() == 0,
		"ticking USB transfer leaves enumeration ticked, nothing refused")
	// Two repeatable modes of one session still exclude each other.
	check(p.Eval("excludes('func', {id: 'a', exclusive: 'g'}, {id: 'b', exclusive: 'g'})").Truthy() &&
		!p.Eval("excludes('func', {id: 'a', exclusive: 'g', once: true}, {id: 'b', exclusive: 'g'})").Truthy(),
		"the page's exclusivity rule: same group and same once-ness")

	// Decision 135: with the reference board set to unused there is no self-loop;
	// a card that needed it says it cannot run in the fixture tests.
	flip := "async u => { await api('/api/stagecfg', {ref_unused: u}); stageCfgs = null; await loadStageCfgs(); }"
	p.Eval(flip, true)
	p.L(lport("din")).Click()
	p.Wait(300)
	check(p.L(`#itemcards [data-subst="din.level"]`).Count() == 1 && p.L(`#itemcards [data-cfgnoauto="din.level"]`).Count() == 1,
		"with the reference board unused, the DI card says there is no self-loop and it cannot run (decision 135)")
	p.Eval(flip, false)
	p.Wait(200)
	check(p.L(`#itemcards [data-subst="din.level"]`).Count() == 0, "and stops saying so when the reference board is back")

	openSub(p, "user")
	starts := startedLines(p.L("#log").Text())
	check(len(starts) == 0, "configuration pages send no pt.start", cut(strings.Join(starts, " "), 200))
	p.L(`.mi[data-menu="rd"]`).Click()
	stageCfgChanged = true
}

const relayWarn = "仅提示：已超100，过多影响寿命" // decision 123: wording and place fixed by the user

func relayCell(p *Page, ch int, k string) *Loc {
	return p.L(fmt.Sprintf(`#itemcards input[data-relay="%d|%s"]`, ch, k))
}

// relayFill types into a relay cell; change saves it on a configuration page.
func relayFill(p *Page, ch int, k, v string) {
	c := relayCell(p, ch, k)
	c.Fill(v)
	c.Dispatch("change")
	p.Wait(250)
}

// checkRelayConfigs: every configuration has the relay card, parameters only,
// empty until filled; above 100 cycles a red note right of the box (decision 123).
func checkRelayConfigs(p *Page) {
	Section("relay card on the configuration pages")
	for _, stage := range []string{"func", "aging", "user"} {
		openSub(p, stage)
		p.L(lport("relay")).Click()
		p.Wait(300)
		card := p.L("#itemcards .relaycard")
		check(card.Count() == 1 && p.L("#itemcards [data-relayrow]").Count() == 6 &&
			p.L("#itemcards .relaycard [data-startitem]").Count() == 0 && p.L("#itemcards .relaycard .rlrun").Count() == 0,
			stage+": the relay card has six rows, no Start and no run column")
		check(relayCell(p, 1, "count").Value() == "" && relayCell(p, 1, "count").Attr("placeholder") == "必填",
			stage+": the total count starts empty and says it is required")
		checkRelayAll(p, stage)
	}
	// Aging is set to 2 h above: with a duration the relays run by it and only
	// the count is typed, so they cannot overrun (decision 123).
	openSub(p, "aging")
	p.L(lport("relay")).Click()
	p.Wait(300)
	p.L(`#itemcards [data-relaytick="1"]`).Check()
	p.Wait(400)
	by := p.L("#itemcards [data-relaybytest]")
	byText := ""
	if by.Count() > 0 {
		byText = by.Text()
	}
	check(by.Count() == 1 && strings.Contains(by.Text(), "2 小时") && p.L(`#itemcards [data-relayseg="by"]`).Count() == 0 &&
		relayCell(p, 1, "closeS").Count() == 0 && relayCell(p, 1, "minutes").Count() == 0,
		"aging 2 h: the relays run by the test's time, only the count is typed", byText)
	relayFill(p, 1, "count", "2")
	derived := p.L(`#itemcards [data-relayderived="1"]`).Text()
	check(strings.Contains(derived, "1800.0"), "2 cycles in 2 h: 1800 s closed and 1800 s open each", derived)
	check(p.L("#itemcards [data-relayover]").Count() == 0, "and there is no over-time note any more")
	relayFill(p, 1, "count", "")
	p.L(`#itemcards [data-relaytick="1"]`).Uncheck()
	p.Wait(400)
	openSub(p, "user")
	p.L(lport("relay")).Click()
	p.Wait(300)
	// Back on the user-level page: 101 shows the note, and it is only a note.
	warn := p.L(`#itemcards [data-relaywarn="1"]`)
	check(!warn.Visible(), "no note while the count is empty")
	relayFill(p, 1, "count", "101")
	wt := ""
	if warn.Count() > 0 {
		wt = warn.Text()
	}
	check(warn.Visible() && warn.Text() == relayWarn, "101 cycles shows the red note right of the box", wt)
	check(p.Eval("getComputedStyle(document.querySelector('#itemcards [data-relaywarn=\"1\"]')).color").S() ==
		p.Eval("(() => { const d = document.createElement('div'); d.style.color = 'var(--err)';"+
			" document.body.appendChild(d); const c = getComputedStyle(d).color; d.remove(); return c; })()").S(),
		"and the note is in the error red")
	check(p.Eval("cfgItem('user', 'relay.cycle').params.count").S() == "1:101",
		"the configuration keeps the count", p.Eval("cfgItem('user', 'relay.cycle').params").String())
	relayFill(p, 1, "count", "")
	check(!warn.Visible(), "clearing the count clears the note")
}

// checkRelayAll: the header's select-all ticks all six, shows half when some are
// ticked, and clears them (decision 151 item 10); the rows are put back afterwards.
func checkRelayAll(p *Page, where string) {
	tick := func(c int) *Loc { return p.L(fmt.Sprintf(`#itemcards [data-relaytick="%d"]`, c)) }
	ticks := func() []bool {
		var out []bool
		for c := 1; c <= 6; c++ {
			out = append(out, tick(c).Checked())
		}
		return out
	}
	all := func(b []bool) bool {
		for _, x := range b {
			if !x {
				return false
			}
		}
		return true
	}
	anyOf := func(b []bool) bool {
		for _, x := range b {
			if x {
				return true
			}
		}
		return false
	}
	allbox := p.L("#itemcards [data-relayall]")
	if !check(allbox.Count() == 1, where+": the relay table has a select-all box in its header") {
		return
	}
	before := ticks()
	if all(before) {
		allbox.Click()
		p.Wait(400)
	}
	allbox.Click()
	p.Wait(400)
	every := all(ticks())
	tick(2).Uncheck()
	p.Wait(400)
	half := p.Eval("(document.querySelector('#itemcards [data-relayall]') || {}).indeterminate === true").B()
	allbox.Click()
	p.Wait(400)
	allbox.Click()
	p.Wait(400)
	none := !anyOf(ticks())
	check(every && half && none, where+": select-all ticks all six, is half-ticked with one off, and clears them",
		fmt.Sprintf("all=%v half=%v none=%v", every, half, none))
	for c, on := range before {
		tick(c + 1).SetChecked(on)
		p.Wait(100)
	}
}
