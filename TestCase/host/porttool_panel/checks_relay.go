package main

import (
	"encoding/json"
	"strconv"
	"strings"
)

func relayState(p *Page) J {
	return p.Eval("((libData.states || {})['relay.cycle'] || {}).relay || null")
}

// checkRelayRd: the R&D relay card on the simulated board: empty or too short
// values block Start, 101 still starts, serial runs one relay after the other
// with the count going up on the card, and the person is asked at the end
// (decision 123, TODO-fixture row 67).
func checkRelayRd(p *Page) {
	Section("relay card in R&D")
	p.L(`.mi[data-menu="rd"]`).Click()
	p.Wait(200)
	p.L(lport("relay")).Click()
	p.Wait(300)
	if !check(p.L(`#itemcards .relaycard [data-startitem="relay.cycle"]`).Count() == 1, "the R&D relay card has its own Start") {
		return
	}
	check(p.L("#itemcards select[data-relaysrc]").Count() == 0,
		"R&D has no data-source choice: a person reports the load's cycles (decisions 130, 133)")
	checkRelayAll(p, "R&D")
	for ch := 1; ch <= 6; ch++ {
		p.L(`#itemcards [data-relaytick="` + strconv.Itoa(ch) + `"]`).SetChecked(ch == 1 || ch == 2)
		p.Wait(100)
	}
	for _, ch := range []int{1, 2} {
		for _, k := range []string{"closeS", "openS", "count"} {
			relayFill(p, ch, k, "")
		}
	}
	errBox := p.L("#itemcards [data-relayerr]")
	start := p.L(`#itemcards .relaycard [data-startitem="relay.cycle"]`)
	start.Click()
	p.Wait(300)
	check(errBox.Text() == "勾选的继电器参数必须填完才能开始" && !p.Eval("libData.running").Truthy(),
		"empty values on a ticked relay block Start", errBox.Text())

	relayFill(p, 1, "closeS", "0.5")
	relayFill(p, 1, "openS", "1")
	relayFill(p, 1, "count", "1")
	relayFill(p, 2, "closeS", "1")
	relayFill(p, 2, "openS", "1")
	relayFill(p, 2, "count", "1")
	start.Click()
	p.Wait(300)
	check(strings.Contains(errBox.Text(), "不到 1 秒") && !p.Eval("libData.running").Truthy(), "a hold under 1 s blocks Start", errBox.Text())

	// 101 is only a note: Start runs, then Stop keeps what was reached.
	relayFill(p, 1, "closeS", "1")
	relayFill(p, 1, "count", "101")
	check(p.L(`#itemcards [data-relaywarn="1"]`).Text() == relayWarn && p.L(`#itemcards [data-relaywarn="1"]`).Visible(),
		"101 shows the red note in R&D too")
	p.L(`#itemcards [data-relayseg="order"] label:has(input[value="par"])`).Click()
	p.Wait(200)
	start = p.L(`#itemcards .relaycard [data-startitem="relay.cycle"]`)
	start.Click()
	if try(func() { p.WaitFunc("() => libData.running", nil, 10000) }) == nil {
		check(true, "with 101 cycles Start still runs")
	} else {
		check(false, "with 101 cycles Start still runs", errBox.Text())
		return
	}
	if try(func() {
		p.WaitFunc("() => { const r = ((libData.states || {})['relay.cycle'] || {}).relay;"+
			" return r && r.chans[1].phase === 'done' && r.chans[0].n >= 1; }", nil, 20000)
		st := relayState(p)
		check(st.K("chans").I(0).K("phase").S() == "run", "in parallel both start together; relay 2 is done while relay 1 runs on", st.String())
		p.Wait(600)
		cnt := p.L(`#itemcards [data-relaycnt="1"]`).Text()
		n, err := strconv.Atoi(strings.TrimSpace(cnt))
		if err != nil {
			throw("invalid literal for int(): %q", cnt)
		}
		check(n >= 1, "the card shows the count reached so far", cnt)
	}) != nil {
		check(false, "the parallel run reports counts", relayState(p).String())
	}
	p.L("#itemcards .relaycard button.stop").Click()
	p.WaitFunc("() => !libData.running", nil, 15000)
	st := p.Eval("(libData.states || {})['relay.cycle']")
	c0 := st.K("relay").K("chans").I(0)
	check(st.K("outcome").S() == "STOPPED" && c0.K("phase").S() == "stopped" && c0.K("n").N() >= 1,
		"Stop ends it early and keeps the count reached", st.K("relay").String())

	// Serial: relay 2 waits until relay 1 reached its count.
	relayFill(p, 1, "count", "2")
	p.L(`#itemcards [data-relayseg="order"] label:has(input[value="ser"])`).Click()
	p.Wait(300)
	p.L(`#itemcards .relaycard [data-startitem="relay.cycle"]`).Click()
	if err := try(func() {
		p.WaitFunc("() => { const r = ((libData.states || {})['relay.cycle'] || {}).relay;"+
			" return r && r.chans[0].phase === 'run'; }", nil, 10000)
		st := relayState(p)
		check(st.K("order").S() == "ser" && st.K("chans").I(1).K("phase").S() == "queue", "serial: relay 2 waits while relay 1 runs", st.String())
		p.WaitFunc("() => { const r = ((libData.states || {})['relay.cycle'] || {}).relay;"+
			" return r && r.chans[0].n === 1; }", nil, 15000)
		check(relayState(p).K("chans").I(1).K("phase").S() == "queue" && p.L(`#itemcards [data-relaycnt="1"]`).Text() == "1",
			"relay 1's count goes up on the card while relay 2 still waits")
		p.WaitFunc("() => { const r = ((libData.states || {})['relay.cycle'] || {}).relay;"+
			" return r && r.chans[1].phase === 'run'; }", nil, 15000)
		check(relayState(p).K("chans").I(0).K("phase").S() == "done" && relayState(p).K("chans").I(0).K("n").N() == 2,
			"relay 2 starts only once relay 1 reached its count")
		// Decision 130: the person types each load's real cycles; they are compared with the count.
		for _, cs := range []struct {
			ch   int
			seen string
		}{{1, "2"}, {2, "1"}} {
			p.WaitFunc("c => !document.querySelector('#libask').hidden && (libData.ask || {}).text"+
				" && libData.ask.text.includes('继电器 ' + c)", cs.ch, 20000)
			check(strings.Contains(p.L("#libask").Text(), "负载实际开合了几次"),
				"at the end the person is asked how many times relay "+strconv.Itoa(cs.ch)+"'s load switched", p.L("#libask").Text())
			p.L(`#libask input[data-ask="value"]`).Fill(cs.seen)
			p.L(`#libask [data-ask="ok"]`).Click()
			p.WaitFunc("id => !libData.ask || libData.ask.id !== id", p.Eval("libData.ask.id").v, 10000)
		}
		p.WaitFunc("() => !libData.running", nil, 10000)
		st = p.Eval("(libData.states || {})['relay.cycle']")
		ch := st.K("relay").K("chans")
		check(st.K("outcome").S() == "PASS" && ch.Len() == 2 && ch.I(0).K("n").N() == 2 && ch.I(1).K("n").N() == 1,
			"the loads' cycles match the counts: PASS, and each relay's count is kept", st.String())
		// Decision 129 「多路」: each relay has its own result, the card shows one per relay.
		p.Wait(600)
		pills := p.Eval("[...document.querySelectorAll('#itemcards [data-chpills=\"relay.cycle\"] [data-chan]')]" +
			".map(p => p.dataset.chan + '=' + p.dataset.outcome)").Strings()
		var own []string
		for _, c := range st.K("chans").Arr() {
			own = append(own, c.K("name").S()+"="+c.K("outcome").S())
		}
		check(eqStrings(own, "RY1=PASS", "RY2=PASS") && eqStrings(pills, "RY1=PASS", "RY2=PASS"),
			"each relay has its own result, shown one per relay on the card", st.K("chans").String()+" "+show(pills))
	}); err != nil {
		check(false, "the serial run goes relay by relay", err.Error()+" / "+relayState(p).String())
		if p.Eval("libData.running").Truthy() {
			p.Eval("libStop()")
		}
	}
	checkRelayByTime(p)
}

// checkRelayByTime: 按时长: time and count give each cycle, closed and open half
// each, and those are the holds sent to the board (decision 123).
func checkRelayByTime(p *Page) {
	Section("relay card: by duration")
	p.L(`#itemcards [data-relayseg="by"] label:has(input[value="time"])`).Click()
	p.Wait(300)
	p.L(`#itemcards [data-relaytick="2"]`).SetChecked(false)
	p.Wait(200)
	check(relayCell(p, 1, "ratio").Count() == 0 && relayCell(p, 1, "minutes").Count() == 1,
		"by duration asks for the time and the count only, no share closed")
	relayFill(p, 1, "minutes", "0.1") // 6 s for 2 cycles: 3 s each, 1.5 s closed, 1.5 s open
	relayFill(p, 1, "count", "2")
	derived := p.L(`#itemcards [data-relayderived="1"]`).Text()
	check(strings.Contains(derived, "吸合 1.5 秒 / 断开 1.5 秒"), "each cycle is split half closed, half open", derived)
	params := p.Eval("libEdits['relay.cycle']")
	check(params.K("close").S() == "1:1500" && params.K("open").S() == "1:1500" && !params.Has("ratio"),
		"the card keeps 1500 ms for both holds", params.String())
	if err := try(func() {
		// The log pane does not echo a run's commands: the Start request and the
		// holds the server took from it (ptlib.relayStart formats exactly these) are checked.
		body := p.ExpectRequest("/api/lib/start", func() {
			p.L(`#itemcards .relaycard [data-startitem="relay.cycle"]`).Click()
		})
		var req struct {
			Items []struct {
				ID     string         `json:"id"`
				Params map[string]any `json:"params"`
			} `json:"items"`
		}
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			throw("the Start request is not JSON: %v", err)
		}
		var sent map[string]any
		for _, it := range req.Items {
			if it.ID == "relay.cycle" {
				sent = it.Params
				break
			}
		}
		if sent == nil {
			throw("list index out of range: no relay.cycle in the Start request")
		}
		r1 := map[string]string{}
		for _, k := range []string{"close", "open", "count"} {
			v, _ := sent[k].(string)
			for _, x := range strings.Split(v, ",") {
				if a, b, ok := strings.Cut(x, ":"); ok && a == "1" {
					r1[k] = b
				}
			}
		}
		_, hasRatio := sent["ratio"]
		ch, _ := sent["ch"].(string)
		check(ch == "1" && r1["close"] == "1500" && r1["open"] == "1500" && r1["count"] == "2" && len(r1) == 3 && !hasRatio,
			"Start sends relay 1 with 1500 ms closed, 1500 ms open, 2 cycles", show(sent))
		p.WaitSel("#libask:not([hidden])", 20000)
		p.L(`#libask input[data-ask="value"]`).Fill("2")
		p.L(`#libask [data-ask="ok"]`).Click()
		p.WaitFunc("() => !libData.running", nil, 10000)
		st := p.Eval("(libData.states || {})['relay.cycle']")
		c0 := st.K("relay").K("chans").I(0)
		check(st.K("outcome").S() == "PASS" && c0.K("n").N() == 2 && c0.K("close_ms").N() == 1500 && c0.K("open_ms").N() == 1500,
			"the board ran 1500 / 1500 ms for its two cycles and the load's 2 cycles pass it", st.K("relay").String())
	}); err != nil {
		check(false, "the by-duration run finishes", err.Error()+" / "+relayState(p).String())
		if p.Eval("libData.running").Truthy() {
			p.Eval("libStop()")
		}
	}
	p.L(`#itemcards [data-relayseg="by"] label:has(input[value="gap"])`).Click()
	p.Wait(300)
}
