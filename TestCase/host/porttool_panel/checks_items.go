package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// libWaitDone waits until every card in ids shows a verdict pill (not "not run", not running).
func libWaitDone(p *Page, ids []string, secs int) []string {
	var pills []string
	for n := 0; n < secs*5; n++ {
		pills = pills[:0]
		done := true
		for _, i := range ids {
			c := p.L(`#itemcards .item[data-item="` + i + `"] .ih .pill`).Last().Attr("class")
			pills = append(pills, c)
			done = done && hasAny(c, "pass", "fail", "rec", "skip")
		}
		if done {
			return pills
		}
		p.Wait(200)
	}
	return pills
}

func hasAny(s string, subs ...string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

// pick sets an item's tick; R&D 「只测一次」 cards have none (decision 116).
func pick(p *Page, item string, on bool) {
	box := p.L(`#itemcards input[data-pick="` + item + `"]`)
	if box.Count() > 0 {
		box.SetChecked(on)
	}
}

// pressStart presses item's own Start (an R&D 「只测一次」 card, decision 116),
// else a card group's Start (decision 115): kind, or the first group with
// ticked items. While a run is going the running card's button is the Stop.
func pressStart(p *Page, kind, item string) {
	if p.Eval("libRunning() || libPageBusy").Truthy() {
		p.L("#itemcards button.stop").First().Click()
		return
	}
	if item != "" && p.Eval(`i => { const b = [...document.querySelectorAll('#itemcards button[data-startitem]')]
            .find(x => x.dataset.startitem.split(',').includes(i));
        if (b && !b.disabled) { b.click(); return true; } return false; }`, item).Truthy() {
		return
	}
	kinds := []string{"once", "repeat"}
	if kind != "" {
		kinds = []string{kind}
	}
	for _, k := range kinds {
		b := p.L(`#itemcards button[data-start="` + k + `"]`)
		if b.Count() > 0 && b.Enabled() {
			b.Click()
			return
		}
	}
}

// clickStart clicks a Start and waits until the page shows that run's /api/lib.
// The libData already on the page is the previous run's, so only a newer one counts.
func clickStart(p *Page, b *Loc) {
	p.Eval("() => { window.__t4lib = libData; }")
	b.Click()
	p.WaitFunc("() => libPageBusy || (libData !== window.__t4lib && (libData.running || libData.ended))", nil, 10000)
}

// runBothGroups runs the once-items (each from its own Start in R&D, decision
// 116), then the ticked repeatable ones. The previous run ends before a button
// is looked at: while it runs, a group's Start is an enabled Stop.
func runBothGroups(p *Page, ids []string) {
	ran := false
	var buttons []string
	for _, i := range ids {
		buttons = append(buttons, `#itemcards button[data-startitem="`+i+`"]`)
	}
	buttons = append(buttons, `#itemcards button[data-start="once"]`, `#itemcards button[data-start="repeat"]`)
	for _, sel := range buttons {
		if ran {
			p.WaitFunc("() => !libData.running && !libPageBusy", nil, 120000)
		}
		b := p.L(sel)
		if b.Count() > 0 && b.Enabled() {
			clickStart(p, b)
			ran = true
		}
	}
}

// checkItemRunner: Start runs the ticked items and the verdicts come back onto
// the cards and the port list (PORTTOOL-FLOW.md C.3.6 "测试项怎么跑"). Judged on
// the simulated board, so what is checked is that verdicts appear, not which ones.
func checkItemRunner(p *Page) {
	Section("item runner: Start on the R&D cards")
	p.L(`.mi[data-menu="rd"]`).Click()
	p.L(lport("sdram")).Click()
	p.Wait(300)
	ids := attrs(p.L("#itemcards .item"), "data-item")
	check(eqStrings(ids, "sdram.probe", "sdram.sweep", "sdram.hold"), "sdram's three items are cards (no window CRC, decision 141)", show(ids))
	// Decision 151 item 3: in R&D the sweep runs once, the long read/write repeats.
	sweepOwn := p.L(`#itemcards button[data-startitem="sdram.sweep"]`)
	check(sweepOwn.Count() == 1 && p.L(`#itemcards input[data-pick="sdram.sweep"]`).Count() == 0 &&
		p.L(`#itemcards input[data-pick="sdram.hold"]`).Count() == 1,
		"R&D: the sweep has its own Start, the long read/write is the repeatable one (decision 151)")
	rep := p.L(`#itemcards button[data-start="repeat"]`)
	own := p.L(`#itemcards .item[data-item="sdram.probe"] button[data-startitem="sdram.probe"]`)
	check(rep.Count() == 1 && rep.Enabled() && own.Count() == 1 && own.Enabled(),
		"the probe has its own Start, the repeatable group a Start enabled with items ticked (decisions 115, 116)")
	runBothGroups(p, []string{"sdram.probe", "sdram.sweep"})
	pills := libWaitDone(p, []string{"sdram.probe", "sdram.sweep", "sdram.hold"}, 60)
	verdicts := true
	for _, c := range pills {
		verdicts = verdicts && hasAny(c, "pass", "fail")
	}
	check(verdicts, "the three items that ran show a verdict", show(pills))
	sweep := p.L(`#itemcards .item[data-item="sdram.sweep"]`).Text()
	check(strings.Contains(sweep, "写速率（MB/秒）") && strings.Contains(sweep, "读速率（MB/秒）"),
		"the sweep shows its write and read rate (decision 151)", tail(sweep, 300))
	hold := p.L(`#itemcards .item[data-item="sdram.hold"]`).Text()
	check(strings.Contains(hold, "循环数") && strings.Contains(hold, "查过的块") && strings.Contains(hold, "失败的块"),
		"the long read/write shows cycles, checked and failed (decision 151)", tail(hold, 300))
	res := p.L(`#itemcards .item[data-item="sdram.probe"] .ib > div`).Nth(1).Text()
	check(len(splitLines(strings.TrimSpace(res))) > 1, "the result half shows what was measured", cut(res, 160))
	tbl := p.L(`#itemcards .item[data-item="sdram.probe"] table.crit`)
	tblText := "no table"
	if tbl.Count() > 0 {
		tblText = cut(tbl.Text(), 200)
	}
	check(tbl.Count() == 1 && strings.Contains(tbl.Text(), "预期") && (strings.Contains(tbl.Text(), "✅") || strings.Contains(tbl.Text(), "❌")),
		"the result half is a 项目 / 预期 / 结果 table with the outcome per criterion (decision 113)", tblText)
	check(p.L(`#itemcards .item[data-item="sdram.probe"] dl.kv`).Count() == 0 &&
		p.Eval(`() => [...document.querySelectorAll('#itemcards .item[data-item="sdram.probe"] table.crit tr')]
              .some(tr => tr.children.length === 3 && tr.children[1].textContent === '' && tr.children[2].textContent !== '')`).Truthy(),
		"and the board's other readings are rows of that table with an empty 预期, not a list of their own (decision 116)")
	check(p.L(`.lport[data-lport="sdram"] [data-portverdict]`).Count() == 1, "the port list shows sdram's verdict")

	if p.L(lport("led")).Count() == 0 {
		check(true, "no indicator LED on this board: the question box is not exercised")
		return
	}
	p.L(lport("led")).Click()
	p.Wait(300)
	card := p.L(`#itemcards .item[data-item="led.blink"]`).Text()
	check(strings.Contains(card, "LED3") && strings.Contains(card, "BOOT0"), "the indicator card names LED3 and the power lamps by BOOT0", cut(card, 160))
	p.L("#clear").Click() // sdram.hold above is a session; this checks only the run-only LED item
	pressStart(p, "", "led.blink")
	p.WaitSel("#libask:not([hidden])", 30000)
	check(p.L(`#libask [data-ask="yes"]`).Count() == 1 && strings.Contains(p.L("#libask").Text(), "LED3"),
		"the indicator item asks the person yes or no about LED3 in the question box")
	p.Wait(1200)
	log := p.L("#log").Text()
	// The board's answers, not the commands: the server sends these itself.
	check(strings.Contains(log, "led fault=1") && strings.Contains(log, "led fault=0"),
		"and the panel keeps the lamp blinking while it asks (C.3.6 「指示灯」)")
	p.L(`#libask [data-ask="yes"]`).Click()
	pills = libWaitDone(p, []string{"led.blink"}, 60)
	check(hasAny(pills[0], "pass", "fail"), "the answer gives the item its verdict", show(pills))
	starts := startedLines(p.L("#log").Text())
	check(len(starts) == 0, "the R&D Start on run-only items sends no pt.start", cut(strings.Join(starts, " "), 200))
}

// checkRunLogShown: a timed R&D run states its run log file under Start for the
// whole run, and the network item says to check the subnet first (decision 106).
func checkRunLogShown(p *Page) {
	Section("a timed run shows where its log is written")
	p.L(`.mi[data-menu="rd"]`).Click()
	if p.L(lport("eth")).Count() > 0 {
		p.L(lport("eth")).Click()
		p.Wait(200)
		check(strings.Contains(p.L(`#itemcards .item[data-item="eth.xfer"]`).Text(), "网段"),
			"the network data item says to check the subnet first")
	}
	p.L(lport("temp")).Click()
	p.Wait(200)
	p.L(`#itemcards select[data-shell="duration"]`).Select("1")
	pressStart(p, "", "")
	if try(func() {
		p.WaitSel("#rtop [data-runlog]", 15000)
		text := p.L("#rtop [data-runlog]").Text()
		check(strings.Contains(text, "run-") && strings.Contains(text, ".log"), "the run log path is under Start while it runs", text)
	}) != nil {
		check(false, "the run log path is under Start while it runs", tail(p.L("#rtop").Text(), 300))
	}
	pressStart(p, "", "")
	p.WaitFunc("() => !libData.running", nil, 15000)
	p.L(`#itemcards select[data-shell="duration"]`).Select("once")
}

// checkTimedReadable (decision 151 item 2): no 「只测一次」 anywhere; a timed R&D
// card says what it measures, the time and the round; the log has one line per
// round and the frames sit behind their own filter.
func checkTimedReadable(p *Page) {
	Section("a timed run says what, how long and which round")
	p.L(`.mi[data-menu="rd"]`).Click()
	for _, port := range []string{"eth", "usb", "sd", "sdram"} {
		if p.L(lport(port)).Count() > 0 {
			p.L(lport(port)).Click()
			p.Wait(200)
			check(!strings.Contains(p.L("#itemcards").Text(), "只测一次"), "R&D "+port+": no 「只测一次」 heading or badge")
		}
	}
	if p.L(lport("sd")).Count() == 0 {
		check(false, "the SD port is there to run a timed round on")
		return
	}
	p.L(lport("sd")).Click()
	p.Wait(200)
	p.L(`#itemcards select[data-shell="duration"]`).Select("1")
	pressStart(p, "repeat", "")
	if err := try(func() {
		p.WaitSel("#itemcards [data-timednow]", 15000)
		text := p.L("#itemcards [data-timednow]").First().Text()
		check(strings.Contains(text, "正在测") && strings.Contains(text, "/ 共 1:00:00"),
			"the running card says what it measures and the time used of the total", text)
		log0 := p.Eval("logBuf.length").v // from here the page knows the run is timed
		p.WaitSel("#itemcards [data-timednow] [data-round]", 90000)
		text = p.L("#itemcards [data-timednow]").First().Text()
		check(strings.Contains(text, "已跑完") && strings.Contains(text, "上一轮"), "the card says how many rounds and how the last one went", text)
		p.WaitFunc(`() => logBuf.some(e => e.kind === 'panel' && /第 \d+ 轮/.test(lineText(e)))`, nil, 10000)
		check(true, "the log has a line per round")
		frames := int(p.Eval("n => logBuf.slice(n).filter(e => e.kind === 'frame').length", log0).N())
		check(frames == 0, "no frame lands in the log as a sample line while the timed run goes", fmt.Sprintf("%d frames", frames))
		check(p.L("#log .l.timed").Count() == 0, "the timed run's frames are hidden until their filter is ticked")
	}); err != nil {
		check(false, "a timed SD round shows on its card and in the log", err.Error()+" / "+cut(p.L("#itemcards").Text(), 300))
	}
	pressStart(p, "", "")
	p.WaitFunc("() => !libData.running", nil, 15000)
	p.L(`#itemcards select[data-shell="duration"]`).Select("once")
}

var bannedOnCards = []string{
	"Klemmblock", "duty", "freq", "period", "miss", "seq=", "rxlines",
	"hold", "blink", "extloop", "loopback", "autoneg", "mismatches",
	"observed", "detected", "Digital Out", "AOUT", "SD Karte", "SDram",
	"temp1", "temperature",
}

// checkAutoechoToggle: the 自动回环应答 box in the header. A bench turns it off
// when a second tool wants the control port; a box that silently does nothing
// looks like a board that stopped answering.
func checkAutoechoToggle(p *Page) {
	Section("the auto-echo box")
	box := p.L("#autoecho")
	if !check(box.Count() == 1, "the box is there once connected") {
		return
	}
	was := box.Checked()
	box.SetChecked(!was)
	p.Wait(600)
	check(box.Checked() != was, "it toggles")
	stat := strings.TrimSpace(p.L("#echostat").Text())
	check(stat != "", "and says what state it is in now", stat)
	box.SetChecked(was)
	p.Wait(600)
	check(box.Checked() == was, "and toggles back")
}

var (
	rawTenthsRe = regexp.MustCompile(`第 \d+ 个数`)
	fieldNameRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
)

// checkNothingInEnglish: every R&D item card, swept for the protocol words a
// person should not meet (decision 82).
func checkNothingInEnglish(p *Page) {
	Section("no protocol words left on the cards")
	p.L(`.mi[data-menu="rd"]`).Click()
	bad := map[string][]string{}
	for _, port := range p.Eval("catalog.ports.map(p => p.port)").Strings() {
		p.L(lport(port)).Click()
		p.Wait(200)
		// A watched session lists the board's own lines on purpose (decision 105).
		body := p.Eval(`() => { const c = document.querySelector('#itemcards').cloneNode(true);
            c.querySelectorAll('pre.events').forEach(n => n.remove()); return c.innerText; }`).S()
		for _, w := range bannedOnCards {
			if strings.Contains(body, w) {
				bad[w] = append(bad[w], port)
			}
		}
	}
	var bl []string
	for _, w := range sortedKeys(bad) {
		bl = append(bl, w+" on "+strings.Join(bad[w], ","))
	}
	check(len(bad) == 0, "no card prints a protocol word at a person", strings.Join(bl, "; "))
	// A criterion's name is a word, not the plan's field name ("value", "follows"):
	// R&D and the three configurations, every port.
	raw := map[string]map[string]bool{}
	add := func(n, where string) {
		if raw[n] == nil {
			raw[n] = map[string]bool{}
		}
		raw[n][where] = true
	}
	for _, v := range []string{"rd", "func", "aging", "user"} {
		for _, port := range p.Eval("catalog.ports.map(p => p.port)").Strings() {
			names := p.Eval(`([v, p]) => { view = v; lport = p; renderShell();
                return [...document.querySelectorAll('#itemcards table.crit tr td')].map(td => td.textContent); }`, []string{v, port}).Strings()
			for _, n := range names {
				if strings.Contains(n, "0.1C") || rawTenthsRe.MatchString(n) {
					add(n, v+"/"+port)
				}
				if fieldNameRe.MatchString(n) {
					add(n, v+"/"+port)
				}
			}
		}
	}
	p.Eval("() => { view = 'rd'; renderShell(); }")
	var rl []string
	for _, n := range sortedKeys(raw) {
		rl = append(rl, n+" on "+strings.Join(sortedKeys(raw[n]), ","))
	}
	check(len(raw) == 0, "no criterion is named by its field name or shows raw tenths of °C, in R&D or a configuration", strings.Join(rl, "; "))
}

func sortedKeys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// checkPeerBinding: 接上 / 断开 on the RS485 row of the connection block. The
// simulated board plays its own peers, so this proves the control and the
// server round trip, not that the right adapter closes the loop.
func checkPeerBinding(p *Page) {
	Section("binding a peer serial port")
	row := p.L(`#connrows .crow[data-conn="rs485"]`)
	sel := row.L("select")
	if sel.Count() == 0 {
		check(strings.TrimSpace(row.Text()) != "", "with no adapter to offer, the RS485 row says why", row.Text())
		return
	}
	check(sel.First().L("option").Count() >= 1, "the RS485 row lists this machine's serial ports")
	row.L("button").HasText("接上").First().Click()
	p.Wait(1500)
	msg := p.L("#msgbox .msg")
	if msg.Count() > 0 && strings.TrimSpace(msg.First().Text()) != "" {
		// The port may be in use on this machine: a refusal stated in words is fine.
		check(row.L("button").HasText("断开").Count() == 0, "a refused binding does not leave the row looking bound", msg.First().Text())
		return
	}
	if !check(row.L("button").HasText("断开").Count() == 1, "a bound peer offers to be unbound", row.Text()) {
		return
	}
	row.L("button").HasText("断开").First().Click()
	p.Wait(1000)
	check(row.L("select").Count() == 1, "and unbinding puts the chooser back")
}

// checkPeerErrorLine: a bound peer that failed shows its error in words on its
// row. Drawn from an injected state: this covers the drawing, not how the
// server notices the failure.
func checkPeerErrorLine(p *Page) {
	Section("a failed peer says why")
	text := p.Eval(`() => {
        const was = state.links;
        state.links = Object.assign({}, was, { rs485: { com: "COM99", error: "port vanished" } });
        refreshConnRows(true);
        const e = document.querySelector('#connrows .crow[data-conn="rs485"] .cerr');
        const t = e ? e.textContent : "";
        state.links = was; refreshConnRows(true);
        return t; }`).S()
	check(strings.Contains(text, "port vanished") && strings.Contains(text, "出问题了"), "the RS485 row shows the peer's error in words", text)
}

// checkAbsentField: a criterion judged only when reported (RS485 overrun on the
// step-6 demo, STG-07) says so, and a result without the field says it was not
// reported - not 「✅ 空」. Drawn from an injected result.
func checkAbsentField(p *Page) {
	Section("a field judged only when reported")
	got := p.Eval(`() => {
        const it = catalog.items.find(i => i.id === 'rs485.peer');
        const st = {checks: [{check: {field: 'overrun', op: 'count_zero', if_present: true}, got: '', pass: true, absent: true}], values: []};
        return [libCritTable(it, st).innerText, runValue(Object.assign({port: 'rs485', item: 'rs485.peer', state: 'PASS'}, st))]; }`).Strings()
	check(len(got) == 2 && strings.Contains(got[0], "板子报了才判") && strings.Contains(got[0], "板子没报，不判") && strings.Contains(got[1], "板子没报"),
		"RS485 「无溢出」 says it is judged only when reported, and that it was not", strings.Join(got, " | "))
}

// checkCanPeer: in R&D the production CAN item's far end is the PC's CANable
// only: R&D uses no reference board (decision 133).
func checkCanPeer(p *Page) {
	Section("CAN: the far end on the R&D card")
	p.L(`.mi[data-menu="rd"]`).Click()
	p.L(lport("can")).Click()
	p.Wait(300)
	check(p.L(`#itemcards select[data-peer="can.peer"]`).Count() == 0, "the R&D CAN card offers no reference board, so no choice of peer")
	sent := p.Eval("(libPicks().find(p => p.id === 'can.peer') || {}).peer")
	check(sent.S() == "pc", "and Start would send the PC's adapter", sent.String())
}

// checkCtrlCounts: a loop=ctrl port's send / receive / lost counts are labelled
// as the control port's, not this port's verdict (decision 9). Uses the sweep's results.
func checkCtrlCounts(p *Page) {
	Section("control-port counts are not a port verdict")
	p.L(`.mi[data-menu="rd"]`).Click()
	hits := p.Eval(`() => Object.entries(libData.states || {}).filter(([id, s]) => {
        const it = catalog.items.find(x => x.id === id);
        const p = it && (state.ports || []).find(x => x.name === it.port);
        return p && p.loop === 'ctrl' && (s.values || []).some(([k]) => k === 'seq' || k === 'rx' || k === 'miss');
      }).map(([id]) => [id, catalog.items.find(x => x.id === id).port])`).Arr()
	if !check(len(hits) > 0, "the sweep left a loop=ctrl card with counts on it") {
		return
	}
	for _, h := range hits {
		item, port := h.I(0).S(), h.I(1).S()
		p.L(lport(port)).Click()
		p.Wait(200)
		check(p.L(`#itemcards [data-ctrlcounts="`+item+`"]`).Count() == 1, item+" says its counts only show the control port is alive")
	}
}
