package ptlib

// Multi-channel items (decision 129 「多路」, PORTTOOL-FLOW.md C.3.6 「多路的项」):
// each channel has its own result and the item none; the item's Outcome is
// only their worst, for stop-on-fail and the like.

import (
	"context"
	"strconv"
	"strings"
	"time"

	"PortTool/src/ptboard"
	"PortTool/src/ptcheck"
	"PortTool/src/ptplan"

	"PortTool/src/ptitems"
	"PortTool/src/ptreport"
)

// Channels is the channels a picked item reports a result for each, nil for
// an item with one result.
func Channels(it ptitems.Item, peer ptitems.Peer, params map[string]string) []int {
	switch {
	case it.ID == "din.level" && peer == ptitems.PeerHuman:
		return WalkChannels(params)
	case it.ID == "din.level" && peer == ptitems.PeerRef:
		return []int{1, 2, 3, 4, 5, 6, 7, 8} // the plan walks all eight
	case it.ID == "dout.load" && (peer == ptitems.PeerHuman || peer == ptitems.PeerRef):
		return dropChannels(params)
	case cardSession[it.ID] && hasCh(params):
		// An R&D card's session ended with ticked rows: only those (live.go liveTicked).
		var out []int
		for _, f := range strings.Split(params["ch"], ",") {
			if n, err := strconv.Atoi(f); err == nil {
				out = append(out, n)
			}
		}
		return out
	case it.ID == "din.quad":
		return []int{1, 2, 3, 4} // four encoder groups
	case it.ID == "ain.live", it.ID == "aout.manual", it.ID == "temp.read":
		return []int{1, 2}
	case it.ID == "dout.manual":
		return []int{1, 2, 3, 4, 5, 6, 7, 8}
	case it.ID == relayItem:
		var out []int
		for _, f := range strings.Split(params["ch"], ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(f)); err == nil && n >= 1 && n <= relayCount {
				out = append(out, n)
			}
		}
		return out
	case it.Load:
		return []int{1, 2}
	}
	return nil
}

// cardSession are the R&D cards' session items (decision 137).
var cardSession = map[string]bool{"ain.live": true, "aout.manual": true, "din.quad": true, "dout.manual": true}

func hasCh(params map[string]string) bool { _, ok := params["ch"]; return ok }

// stepChan is the DI channel a din.level plan step belongs to: digital-in is
// DI1, digital-in-N and its companion digital-in-ref-N are DIN; 0 for none.
func stepChan(id string) int {
	if id == "digital-in" {
		return 1
	}
	for _, p := range []string{"digital-in-ref-", "digital-in-"} {
		if n, err := strconv.Atoi(strings.TrimPrefix(id, p)); err == nil && strings.HasPrefix(id, p) {
			return n
		}
	}
	return 0
}

// stepChans gives each DI of a plan-walked din.level its worst step's result.
func stepChans(e *Engine, j *job, steps []ptreport.StepResult) {
	if j.item.ID != "din.level" {
		return
	}
	by := map[int][]ptreport.ChannelResult{}
	for _, st := range steps {
		if ch := stepChan(st.ID); ch > 0 && st.Outcome != ptreport.OutcomeSkipped {
			by[ch] = append(by[ch], ptreport.ChannelResult{Outcome: st.Outcome})
		}
	}
	for ch, rs := range by {
		e.setChan(j, ch, worst(rs), "")
	}
}

// ChanName is how a channel is called on screen and in the report; the page
// words temperatures itself (lib.chan.temp).
func ChanName(port string, ch int) string {
	p := map[string]string{"din": "DI", "dout": "DO", "relay": "RY", "aout": "AO", "ain": "AI", "temp": "T"}[port]
	return p + strconv.Itoa(ch)
}

// chanLabel is ChanName, except a quadrature group is named by its two inputs.
func chanLabel(it ptitems.Item, ch int) string {
	if it.ID == "din.quad" {
		return "DI" + strconv.Itoa(2*ch-1) + "/DI" + strconv.Itoa(2*ch)
	}
	return ChanName(it.Port, ch)
}

// checkChans gives each channel of an item judged by plan checks its own
// result: a failed check on ch<n> fails channel n, a failed check on no
// channel (vdda, ok, mode) fails them all; a channel no check names was only
// recorded (temp.read, decision 129).
func (e *Engine) checkChans(j *job, chs []int, checks []ptcheck.Result) {
	named := map[int]bool{}
	failed := map[int]bool{}
	all := false
	for _, r := range checks {
		f := r.Check.Field
		if i := strings.IndexByte(f, '['); i >= 0 {
			f = f[:i]
		}
		n, err := strconv.Atoi(strings.TrimPrefix(f, "ch"))
		switch {
		case strings.HasPrefix(f, "ch") && err == nil:
			named[n] = true
			failed[n] = failed[n] || !r.Pass
		case !r.Pass:
			all = true
		}
	}
	for _, c := range chs {
		switch {
		case all || failed[c]:
			e.setChan(j, c, ptreport.OutcomeFail, "")
		case named[c]:
			e.setChan(j, c, ptreport.OutcomePass, "")
		default:
			e.setChan(j, c, Recorded, "")
		}
	}
}

// worst is the outcome that decides what runs next: a failure is not hidden
// by a channel that was stopped (rank in run.go).
func worst(rs []ptreport.ChannelResult) ptreport.Outcome {
	o, judged := ptreport.OutcomePass, false
	for _, r := range rs {
		if r.Outcome == Recorded { // a recorded channel judged nothing
			continue
		}
		judged = true
		if rank(r.Outcome) > rank(o) {
			o = r.Outcome
		}
	}
	if !judged && len(rs) > 0 {
		return Recorded
	}
	return o
}

// Worst is the same for callers outside ptlib.
func Worst(rs []ptreport.ChannelResult) ptreport.Outcome { return worst(rs) }

// setChan records one channel's own result.
func (e *Engine) setChan(j *job, ch int, o ptreport.Outcome, reason string) {
	e.setChanDetail(j, ch, o, reason, "")
}

// setChanDetail is setChan with the detail the reason's sentence needs.
func (e *Engine) setChanDetail(j *job, ch int, o ptreport.Outcome, reason, detail string) {
	e.change(j.item.ID, func(s *State) {
		for i := range s.Chans {
			if s.Chans[i].Ch == ch {
				s.Chans[i].Outcome, s.Chans[i].Reason, s.Chans[i].Detail = o, reason, detail
				return
			}
		}
		s.Chans = append(s.Chans, ptreport.ChannelResult{Ch: ch, Name: chanLabel(j.item, ch), Outcome: o, Reason: reason, Detail: detail})
	})
}

// fillChans gives every channel without a result of its own the item's
// ending (refused before it ran, stopped before it got there), and returns
// the item's outcome as their worst.
func (e *Engine) fillChans(j *job, o ptreport.Outcome, reason string) ptreport.Outcome {
	chs := Channels(j.item, j.peer, j.params)
	if len(chs) == 0 || o == ptreport.OutcomeSkipped {
		return o
	}
	have := map[int]bool{}
	e.mu.Lock()
	for _, c := range e.states[j.item.ID].Chans {
		have[c.Ch] = c.Outcome != ""
	}
	checks := e.states[j.item.ID].Checks
	e.mu.Unlock()
	// Items judged by plan checks name their channels in the fields.
	byChecks := j.item.ID == "temp.read"
	if byChecks && len(have) == 0 && (o == ptreport.OutcomePass || o == ptreport.OutcomeFail) && len(checks) > 0 {
		e.checkChans(j, chs, checks)
		have = map[int]bool{}
		e.mu.Lock()
		for _, c := range e.states[j.item.ID].Chans {
			have[c.Ch] = true
		}
		e.mu.Unlock()
	}
	for _, ch := range chs {
		if !have[ch] {
			e.setChan(j, ch, o, reason)
		}
	}
	e.mu.Lock()
	rs := append([]ptreport.ChannelResult(nil), e.states[j.item.ID].Chans...)
	e.results[j.item.ID].Channels = rs
	e.mu.Unlock()
	return worst(rs)
}

// rewireDin points each DI beat's reference DO at the channel the
// registration wires to that DI, drops the beats of a DI wired to nothing
// (marked unwired), and closes the relays a DI runs through for the walk
// (decision 135). The returned stop opens them again.
func (e *Engine) rewireDin(ctx context.Context, j *job) (func(), ptreport.Outcome, string) {
	w := e.wiring()
	var steps []ptplan.Step
	var rys []int
	drop := map[int]bool{}
	for _, st := range j.steps {
		ch := stepChan(st.ID)
		if ch == 0 {
			steps = append(steps, st)
			continue
		}
		l := w.DILink(ch)
		if l.To == 0 {
			if !drop[ch] {
				e.setChan(j, ch, ptreport.OutcomeError, ReasonRefUnwired)
			}
			drop[ch] = true
			continue
		}
		if st.OnRef() {
			to := strconv.Itoa(l.To)
			p := ptplan.Params{}
			for k, v := range st.Params {
				p[k] = v
			}
			p["ch"] = to
			st.Params = p
			checks := append([]ptcheck.Check(nil), st.Checks...)
			for i := range checks {
				if strings.HasPrefix(checks[i].Field, "ch") {
					checks[i].Field = "ch" + to
				}
			}
			st.Checks = checks
			if l.Via > 0 {
				rys = append(rys, l.Via)
			}
		}
		steps = append(steps, st)
	}
	j.steps = steps
	if len(rys) == 0 {
		return func() {}, "", ""
	}
	cmd := "pt.start relay ch=" + intList(rys) + " mode=hold on=1"
	att := ptreport.Attempt{StartedAt: time.Now(), Sent: []string{cmd}}
	lines, err := e.Board.Send(cmd, ptboard.ExpectOne, ptboard.DefaultTimeout)
	att.Raw = lines
	stop := func() { _, _ = e.Board.Send("pt.stop relay", ptboard.ExpectOne, ptboard.DefaultTimeout) }
	if err != nil {
		o, d := e.recordErr(j, att, err)
		return stop, o, d
	}
	return stop, "", ""
}

// liveChans gives each row of a live card its own result from what the
// person typed: matching passes, not matching fails, left blank is stopped;
// a quadrature card that missed steps fails every typed group, the miss count
// is the card's (decision 129).
func (e *Engine) liveChans(j *job, st LiveState) {
	typed := map[int]*bool{}
	for _, r := range st.Ref {
		typed[r.Ch] = r.Pass
	}
	for _, c := range Channels(j.item, j.peer, j.params) {
		p, ok := typed[c]
		switch {
		case !ok || p == nil:
			e.setChan(j, c, ptreport.OutcomeStopped, ReasonStopped)
		case !*p || (st.Kind == "quad" && st.Err > 0):
			e.setChan(j, c, ptreport.OutcomeFail, "")
		default:
			e.setChan(j, c, ptreport.OutcomePass, "")
		}
	}
}
