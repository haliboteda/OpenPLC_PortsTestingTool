package ptpanel

import (
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"PortTool/internal/serialx"
)

// A link port is the far end of a loop=link session: a second serial adapter,
// on the terminal being tested, whose whole job is to send back whatever the
// board sends it.
//
// This is what makes such a session mean anything. The board puts a number on
// the RS485 pair and takes the reply off that same pair, so the reply has to
// come back over the pair - answering on the control port instead would let the
// counter climb with the pair dead, and the firmware refuses pt.echo there for
// exactly that reason.
//
// It repeats the line verbatim rather than incrementing it: the board is what
// counts on, comparing the number that came back against the one it sent.
type linkPort struct {
	Board string // the board's port name, e.g. "rs485"
	COM   string // the adapter on this machine, or host:port for TCP
	Baud  int
	// Kind is "serial" or "tcp". The Ethernet session's far end is a
	// TCP client, not an adapter - a serial port has nothing to do with
	// it, and offering one was the panel telling people to plug the
	// wrong thing in (2026-09-14).
	Kind string

	port io.ReadWriteCloser
	stop func()

	// Echoing and sinking can both be writing at the same moment - a board
	// that sends something while this end is pushing a stream at it. A serial
	// port is not safe for two writers.
	writeMu sync.Mutex

	mu      sync.Mutex
	rxLines uint64
	echoed  uint64
	rxBytes uint64
	txBytes uint64
	// Mode is what the board's session is doing, so this end can do the
	// matching half. Empty means echo, which is what every port that has no
	// modes at all wants.
	mode     string
	sinkStop func()
	lastErr  string
}

func (l *linkPort) snapshot() map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	mode := l.mode
	if mode == "" {
		mode = modeEcho
	}
	return map[string]any{
		"com":     l.COM,
		"kind":    l.Kind,
		"baud":    l.Baud,
		"rxLines": l.rxLines,
		"echoed":  l.echoed,
		"rxBytes": l.rxBytes,
		"txBytes": l.txBytes,
		"mode":    mode,
		"error":   l.lastErr,
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

	l := &linkPort{Board: boardPort, COM: com, Baud: baud, Kind: "serial", port: port}
	done := make(chan struct{})

	// A quiet adapter must not look like the end of the stream
	// (serialx.SteadyReader), the same reason the control port needs it.
	src := io.Reader(serialx.SteadyReader{R: l.port})
	go func() {
		defer close(done)
		l.pump(src, func(what string) { s.linkLog(l, what) })
	}()

	l.stop = func() {
		l.setMode(modeEcho, nil)
		_ = l.port.Close()
		<-done
	}
	return l, nil
}

// pump moves bytes between the board and this end of the link.
//
// *** Bytes, not lines. *** A session in sink or source mode puts a continuous
// stream on the wire with no newline anywhere in it, and bufio.Scanner answers
// that with "token too long" and stops - on a link that is working perfectly.
// Found on a bench 2026-09-14: the board's tx_bytes froze at 20608 while busy
// climbed past half a million, and every symptom pointed at the board.
//
// Echoing verbatim is what makes echo mode mean anything: the board compares
// the number that came back against the one it sent, so a peer that reformats
// or re-chunks the payload reads as a dead link. Source mode does not check
// what comes back, so sending it back costs nothing and keeps one code path.
func (l *linkPort) pump(src io.Reader, log func(string)) {
	buf := make([]byte, 4096)
	lg := &linkLogger{log: log}
	defer lg.flush()

	for {
		n, err := src.Read(buf)
		if n > 0 {
			l.mu.Lock()
			l.rxBytes += uint64(n)
			l.rxLines++
			l.mu.Unlock()
			lg.saw("收到", buf[:n])

			l.writeMu.Lock()
			_, werr := l.port.Write(buf[:n])
			l.writeMu.Unlock()
			if werr != nil {
				l.mu.Lock()
				l.lastErr = werr.Error()
				l.mu.Unlock()
				lg.flush()
				log("回不出去：" + werr.Error())
				return
			}
			l.mu.Lock()
			l.echoed++
			l.txBytes += uint64(n)
			l.lastErr = ""
			l.mu.Unlock()
			lg.saw("送回", buf[:n])
		}
		if err != nil {
			if err != io.EOF {
				l.mu.Lock()
				l.lastErr = err.Error()
				l.mu.Unlock()
				lg.flush()
				log("读不下去了：" + err.Error())
			}
			return
		}
	}
}

// setMode tells this end which half of the session it is running.
//
// *** Only sink originates traffic. *** In echo mode the board compares what
// comes back against what it sent, so a byte this end started would read as a
// corrupted link. In source mode the board does the talking and this end only
// has to keep reading - stop reading and the endpoint fills, which the board
// reports as busy climbing while its byte count stands still.
func (l *linkPort) setMode(mode string, log func(string)) {
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
	if mode == modeSink && log != nil {
		l.startSink(log)
	}
}

// startSink pushes a pattern at the board for as long as the mode lasts.
//
// The board counts bytes and never reads them, so the content only has to make
// a half-delivered block visible to a person looking at the wire. The write
// itself is the pacing: the port blocks when the far end cannot take any more,
// and that is the throughput being measured.
func (l *linkPort) startSink(log func(string)) {
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
		lg := &linkLogger{log: log, last: time.Now()}
		defer lg.flush()
		for {
			select {
			case <-done:
				return
			default:
			}
			l.writeMu.Lock()
			n, err := l.port.Write(block)
			l.writeMu.Unlock()
			if n > 0 {
				l.mu.Lock()
				l.txBytes += uint64(n)
				l.mu.Unlock()
				lg.saw("灌给板子", block[:n])
			}
			if err != nil {
				l.mu.Lock()
				l.lastErr = err.Error()
				l.mu.Unlock()
				lg.flush()
				log("灌不进去：" + err.Error())
				return
			}
		}
	}()
}

// linkLogger keeps the log readable at both speeds this link runs at.
//
// An echo session sends one short line every few hundred milliseconds, and
// seeing each one is how a person tells a live link from a dead one. A sink or
// source session moves thousands of blocks a second: one line each would bury
// the board's own frames just when the numbers start mattering. So short
// printable text is printed as it arrives and everything else is summed once a
// second.
type linkLogger struct {
	log  func(string)
	last time.Time
	rx   uint64
	tx   uint64
}

func (g *linkLogger) saw(what string, b []byte) {
	if s, ok := readableLine(b); ok {
		g.log(what + " " + s)
		return
	}
	if what == "收到" {
		g.rx += uint64(len(b))
	} else {
		g.tx += uint64(len(b))
	}
	if time.Since(g.last) >= time.Second {
		g.flush()
	}
}

func (g *linkLogger) flush() {
	if g.rx == 0 && g.tx == 0 {
		return
	}
	g.log(fmt.Sprintf("这一秒：收到 %d 字节，发出 %d 字节", g.rx, g.tx))
	g.rx, g.tx = 0, 0
	g.last = time.Now()
}

// readableLine says whether a block is the short printable text an echo
// session sends. Anything else is a stream, and gets counted rather than
// printed.
func readableLine(b []byte) (string, bool) {
	if len(b) == 0 || len(b) > 200 {
		return "", false
	}
	for _, c := range b {
		if c == '\r' || c == '\n' || c == '\t' {
			continue
		}
		if c < 0x20 || c == 0x7f {
			return "", false
		}
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", false
	}
	return s, true
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
		l.setMode(mode, func(what string) { s.linkLog(l, what) })
	}
}

// linkLog puts one line about a link port into the panel's log, so the bytes
// actually crossing the terminal are visible next to the board's own frames.
func (s *Server) linkLog(l *linkPort, what string) {
	s.emit(fmt.Sprintf("[link %s@%s] %s", l.Board, l.COM, what))
}

// emit pushes a line the panel itself produced to every open event stream.
//
// Kept separate from the board's own events: these are the panel's words, not
// the board's, and the log pane labels them so nobody reads a line the panel
// wrote as evidence from the hardware.
func (s *Server) emit(line string) {
	s.mu.Lock()
	s.emitSeq++
	ev := panelEvent{Seq: s.emitSeq, Line: line, At: time.Now()}
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
// sends.
//
// *** This is the far end the Ethernet session has always needed and the
// *** panel never had. *** The board runs the server; somebody has to connect
// to it, or conn= stays 0 and the card says "对端没连上" with no way to do
// anything about it from here. The command line had this all along, inside
// porttool run; the panel did not.
//
// Bytes, not lines. The board compares the counter that comes back against
// the one it sent, and a byte stream has no obligation to arrive in the same
// chunks it left in - splitting on newlines would work until a counter
// straddled two reads.
func (s *Server) bindTCP(boardPort, addr string) (*linkPort, error) {
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, err
	}

	l := &linkPort{Board: boardPort, COM: addr, Kind: "tcp", port: conn}
	done := make(chan struct{})

	go func() {
		defer close(done)
		l.pump(l.port, func(what string) { s.linkLog(l, what) })
	}()

	l.stop = func() {
		l.setMode(modeEcho, nil)
		_ = l.port.Close()
		<-done
	}
	return l, nil
}
