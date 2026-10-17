package main

import "strings"

// checkRecoverCards: each communication port's 断开恢复 card (decision 150,
// PORTTOOL-FLOW.md C.3.6 「断开恢复」) asks to pull, sees the break, asks to
// plug back, sees the traffic catch up and judges it by the criteria plan's
// provisional limits (recovery within 10 s, no restart). The simulated
// board cuts the port itself (sim.cut), so this proves the panel's flow, not
// what a real cable does.
func checkRecoverCards(p *Page, com string) {
	Section("disconnect and recover: one card per communication port")
	if !isSim(com) {
		check(true, "a real bench: these cards need a person at the cable - not clicked here")
		return
	}
	p.L(`.mi[data-menu="rd"]`).Click()
	for _, port := range []string{"eth", "usb", "rs232", "rs485", "can", "knx"} {
		if p.L(lport(port)).Count() == 0 {
			check(false, port+" is on the simulated board")
			continue
		}
		id := port + ".recover"
		p.L(lport(port)).Click()
		p.Wait(300)
		card := p.L(`#itemcards .item[data-item="` + id + `"]`)
		if !check(card.Count() == 1, port+": the 断开恢复 card is there") {
			continue
		}
		check(strings.Contains(card.Text(), "断开恢复") && strings.Contains(card.Text(), "恢复用时") && strings.Contains(card.Text(), "10000"),
			port+": it says what it does and shows the plan's limit on the recovery time", cut(card.Text(), 200))
		pressStart(p, "", id)
		pulled := try(func() {
			p.WaitFunc(`() => { const b = document.querySelector('#libask');
                return b && !b.hidden && b.textContent.indexOf('拔掉') >= 0; }`, nil, 20000)
		}) == nil
		check(pulled && strings.Contains(p.L("#libask").Text(), "模拟板上不用动手"),
			port+": the question box asks to pull the cable and says the simulated board does it itself", cut(p.L("#libask").Text(), 160))
		plugged := try(func() {
			p.WaitFunc(`() => { const b = document.querySelector('#libask');
                return b && !b.hidden && b.textContent.indexOf('插回') >= 0; }`, nil, 20000)
		}) == nil
		check(plugged, port+": once the board shows the break it asks to plug it back", cut(p.L("#libask").Text(), 160))
		pills := libWaitDone(p, []string{id}, 40)
		text := p.L(`#itemcards .item[data-item="` + id + `"]`).Text()
		o := p.Eval(`id => ((libData.states || {})[id] || {}).outcome || ''`, id).S()
		check(hasAny(pills[0], "pass") && o == "PASS" && strings.Contains(text, "恢复用时") && strings.Contains(text, "断开期间丢的帧"),
			port+": it ends passed, with the recovery time and the frames lost", o+" "+show(pills)+" "+cut(text, 240))
	}
}
