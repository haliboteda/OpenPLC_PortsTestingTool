package ptpanel

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"PortTool/src/ptboard"
	"PortTool/src/ptproto"
	"PortTool/src/serialx"
	"PortTool/src/simboard"
	"PortTool/src/stlink"
)

// The connection block at the top left: the board's clock under the control
// port, the reference board as a second board connection, and whether an
// ST-Link is plugged in. $PROD/docs/modules/M4/PORTTOOL-FLOW.md C.3.6, "连接区".

type connState struct {
	mu      sync.Mutex
	clock   boardClock
	reading bool
	ref     refConn
	probe   probeState
	knx     knxState // the KNX USB interface row, knxusb.go
}

type boardClock struct {
	board *ptboard.Board // the connection this reading came from
	Date  string         `json:"date"`
	Time  string         `json:"time"`
	Init  bool           `json:"init"` // false: nobody ever set the calendar
	Clk   string         `json:"clk"`
	At    time.Time      `json:"-"`
	Err   msg            `json:"error"`
}

type refConn struct {
	board    *ptboard.Board
	port     string
	firmware string
	err      msg
}

type probeState struct {
	checked bool
	busy    bool
	cli     bool
	probes  []stlink.Probe
	err     msg
}

// clockEvery is how often the board's time may be asked for; the page polls
// once a second, and a second tab polling too must not double the traffic.
const clockEvery = 900 * time.Millisecond

// clockTimeout keeps one poll from holding the command line for long; the
// board answers rtc.read at once.
const clockTimeout = time.Second

func (s *Server) connRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/conn", s.handleConn)
	mux.HandleFunc("/api/ref", s.handleRef)
	mux.HandleFunc("/api/knxusb", s.handleKNXUSB)
}

// handleConn answers the page's once-a-second poll. POST {"stlink": true}
// looks for ST-Links again before answering.
func (s *Server) handleConn(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var body struct {
			Stlink bool `json:"stlink"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Stlink {
			s.probeStlink()
		}
	} else {
		s.conn.mu.Lock()
		first := !s.conn.probe.checked && !s.conn.probe.busy
		s.conn.mu.Unlock()
		if first {
			// Listing probes takes a second or two; the first poll starts it
			// and later polls pick the answer up.
			go s.probeStlink()
		}
	}
	s.readClock()
	out := s.connJSON()
	// The peer rows' counts, refreshed with the clock (PORTTOOL-FLOW.md C.3.6 "连接区").
	s.mu.Lock()
	out["links"] = s.linksLocked()
	out["boardIP"] = s.ipJSON()
	s.mu.Unlock()
	writeJSON(w, 200, out)
}

// readClock asks the board for its time, unless it was asked a moment ago or
// a command is on the line: skipped, not queued, so a poll never delays a command.
func (s *Server) readClock() {
	s.mu.Lock()
	b := s.board
	s.mu.Unlock()

	c := &s.conn
	c.mu.Lock()
	if b == nil {
		c.clock = boardClock{}
		c.mu.Unlock()
		return
	}
	if c.reading || (c.clock.board == b && time.Since(c.clock.At) < clockEvery) {
		c.mu.Unlock()
		return
	}
	c.reading = true
	c.mu.Unlock()

	lines, sent, err := b.TrySend("pt.run rtc.read", ptboard.ExpectOne, clockTimeout)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.reading = false
	if !sent {
		return
	}
	next := boardClock{board: b, At: time.Now()}
	if err != nil {
		next.Err = msgOf(err)
	} else {
		next.Date, next.Time, next.Init, next.Clk = parseClock(lines)
		if next.Time == "" {
			next.Err = m("go.conn.clock_unreadable")
		}
	}
	c.clock = next
}

// parseClock reads "OK rtc.read init=1 clk=lse date=26-09-07 time=13:45:07".
func parseClock(lines []string) (date, clock string, init bool, clk string) {
	for _, l := range lines {
		kind, body := ptproto.Classify(l)
		if kind != ptproto.LineOK {
			continue
		}
		f := ptproto.Fields(body)
		d, _ := ptproto.Get(f, "date")
		t, _ := ptproto.Get(f, "time")
		i, _ := ptproto.Get(f, "init")
		k, _ := ptproto.Get(f, "clk")
		if d != "" && len(d) == 8 {
			d = "20" + d // the RTC keeps a two-digit year
		}
		return d, t, i == "1", k
	}
	return "", "", false, ""
}

func (s *Server) connJSON() map[string]any {
	c := &s.conn
	c.mu.Lock()
	defer c.mu.Unlock()
	var clock any
	if c.clock.board != nil {
		clock = map[string]any{
			"date":   c.clock.Date,
			"time":   c.clock.Time,
			"init":   c.clock.Init,
			"clk":    c.clock.Clk,
			"age_ms": time.Since(c.clock.At).Milliseconds(),
			"error":  c.clock.Err,
		}
	}
	probes := c.probe.probes
	if probes == nil {
		probes = []stlink.Probe{}
	}
	return map[string]any{
		"clock": clock,
		"ref":   c.refJSONLocked(),
		"stlink": map[string]any{
			"checked": c.probe.checked,
			"busy":    c.probe.busy,
			"cli":     c.probe.cli,
			"probes":  probes,
			"error":   c.probe.err,
		},
	}
}

func (s *Server) probeStlink() {
	c := &s.conn
	c.mu.Lock()
	if c.probe.busy {
		c.mu.Unlock()
		return
	}
	c.probe.busy = true
	c.mu.Unlock()

	probes, err := stlink.ListProbes()

	c.mu.Lock()
	defer c.mu.Unlock()
	c.probe = probeState{checked: true, cli: !stlink.ErrNotFound(err), probes: probes}
	switch {
	case stlink.ErrNotFound(err):
		c.probe.err = m("go.conn.stlink_no_cli")
	case err != nil:
		c.probe.err = m("go.conn.stlink_failed", "detail", err.Error())
	}
}

// ---- the reference board (decision 103) ----

func (c *connState) refJSONLocked() map[string]any {
	return map[string]any{
		"connected": c.ref.board != nil,
		"port":      c.ref.port,
		"firmware":  c.ref.firmware,
		"error":     c.ref.err,
	}
}

// refBoard is the reference board's connection, nil when there is none. The
// executor takes it as ptseq.Runner.Ref (PORTTOOL-FLOW.md C.3.7).
func (s *Server) refBoard() *ptboard.Board {
	s.conn.mu.Lock()
	defer s.conn.mu.Unlock()
	return s.conn.ref.board
}

// handleRef connects ({"port": "COM7"} or "sim"), disconnects ({"port": ""})
// or, on GET, reports the reference board.
func (s *Server) handleRef(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var body struct {
			Port string `json:"port"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, m("go.ref.bad_request"))
			return
		}
		if body.Port == "" {
			s.closeRef()
		} else if x, ok := s.openRef(body.Port); !ok {
			writeJSON(w, 200, map[string]any{"error": x})
			return
		}
	}
	s.conn.mu.Lock()
	out := s.conn.refJSONLocked()
	s.conn.mu.Unlock()
	writeJSON(w, 200, out)
}

func (s *Server) openRef(port string) (msg, bool) {
	s.mu.Lock()
	control, ctrlRW := s.portNam, s.ctrlRW
	s.mu.Unlock()
	// Two simulated boards are two processes; two real ones cannot share a port.
	if !simboard.IsSim(port) && strings.EqualFold(port, control) {
		return m("go.ref.is_control", "port", port), false
	}
	s.closeRef()

	rw, err := s.Open(port, serialx.DefaultBaud)
	if err != nil {
		return m("go.ref.open_failed", "port", port, "detail", err), false
	}
	// Two simulated boards get the STG-02 DO->DI wire between them, as
	// porttool run --ref does (simboard.OpenPair; PORTTOOL-FLOW.md C.3.7).
	if ctrlRW != nil && simboard.IsSim(control) && simboard.IsSim(port) {
		simboard.Link(ctrlRW, rw)
	}
	b := ptboard.New(rw, 0)
	caps, err := b.Caps()
	if err != nil {
		_ = b.Close()
		return m("go.ref.no_caps", "port", port, "detail", err.Error()), false
	}

	s.conn.mu.Lock()
	s.conn.ref = refConn{board: b, port: port, firmware: caps.Version}
	s.conn.mu.Unlock()
	go s.watchRef(b)
	s.say(m("go.ref.up", "port", port, "firmware", caps.Version))
	return msg{}, true
}

// watchRef keeps the row from saying "connected" over a cable that came out.
func (s *Server) watchRef(b *ptboard.Board) {
	<-b.Lost()
	s.conn.mu.Lock()
	if s.conn.ref.board != b {
		s.conn.mu.Unlock()
		return
	}
	port := s.conn.ref.port
	s.conn.ref.board = nil
	s.conn.ref.err = m("go.ref.lost", "port", port)
	s.conn.mu.Unlock()
	_ = b.Close()
	s.say(m("go.ref.lost", "port", port))
}

func (s *Server) closeRef() {
	s.conn.mu.Lock()
	b, port := s.conn.ref.board, s.conn.ref.port
	s.conn.ref = refConn{}
	s.conn.mu.Unlock()
	if b != nil {
		_ = b.Close()
		s.say(m("go.ref.down", "port", port))
	}
}
