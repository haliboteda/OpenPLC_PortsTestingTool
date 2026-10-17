package ptpanel

import (
	"sync"
	"time"

	"PortTool/src/ptboard"
	"PortTool/src/ptproto"
	"PortTool/src/simboard"
)

// The board's network address for the connection zone's 网口 row, learned
// right after connecting rather than only from an Ethernet test: the board has
// an address only once an eth session has brought lwIP up, and lwIP stays up
// after the session stops (porttool_eth.c eth_stop). PORTTOOL-FLOW.md C.3.6
// 「连接区」网口.

// ipProbeTimeout is how long the probe waits for DHCP to hand out an address.
const ipProbeTimeout = 15 * time.Second

// ipStartTimeout bounds the probe's pt.start: the command lock is held while
// it waits, and a test started meanwhile should not wait long.
const ipStartTimeout = time.Second

type boardIP struct {
	addr   string        // the last address an eth frame reported; "" while unknown
	state  string        // "probing", "ok" or "none"
	why    msg           // why there is no address, when state is "none"
	done   chan struct{} // closed when the probe has finished
	cancel chan struct{} // closed to end the probe early
	once   *sync.Once
}

// frameIP is the address an eth frame reports, or "" when it has none yet.
func frameIP(ev ptboard.Event) string {
	if ev.Kind != ptproto.LineFrame || ev.Frame.Port != "eth" {
		return ""
	}
	ip, _ := ptproto.Get(ev.Frame.Fields, "ip")
	if ip == "" || ip == "dhcp" || ip == "0.0.0.0" {
		return ""
	}
	return ip
}

// startIPProbe resets the address for a new connection and starts finding it.
// Called with s.board already set to b.
func (s *Server) startIPProbe(b *ptboard.Board, caps ptproto.Caps) {
	done, cancel := make(chan struct{}), make(chan struct{})
	s.mu.Lock()
	s.ip = boardIP{state: "probing", done: done, cancel: cancel, once: &sync.Once{}}
	s.mu.Unlock()
	go s.watchIP(b)
	go s.probeIP(b, caps, done, cancel)
}

// watchIP keeps the address current from every eth frame for as long as b is
// the connected board, so a later Ethernet test updates it too.
func (s *Server) watchIP(b *ptboard.Board) {
	events, stop := b.Subscribe(64)
	defer stop()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return
			}
			if ip := frameIP(ev); ip != "" {
				s.setIP(b, "ok", ip, msg{})
			}
		case <-b.Lost():
			return
		}
	}
}

// probeIP runs a short eth session with the board's current settings until a
// frame carries an address, then stops it.
func (s *Server) probeIP(b *ptboard.Board, caps ptproto.Caps, done, cancel chan struct{}) {
	defer close(done)
	if _, ok := caps.Port("eth"); !ok {
		s.setIP(b, "none", "", m("conn.eth_noport"))
		return
	}
	events, stop := b.Subscribe(64)
	defer stop()
	const start = "pt.start eth"
	if _, err := b.Send(start, ptboard.ExpectFor(start), ipStartTimeout); err != nil {
		s.setIP(b, "none", "", m("conn.eth_probe_err", "detail", err))
		return
	}
	defer func() { _, _ = b.Send("pt.stop eth", ptboard.ExpectOne, ptboard.DefaultTimeout) }()
	timer := time.NewTimer(ipProbeTimeout)
	defer timer.Stop()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return
			}
			if ip := frameIP(ev); ip != "" {
				s.setIP(b, "ok", ip, msg{})
				return
			}
		case <-timer.C:
			s.setIP(b, "none", "", m("conn.eth_no_address"))
			return
		case <-cancel:
			s.setIP(b, "none", "", m("conn.eth_after_test"))
			return
		case <-b.Lost():
			return
		}
	}
}

// setIP records what is known, unless b is no longer the connected board.
// An address once found is kept when a later probe result has none.
func (s *Server) setIP(b *ptboard.Board, state, addr string, why msg) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.board != b {
		return
	}
	if addr == "" && s.ip.addr != "" {
		return
	}
	if addr != "" {
		s.ip.addr = addr
	}
	s.ip.state, s.ip.why = state, why
}

// waitIPProbe ends a probe still running and waits until its eth session is
// stopped, so a test never fights it over the eth port.
func (s *Server) waitIPProbe() {
	s.mu.Lock()
	done, cancel, once := s.ip.done, s.ip.cancel, s.ip.once
	s.mu.Unlock()
	if done == nil {
		return
	}
	once.Do(func() { close(cancel) })
	select {
	case <-done:
	case <-time.After(2*ptboard.DefaultTimeout + ipStartTimeout):
	}
}

// ipJSON is the 网口 row's data. Caller holds s.mu.
func (s *Server) ipJSON() map[string]any {
	return map[string]any{
		"addr":  s.ip.addr,
		"state": s.ip.state,
		"why":   s.ip.why,
		"sim":   simboard.IsSim(s.portNam),
	}
}

// probing says the address lookup still holds the eth session.
func (s *Server) probing() bool {
	s.mu.Lock()
	done := s.ip.done
	s.mu.Unlock()
	if done == nil {
		return false
	}
	select {
	case <-done:
		return false
	default:
		return true
	}
}
