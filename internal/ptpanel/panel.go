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

	"PortTool/internal/portmap"
	"PortTool/internal/ptboard"
	"PortTool/internal/ptproto"
	"PortTool/internal/serialx"
	"PortTool/internal/simboard"
)

//go:embed web
var webFS embed.FS

// Server holds the one board connection the panel is driving. One at a time is
// deliberate for now: phase one is the control port only. The link ports that
// the loopback sessions need come next, which is why the port is addressed by
// role rather than by name.
type Server struct {
	// Open is how a serial port is obtained. It exists so the whole HTTP
	// surface can be exercised against a fake board: the panel's own logic --
	// caps refresh after every command, SSE fan-out, refusal wording -- is
	// worth testing without a board on the bench, and this is the seam.
	Open func(name string, baud int) (io.ReadWriteCloser, error)

	// Version goes into the reports a plan run produces, so a result can be
	// traced to the build that judged it.
	Version string

	plan planState

	mu      sync.Mutex
	board   *ptboard.Board
	portNam string
	caps    ptproto.Caps
	capsErr msg
	// Why the control port stopped working while still connected: an
	// unplugged adapter, a board that lost power. Shown until disconnect.
	linkErr msg

	// Answering the board's echo frames is on by default, because a counter
	// nobody answers only ever reports misses. It can be switched off, which
	// is the one way to prove from the panel that the counter is real: turn it
	// off and miss must start climbing while seq stands still.
	autoEcho  bool
	echoStop  func()
	echoNote  string // the last thing that went wrong, shown once rather than spammed
	echoCount uint64

	// The "持续" run in flight, if any: the PC's own clock for it, and the
	// goroutine renewing the board's deadman. See hold.go.
	run *timedRun

	// The far ends of the loop=link sessions, keyed by the board's port name.
	// A loop=link counter only closes when one of these is bound, which is why
	// binding is part of setting the bench up rather than an option.
	links map[string]*linkPort

	// Lines the panel itself produced, for the log pane. Separate from the
	// board's events so nobody reads the panel's words as evidence from the
	// hardware.
	emitRing    []panelEvent
	emitSubs    map[int]chan panelEvent
	emitNextSub int
	emitSeq     uint64
}

// New returns a panel that talks to real serial ports.
func New() *Server {
	return &Server{
		autoEcho: true,
		Open:     simboard.OpenPort,
	}
}

// Handler is the panel's whole HTTP surface: the embedded page plus /api.
func (s *Server) Handler() http.Handler { return s.routes() }

// Close drops the board connection, if there is one.
func (s *Server) Close() { s.disconnect() }

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
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/api/autoecho", s.handleAutoEcho)
	mux.HandleFunc("/api/link", s.handleLink)
	mux.HandleFunc("/api/unlink", s.handleUnlink)
	mux.HandleFunc("/api/hold", s.handleHold)
	mux.HandleFunc("/api/fault", s.handleFault)
	mux.HandleFunc("/api/judge", s.handleJudge)
	mux.HandleFunc("/api/portplan", s.handlePortPlan)
	mux.HandleFunc("/api/criteria", s.handleCriteria)
	mux.HandleFunc("/api/fit", s.handleFit)
	mux.HandleFunc("/api/net", s.handleNet)
	mux.HandleFunc("/api/ping", s.handlePing)
	mux.HandleFunc("/api/settings", s.handleSettings)
	s.planRoutes(mux)
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

	// The simulated board. It is listed either way: leaving it out when the
	// binary is missing is a silent absence, and somebody looking for it has
	// nothing to go on - which is where an afternoon went on 2026-09-15. When
	// it cannot be found the row says so and cannot be picked.
	//
	// A production PC gets the exe on its own and never builds the simulator,
	// so there it reads as "not built here", which is the truth.
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

// portJSON is one row of the port tree. It is a straight rendering of what the
// board reported: the panel has no list of its own, so a port added to the
// firmware appears here with no change to this program.
func portJSON(p ptproto.Port) map[string]any {
	// Per-channel parameters are handed over already split, so the page can
	// draw one control per channel instead of a text box holding "1:20,5:75".
	perCh := map[string]map[int]int{}
	for _, param := range p.Params {
		if v := p.ChannelValues(param); v != nil {
			perCh[param] = v
		}
	}
	// The firmware states what each parameter accepts; handing that to the page
	// is what lets a dropdown offer exactly the values the board will take,
	// instead of a table here that drifts the first time a mode is added.
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
	}
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
	}
	// Which limits the ticks on screen were produced by. A verdict without it
	// is not a claim anybody can check later: the same board passes under one
	// limit set and fails under another.
	cName, cLimits, cProblem := criteriaSource()
	st["criteriaPlan"] = cName
	st["criteriaLimits"] = cLimits
	st["criteriaProblem"] = cProblem
	links := map[string]any{}
	for name, l := range s.links {
		links[name] = l.snapshot()
	}
	st["links"] = links
	st["run"] = s.runStateJSON()
	if s.board != nil {
		ports := make([]map[string]any, 0, len(s.caps.Ports))
		for _, p := range s.caps.Ports {
			ports = append(ports, portJSON(p))
		}
		st["firmware"] = s.caps.Version
		st["ports"] = ports
	}
	// What was chosen last time, so nobody has to work out which adapter is
	// which twice. Sent with every state so the page never has to ask
	// separately.
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
		// TCP is the Ethernet session's far end: the board listens and this
		// connects. Separate from COM because they are not alternatives for
		// the same port - each port has exactly one kind of far end, and
		// letting eth be handed a serial adapter is what this replaced.
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
		// Binding an adapter for a port that answers on the control port would
		// do nothing at all, and leave somebody looking for a fault in the
		// wiring instead.
		writeErr(w, 400, m("go.link.no_link_loop", "port", body.Port))
		return
	}
	if body.TCP == "" && body.COM == s.portNam {
		writeErr(w, 400, m("go.link.is_control"))
		return
	}
	// The board only listens while the session is running, so connecting to a
	// stopped one is refused by the operating system - and "connection
	// refused" reads like a network fault, which sends somebody to check
	// cables and subnets that were fine all along. Said plainly instead.
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
		// 200, not 400: a port that is busy or unplugged is an ordinary thing
		// to run into at a bench, not a malformed request. The page already
		// says so in words - answering 4xx only adds a red line to the
		// browser console for a case that is handled. The checks above stay
		// 4xx: those are requests the panel itself cannot produce.
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
	// Deliberate: somebody took that adapter away, so stop offering it.
	// unbindAllLinks (on disconnect) does NOT forget - that is the panel
	// closing down, not a person changing their mind.
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
	s.portNam = body.Port
	portmap.SetControl(body.Port)
	s.caps = caps
	s.capsErr = msg{}
	s.linkErr = msg{}
	if capsErr != nil {
		// Connected but not answering is worth staying connected for: the log
		// pane is now showing whatever the board *is* saying, and that is the
		// evidence somebody needs to work out why.
		s.capsErr = advice
	}
	s.mu.Unlock()

	// Started only after caps is stored: the responder has to know which ports
	// are loop=ctrl before it answers anything.
	stopEcho := s.runEchoResponder(b)
	s.mu.Lock()
	s.echoStop = stopEcho
	s.mu.Unlock()
	go s.watchLost(b, port)

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

// watchLost says so when the control port dies under a connected panel.
// Without it the page keeps showing "已连上" over a board that answers
// nothing (decision 77). A disconnect clears s.board before closing, so a
// close the panel asked for is not reported as a loss.
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
}

// echoTimeout is short on purpose. An automatic echo holds the command lock
// while it waits, and a board that has stopped answering must not make the
// panel unresponsive to the person sitting in front of it.
const echoTimeout = 500 * time.Millisecond

// runEchoResponder answers every frame from a loop=ctrl session with the number
// that frame carried, which is the round trip the board counts on from.
//
// Only loop=ctrl ports: a loop=link port takes its reply off the link under
// test, and the firmware refuses pt.echo for those - answering anyway would
// just fill the log with refusals.
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
				// Kept as the latest note rather than appended: a dead loop
				// would otherwise produce one identical line per frame.
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
	// Before anything else: a timed run renews the deadman on this board, and
	// a renewal goroutine outliving the connection would be sending into a
	// closed port. Stopping it here also releases the outputs while the port is
	// still open - the board's own deadman would do it a few seconds later
	// anyway, but there is no reason to leave 24 V on for those seconds.
	s.stopTimedRun(m("go.reason.disconnected"))

	// The far ends belong to this board connection; a repeater left running
	// against a board that is gone would answer nothing and look bound.
	s.unbindAllLinks()

	s.mu.Lock()
	b := s.board
	stopEcho := s.echoStop
	s.board = nil
	s.echoStop = nil
	s.portNam = ""
	s.caps = ptproto.Caps{}
	s.capsErr = msg{}
	s.linkErr = msg{}
	s.echoNote = ""
	s.echoCount = 0
	s.mu.Unlock()

	// The responder goes down before the port does, so it cannot be mid-send
	// on a closed board.
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

	// Only once the board took it. A peer that started pushing at a session
	// the board refused would be traffic nobody asked for, on a terminal
	// somebody may be holding a probe against.
	if err == nil {
		s.applyLinkMode(body.Cmd)
	}

	resp := map[string]any{"cmd": body.Cmd, "lines": lines}
	if err != nil {
		var refused *ptboard.RefusedError
		if ok := asRefused(err, &refused); ok {
			// The board's own words. Summarising a refusal to "start failed"
			// throws away the only thing that says which parameter was wrong.
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

// handleAutoEcho turns the automatic echo reply on or off.
//
// Worth being able to switch off: with it on, a healthy counter is the only
// thing anyone ever sees. Turning it off and watching miss climb while seq
// stands still is how a person confirms the counter means anything at all.
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
// SSE rather than a WebSocket because the Go standard library has one and not
// the other, and adding a dependency would cost the single-file zero-install
// build - see DECISIONS.md 15. The direction that needs pushing is only board
// to page; commands go the other way as ordinary POSTs.
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

	// The panel's own lines - what a link adapter sent and received - are a
	// second source. Merged here rather than pushed through the board's stream
	// because they did not come from the board, and the log pane labels them
	// so nobody reads them as evidence from the hardware.
	panelEvents, panelBacklog, stopPanel := s.subscribePanel(256)
	defer stopPanel()

	// What happened before this page opened. The reader has been running since
	// the connection was made, so nothing was missed while nobody was looking.
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
			if !sendEvent(w, flusher, ev) {
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

// serve binds the panel to loopback only.
//
// 127.0.0.1 rather than 0.0.0.0 is deliberate: a listener on all interfaces is
// what makes Windows Firewall ask for administrator rights, and this panel has
// no reason to be reachable from another machine.
func Serve(open bool, version string) int {
	s := New()
	s.Version = version
	defer s.disconnect()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
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
