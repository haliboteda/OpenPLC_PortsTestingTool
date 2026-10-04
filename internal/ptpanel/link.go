package ptpanel

import (
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"PortTool/internal/ptecho"
	"PortTool/internal/serialx"
)

// A link port is the far end of a loop=link session: a second serial adapter,
// on the terminal being tested, or a TCP connection to the board's Ethernet
// session. The echoing itself is ptecho's, the same one a plan step uses, so
// the panel and a production run cannot disagree about what "echo" means.
//
// This is what makes such a session mean anything. The board puts a number on
// the RS485 pair and takes the reply off that same pair, so the reply has to
// come back over the pair - answering on the control port instead would let the
// counter climb with the pair dead, and the firmware refuses pt.echo there for
// exactly that reason.
type linkPort struct {
	Board string // the board's port name, e.g. "rs485"
	COM   string // the adapter on this machine, or host:port for TCP
	Baud  int
	// Kind is "serial" or "tcp". The Ethernet session's far end is a TCP
	// client, not an adapter (2026-09-14).
	Kind string

	peer *ptecho.Peer
	say  func(msg) // the panel's own sentences about this link; nil in tests

	mu sync.Mutex
	// Mode is what the board's session is doing, so this end can do the
	// matching half. Empty means echo, which is what every port that has no
	// modes at all wants.
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
	return map[string]any{
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
}

// The session modes this end has to answer differently.
const (
	modeEcho   = "echo"
	modeSink   = "sink"
	modeSource = "source"
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

// setMode tells this end which half of the session it is running.
//
// *** Only sink originates traffic. *** In echo mode the board compares what
// comes back against what it sent, so a byte this end started would read as a
// corrupted link. In source mode the board does the talking and this end only
// has to keep reading - stop reading and the endpoint fills, which the board
// reports as busy climbing while its byte count stands still.
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
	if mode == modeSink {
		l.startSink()
	}
}

// startSink pushes a pattern at the board for as long as the mode lasts.
//
// The board counts bytes and never reads them, so the content only has to make
// a half-delivered block visible to a person looking at the wire. The write
// itself is the pacing: the port blocks when the far end cannot take any more,
// and that is the throughput being measured.
func (l *linkPort) startSink() {
	block := make([]byte, 4096)
	for i := range block {
		block[i] = byte('0' + i%10)
	}
	done := make(chan struct{})
	var once sync.Once

	l.mu.Lock()
	l.sinkStop = func() { once.Do(func() { close(done) }) }
	l.mu.Unlock()

	go func() {
		lg := &ptecho.Logger{Say: l.echoSay}
		defer lg.Flush()
		for {
			select {
			case <-done:
				return
			default:
			}
			n, err := l.peer.Write(block)
			if n > 0 {
				lg.Saw(ptecho.ToBoard, block[:n])
			}
			if err != nil {
				lg.Flush()
				if l.say != nil {
					l.say(m("go.link.write_failed", "detail", err))
				}
				return
			}
		}
	}()
}

// linkModeFromCmd reads the session mode out of a command on its way to the
// board, so the far end can change what it does at the same moment the board
// does.
//
// *** The command is the only place this is knowable in time. *** The mode is
// in the board's frames too, but those arrive after the session has started -
// and a sink session nobody was pushing at reports zero bytes for exactly the
// frames it was supposed to prove something with.
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
		// A stopped session has no mode, and a sink left pushing at a board
		// that stopped listening fills the log with write errors.
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

// emit pushes a line the panel itself produced to every open event stream.
//
// Kept separate from the board's own events: these are the panel's words, not
// the board's, and the log pane labels them so nobody reads a line the panel
// wrote as evidence from the hardware.
func (s *Server) emit(line string) { s.emitEvent(panelEvent{Line: line}) }

// say is emit for the panel's own sentences: the page words them in its
// language, and the event also carries the English.
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
