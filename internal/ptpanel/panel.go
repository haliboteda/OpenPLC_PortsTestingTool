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
	capsErr string

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
		Open: func(name string, baud int) (io.ReadWriteCloser, error) {
			// One reserved name reaches the simulated board. Doing it here
			// rather than with a mode flag means every path that opens a port
			// - the panel, a plan run, a test - gets it for the same reason
			// and cannot disagree about what "sim" means.
			if simboard.IsSim(name) {
				return simboard.Open()
			}
			return serialx.Open(name, baud)
		},
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
	mux.Handle("/", http.FileServer(http.FS(sub)))
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
func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

func (s *Server) handlePorts(w http.ResponseWriter, r *http.Request) {
	ports, err := serialx.List()
	if err != nil {
		writeErr(w, 500, "读不到这台电脑的串口列表："+err.Error())
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
			"label": simboard.Label,
			"usb":   false,
			"path":  path,
		})
	} else {
		out = append(out, map[string]any{
			"name":        simboard.PortName,
			"label":       simboard.Label,
			"usb":         false,
			"unavailable": err.Error(),
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
	st["saved"] = savedJSON()
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
		writeErr(w, 400, "要说清楚哪个端口配哪个对端。")
		return
	}

	s.mu.Lock()
	p, found := s.caps.Port(body.Port)
	s.mu.Unlock()
	if !found {
		writeErr(w, 400, "板子没报过叫 "+body.Port+" 的端口。先连上板子。")
		return
	}
	if p.Loop != ptproto.LoopLink {
		// Binding an adapter for a port that answers on the control port would
		// do nothing at all, and leave somebody looking for a fault in the
		// wiring instead.
		writeErr(w, 400, body.Port+" 的回环不走被测链路，用不着第二个串口。")
		return
	}
	if body.TCP == "" && body.COM == s.portNam {
		writeErr(w, 400, "这个串口已经是控制口了，不能同时当被测链路的对端。")
		return
	}
	// The board only listens while the session is running, so connecting to a
	// stopped one is refused by the operating system - and "connection
	// refused" reads like a network fault, which sends somebody to check
	// cables and subnets that were fine all along. Said plainly instead.
	if body.TCP != "" && !p.Running {
		writeJSON(w, 200, map[string]any{"error": body.Port +
			" 还没在跑 —— 板子是会话起来才开始监听的。先点「开始」，" +
			"而且要选「持续」：「单次」跑完就停了，那时候再连就没人听了。"})
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
		what := body.COM
		why := "看看是不是别的程序占着它，或者适配器没插好。"
		if body.TCP != "" {
			what = body.TCP
			why = "板子的 IP 和端口对不对？先 ping 一下 —— ping 不通就不是这一步的事。"
		}
		writeJSON(w, 200, map[string]any{"error": fmt.Sprintf(
			"连不上 %s：%v。%s", what, err, why)})
		return
	}

	s.mu.Lock()
	if s.links == nil {
		s.links = map[string]*linkPort{}
	}
	s.links[body.Port] = l
	s.mu.Unlock()
	if body.TCP != "" {
		s.emit(fmt.Sprintf("[link %s@%s] 连上了。板子发什么就原样送回去。",
			body.Port, body.TCP))
	} else {
		rememberPeer(body.Port, body.COM, l.Baud)
		s.emit(fmt.Sprintf("[link %s@%s] 绑好了，%d 8N1。板子发什么就原样送回去。",
			body.Port, body.COM, l.Baud))
	}

	writeJSON(w, 200, s.stateJSON())
}

func (s *Server) handleUnlink(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Port string `json:"port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Port == "" {
		writeErr(w, 400, "没说要解开哪个端口。")
		return
	}
	s.unbindLink(body.Port)
	// Deliberate: somebody took that adapter away, so stop offering it.
	// unbindAllLinks (on disconnect) does NOT forget - that is the panel
	// closing down, not a person changing their mind.
	forgetPeer(body.Port)
	writeJSON(w, 200, s.stateJSON())
}

func (s *Server) unbindLink(boardPort string) {
	s.mu.Lock()
	l := s.links[boardPort]
	delete(s.links, boardPort)
	s.mu.Unlock()
	if l != nil {
		l.stop()
		s.emit(fmt.Sprintf("[link %s@%s] 解开了", l.Board, l.COM))
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
		writeErr(w, 400, "没说要连哪个串口。")
		return
	}
	if body.Baud == 0 {
		body.Baud = serialx.DefaultBaud
	}

	s.disconnect()

	port, err := s.Open(body.Port, body.Baud)
	if err != nil {
		writeErr(w, 400, fmt.Sprintf(
			"打不开 %s：%v。看看是不是别的程序占着它（串口监视器、Arduino IDE），或者适配器没插好。",
			body.Port, err))
		return
	}

	b := ptboard.New(port, 0)
	caps, capsErr := b.Caps()

	s.mu.Lock()
	s.board = b
	s.portNam = body.Port
	rememberControl(body.Port)
	s.caps = caps
	s.capsErr = ""
	if capsErr != nil {
		// Connected but not answering is worth staying connected for: the log
		// pane is now showing whatever the board *is* saying, and that is the
		// evidence somebody needs to work out why.
		s.capsErr = fmt.Sprintf(
			"串口开了，但板子没有回答 pt.caps：%v。检查固件是不是用 PORTTOOL_ENABLE=1 编的，波特率是不是 115200，接线是 C05/C06/C02。",
			capsErr)
	}
	s.mu.Unlock()

	// Started only after caps is stored: the responder has to know which ports
	// are loop=ctrl before it answers anything.
	stopEcho := s.runEchoResponder(b)
	s.mu.Lock()
	s.echoStop = stopEcho
	s.mu.Unlock()

	writeJSON(w, 200, s.stateJSON())
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
			seq, ok := ptproto.Get(ev.Frame.Fields, "seq")
			if !ok {
				continue
			}

			s.mu.Lock()
			on := s.autoEcho
			p, found := s.caps.Port(ev.Frame.Port)
			s.mu.Unlock()
			if !on || !found || p.Kind != ptproto.KindSession {
				continue
			}
			// ctrl and self both take their reply on this port. A link port
			// takes it off the link under test, which the firmware enforces by
			// refusing pt.echo - answering here would let its counter climb
			// with that link dead.
			if p.Loop != ptproto.LoopCtrl && p.Loop != ptproto.LoopSelf {
				continue
			}

			_, err := b.Send("pt.echo "+ev.Frame.Port+" "+seq, ptboard.ExpectOne, echoTimeout)

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
	s.stopTimedRun("断开了板子")

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
	s.capsErr = ""
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
		writeErr(w, 400, "命令是空的。")
		return
	}

	s.mu.Lock()
	b := s.board
	s.mu.Unlock()
	if b == nil {
		writeErr(w, 409, "还没有连上板子。")
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
		writeErr(w, 400, "没说要开还是要关。")
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
		writeErr(w, 500, "这个浏览器不支持持续推送。")
		return
	}

	s.mu.Lock()
	b := s.board
	s.mu.Unlock()
	if b == nil {
		writeErr(w, 409, "还没有连上板子。")
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
	b, err := json.Marshal(map[string]any{
		"kind": "panel",
		"line": ev.Line,
		"at":   ev.At.Format("15:04:05.000"),
	})
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
