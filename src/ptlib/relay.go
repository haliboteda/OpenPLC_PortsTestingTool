package ptlib

// Relay switching (decision 123, PORTTOOL-FLOW.md B.2 and C.3.6 「继电器开合」):
// every ticked relay with its own closed hold, open hold and count; the board
// times and counts and stops each relay at its count. Parallel is one
// mode=cycle session with every ticked relay; serial is one session per relay
// in relay order, the next once the last reported done=1.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"PortTool/src/ptboard"
	"PortTool/src/ptcheck"
	"PortTool/src/ptitems"
	"PortTool/src/ptplan"
	"PortTool/src/ptproto"
	"PortTool/src/ptreport"
)

// relayItem is the one relay item (ptitems.Catalogue).
const relayItem = "relay.cycle"

// RunsToCount says an item joins a timed phase and stops at its own count
// instead of running first on its own (decision 123): the relay item.
func RunsToCount(it ptitems.Item) bool { return it.ID == relayItem }

// relayCount is the number of relays on the board (PORTTOOL-FLOW.md B.2).
const relayCount = 6

// RelayForDuration is the relay item's params for a test with duration d: each
// ticked relay's cycle is d ÷ its count, closed and open half each, so the
// relays end with the test; one after another they share d evenly. Only the
// count is the person's (decision 123, PORTTOOL-FLOW.md C.3.6 「继电器开合」).
func RelayForDuration(params map[string]string, d time.Duration) map[string]string {
	out := map[string]string{}
	for k, v := range params {
		out[k] = v
	}
	counts, _ := relayValues(params["count"])
	var chs []int
	for _, f := range strings.Split(params["ch"], ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(f)); err == nil && counts[n] >= 1 {
			chs = append(chs, n)
		}
	}
	sort.Ints(chs)
	span := d
	if params[ptitems.ParamOrder] == "ser" && len(chs) > 0 {
		span = d / time.Duration(len(chs))
	}
	var half []string
	for _, ch := range chs {
		ms := span.Milliseconds() / int64(counts[ch]) / 2
		half = append(half, strconv.Itoa(ch)+":"+strconv.FormatInt(ms, 10))
	}
	if len(half) > 0 {
		out["close"], out["open"] = strings.Join(half, ","), strings.Join(half, ",")
	}
	out[ptitems.ParamBy] = "time"
	delete(out, ptitems.ParamMinutes)
	return out
}

// RelayHostParamOK checks a relay parameter kept on the PC.
func RelayHostParamOK(k, v string) bool {
	switch k {
	case ptitems.ParamOrder:
		return v == "par" || v == "ser"
	case ptitems.ParamBy:
		return v == "gap" || v == "time"
	case ptitems.ParamMinutes:
		m, ok := relayValues(v)
		for _, x := range m {
			ok = ok && x > 0
		}
		return ok
	}
	return true
}

// relayValues reads "2000" (every relay) or "1:2000,2:1500" (per relay).
func relayValues(v string) (map[int]float64, bool) {
	out := map[int]float64{}
	v = strings.TrimSpace(v)
	if v == "" {
		return out, false
	}
	if !strings.Contains(v, ":") {
		x, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return out, false
		}
		for ch := 1; ch <= relayCount; ch++ {
			out[ch] = x
		}
		return out, true
	}
	for _, part := range strings.Split(v, ",") {
		c, x, ok := strings.Cut(part, ":")
		ch, err1 := strconv.Atoi(c)
		f, err2 := strconv.ParseFloat(x, 64)
		if !ok || err1 != nil || err2 != nil || ch < 1 || ch > relayCount {
			return out, false
		}
		if _, dup := out[ch]; dup {
			return out, false
		}
		out[ch] = f
	}
	return out, true
}

// RelayChan is one ticked relay: what it was asked for and how far it got.
type RelayChan struct {
	Ch      int    `json:"ch"`
	CloseMS uint64 `json:"close_ms"`
	OpenMS  uint64 `json:"open_ms"`
	Count   uint64 `json:"count"`
	N       uint64 `json:"n"`      // cycles completed, from the frame's n<ch>
	Seen    uint64 `json:"seen"`   // cycles the reference DI counted, on the chain (chain.go)
	Ref     int    `json:"ref"`    // the reference DI counting it on the chain; 0 when the registration wires none
	Closed  bool   `json:"closed"` // the frame's ch<ch>
	Phase   string `json:"phase"`  // queue | run | done | stopped
}

// RelayState is what the card draws while the item runs and after.
type RelayState struct {
	Order string      `json:"order"`
	Chans []RelayChan `json:"chans"`
}

// relayPlan reads the ticked relays and their values, checked against the
// board's limits; every ticked relay needs all three values.
func relayPlan(params map[string]string, caps ptproto.Caps) ([]RelayChan, error) {
	var chs []int
	for _, f := range strings.Split(params["ch"], ",") {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		n, err := strconv.Atoi(f)
		if err != nil || n < 1 || n > relayCount {
			return nil, fmt.Errorf("ch=%q must be relays 1..%d", params["ch"], relayCount)
		}
		chs = append(chs, n)
	}
	if len(chs) == 0 {
		return nil, errors.New("no relay ticked")
	}
	sort.Ints(chs) // serial runs in relay order (PORTTOOL-FLOW.md C.3.6)
	port, _ := caps.Port("relay")
	vals := map[string]map[int]float64{}
	for _, k := range []string{"close", "open", "count"} {
		m, ok := relayValues(params[k])
		if params[k] != "" && !ok {
			return nil, fmt.Errorf("%s=%q is not a number or relay:number pairs", k, params[k])
		}
		vals[k] = m
	}
	var out []RelayChan
	for _, ch := range chs {
		c := RelayChan{Ch: ch, Phase: "queue"}
		for _, k := range []string{"close", "open", "count"} {
			x, ok := vals[k][ch]
			if !ok {
				return nil, fmt.Errorf("relay %d has no %s", ch, k)
			}
			if x != float64(uint64(x)) || x < 1 {
				return nil, fmt.Errorf("relay %d: %s=%g must be a whole number", ch, k, x)
			}
			if lim, ok := port.Limits[k]; ok {
				if fine, why := lim.Accepts(strconv.FormatFloat(x, 'f', -1, 64)); !fine {
					return nil, fmt.Errorf("relay %d: %s=%g %s (%s)", ch, k, x, why, lim.Spec)
				}
			}
			switch k {
			case "close":
				c.CloseMS = uint64(x)
			case "open":
				c.OpenMS = uint64(x)
			default:
				c.Count = uint64(x)
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// relayArg is one value for every relay of the session when they agree, else
// ch:value pairs (PORTTOOL-FLOW.md B.2).
func relayArg(set []RelayChan, get func(RelayChan) uint64) string {
	same := true
	for _, c := range set {
		same = same && get(c) == get(set[0])
	}
	if same {
		return strconv.FormatUint(get(set[0]), 10)
	}
	parts := make([]string, len(set))
	for i, c := range set {
		parts[i] = fmt.Sprintf("%d:%d", c.Ch, get(c))
	}
	return strings.Join(parts, ",")
}

func relayStart(set []RelayChan) string {
	chs := make([]string, len(set))
	for i, c := range set {
		chs[i] = strconv.Itoa(c.Ch)
	}
	return "pt.start relay ch=" + strings.Join(chs, ",") + " mode=cycle" +
		" close=" + relayArg(set, func(c RelayChan) uint64 { return c.CloseMS }) +
		" open=" + relayArg(set, func(c RelayChan) uint64 { return c.OpenMS }) +
		" count=" + relayArg(set, func(c RelayChan) uint64 { return c.Count })
}

// runRelay runs the ticked relays to their counts. Recorded when every one
// reached its count; the verdict follows in relayVerdict. On the chain the
// relays' DOs are held high and the reference DI counts (chain.go); held says
// the timed phase already holds the deadman.
func (e *Engine) runRelay(ctx context.Context, j *job, held bool) (ptreport.Outcome, string, string) {
	chans, err := relayPlan(j.params, e.Caps)
	if err != nil {
		return ptreport.OutcomeError, "", err.Error()
	}
	st := RelayState{Order: j.params[ptitems.ParamOrder], Chans: chans}
	if st.Order != "ser" {
		st.Order = "par"
	}
	var chain *chainCount
	show := func() {
		c := st
		c.Chans = append([]RelayChan(nil), st.Chans...)
		if chain != nil {
			for i := range c.Chans {
				c.Chans[i].Seen = chain.count(c.Chans[i].Ch)
			}
		}
		e.change(j.item.ID, func(s *State) { s.Relay = &c })
	}
	att := ptreport.Attempt{StartedAt: time.Now()}
	att.SetExtra("order", st.Order)
	finish := func(o ptreport.Outcome, reason, why string) (ptreport.Outcome, string, string) {
		for i := range st.Chans {
			c := &st.Chans[i]
			if o != Recorded && c.Phase != "done" {
				c.Phase, c.Closed = "stopped", false
			}
			p := "relay" + strconv.Itoa(c.Ch)
			att.SetExtra(p+"_close_ms", strconv.FormatUint(c.CloseMS, 10))
			att.SetExtra(p+"_open_ms", strconv.FormatUint(c.OpenMS, 10))
			att.SetExtra(p+"_count", strconv.FormatUint(c.Count, 10))
			att.SetExtra(p+"_done", strconv.FormatUint(c.N, 10))
			if chain != nil {
				c.Seen = chain.count(c.Ch)
				att.SetExtra(p+"_seen", strconv.FormatUint(c.Seen, 10))
			}
		}
		show()
		j.relayRan = append([]RelayChan(nil), st.Chans...)
		att.Outcome, att.Err = o, why
		att.DurationMS = time.Since(att.StartedAt).Milliseconds()
		e.addAttempts(j, att)
		return o, reason, why
	}
	show()
	if j.peer == ptitems.PeerRef {
		c, stop, err := e.startChain(chans, held, &att)
		defer stop()
		switch {
		case errors.Is(err, errNoRef):
			return finish(ptreport.OutcomeError, ReasonNoRef, "")
		case err != nil:
			return finish(ptreport.OutcomeError, "", err.Error())
		}
		chain = c
	}

	var sets [][]int // indexes into st.Chans, one session each
	if st.Order == "ser" {
		for i := range st.Chans {
			sets = append(sets, []int{i})
		}
	} else {
		all := make([]int, len(st.Chans))
		for i := range all {
			all[i] = i
		}
		sets = [][]int{all}
	}

	events, unsub := e.Board.Subscribe(256)
	defer unsub()
	for _, set := range sets {
		run := make([]RelayChan, len(set))
		var hold uint64
		for k, i := range set {
			st.Chans[i].Phase = "run"
			run[k] = st.Chans[i]
			hold = max(hold, st.Chans[i].CloseMS, st.Chans[i].OpenMS)
		}
		show()
		cmd := relayStart(run)
		att.Sent = append(att.Sent, cmd)
		lines, err := e.Board.Send(cmd, ptboard.ExpectOne, ptboard.DefaultTimeout)
		att.Raw = append(att.Raw, lines...)
		if err != nil {
			e.relayStop(&att)
			var refused *ptboard.RefusedError
			if errors.As(err, &refused) {
				return finish(ptreport.OutcomeFail, "", refused.Reason)
			}
			return finish(ptreport.OutcomeError, "", err.Error())
		}
		// A frame per edge: the longest hold of this session bounds the gap.
		timeout := time.Duration(hold)*time.Millisecond + time.Duration(ptplan.DefaultTimeoutMS)*time.Millisecond
		o, why := e.relayWatch(ctx, events, &st, set, timeout, &att, show)
		e.relayStop(&att)
		if o != "" {
			return finish(o, "", why)
		}
	}
	if chain != nil && len(chain.refOf) > 0 {
		// The last open has to hold its settle frames before it counts.
		chain.settleWait(ctx, chain.settle+2)
		if chain.gapErr != "" {
			return finish(ptreport.OutcomeError, "", chain.gapErr)
		}
	}
	return finish(Recorded, "", "")
}

// relayVerdict judges each relay by the cycles its load fed back against its
// count: typed by a person, or counted by the reference DI on the chain; the
// board's own n only says the coil was driven (decisions 130, 133).
func (e *Engine) relayVerdict(ctx context.Context, j *job) (ptreport.Outcome, string) {
	if j.peer == ptitems.PeerRef {
		return e.relayChainVerdict(j)
	}
	att := ptreport.Attempt{StartedAt: time.Now(), Sent: []string{"ask the operator for each relay's cycles"}}
	defer func() {
		att.DurationMS = time.Since(att.StartedAt).Milliseconds()
		e.addAttempts(j, att)
		e.change(j.item.ID, func(s *State) { s.Checks = att.Checks })
	}()
	failed, blank := false, false
	for _, c := range j.relayRan {
		ch := strconv.Itoa(c.Ch)
		a, err := e.Ask(ctx, Question{Item: j.item.ID, Kind: ptitems.AnswerNumber, Channel: ch,
			Set: float64(c.Count), Walk: "relay"})
		if err != nil {
			att.Outcome = ptreport.OutcomeStopped
			return ptreport.OutcomeStopped, ReasonStopped
		}
		typed := strings.TrimSpace(a.Value)
		att.SetExtra("relay"+ch+"_seen", typed)
		if typed == "" {
			blank = true
			if e.RD {
				e.setChan(j, c.Ch, ptreport.OutcomeStopped, ReasonStopped)
			} else {
				e.setChan(j, c.Ch, ptreport.OutcomeError, ReasonNoReading)
			}
			continue
		}
		want := strconv.FormatUint(c.Count, 10)
		checks, pass := ptcheck.EvalAll([]ptcheck.Check{{Field: "relay" + ch, Op: ptcheck.OpEq, Value: want}},
			func(string) (string, bool) { return typed, true })
		att.Checks = append(att.Checks, checks...)
		failed = failed || !pass
		if pass {
			e.setChan(j, c.Ch, ptreport.OutcomePass, "")
		} else {
			e.setChan(j, c.Ch, ptreport.OutcomeFail, "")
		}
	}
	switch {
	case failed:
		att.Outcome = ptreport.OutcomeFail
	case blank && e.RD:
		att.Outcome = ptreport.OutcomeStopped
		return att.Outcome, ReasonStopped // not measured to the end (decision 129)
	case blank:
		att.Outcome = ptreport.OutcomeError
		return att.Outcome, ReasonNoReading
	default:
		att.Outcome = ptreport.OutcomePass
	}
	return att.Outcome, ""
}

// relayChainVerdict compares what the reference DI counted with each count.
func (e *Engine) relayChainVerdict(j *job) (ptreport.Outcome, string) {
	att := ptreport.Attempt{StartedAt: time.Now(), Sent: []string{"compare the reference DI's counts"}}
	var checks []ptcheck.Check
	seen := map[string]string{}
	var wired []RelayChan
	for _, c := range j.relayRan {
		if c.Ref == 0 { // on no DO, or on a DO wired to nothing (decision 135)
			e.setChan(j, c.Ch, ptreport.OutcomeError, ReasonRefUnwired)
			continue
		}
		wired = append(wired, c)
	}
	for _, c := range wired {
		f := "relay" + strconv.Itoa(c.Ch)
		checks = append(checks, ptcheck.Check{Field: f, Op: ptcheck.OpEq, Value: strconv.FormatUint(c.Count, 10)})
		seen[f] = strconv.FormatUint(c.Seen, 10)
	}
	res, pass := ptcheck.EvalAll(checks, func(f string) (string, bool) { v, ok := seen[f]; return v, ok })
	for i, c := range wired {
		if res[i].Pass {
			e.setChan(j, c.Ch, ptreport.OutcomePass, "")
		} else {
			e.setChan(j, c.Ch, ptreport.OutcomeFail, ReasonCheckChain)
		}
	}
	att.Checks, att.Outcome = res, ptreport.OutcomePass
	reason := ""
	if !pass {
		att.Outcome, reason = ptreport.OutcomeFail, ReasonCheckChain
	}
	e.addAttempts(j, att)
	e.change(j.item.ID, func(s *State) { s.Checks = res })
	return att.Outcome, reason
}

func (e *Engine) relayStop(att *ptreport.Attempt) {
	att.Sent = append(att.Sent, "pt.stop relay")
	l, _ := e.Board.Send("pt.stop relay", ptboard.ExpectOne, ptboard.DefaultTimeout)
	att.Raw = append(att.Raw, l...)
}

// relayWatch follows one session's frames until done=1; "" when it got there.
// Only the latest frame is kept in the report: a long run has millions.
func (e *Engine) relayWatch(ctx context.Context, events <-chan ptboard.Event, st *RelayState, set []int,
	timeout time.Duration, att *ptreport.Attempt, show func()) (ptreport.Outcome, string) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	last := ""
	keep := func() {
		if last != "" {
			att.Raw = append(att.Raw, last)
		}
	}
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				keep()
				return ptreport.OutcomeError, "the board stopped answering"
			}
			if ev.Kind != ptproto.LineFrame || ev.Frame.Port != "relay" {
				continue
			}
			last = ev.Line
			timer.Reset(timeout)
			for _, i := range set {
				c := &st.Chans[i]
				p := strconv.Itoa(c.Ch)
				if v, ok := ptproto.Get(ev.Frame.Fields, "ch"+p); ok {
					c.Closed = v == "1"
				}
				if n, ok := ptproto.GetU32(ev.Frame.Fields, "n"+p); ok {
					c.N = uint64(n)
				}
				if c.N >= c.Count {
					c.Phase, c.Closed = "done", false
				}
			}
			show()
			if v, _ := ptproto.Get(ev.Frame.Fields, "done"); v == "1" {
				keep()
				return "", ""
			}
		case <-timer.C:
			keep()
			return ptreport.OutcomeTimeout, fmt.Sprintf("no frame from relay within %s", timeout)
		case <-ctx.Done():
			keep()
			return ptreport.OutcomeStopped, ""
		}
	}
}

// boardLineMax is the longest line the board takes from the PC
// (PORTTOOL_LINE_MAX, PORTTOOL-FLOW.md A.3).
const boardLineMax = 256

// relayCheck refuses a relay pick before the board is touched: bad values, or
// a pt.start line the board would drop as too long (decision 123).
func relayCheck(it ptitems.Item, caps ptproto.Caps, params map[string]string) error {
	if it.ID != relayItem {
		return nil
	}
	chans, err := relayPlan(params, caps)
	if err != nil {
		return &PickError{Code: "param_refused", Item: it.ID, Param: "ch", Why: err.Error()}
	}
	sets := [][]RelayChan{chans}
	if params[ptitems.ParamOrder] == "ser" {
		sets = nil
		for _, c := range chans {
			sets = append(sets, []RelayChan{c})
		}
	}
	for _, set := range sets {
		if n := len(relayStart(set)); n > boardLineMax {
			return &PickError{Code: "line_too_long", Item: it.ID, Why: fmt.Sprintf("%d > %d", n, boardLineMax)}
		}
	}
	return nil
}
