package ptpanel

// Asking the person at the bench: one question at a time, answered from the
// page. Any runner may ask; the page polls GET /api/ask.
// $PROD/docs/modules/M4/PORTTOOL-FLOW.md C.3.6 "问人".

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"PortTool/src/ptlib"
)

// askQuestion is what the page shows.
type askQuestion struct {
	ID     int    `json:"id"`
	Item   string `json:"item,omitempty"`
	Kind   string `json:"kind"` // yes_no | number | wait (only stop; the board answers)
	Text   msg    `json:"text"`
	Prompt string `json:"prompt,omitempty"` // a plan step's own prompt, as written
	Unit   string `json:"unit,omitempty"`
	Hint   *msg   `json:"hint,omitempty"`
}

// askReply is the page's answer.
type askReply struct {
	ID    int     `json:"id"`
	Yes   *bool   `json:"yes"`
	Value *string `json:"value"`
	Stop  bool    `json:"stop"`
}

type asker struct {
	turn sync.Mutex // one question at a time
	mu   sync.Mutex
	seq  int
	cur  *askQuestion
	ch   chan askReply
}

// ask waits for the person's answer. ptlib.ErrStopped when they press stop
// or ctx ends.
func (s *Server) ask(ctx context.Context, q askQuestion) (askReply, error) {
	a := &s.asks
	a.turn.Lock()
	defer a.turn.Unlock()
	ch := make(chan askReply, 1)
	a.mu.Lock()
	a.seq++
	q.ID = a.seq
	a.cur, a.ch = &q, ch
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.cur, a.ch = nil, nil
		a.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return askReply{}, ptlib.ErrStopped
	case r := <-ch:
		if r.Stop {
			return r, ptlib.ErrStopped
		}
		return r, nil
	}
}

func (s *Server) askJSON() any {
	s.asks.mu.Lock()
	defer s.asks.mu.Unlock()
	if s.asks.cur == nil {
		return nil
	}
	return *s.asks.cur
}

func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 200, map[string]any{"ask": s.askJSON()})
		return
	}
	var rep askReply
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		writeErr(w, 400, m("lib.ask.bad_request"))
		return
	}
	a := &s.asks
	a.mu.Lock()
	cur, ch := a.cur, a.ch
	a.mu.Unlock()
	if cur == nil || cur.ID != rep.ID {
		writeJSON(w, 200, map[string]any{"error": m("lib.ask.not_waiting")})
		return
	}
	if !rep.Stop {
		switch cur.Kind {
		case "wait":
			writeJSON(w, 200, map[string]any{"error": m("lib.ask.not_waiting")})
			return
		case "yes_no":
			if rep.Yes == nil {
				writeJSON(w, 200, map[string]any{"error": m("lib.ask.need_yes_no")})
				return
			}
		default:
			if rep.Value == nil {
				writeJSON(w, 200, map[string]any{"error": m("lib.ask.not_number")})
				return
			}
			if _, err := strconv.ParseFloat(strings.TrimSpace(*rep.Value), 64); err != nil {
				writeJSON(w, 200, map[string]any{"error": m("lib.ask.not_number")})
				return
			}
		}
	}
	select {
	case ch <- rep:
	default:
		writeJSON(w, 200, map[string]any{"error": m("lib.ask.not_waiting")})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
