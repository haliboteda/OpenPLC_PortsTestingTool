package ptpanel

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"PortTool/src/portmap"
	"PortTool/src/ptboard"
	"PortTool/src/ptitems"
	"PortTool/src/ptproto"
	"PortTool/src/ptstages"
	"PortTool/src/serialx"
	"PortTool/src/simboard"
)

//go:embed web
var webFS embed.FS

// Server holds the one board connection the panel is driving and the far ends
// bound to its link ports.
type Server struct {
	// Open is how a serial port is obtained; tests put a fake board here.
	Open func(name string, baud int) (io.ReadWriteCloser, error)

	// Version goes into plan-run reports, tracing a result to the build that judged it.
	Version string

	// Stages is what runs each step of a full fixture test. New fills steps
	// 2, 4 and 5; step 6 is built from the delivery's files when this map has
	// none (full.go stepWhy). Tests put fakes here, the same seam as Open.
	Stages map[ptstages.Step]ptstages.Func
	full   fullState

	// RunItems runs catalogue items for the configured steps 2 and 4; nil
	// records those steps ERROR. Contract: ItemRunner in stagecfg.go.
	RunItems ItemRunner

	mu      sync.Mutex
	board   *ptboard.Board
	ctrlRW  io.ReadWriteCloser // the stream under board; simboard.Link wires a simulated reference board to it
	portNam string
	baud    int    // the control port's, for reopening it under the same name (reconnect.go)
	uid     string // the connected board's UID once /api/uid asked; "" until then
	caps    ptproto.Caps
	capsErr msg
	// Why the control port stopped working while still connected: an
	// unplugged adapter, a board that lost power. Shown until disconnect.
	linkErr msg

	// Answering the board's echo frames is on by default: an unanswered counter
	// only reports misses. Off proves the counter is real (miss climbs, seq stands still).
	autoEcho  bool
	echoStop  func()
	echoNote  string // the last thing that went wrong, shown once rather than spammed
	echoCount uint64

	// The "持续" run in flight, if any: the PC's own clock for it, and the
	// goroutine renewing the board's deadman. See hold.go.
	run *timedRun

	// The far ends of the loop=link sessions, keyed by the board's port name;
	// a loop=link counter only closes when one is bound.
	links map[string]*linkPort

	// Lines the panel itself produced, kept apart from the board's events so
	// they are not read as evidence from the hardware.
	emitRing    []panelEvent
	emitSubs    map[int]chan panelEvent
	emitNextSub int
	emitSeq     uint64

	// The connection block: board clock, reference board, ST-Link. conn.go.
	conn connState
	// The board's network address for the 网口 row. boardip.go.
	ip boardIP
	// Held while the AI card peeks at a reading and while a test starts, so
	// the peek's short ain session never overlaps a test's. peek.go.
	peekMu sync.Mutex

	// The item runner behind the top-right Start, and the questions it puts
	// to the person at the bench. library.go, ask.go.
	lib  libState
	asks asker
}

// New returns a panel that talks to real serial ports.
func New() *Server {
	s := &Server{
		autoEcho: true,
		Open:     simboard.OpenPort,
	}
	s.Stages = map[ptstages.Step]ptstages.Func{
		ptstages.Functional: s.configStep(ptitems.Func),
		ptstages.Aging:      s.configStep(ptitems.Aging),
		ptstages.Safety:     s.safetyStep,
	}
	s.RunItems = s.runItems
	return s
}

// Handler is the panel's whole HTTP surface: the embedded page plus /api.
func (s *Server) Handler() http.Handler { return s.routes() }

// Close drops the board connection, if there is one.
func (s *Server) Close() { s.disconnect(); s.closeRef() }

func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err) // the files are embedded at build time; this cannot fail at run time
	}
	files := http.FileServer(http.FS(sub))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			s.handleIndex(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
	// Browsers ask for this unprompted, and a 404 for it lands in the
	// console next to errors that matter.
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("/api/ports", s.handlePorts)
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/connect", s.handleConnect)
	mux.HandleFunc("/api/disconnect", s.handleDisconnect)
	mux.HandleFunc("/api/command", s.handleCommand)
	mux.HandleFunc("/api/analog/peek", s.handlePeek)
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/api/autoecho", s.handleAutoEcho)
	mux.HandleFunc("/api/link", s.handleLink)
	mux.HandleFunc("/api/unlink", s.handleUnlink)
	mux.HandleFunc("/api/criteria", s.handleCriteria)
	mux.HandleFunc("/api/settings", s.handleSettings)
	mux.HandleFunc("/api/cal/burn", s.handleCalBurn)
	mux.HandleFunc("/api/items", s.handleItems)
	mux.HandleFunc("/api/stagecfg", s.handleStageCfg)
	mux.HandleFunc("/api/memo", s.handlePageMemo)
	mux.HandleFunc("/api/uid", s.handleUID)
	s.planRoutes(mux)
	s.fullRoutes(mux)
	s.libRoutes(mux)
	s.connRoutes(mux)
	s.analogRoutes(mux)
	s.refRoutes(mux)
	s.sdfileRoutes(mux)
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// Every message the panel shows a person is written here, in plain language,
// saying what to do next rather than what failed internally.
func writeErr(w http.ResponseWriter, code int, x msg) {
	writeJSON(w, code, map[string]any{"error": x})
}

func (s *Server) handlePorts(w http.ResponseWriter, r *http.Request) {
	ports, err := serialx.List()
	if err != nil {
		writeErr(w, 500, m("go.ports.list_failed", "detail", err))
		return
	}
	out := make([]map[string]any, 0, len(ports)+1)
	for _, p := range ports {
		out = append(out, map[string]any{
			"name":  p.Name,
			"label": p.Label(),
			"usb":   p.IsUSB,
		})
	}

	// The simulated board is always listed; when its binary is missing the row
	// says why and cannot be picked, since a silent absence leaves nothing to go on.
	if path, err := simboard.Find(); err == nil {
		out = append(out, map[string]any{
			"name":  simboard.PortName,
			"label": m(simboard.LabelKey),
			"usb":   false,
			"path":  path,
		})
	} else {
		out = append(out, map[string]any{
			"name":        simboard.PortName,
			"label":       m(simboard.LabelKey),
			"usb":         false,
			"unavailable": msgOf(err),
		})
	}

	writeJSON(w, 200, map[string]any{"ports": out})
}

// portJSON is one row of the port tree, rendered straight from pt.caps: the
// panel has no port list of its own.
func portJSON(p ptproto.Port) map[string]any {
	// Per-channel parameters handed over split, one control per channel.
	perCh := map[string]map[int]int{}
	for _, param := range p.Params {
		if v := p.ChannelValues(param); v != nil {
			perCh[param] = v
		}
	}
	// What each parameter accepts comes from the firmware, so a dropdown offers
	// exactly the values the board takes.
	limits := map[string]any{}
	for name, l := range p.Limits {
		limits[name] = map[string]any{
			"spec": l.Spec,
			"enum": l.Enum,
			"min":  l.Min,
			"max":  l.Max,
		}
	}
	return map[string]any{
		"perChannel": perCh,
		"limits":     limits,
		"name":       p.Name,
		"board":      p.Board,
		"kind":       string(p.Kind),
		"blk":        p.Block,
		"term":       p.Term,
		"channels":   p.Channels,
		"terminals":  p.TerminalLabels(),
		"loop":       string(p.Loop),
		"params":     p.Params,
		"values":     p.Values,
		"running":    p.Running,
		"runs":       p.Runs,
		"info":       p.Info,
	}
}

// linksLocked is every bound peer's snapshot; s.mu held.
func (s *Server) linksLocked() map[string]any {
	links := map[string]any{}
	for name, l := range s.links {
		links[name] = l.snapshot()
	}
	return links
}

func (s *Server) stateJSON() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := map[string]any{
		"connected": s.board != nil,
		"port":      s.portNam,
		"capsError": s.capsErr,
		"linkError": s.linkErr,
		"autoEcho":  s.autoEcho,
		"echoCount": s.echoCount,
		"echoNote":  s.echoNote,
		"tool":      s.Version,
	}
	// Which limit set produced the ticks on screen; without it a verdict cannot be checked later.
	cName, cLimits, cProblem := criteriaSource()
	st["criteriaPlan"] = cName
	st["criteriaLimits"] = cLimits
	st["criteriaProblem"] = cProblem
	st["links"] = s.linksLocked()
	st["boardIP"] = s.ipJSON()
	st["run"] = s.runStateJSON()
	if s.board != nil {
		ports := make([]map[string]any, 0, len(s.caps.Ports))
		for _, p := range s.caps.Ports {
			ports = append(ports, portJSON(p))
		}
		st["firmware"] = s.caps.Version
		st["ports"] = ports
	}
	// The adapters chosen last time, sent with every state.
	st["saved"] = portmap.Saved()
	return st
}

// handleLink binds a second serial adapter as the far end of a loop=link
// session, and starts repeating whatever the board sends on it.
func (s *Server) handleLink(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Port string `json:"port"` // the board's port name, e.g. "rs485"
		COM  string `json:"com"`
		Baud int    `json:"baud"`
		// TCP is the Ethernet session's far end (the board listens, this
		// connects); each port has exactly one kind of far end.
		TCP string `json:"tcp"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil ||
		body.Port == "" || (body.COM == "" && body.TCP == "") {
		writeErr(w, 400, m("go.link.bad_request"))
		return
	}

	s.mu.Lock()
	p, found := s.caps.Port(body.Port)
	s.mu.Unlock()
	if !found {
		writeErr(w, 400, m("go.link.no_such_port", "port", body.Port))
		return
	}
	if p.Loop != ptproto.LoopLink {
		// A peer on a port that answers on the control port does nothing.
		writeErr(w, 400, m("go.link.no_link_loop", "port", body.Port))
		return
	}
	if body.TCP == "" && body.COM == s.portNam {
		writeErr(w, 400, m("go.link.is_control"))
		return
	}
	// The board only listens while the session runs; said plainly, because the
	// OS's "connection refused" reads like a network fault.
	if body.TCP != "" && !p.Running {
		writeJSON(w, 200, map[string]any{"error": m("go.link.not_running", "port", body.Port)})
		return
	}

	s.unbindLink(body.Port)

	var l *linkPort
	var err error
	if body.TCP != "" {
		l, err = s.bindTCP(body.Port, body.TCP)
	} else {
		l, err = s.bindLink(body.Port, body.COM, body.Baud)
	}
	if err != nil {
		// 200, not 4xx: a busy or unplugged adapter is ordinary at a bench and
		// the page words it. The 4xx above are requests the panel cannot produce.
		what, why := body.COM, m("go.link.hint_busy")
		if body.TCP != "" {
			what, why = body.TCP, m("go.link.hint_ip")
		}
		writeJSON(w, 200, map[string]any{"error": m("go.link.connect_failed", "what", what, "detail", err, "why", why)})
		return
	}

	s.mu.Lock()
	if s.links == nil {
		s.links = map[string]*linkPort{}
	}
	s.links[body.Port] = l
	s.mu.Unlock()
	// Bound after the session started: take up its mode (PORTTOOL-FLOW.md B.9).
	if p.Running {
		l.setMode(p.Values["mode"])
	}
	if body.TCP != "" {
		s.say(m("go.link.tcp_up", "port", body.Port, "addr", body.TCP))
	} else {
		portmap.SetPeer(body.Port, body.COM, l.Baud)
		s.say(m("go.link.serial_up", "port", body.Port, "com", body.COM, "baud", l.Baud))
	}

	writeJSON(w, 200, s.stateJSON())
}

func (s *Server) handleUnlink(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Port string `json:"port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Port == "" {
		writeErr(w, 400, m("go.link.no_unbind"))
		return
	}
	s.unbindLink(body.Port)
	// A person unbinding forgets the adapter; unbindAllLinks on disconnect does not.
	portmap.ForgetPeer(body.Port)
	writeJSON(w, 200, s.stateJSON())
}

func (s *Server) unbindLink(boardPort string) {
	s.mu.Lock()
	l := s.links[boardPort]
	delete(s.links, boardPort)
	s.mu.Unlock()
	if l != nil {
		l.stop()
		s.say(m("go.link.down", "port", l.Board, "com", l.COM))
	}
}

func (s *Server) unbindAllLinks() {
	s.mu.Lock()
	names := make([]string, 0, len(s.links))
	for name := range s.links {
		names = append(names, name)
	}
	s.mu.Unlock()
	for _, n := range names {
		s.unbindLink(n)
	}
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.stateJSON())
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Port string `json:"port"`
		Baud int    `json:"baud"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Port == "" {
		writeErr(w, 400, m("go.connect.no_port"))
		return
	}
	if body.Baud == 0 {
		body.Baud = serialx.DefaultBaud
	}

	s.disconnect()

	port, err := s.Open(body.Port, body.Baud)
	if err != nil {
		writeErr(w, 400, m("go.connect.open_failed", "port", body.Port, "detail", err))
		return
	}

	b := ptboard.New(port, 0)
	caps, capsErr := b.Caps()
	advice := msg{}
	if capsErr != nil {
		advice = noCapsAdvice(port, capsErr) // may wait briefly; kept outside the lock
	}

	s.mu.Lock()
	s.board = b
	s.ctrlRW = port
	s.portNam = body.Port
	s.baud = body.Baud
	s.uid = ""
	portmap.SetControl(body.Port)
	s.caps = caps
	s.capsErr = msg{}
	s.linkErr = msg{}
	if capsErr != nil {
		// Stay connected: the log pane shows whatever the board is saying.
		s.capsErr = advice
	}
	s.mu.Unlock()

	// After caps is stored: the responder needs to know the loop=ctrl ports.
	stopEcho := s.runEchoResponder(b)
	s.mu.Lock()
	s.echoStop = stopEcho
	s.mu.Unlock()
	go s.watchLost(b, port)
	if capsErr == nil {
		s.startIPProbe(b, caps)
	}

	writeJSON(w, 200, s.stateJSON())
}

// simExit waits up to a second for the simulated board's exit code. Its pipe
// closes as it exits, so a read fails a moment before the code is reaped.
func simExit(port io.ReadWriteCloser) (code int, exited bool) {
	for i := 0; i < 20; i++ {
		if code, exited = simboard.Exited(port); exited {
			return code, true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0, false
}

// noCapsAdvice says why a freshly opened port gave no pt.caps. The simulated
// board gets its own wording: firmware flags, baud and terminals mean nothing
// for a program on this PC, and its exit code is the one fact that does.
func noCapsAdvice(port io.ReadWriteCloser, capsErr error) msg {
	if path := simboard.Path(port); path != "" {
		if code, exited := simExit(port); exited {
			return m("go.caps.sim_exited", "code", code, "path", path)
		}
		return m("go.caps.sim_old", "detail", capsErr)
	}
	return m("go.caps.no_answer", "detail", capsErr)
}

// watchLost says so when the control port dies under a connected panel
// (decision 77). A disconnect clears s.board first, so it is not reported as a loss.
func (s *Server) watchLost(b *ptboard.Board, port io.ReadWriteCloser) {
	<-b.Lost()
	why := m("go.lost.no_data")
	if err := b.Err(); err != nil {
		why = msgOf(err)
	}
	lost := m("go.lost.control", "why", why)
	if path := simboard.Path(port); path != "" {
		if code, exited := simExit(port); exited {
			lost = m("go.lost.sim_exited", "code", code, "path", path)
		} else {
			lost = m("go.lost.sim", "why", why)
		}
	}
	s.mu.Lock()
	if s.board != b {
		s.mu.Unlock()
		return
	}
	s.linkErr = lost
	s.mu.Unlock()
	s.say(m("go.alert.lost", "msg", lost))
	s.stopTimedRun(m("go.reason.control_lost"))
	s.stopFull("control port lost")
}

// echoTimeout is short: an automatic echo holds the command lock while it waits.
const echoTimeout = 500 * time.Millisecond

// runEchoResponder answers every frame from a loop=ctrl session with the number
// it carried. loop=link ports are skipped: the firmware refuses pt.echo for them.
func (s *Server) runEchoResponder(b *ptboard.Board) func() {
	events, stop := b.Subscribe(256)
	done := make(chan struct{})

	go func() {
		defer close(done)
		for ev := range events {
			if ev.Kind != ptproto.LineFrame || ev.Frame.Port == "" {
				continue
			}
			s.mu.Lock()
			on := s.autoEcho
			cmd, answer := s.caps.EchoCommand(ev.Frame)
			s.mu.Unlock()
			if !on || !answer {
				continue
			}

			_, err := b.Send(cmd, ptboard.ExpectOne, echoTimeout)

			s.mu.Lock()
			if err != nil {
				// Latest note only: a dead loop would repeat it every frame.
				s.echoNote = err.Error()
			} else {
				s.echoNote = ""
				s.echoCount++
			}
			s.mu.Unlock()
		}
	}()

	return func() {
		stop()
		<-done
	}
}

func (s *Server) disconnect() {
	// First: the timed run's deadman renewal must not outlive the port, and
	// stopping it now releases the outputs while the port is still open.
	s.stopTimedRun(m("go.reason.disconnected"))
	s.stopFull("disconnected")
	s.stopLib()

	// The far ends belong to this board connection.
	s.unbindAllLinks()

	s.mu.Lock()
	b := s.board
	stopEcho := s.echoStop
	s.board = nil
	s.ctrlRW = nil
	s.echoStop = nil
	s.portNam = ""
	s.uid = ""
	s.caps = ptproto.Caps{}
	s.capsErr = msg{}
	s.linkErr = msg{}
	s.echoNote = ""
	s.echoCount = 0
	s.ip = boardIP{}
	s.mu.Unlock()

	// The responder goes down before the port, so it is never mid-send on a closed board.
	if stopEcho != nil {
		stopEcho()
	}
	if b != nil {
		_ = b.Close()
	}
}

func (s *Server) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	s.disconnect()
	writeJSON(w, 200, s.stateJSON())
}

func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Cmd string `json:"cmd"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Cmd == "" {
		writeErr(w, 400, m("go.cmd.empty"))
		return
	}

	s.mu.Lock()
	b := s.board
	s.mu.Unlock()
	if b == nil {
		writeErr(w, 409, m("go.cmd.no_board"))
		return
	}

	lines, err := b.Send(body.Cmd, ptboard.ExpectFor(body.Cmd), ptboard.TimeoutFor(body.Cmd))

	// Only once the board took it: a refused session gets no peer traffic.
	if err == nil {
		s.applyLinkMode(body.Cmd)
	}

	resp := map[string]any{"cmd": body.Cmd, "lines": lines}
	if err != nil {
		var refused *ptboard.RefusedError
		if ok := asRefused(err, &refused); ok {
			// The board's own words: they say which parameter was wrong.
			resp["refused"] = refused.Reason
		} else {
			resp["error"] = err.Error()
		}
	}

	// Any command can change what a port reports, so re-reading is cheaper
	// than tracking which ones do.
	if err == nil || resp["refused"] != nil {
		if caps, cerr := b.Caps(); cerr == nil {
			s.mu.Lock()
			s.caps = caps
			s.mu.Unlock()
		}
	}
	resp["state"] = s.stateJSON()
	writeJSON(w, 200, resp)
}

// handleAutoEcho turns the automatic echo reply on or off (see Server.autoEcho).
func (s *Server) handleAutoEcho(w http.ResponseWriter, r *http.Request) {
	var body struct {
		On bool `json:"on"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, m("go.echo.bad_request"))
		return
	}
	s.mu.Lock()
	s.autoEcho = body.On
	if !body.On {
		s.echoNote = ""
	}
	s.mu.Unlock()
	writeJSON(w, 200, s.stateJSON())
}

// handleEvents streams every line from the board as Server-Sent Events.
//
// SSE, not WebSocket: the standard library has it, so the build stays
// dependency-free (DECISIONS.md 15).
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, m("go.events.no_stream"))
		return
	}

	s.mu.Lock()
	b := s.board
	s.mu.Unlock()
	if b == nil {
		writeErr(w, 409, m("go.cmd.no_board"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(200)

	events, stop := b.Subscribe(1024)
	defer stop()

	// The panel's own lines, merged here and labelled apart from the board's.
	panelEvents, panelBacklog, stopPanel := s.subscribePanel(256)
	defer stopPanel()

	// The commands the panel sends, word for word (PORTTOOL-FLOW.md C.3.6 「研发测试」).
	// Drained before each board line to keep a command ahead of its reply; a full
	// buffer drops, never blocks Send.
	sent := make(chan panelEvent, 256)
	unwatch := b.WatchSent(func(cmd string, at time.Time) {
		select {
		case sent <- panelEvent{Line: "> " + cmd, At: at}:
		default:
		}
	})
	defer unwatch()
	drainSent := func() bool {
		for {
			select {
			case ev := <-sent:
				if !sendTxEvent(w, flusher, ev) {
					return false
				}
			default:
				return true
			}
		}
	}

	// What happened before this page opened.
	for _, ev := range b.Backlog() {
		if !sendEvent(w, flusher, ev) {
			return
		}
	}
	for _, ev := range panelBacklog {
		if !sendPanelEvent(w, flusher, ev) {
			return
		}
	}

	// A comment line every so often keeps the connection from being closed as
	// idle when no session is running and the board is silent.
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return
			}
			if !drainSent() || !sendEvent(w, flusher, ev) {
				return
			}
		case ev := <-sent:
			if !sendTxEvent(w, flusher, ev) {
				return
			}
		case ev, ok := <-panelEvents:
			if !ok {
				return
			}
			if !sendPanelEvent(w, flusher, ev) {
				return
			}
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func sendEvent(w http.ResponseWriter, flusher http.Flusher, ev ptboard.Event) bool {
	payload := map[string]any{
		"seq":  ev.Seq,
		"kind": ev.Kind.String(),
		"line": ev.Line,
		"at":   ev.At.Format("15:04:05.000"),
	}
	if ev.Kind == ptproto.LineFrame && ev.Frame.Port != "" {
		fields := map[string]string{}
		for _, p := range ev.Frame.Fields {
			fields[p.Key] = p.Value
		}
		payload["port"] = ev.Frame.Port
		payload["tick"] = ev.Frame.Tick
		payload["fields"] = fields
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return true
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
		return false
	}
	flusher.Flush()
	return true
}

// sendTxEvent writes one command the panel sent the board, kind "tx" like a
// command typed into the box.
func sendTxEvent(w http.ResponseWriter, flusher http.Flusher, ev panelEvent) bool {
	b, err := json.Marshal(map[string]any{"kind": "tx", "line": ev.Line, "at": ev.At.Format("15:04:05.000")})
	if err != nil {
		return true
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
		return false
	}
	flusher.Flush()
	return true
}

// sendPanelEvent writes one line the panel produced. Marked kind "panel" so
// the log pane can colour and filter it apart from anything the board said.
func sendPanelEvent(w http.ResponseWriter, flusher http.Flusher, ev panelEvent) bool {
	payload := map[string]any{
		"kind": "panel",
		"line": ev.Line,
		"at":   ev.At.Format("15:04:05.000"),
	}
	if ev.Msg != nil {
		payload["msg"], payload["alert"] = *ev.Msg, ev.Msg.alert()
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return true
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
		return false
	}
	flusher.Flush()
	return true
}

func asRefused(err error, target **ptboard.RefusedError) bool {
	if r, ok := err.(*ptboard.RefusedError); ok {
		*target = r
		return true
	}
	return false
}

// PanelPort is the panel's preferred loopback port (decision 118).
const PanelPort = 65432

// Serve binds the panel to loopback only: all interfaces would make Windows
// Firewall ask for administrator rights.
func Serve(open bool, version string) int {
	s := New()
	s.Version = version
	defer s.Close()

	// A fixed port keeps bookmarks working; any free port if it is taken (decision 118).
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", PanelPort))
	if err != nil {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		fmt.Printf("Could not start the local server: %v\n", err)
		return 1
	}
	url := fmt.Sprintf("http://%s/", ln.Addr().String())

	fmt.Printf("PortTool %s\n", version)
	fmt.Printf("Panel: %s\n", url)
	fmt.Println("Leave this window open. Close it to stop the panel.")

	if open {
		openBrowser(url)
	}

	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		fmt.Printf("Server stopped: %v\n", err)
		return 1
	}
	return 0
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		// Not fatal: the address is printed above, so a person can paste it.
		fmt.Printf("Could not open a browser (%v). Open the address above yourself.\n", err)
	}
}
