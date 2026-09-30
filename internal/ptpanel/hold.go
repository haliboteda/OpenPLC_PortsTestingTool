package ptpanel

// A timed run: the PC owns the clock, and the board is kept alive by a deadman
// the PC has to keep renewing.
//
// The split is deliberate (see $PROD/docs/tables/DECISIONS.md 37). Every
// verdict is made up here; the board only samples. The one thing the board
// decides on its own is whether anybody is still listening, and pt.hold is how
// it is told. Without it a PC that dies mid-run leaves the outputs driven until
// somebody presses reset - which on a burn-in rack is exactly the case nobody
// is watching.
//
// The clock lives on the server rather than in the page for the same reason:
// the server is what holds the serial port. A browser tab that is closed, or a
// laptop that sleeps, must not be able to leave 24 V on eight channels for
// hours, and here it cannot - the renewals stop with the process.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"PortTool/internal/ptboard"
)

// The deadman's span and how often it is renewed.
//
// Neither number has a physical basis; they are the trade between a false stop
// and a slow one. Too short and a busy serial line or a stalled renewal drops
// the outputs on a healthy board; too long and a genuinely dead PC leaves 24 V
// on for that much longer. At these values two renewals in a row can be lost
// before anything stops.
//
// ⚠️ The renewal rides the same UART as the sample frames, so the span has to
// stay above the worst command queueing delay. That is the same constraint the
// page's bandwidth guard enforces when ports are picked: hold both or neither.
const (
	holdSpan    = 6 * time.Second
	holdRenewal = 2 * time.Second
	holdTimeout = 800 * time.Millisecond
)

// timedRun is one "持续" run: the ports it started, when it should end, and the
// goroutine renewing the deadman until then.
type timedRun struct {
	ports    []string
	started  time.Time
	deadline time.Time // zero means 一直 - runs until somebody stops it
	stop     chan struct{}
	done     chan struct{}
	log      *runLog // nil when the file could not be opened; the run goes on
}

func (s *Server) handleHold(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ports []string `json:"ports"`
		Hours int      `json:"hours"` // 0 means 一直
		Stop  bool     `json:"stop"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "没看懂要跑哪些端口、跑多久。")
		return
	}

	if body.Stop {
		s.stopTimedRun("你点了停止")
		writeJSON(w, 200, s.stateJSON())
		return
	}

	if body.Hours < 0 || body.Hours > 4 {
		writeErr(w, 400, "时长只能选 1、2、3、4 小时，或者一直跑。")
		return
	}

	s.mu.Lock()
	b := s.board
	already := s.run != nil
	s.mu.Unlock()

	if b == nil {
		// 200 rather than 4xx: losing the board is an ordinary bench event, and
		// the page already says so in words. A 4xx would only add a red line to
		// the browser console, which case T4-02 reads as a failure.
		writeJSON(w, 200, map[string]any{"error": "板子没连上，先连上再开始。"})
		return
	}
	if already {
		writeJSON(w, 200, map[string]any{"error": "已经有一轮持续测试在跑了，先停掉它。"})
		return
	}

	s.startTimedRun(b, body.Ports, body.Hours)
	writeJSON(w, 200, s.stateJSON())
}

// startTimedRun arms the deadman and keeps it renewed until the run's own
// deadline, or until something stops it.
func (s *Server) startTimedRun(b *ptboard.Board, ports []string, hours int) {
	run := &timedRun{
		ports:   append([]string(nil), ports...),
		started: time.Now(),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	if hours > 0 {
		run.deadline = run.started.Add(time.Duration(hours) * time.Hour)
	}

	// Opened before the run is published, so the file covers the run from its
	// first frame rather than from whenever the goroutine below got scheduled.
	run.log = s.startRunLog(b, run.ports, hours)

	s.mu.Lock()
	s.run = run
	s.mu.Unlock()

	// Armed before the first renewal rather than by it: between here and the
	// first tick the outputs are already live, and an unarmed board in that
	// window would keep driving them if this process died right now.
	s.holdRenew(b)

	if run.deadline.IsZero() {
		s.emit("[持续] 开始，不限时长。板子每 6 秒要听到一次上位机还在；听不到就自己关输出。")
	} else {
		s.emit(fmt.Sprintf("[持续] 开始，跑 %d 小时，到 %s 停。板子每 6 秒要听到一次上位机还在；听不到就自己关输出。",
			hours, run.deadline.Format("15:04:05")))
	}

	go func() {
		defer close(run.done)
		t := time.NewTicker(holdRenewal)
		defer t.Stop()

		for {
			select {
			case <-run.stop:
				return
			case <-t.C:
				if !run.deadline.IsZero() && !time.Now().Before(run.deadline) {
					// The PC owns the clock, so the PC is what ends the run.
					s.finishTimedRun(run, "时间到了")
					return
				}
				if !s.holdRenew(b) {
					// The board stopped answering. Nothing to do but say so:
					// its own deadman is what releases the outputs now, and it
					// will do that within holdSpan whatever happens here.
					s.emit("[持续] 板子没有应答续期。它会在 6 秒内自己关掉输出。")
					s.finishTimedRun(run, "板子没有应答")
					return
				}
			}
		}
	}()
}

// holdRenew tells the board it has another holdSpan. Explicit renewal is the
// whole point: no other command refreshes it, so a PC that is merely echoing
// frames on a dead panel does not keep the outputs live.
func (s *Server) holdRenew(b *ptboard.Board) bool {
	cmd := fmt.Sprintf("pt.hold %d", holdSpan.Milliseconds())
	_, err := b.Send(cmd, ptboard.ExpectOne, holdTimeout)
	return err == nil
}

// finishTimedRun ends a run from inside its own goroutine.
func (s *Server) finishTimedRun(run *timedRun, why string) {
	s.mu.Lock()
	if s.run != run {
		s.mu.Unlock()
		return
	}
	s.run = nil
	b := s.board
	s.mu.Unlock()

	s.releaseBoard(b)
	s.emit("[持续] 结束：" + why + "。所有端口已停，输出已放开。")
	// Closed after the board is released, so the file records the stop too.
	run.log.close(s, why)
}

// stopTimedRun ends the current run from outside it, and waits for the renewal
// goroutine to be gone before returning - otherwise a stop followed straight
// away by a start could have two goroutines renewing the same deadman.
func (s *Server) stopTimedRun(why string) {
	s.mu.Lock()
	run := s.run
	s.run = nil
	b := s.board
	s.mu.Unlock()

	if run == nil {
		return
	}
	close(run.stop)
	<-run.done

	s.releaseBoard(b)
	s.emit("[持续] 结束：" + why + "。所有端口已停，输出已放开。")
	run.log.close(s, why)
}

// releaseBoard puts the hardware back in a safe state: sessions stopped first,
// then the deadman disarmed. That order matters - disarming first would leave a
// window where the outputs are driven and nothing is watching the PC.
func (s *Server) releaseBoard(b *ptboard.Board) {
	if b == nil {
		return
	}
	_, _ = b.Send("pt.stop all", ptboard.ExpectOne, holdTimeout)
	_, _ = b.Send("pt.hold 0", ptboard.ExpectOne, holdTimeout)
}

// handleFault is what the page calls when it has judged something failed.
//
// Every verdict is made up here, so the lamp is told what to show rather than
// deciding anything on the board. The run stops with it: a fault anywhere stops
// every port, because what production wants to know is "this board is bad", not
// which of five ports noticed first.
func (s *Server) handleFault(w http.ResponseWriter, r *http.Request) {
	var body struct {
		On  bool   `json:"on"`
		Why string `json:"why"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "没看懂是要点灯还是灭灯。")
		return
	}

	s.mu.Lock()
	b := s.board
	s.mu.Unlock()
	if b == nil {
		writeJSON(w, 200, map[string]any{"error": "板子没连上。"})
		return
	}

	if body.On {
		why := body.Why
		if why == "" {
			why = "上位机判出故障"
		}
		s.stopTimedRun(why)
		s.emit("[故障] " + why + "。状态灯已点亮。")
	}

	arg := "0"
	if body.On {
		arg = "1"
	}
	if _, err := b.Send("pt.led fault="+arg, ptboard.ExpectOne, holdTimeout); err != nil {
		writeJSON(w, 200, map[string]any{"error": "点灯这条命令板子没应答：" + err.Error()})
		return
	}
	writeJSON(w, 200, s.stateJSON())
}

// runStateJSON is what the page needs to draw the countdown and the stop button.
func (s *Server) runStateJSON() map[string]any {
	if s.run == nil {
		return nil
	}
	out := map[string]any{
		"ports":     s.run.ports,
		"elapsed_s": int(time.Since(s.run.started).Seconds()),
	}
	if s.run.log != nil {
		out["log"] = s.run.log.path
	}
	if s.run.deadline.IsZero() {
		out["left_s"] = nil // 一直
	} else {
		left := int(time.Until(s.run.deadline).Seconds())
		if left < 0 {
			left = 0
		}
		out["left_s"] = left
	}
	return out
}
