package ptpanel

import (
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"PortTool/src/ptecho"
	"PortTool/src/serialx"
)

// A link port is the far end of a loop=link session: a serial adapter on the
// terminal under test, or a TCP connection to the board's Ethernet session.
// The echoing is ptecho's, the same as a plan step's (decision 77); the reply
// must come back over the link under test, never the control port.
type linkPort struct {
	Board string // the board's port name, e.g. "rs485"
	COM   string // the adapter on this machine, or host:port for TCP
	Baud  int
	// Kind is "serial" or "tcp" (the Ethernet session's far end).
	Kind string

	peer *ptecho.Peer
	say  func(msg) // the panel's own sentences about this link; nil in tests

	mu sync.Mutex
	// mode is what the board's session is doing, so this end does the
	// matching half; empty means echo.
	mode     string
	sinkStop func()
}

func newLinkPort(boardPort, com string, baud int, kind string, rw io.ReadWriteCloser, say func(msg)) *linkPort {
	l := &linkPort{Board: boardPort, COM: com, Baud: baud, Kind: kind, say: say}
	l.peer = ptecho.New(boardPort+"@"+com, rw, l.echoSay)
	return l
}

// echoSay words ptecho's lines through the panel dictionary.
func (l *linkPort) echoSay(key string, kv ...any) {
	if l.say != nil {
		l.say(m(key, kv...))
	}
}

func (l *linkPort) stop() {
	l.setMode(modeEcho)
	l.peer.Stop()
}

func (l *linkPort) snapshot() map[string]any {
	l.mu.Lock()
	mode := l.mode
	l.mu.Unlock()
	if mode == "" {
		mode = modeEcho
	}
	st := l.peer.Stats()
	out := map[string]any{
		"com":     l.COM,
		"kind":    l.Kind,
		"baud":    l.Baud,
		"rxLines": st.Reads,
		"echoed":  st.Echoes,
		"rxBytes": st.RxBytes,
		"txBytes": st.TxBytes,
		"mode":    mode,
		"error":   st.Err,
	}
	if fl, ok := l.peer.Flooding(); ok {
		out["flood"] = fl
	}
	return out
}

// The session modes this end has to answer differently.
const (
	modeEcho   = "echo"
	modeSink   = "sink"
	modeSource = "source"
	modeMirror = "mirror"
)

// bindLink opens the adapter and starts repeating whatever arrives on it.
func (s *Server) bindLink(boardPort, com string, baud int) (*linkPort, error) {
	if baud == 0 {
		baud = serialx.DefaultBaud
	}
	port, err := s.Open(com, baud)
	if err != nil {
		return nil, err
	}
	l := newLinkPort(boardPort, com, baud, "serial", port, func(x msg) { s.say(m("go.link.line", "board", boardPort, "com", com, "what", x)) })
	return l, nil
}

// setMode tells this end which half of the session it is running. Only sink
// and mirror originate traffic: in echo mode a byte started here reads as a
// corrupted link; in source mode this end only keeps reading (PORTTOOL-FLOW.md B.9).
func (l *linkPort) setMode(mode string) {
	if mode == "" {
		mode = modeEcho
	}
	l.mu.Lock()
	if l.mode == mode {
		l.mu.Unlock()
		return
	}
	l.mode = mode
	stop := l.sinkStop
	l.sinkStop = nil
	l.mu.Unlock()

	if stop != nil {
		stop()
	}
	_, _ = l.peer.StopFlood(0)
	l.peer.SetDrain(mode == modeSource)
	if mode == modeSink {
		l.startSink()
	}
	if mode == modeMirror {
		// The board mirrors, so this end floods and measures (PORTTOOL-FLOW.md B.9).
		if l.Kind == "tcp" {
			l.peer.StartFlood(ptecho.FloodTCP)
		} else {
			l.peer.StartFlood(ptecho.FloodUSB)
		}
	}
}

// startSink pushes at the board for as long as the mode lasts (ptecho.Push).
func (l *linkPort) startSink() {
	stop := l.peer.Push(l.echoSay, func(err error) {
		if l.say != nil {
			l.say(m("go.link.write_failed", "detail", err))
		}
	})
	l.mu.Lock()
	l.sinkStop = stop
	l.mu.Unlock()
}

// linkModeFromCmd reads the session mode out of a command on its way to the
// board, so the far end changes with the board; the frames carry it too late.
func linkModeFromCmd(cmd string) (port, mode string) {
	f := strings.Fields(cmd)
	if len(f) < 2 {
		return "", ""
	}
	switch f[0] {
	case "pt.start", "pt.set":
		for _, t := range f[2:] {
			if v, ok := strings.CutPrefix(t, "mode="); ok {
				mode = v
			}
		}
		if mode == "" {
			return "", ""
		}
		return f[1], mode
	case "pt.stop":
		// A stopped session has no mode; a sink left pushing would only log write errors.
		return f[1], modeEcho
	}
	return "", ""
}

// applyLinkMode moves the far end to whatever mode the command just put the
// board in. "all" reaches every bound link, the way pt.stop all does.
func (s *Server) applyLinkMode(cmd string) {
	port, mode := linkModeFromCmd(cmd)
	if port == "" {
		return
	}
	s.mu.Lock()
	var targets []*linkPort
	if port == "all" {
		for _, l := range s.links {
			targets = append(targets, l)
		}
	} else if l := s.links[port]; l != nil {
		targets = append(targets, l)
	}
	s.mu.Unlock()

	for _, l := range targets {
		l.setMode(mode)
	}
}

// say pushes the panel's own sentence to every open event stream: the page
// words it in its language, the event also carries the English, and the log
// pane labels it as the panel's, not the board's.
func (s *Server) say(x msg) { s.emitEvent(panelEvent{Line: x.English(), Msg: &x}) }

func (s *Server) emitEvent(ev panelEvent) {
	s.mu.Lock()
	s.emitSeq++
	ev.Seq, ev.At = s.emitSeq, time.Now()
	if len(s.emitRing) >= emitRingCap {
		s.emitRing = s.emitRing[len(s.emitRing)-emitRingCap+1:]
	}
	s.emitRing = append(s.emitRing, ev)
	for _, c := range s.emitSubs {
		select {
		case c <- ev:
		default:
		}
	}
	s.mu.Unlock()
}

const emitRingCap = 4000

type panelEvent struct {
	Seq  uint64
	Line string
	Msg  *msg // set for the panel's own sentences
	At   time.Time
}

func (s *Server) subscribePanel(buffer int) (<-chan panelEvent, []panelEvent, func()) {
	ch := make(chan panelEvent, buffer)
	s.mu.Lock()
	if s.emitSubs == nil {
		s.emitSubs = map[int]chan panelEvent{}
	}
	id := s.emitNextSub
	s.emitNextSub++
	s.emitSubs[id] = ch
	backlog := make([]panelEvent, len(s.emitRing))
	copy(backlog, s.emitRing)
	s.mu.Unlock()

	return ch, backlog, func() {
		s.mu.Lock()
		if c, ok := s.emitSubs[id]; ok {
			delete(s.emitSubs, id)
			close(c)
		}
		s.mu.Unlock()
	}
}

// bindTCP connects to the board as a TCP client and echoes back whatever it
// sends: the far end the Ethernet session needs, since the board runs the
// server and somebody has to connect to it, or conn= stays 0.
func (s *Server) bindTCP(boardPort, addr string) (*linkPort, error) {
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	l := newLinkPort(boardPort, addr, 0, "tcp", conn, func(x msg) { s.say(m("go.link.line", "board", boardPort, "com", addr, "what", x)) })
	return l, nil
}
