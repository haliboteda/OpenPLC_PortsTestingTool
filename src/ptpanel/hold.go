package ptpanel

// A timed run: the server owns the clock and keeps renewing the board's
// deadman (pt.hold), so a PC or page that dies mid-run cannot leave the outputs
// driven. DECISIONS.md 37 「看门狗」, PROD-CONFIG-ITEMS.md F.

import (
	"fmt"
	"time"

	"PortTool/src/ptboard"
	"PortTool/src/pthold"
	"PortTool/src/ptproto"
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
	// why the run ended; set before done closes. The item runner reports it
	// for a reset or a silent port (ptlib.Holder).
	why msg
}

// startTimedRun arms the deadman and keeps it renewed until something stops
// it. dur only labels the run (log header, countdown, < 0 until stopped): the
// item runner owns the deadline (ptlib.Holder).
func (s *Server) startTimedRun(b *ptboard.Board, ports []string, dur time.Duration) {
	hours := 0
	if dur > 0 {
		hours = int(dur / time.Hour)
	}
	run := &timedRun{
		ports:   append([]string(nil), ports...),
		started: time.Now(),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	if dur > 0 {
		run.deadline = run.started.Add(dur)
	}

	// Opened before the run is published, so the file covers its first frame.
	run.log = s.startRunLog(b, run.ports, hours)

	s.mu.Lock()
	s.run = run
	s.mu.Unlock()

	// Armed now, not by the first renewal: the outputs are already live.
	s.holdRenew(b)
	// A lamp left lit by an earlier fault would read as this run's verdict.
	_, _ = b.Send(ptproto.LEDFaultOff, ptboard.ExpectOne, pthold.Timeout)
	go s.watchRun(b, run)

	if run.deadline.IsZero() {
		s.say(m("go.hold.started_forever"))
	} else {
		s.say(m("go.hold.started", "hours", hours, "until", run.deadline.Format("15:04:05")))
	}

	go func() {
		defer close(run.done)
		t := time.NewTicker(pthold.Renewal)
		defer t.Stop()

		for {
			select {
			case <-run.stop:
				return
			case <-t.C:
				if !s.holdRenew(b) {
					// The board stopped answering; its own deadman releases
					// the outputs within pthold.Span.
					s.say(m("go.hold.no_renew"))
					s.finishTimedRun(run, m("go.reason.no_reply"))
					return
				}
			}
		}
	}()
}

// holdRenew tells the board it has another pthold.Span. No other command
// refreshes it, so echoing frames alone does not keep the outputs live.
func (s *Server) holdRenew(b *ptboard.Board) bool {
	_, err := b.Send(pthold.Command(), ptboard.ExpectOne, pthold.Timeout)
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
	run.why = why

	s.releaseBoard(b)
	s.say(m("go.hold.finished", "why", why))
	// Closed after the board is released, so the file records the stop too.
	run.log.close(s, why)
}

// stopTimedRun ends the current run from outside it and waits for the renewal
// goroutine to be gone, so a stop then start never has two renewing.
func (s *Server) stopTimedRun(why msg) {
	s.mu.Lock()
	run := s.run
	s.run = nil
	b := s.board
	s.mu.Unlock()

	if run == nil {
		return
	}
	run.why = why
	close(run.stop)
	<-run.done

	s.releaseBoard(b)
	s.say(m("go.hold.finished", "why", why))
	run.log.close(s, why)
}

// releaseBoard puts the hardware back in a safe state: sessions stopped first,
// then the deadman disarmed, so the outputs are never driven unwatched.
func (s *Server) releaseBoard(b *ptboard.Board) {
	if b == nil {
		return
	}
	_, _ = b.Send("pt.stop all", ptboard.ExpectOne, pthold.Timeout)
	_, _ = b.Send("pt.hold 0", ptboard.ExpectOne, pthold.Timeout)
}

// raiseFault is decision 37 item 3: stop every port, say why, light the lamp.
// Every fault - a failed verdict from the page, a reset seen by watchRun -
// goes through here, so they all look the same on the bench.
func (s *Server) raiseFault(b *ptboard.Board, why msg) error {
	s.stopTimedRun(why)
	s.say(m("go.alert.fault", "why", why))
	_, err := b.Send(ptproto.LEDFaultOn, ptboard.ExpectOne, pthold.Timeout)
	return err
}

// silenceFloor is the shortest gap watchRun will call silence, whatever a
// port's own period: below it a busy serial line alone can delay a frame.
const silenceFloor = 5 * time.Second

// watchRun notices that the board restarted or hung under a run (decision 77):
// a port's frames stop, or its tick goes backwards. Silent means three of its
// own periods (learnt from the frames) plus silenceFloor.
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
