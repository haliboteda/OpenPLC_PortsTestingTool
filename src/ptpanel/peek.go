package ptpanel

import (
	"net/http"
	"time"

	"PortTool/src/ptboard"
	"PortTool/src/ptlib"
	"PortTool/src/ptproto"
)

// GET /api/analog/peek: the AI channels' present reading for the 「AI 读数」
// card before anything runs (decision 114, PORTTOOL-FLOW.md C.3.2). A short
// ain session, one frame, stopped again; skipped while anything else runs so
// it never takes the port from a test.
func (s *Server) handlePeek(w http.ResponseWriter, r *http.Request) {
	if !s.peekMu.TryLock() {
		writeJSON(w, 200, map[string]any{"busy": true})
		return
	}
	defer s.peekMu.Unlock()
	s.mu.Lock()
	b, timed := s.board, s.run != nil
	s.mu.Unlock()
	s.full.mu.Lock()
	full := s.full.running
	s.full.mu.Unlock()
	if b == nil || timed || full || s.libBusy() || s.probing() {
		writeJSON(w, 200, map[string]any{"busy": true})
		return
	}
	f, err := peekFrame(b)
	if err != nil {
		writeJSON(w, 200, map[string]any{"error": msgOf(err)})
		return
	}
	e := &ptlib.Engine{Analog: s.AnalogSource(), AnalogNominal: s.AnalogNominal()}
	writeJSON(w, 200, map[string]any{"analog": e.PeekAnalog(f)})
}

// peekFrame runs the ain session until its first frame.
func peekFrame(b *ptboard.Board) (ptproto.Frame, error) {
	return peekPort(b, "ain", "pt.start ain ch=1,2 period=100")
}

// peekPort runs one session until its first frame, then stops it.
func peekPort(b *ptboard.Board, port, start string) (ptproto.Frame, error) {
	events, stop := b.Subscribe(16)
	defer stop()
	if _, err := b.Send(start, ptboard.ExpectOne, ptboard.DefaultTimeout); err != nil {
		return ptproto.Frame{}, err
	}
	defer func() { _, _ = b.Send("pt.stop "+port, ptboard.ExpectOne, ptboard.DefaultTimeout) }()
	timer := time.NewTimer(1500 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return ptproto.Frame{}, errBoardGone
			}
			if ev.Kind == ptproto.LineFrame && ev.Frame.Port == port {
				return ev.Frame, nil
			}
		case <-timer.C:
			return ptproto.Frame{}, errNoFrame
		}
	}
}

var (
	errBoardGone = msgError{m("go.peek.board_gone")}
	errNoFrame   = msgError{m("go.peek.no_frame")}
)
