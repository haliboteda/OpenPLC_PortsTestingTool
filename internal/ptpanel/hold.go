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
	"PortTool/internal/ptproto"
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
		writeErr(w, 400, m("go.hold.bad_request"))
		return
	}

	if body.Stop {
		s.stopTimedRun(m("go.reason.you_stopped"))
		writeJSON(w, 200, s.stateJSON())
		return
	}

	if body.Hours < 0 || body.Hours > 4 {
		writeErr(w, 400, m("go.hold.bad_hours"))
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
		writeJSON(w, 200, map[string]any{"error": m("go.hold.no_board")})
		return
	}
	if already {
		writeJSON(w, 200, map[string]any{"error": m("go.hold.already")})
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
	// A lamp left lit by an earlier fault would read as this run's verdict.
	_, _ = b.Send("pt.led fault=0", ptboard.ExpectOne, holdTimeout)
	go s.watchRun(b, run)

	if run.deadline.IsZero() {
		s.say(m("go.hold.started_forever"))
	} else {
		s.say(m("go.hold.started", "hours", hours, "until", run.deadline.Format("15:04:05")))
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
					s.finishTimedRun(run, m("go.reason.time_up"))
					return
				}
				if !s.holdRenew(b) {
					// The board stopped answering. Nothing to do but say so:
					// its own deadman is what releases the outputs now, and it
					// will do that within holdSpan whatever happens here.
					s.say(m("go.hold.no_renew"))
					s.finishTimedRun(run, m("go.reason.no_reply"))
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
func (s *Server) finishTimedRun(run *timedRun, why msg) {
	s.mu.Lock()
	if s.run != run {
		s.mu.Unlock()
		return
	}
	s.run = nil
	b := s.board
	s.mu.Unlock()

	s.releaseBoard(b)
	s.say(m("go.hold.finished", "why", why))
	// Closed after the board is released, so the file records the stop too.
	run.log.close(s, why)
}

// stopTimedRun ends the current run from outside it, and waits for the renewal
// goroutine to be gone before returning - otherwise a stop followed straight
// away by a start could have two goroutines renewing the same deadman.
func (s *Server) stopTimedRun(why msg) {
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
	s.say(m("go.hold.finished", "why", why))
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
		writeErr(w, 400, m("go.fault.bad_request"))
		return
	}

	s.mu.Lock()
	b := s.board
	s.mu.Unlock()
	if b == nil {
		writeJSON(w, 200, map[string]any{"error": m("go.fault.no_board")})
		return
	}

	var err error
	if body.On {
		// The page words this in English already: it goes into the run log.
		why := m("go.reason.text", "text", body.Why)
		if body.Why == "" {
			why = m("go.reason.panel_fault")
		}
		err = s.raiseFault(b, why)
	} else {
		_, err = b.Send("pt.led fault=0", ptboard.ExpectOne, holdTimeout)
	}
	if err != nil {
		writeJSON(w, 200, map[string]any{"error": m("go.fault.led_failed", "detail", err)})
		return
	}
	writeJSON(w, 200, s.stateJSON())
}

// raiseFault is decision 37 item 3: stop every port, say why, light the lamp.
// Every fault - a failed verdict from the page, a reset seen by watchRun -
// goes through here, so they all look the same on the bench.
func (s *Server) raiseFault(b *ptboard.Board, why msg) error {
	s.stopTimedRun(why)
	s.say(m("go.alert.fault", "why", why))
	_, err := b.Send("pt.led fault=1", ptboard.ExpectOne, holdTimeout)
	return err
}

// silenceFloor is the shortest gap watchRun will call silence, whatever a
// port's own period: below it a busy serial line alone can delay a frame.
const silenceFloor = 5 * time.Second

// watchRun is how a run notices that the board restarted or hung underneath
// it (decision 77). Two signs, because a restart shows up either way: the
// sessions die with it, so their frames stop; and if a frame does arrive after
// one, its tick has gone backwards. A port is called silent after three of its
// own periods (learnt from the frames themselves - a period has no upper
// limit) plus silenceFloor.
func (s *Server) watchRun(b *ptboard.Board, run *timedRun) {
	events, unsub := b.Subscribe(1024)
	defer unsub()

	type seen struct {
		ticks    ptproto.TickUnwrapper
		last     time.Time
		interval time.Duration
		frames   int
	}
	watched := map[string]*seen{}
	for _, p := range run.ports {
		watched[p] = &seen{}
	}
	check := time.NewTicker(time.Second)
	defer check.Stop()

	for {
		select {
		case <-run.stop:
			return
		case <-run.done:
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			if ev.Kind != ptproto.LineFrame {
				continue
			}
			w := watched[ev.Frame.Port]
			if w == nil {
				continue
			}
			if _, restarted := w.ticks.Unwrap(ev.Frame.Tick); restarted {
				_ = s.raiseFault(b, m("go.reason.reset", "port", ev.Frame.Port))
				return
			}
			now := ev.At
			if w.frames > 0 {
				w.interval = now.Sub(w.last)
			}
			w.last = now
			w.frames++
		case now := <-check.C:
			for port, w := range watched {
				if w.frames < 2 {
					continue // no period learnt yet
				}
				if gap := now.Sub(w.last); gap > 3*w.interval+silenceFloor {
					_ = s.raiseFault(b, m("go.reason.silent", "port", port,
						"secs", int(gap.Seconds()), "period", fmt.Sprintf("%.1f", w.interval.Seconds())))
					return
				}
			}
		}
	}
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
