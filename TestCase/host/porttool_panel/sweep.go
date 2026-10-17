package main

import (
	"fmt"
	"strings"
)

// What a port's own items give on the simulated board with every item ticked
// that needs no person, reference board or PC peer. Ports not listed must
// pass; "either" is a verdict this bench cannot decide. On a real bench every
// port is "either": its wiring decides.
var sweepExpectSim = map[string][2]string{}

// autoPicks ticks the R&D items of port that run with nobody and nothing
// attached: peer none, not a calibration walk, not pending, not watched; one
// per exclusive group, a judged one when the group has it. Returns their ids.
func autoPicks(p *Page, port string) []string {
	return p.Eval(`p => {
        const items = itemsOf('rd', p);
        const can = it => it.peers[0] === 'none' && !it.pending && !it.watch_s
                          && !(ITEM_BODIES[it.id] && ITEM_BODIES[it.id].run);
        const pick = {};
        for (const it of items) {
          if (!can(it) || !it.exclusive) { continue; }
          const was = pick[it.exclusive];
          if (!was || (!was.criteria.length && it.criteria.length)) { pick[it.exclusive] = it; }
        }
        const on = [];
        for (const it of items) {
          const auto = can(it) && (!it.exclusive || pick[it.exclusive] === it);
          itemOff['rd|' + it.id] = !auto;
          if (auto) { on.push(it.id); }
        }
        renderShell();
        return on;
      }`, port).Strings()
}

// checkEveryPort: every port, started once from its R&D cards, and the port
// verdict checked against what this bench should give - not merely that one appeared.
func checkEveryPort(p *Page, com string) {
	Section("every port, one Start on its R&D cards")
	p.L(`.mi[data-menu="rd"]`).Click()
	type row struct{ port, verdict, note string }
	var produced []row
	for _, port := range p.Eval("catalog.ports.map(p => p.port)").Strings() {
		if p.L("#afterconn.live").Count() == 0 {
			Fail("the panel lost the board - stopping the sweep")
			addFailure("connection lost mid-sweep")
			return
		}
		p.L(lport(port)).Click()
		p.Wait(200)
		picks := autoPicks(p, port)
		if len(picks) == 0 {
			produced = append(produced, row{port, "-", "every item needs a person or a peer"})
			continue
		}
		fmt.Printf("  ... %s: %s\n", port, strings.Join(picks, ", "))
		runBothGroups(p, picks)
		// One read: the page replaces libData every poll, so a second evaluate may see a different one.
		var got J
		if try(func() {
			got = p.WaitFunc(`([ids, p]) => {
                if (libData.running) { return null; }
                const rows = [];
                for (const i of ids) {
                  const s = (libData.states || {})[i];
                  if (!s || s.running || !s.outcome) { return null; }
                  rows.push([i, s.outcome, ((s.reason || '') + ' ' + (s.detail || '')).trim()]);
                }
                return { rows, verdict: (libData.ports || {})[p] || '' };
              }`, []any{picks, port}, 120000)
		}) != nil {
			check(false, port+" finishes its run")
			p.Eval("libStop()")
			continue
		}
		verdict := got.K("verdict").S()
		var notes []string
		for _, r := range got.K("rows").Arr() {
			var parts []string
			for _, x := range r.Strings() {
				if x != "" {
					parts = append(parts, x)
				}
			}
			notes = append(notes, strings.Join(parts, " "))
		}
		note := strings.Join(notes, "; ")
		produced = append(produced, row{port, verdict, note})
		want, why := "pass", ""
		if !isSim(com) {
			want, why = "either", "a real bench: wiring decides"
		} else if e, ok := sweepExpectSim[port]; ok {
			want, why = e[0], e[1]
		}
		if want == "either" {
			check(verdict == "pass" || verdict == "fail" || verdict == "unjudged",
				port+" reaches a port verdict ("+why+")", verdict+" / "+note)
		} else {
			check(verdict == want, port+": port verdict "+want+" on this bench", verdict+" / "+note)
		}
	}
	Section("what the bench produced")
	for _, r := range produced {
		fmt.Printf("  %-7s %-8s %s\n", r.port, r.verdict, cut(r.note, 160))
	}
}
