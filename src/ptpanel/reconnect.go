package ptpanel

import (
	"errors"

	"PortTool/src/ptboard"
)

// reconnect opens the control port again under its name after the board
// connection old was lost, and makes it the panel's: the RS232 recover item's
// plug back (decision 150, PORTTOOL-FLOW.md C.3.6 「断开恢复」). It fails while
// old is still up, after the person connected elsewhere, or when the port
// does not open or answer yet.
func (s *Server) reconnect(old *ptboard.Board, port string) (*ptboard.Board, error) {
	select {
	case <-old.Lost():
	default:
		return nil, errors.New("the control port is still open")
	}
	s.mu.Lock()
	cur, baud := s.board, s.baud
	s.mu.Unlock()
	if cur != old {
		return nil, errors.New("the panel is connected to another port now")
	}
	rw, err := s.Open(port, baud)
	if err != nil {
		return nil, err
	}
	b := ptboard.New(rw, 0)
	if _, err := b.Caps(); err != nil {
		_ = b.Close()
		return nil, err
	}
	s.mu.Lock()
	if s.board != old {
		s.mu.Unlock()
		_ = b.Close()
		return nil, errors.New("the panel is connected to another port now")
	}
	stopEcho := s.echoStop
	s.board, s.ctrlRW, s.echoStop, s.linkErr = b, rw, nil, msg{}
	s.mu.Unlock()
	if stopEcho != nil {
		stopEcho()
	}
	_ = old.Close()
	echo := s.runEchoResponder(b)
	s.mu.Lock()
	s.echoStop = echo
	s.mu.Unlock()
	go s.watchLost(b, rw)
	s.say(m("go.lost.back", "port", port))
	return b, nil
}
