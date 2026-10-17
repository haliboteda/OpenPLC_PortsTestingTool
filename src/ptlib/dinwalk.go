package ptlib

// DI level walked by a person in R&D (decisions 128, 133, PORTTOOL-FLOW.md
// C.3.6 "DI 逐路电平（研发）"): one din session reads all eight inputs;
// channel by channel the person applies 24 V and the board's reading, judged
// by that channel's plan step, answers.

import (
	"context"
	"strconv"
	"strings"
	"time"

	"PortTool/src/ptboard"
	"PortTool/src/ptcheck"
	"PortTool/src/ptitems"
	"PortTool/src/ptproto"
	"PortTool/src/ptreport"
	"PortTool/src/ptseq"
)

// dinWalks are items a person walks input by input.
var dinWalks = map[string]bool{"din.level": true}

// The walk's timing; the values are in PORTTOOL-FLOW.md C.3.6.
const (
	dinStable = 3  // frames in a row reading exactly this input
	DinWrongS = 2  // another input high this many seconds fails the channel
	DinWaitS  = 60 // neither within this many seconds fails the channel

	dinWrong = DinWrongS * time.Second
	dinWait  = DinWaitS * time.Second
)

// Why a channel of the walk ended.
const (
	DinOK    = "ok"
	DinWrong = "wrong" // another input high: wrong terminal or two inputs shorted
	DinNone  = "none"  // no 24 V seen on this input
)

// DinRow is one channel of the walk, for the card.
type DinRow struct {
	Ch   int    `json:"ch"`
	Want string `json:"want"` // the plan's expected v
	Seen string `json:"seen"` // the board's v when it ended
	Why  string `json:"why"`
	Pass bool   `json:"pass"`
}

// DinWalkResult is what the card draws.
type DinWalkResult struct {
	On   int      `json:"on"`            // the channel being asked now, 0 between
	Now  string   `json:"now,omitempty"` // the board's latest v
	Rows []DinRow `json:"rows"`
}

// dinJudge decides one channel from the frames as they come.
type dinJudge struct {
	streak     int
	wrongSince time.Time
}

// frame takes one reading: match says the plan step passed, high that an
// input that must not be high reads 1. It returns DinOK, DinWrong or "" while undecided.
func (d *dinJudge) frame(match, high bool, now time.Time) string {
	if match {
		d.wrongSince = time.Time{}
		d.streak++
		if d.streak >= dinStable {
			return DinOK
		}
		return ""
	}
	d.streak = 0
	if !high {
		d.wrongSince = time.Time{}
		return ""
	}
	if d.wrongSince.IsZero() {
		d.wrongSince = now
	}
	if now.Sub(d.wrongSince) >= dinWrong {
		return DinWrong
	}
	return ""
}

// dinOther says an input outside mask reads 1. mask holds this channel and
// the channels already passed: a wire still on the last input is the person
// moving it, and a short to it was caught on its own turn.
func dinOther(v string, mask uint64) bool {
	n, err := strconv.ParseUint(strings.TrimSpace(v), 0, 32)
	return err == nil && n&^mask != 0
}

func (e *Engine) runDinWalk(ctx context.Context, j *job) (ptreport.Outcome, string, string) {
	res := DinWalkResult{}
	show := func() {
		c := res
		c.Rows = append([]DinRow(nil), res.Rows...)
		e.change(j.item.ID, func(s *State) { s.DinWalk = &c })
	}
	show()
	att := ptreport.Attempt{N: 1, StartedAt: time.Now()}
	started := false
	var send func(cmd string) error
	finish := func(o ptreport.Outcome, why string) (ptreport.Outcome, string, string) {
		if started {
			_ = send("pt.stop din")
		}
		att.Outcome, att.Err = o, why
		att.DurationMS = time.Since(att.StartedAt).Milliseconds()
		e.addAttempts(j, att)
		return o, "", why
	}
	send = func(cmd string) error {
		att.Sent = append(att.Sent, cmd)
		lines, err := e.Board.Send(cmd, ptboard.ExpectOne, ptboard.DefaultTimeout)
		att.Raw = append(att.Raw, lines...)
		return err
	}

	// One session for the whole walk, all eight inputs, so another input
	// going high is seen whatever channels were picked.
	st := j.steps[0]
	st.Params = map[string]string{}
	for k, v := range j.steps[0].Params {
		st.Params[k] = v
	}
	st.Params["mode"], st.Params["ch"] = "level", "1,2,3,4,5,6,7,8"
	events, unsub := e.Board.Subscribe(256)
	defer unsub()
	if err := send(startLine(st)); err != nil {
		return finish(ptreport.OutcomeError, err.Error())
	}
	started = true

	failed, passed := false, uint64(0)
	for _, ch := range dropChannels(j.params) {
		if ch > len(j.steps) {
			continue
		}
		step := j.steps[ch-1]
		row := DinRow{Ch: ch}
		for _, c := range step.Checks {
			if c.Field == "v" {
				row.Want = c.Value
			}
		}
		res.On = ch
		show()

		askCtx, cancel := context.WithCancel(ctx)
		asked, done := make(chan error, 1), make(chan struct{})
		go func() {
			defer close(done)
			_, err := e.Ask(askCtx, Question{Item: j.item.ID, Kind: ptitems.AnswerWait,
				Channel: "DI" + strconv.Itoa(ch), Walk: "din"})
			asked <- err
		}()
		why, checks, stopped, gone := e.dinChannel(ctx, events, step.Checks, passed|1<<(ch-1), &res, show, asked)
		cancel()
		<-done
		res.On = 0
		if gone != nil {
			show()
			return finish(ptreport.OutcomeError, gone.Error())
		}
		if stopped {
			show()
			return finish(ptreport.OutcomeStopped, "")
		}
		row.Seen, row.Why, row.Pass = res.Now, why, why == DinOK
		att.SetExtra("din"+strconv.Itoa(ch), why+" "+res.Now)
		att.Checks = append(att.Checks, checks...)
		failed = failed || !row.Pass
		if row.Pass {
			passed |= 1 << (ch - 1)
		}
		res.Rows = append(res.Rows, row)
		if row.Pass {
			e.setChan(j, ch, ptreport.OutcomePass, "")
		} else {
			e.setChan(j, ch, ptreport.OutcomeFail, "")
		}
		show()
	}
	if failed {
		return finish(ptreport.OutcomeFail, "")
	}
	return finish(ptreport.OutcomePass, "")
}

// dinChannel waits for one channel's verdict. stopped: the person pressed
// stop or the run ended; gone: the board went away.
func (e *Engine) dinChannel(ctx context.Context, events <-chan ptboard.Event, checks []ptcheck.Check, mask uint64,
	res *DinWalkResult, show func(), asked <-chan error) (why string, last []ptcheck.Result, stopped bool, gone error) {
	// What queued up during the last channel is not this channel's reading.
	for drained := false; !drained; {
		select {
		case _, ok := <-events:
			if !ok {
				return "", nil, false, errBoardGone
			}
		default:
			drained = true
		}
	}
	timer := time.NewTimer(dinWait)
	defer timer.Stop()
	d := dinJudge{}
	for {
		select {
		case <-ctx.Done():
			return "", last, true, nil
		case err := <-asked:
			if err != nil {
				return "", last, true, nil
			}
			asked = nil // answered without stopping: keep watching the board
		case <-timer.C:
			return DinNone, last, false, nil
		case ev, ok := <-events:
			if !ok {
				return "", last, false, errBoardGone
			}
			if ev.Kind != ptproto.LineFrame || ev.Frame.Port != "din" {
				continue
			}
			look := ptseq.FrameLookup(ev.Frame)
			v, _ := look("v")
			if v != res.Now {
				res.Now = v
				show()
			}
			var match bool
			last, match = ptcheck.EvalAll(checks, look)
			if w := d.frame(match, dinOther(v, mask), time.Now()); w != "" {
				return w, last, false, nil
			}
		}
	}
}
