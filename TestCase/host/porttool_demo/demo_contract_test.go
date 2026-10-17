// Case T4-05: the step-6 demo and the fixture firmware speak one protocol.
//
// The same conversation is held with the fixture's sim board and the demo's host
// build, both parsed by src/ptproto; differences are allowed only where
// PORTTOOL-FLOW.md "A.9" lists them (the tables below). With -build,
// build_test.go compiles both boards first; without it the tests skip.
package porttool_demo

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"PortTool/src/ptitems"
	"PortTool/src/ptplan"
	"PortTool/src/ptproto"
)

const lineMax = 192 // PORTTOOL_REPLY_MAX: every line the board sends

// How each port the demo has is started. Only parameters the demo accepts, so
// the firmware gets the very same command.
var startArgs = map[string]string{
	"din":   "ch=1,3,5 period=200",
	"dout":  "ch=1,5 duty=255 period=200",
	"relay": "ch=1,2 mode=cycle close=1000 open=1000 count=1",
	"ain":   "ch=1,2 period=200",
	"aout":  "ch=1,2 mv=500 period=200",
	"temp":  "ch=1,2 period=200",
	"rs232": "period=200",
	"rs485": "baud=115200 period=200",
	"can":   "baud=500000 mode=normal period=200",
	"eth":   "mode=echo port=%d period=200",
	"usb":   "mode=echo period=200",
	"sd":    "period=200",
}

// Frame fields the demo adds or lacks, per port (PORTTOOL-FLOW.md "A.9").
var demoExtra = map[string][]string{
	"ain":  {"ai1_mv", "ai2_ua", "cal"},
	"aout": {"cal"},
}

var demoMissing = map[string][]string{
	"dout":  {"tick"},
	"rs485": {"overrun"},
}

// How each pt.run target the demo has is asked, and the reply fields it lacks
// (PORTTOOL-FLOW.md "A.9").
var runArgs = map[string][]string{
	"sd.integrity": {"bytes=8192", "bytes=4096 passes=3"},
	"sd.speed":     {"bytes=8192"},
}

var runMissing = map[string][]string{
	"sd.probe": {"err"},
}

// demoKind: hardware the demo reports with a different kind (A.9: sdram has
// pt.run targets only).
var demoKind = map[string]ptproto.Kind{"sdram": ptproto.KindRun}

// Commands whose replies must be word for word the same on both.
var sameReply = []string{
	"pt.list",
	"pt.start nosuch",
	"pt.start din ch=1,9",
	"pt.start din period=abc",
	"pt.set din ch=2",
	"pt.echo din 1",
	"pt.stop nosuch",
	"pt.foo",
	"pt.hold abc",
	"pt.hold 6000",
	"pt.hold 0",
	"pt.led fault=2",
	"pt.led fault=0",
	"pt.run nosuch",
	"pt.start rs232 baud=1234",
	"pt.start eth ip=abc",
}

type board struct {
	t      *testing.T
	name   string
	cmd    *exec.Cmd
	in     io.WriteCloser
	lines  chan string
	all    []string // every line received, for the line-length check
	frames map[string][]ptproto.Frame
}

func start(t *testing.T, name, path string) *board {
	t.Helper()
	cmd := exec.Command(path)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	b := &board{t: t, name: name, cmd: cmd, in: in, lines: make(chan string, 4096),
		frames: map[string][]ptproto.Frame{}}
	go func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 64*1024), 64*1024)
		for sc.Scan() {
			b.lines <- strings.TrimRight(sc.Text(), "\r")
		}
		close(b.lines)
	}()
	t.Cleanup(func() {
		in.Close()
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			cmd.Process.Kill()
		}
	})
	return b
}

// next returns the next line, filing frames and skipping logs when asked to.
func (b *board) next(wantReply bool) (string, ptproto.LineKind) {
	b.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case l, ok := <-b.lines:
			if !ok {
				b.t.Fatalf("%s: board exited", b.name)
			}
			b.all = append(b.all, l)
			kind, body := ptproto.Classify(l)
			switch kind {
			case ptproto.LineFrame:
				if f, ok := ptproto.ParseFrame(body); ok {
					b.frames[f.Port] = append(b.frames[f.Port], f)
				}
				if !wantReply {
					return l, kind
				}
			case ptproto.LineLog:
				continue
			default:
				return l, kind
			}
		case <-deadline:
			b.t.Fatalf("%s: no reply within 10 s", b.name)
		}
	}
}

func (b *board) send(line string) {
	b.t.Helper()
	if _, err := io.WriteString(b.in, line+"\n"); err != nil {
		b.t.Fatalf("%s: %v", b.name, err)
	}
}

// ask sends one command and returns its single reply line.
func (b *board) ask(line string) string {
	b.send(line)
	l, _ := b.next(true)
	return l
}

func (b *board) caps() (ptproto.Caps, []string) {
	b.t.Helper()
	b.send("pt.caps")
	head, _ := b.next(true)
	m := regexp.MustCompile(`lines=(\d+)`).FindStringSubmatch(head)
	if m == nil {
		b.t.Fatalf("%s: caps header without lines=: %q", b.name, head)
	}
	n, _ := strconv.Atoi(m[1])
	lines := []string{head}
	for i := 0; i < n; i++ {
		l, _ := b.next(true)
		lines = append(lines, l)
	}
	c, err := ptproto.ParseCaps(lines)
	if err != nil {
		b.t.Fatalf("%s: %v", b.name, err)
	}
	return c, lines
}

// frame waits for the next frame of one port.
func (b *board) frame(port string) ptproto.Frame {
	b.t.Helper()
	have := len(b.frames[port])
	for len(b.frames[port]) == have {
		b.next(false)
	}
	return b.frames[port][have]
}

func u32(t *testing.T, f ptproto.Frame, key string) uint32 {
	t.Helper()
	v, ok := ptproto.GetU32(f.Fields, key)
	if !ok {
		t.Fatalf("frame %q has no %s=", f.Raw, key)
	}
	return v
}

func keys(pairs []ptproto.Pair) map[string]bool {
	out := map[string]bool{}
	for _, p := range pairs {
		out[p.Key] = true
	}
	return out
}

func diff(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// transcript is what one board said, reduced to what the two are compared on.
type transcript struct {
	id      []ptproto.Pair
	caps    ptproto.Caps
	header  []ptproto.Pair
	replies map[string]string
	echoes  map[string]string // pt.echo on a link port, before it is started
	keys    map[string]map[string]bool
	runs    map[string]map[string]bool // reply fields per pt.run command
	cal     string
	line    []string // replies of the rs232 rate switch
}

// runReply asks one pt.run and returns its fields.
func runReply(t *testing.T, b *board, target, args string) map[string]bool {
	t.Helper()
	r := b.ask(strings.TrimSpace("pt.run " + target + " " + args))
	kind, body := ptproto.Classify(r)
	if kind != ptproto.LineOK || !strings.HasPrefix(body, target+" ") {
		t.Errorf("%s: pt.run %s %s: %q", b.name, target, args, r)
		return nil
	}
	return keys(ptproto.Fields(strings.TrimPrefix(body, target+" ")))
}

// nudgedFrame waits for a frame of port while sending empty lines, which is
// what lets frames out after a line-rate switch (PORTTOOL-FLOW.md B.7).
func (b *board) nudgedFrame(port string) ptproto.Frame {
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(100 * time.Millisecond):
				io.WriteString(b.in, "\n")
			}
		}
	}()
	defer close(stop)
	return b.frame(port)
}

func converse(t *testing.T, b *board, sessions []string, targets []string) transcript {
	tr := transcript{replies: map[string]string{}, echoes: map[string]string{},
		keys: map[string]map[string]bool{}, runs: map[string]map[string]bool{}}

	id := b.ask("pt.id")
	kind, body := ptproto.Classify(id)
	if kind != ptproto.LineOK {
		t.Fatalf("%s: pt.id: %q", b.name, id)
	}
	tr.id = ptproto.Fields(body)
	if uid, _ := ptproto.Get(tr.id, "uid"); !regexp.MustCompile(`^[0-9A-F]{24}$`).MatchString(uid) {
		t.Errorf("%s: pt.id uid=%q is not 24 hex digits", b.name, uid)
	}

	var capsLines []string
	tr.caps, capsLines = b.caps()
	_, hb := ptproto.Classify(capsLines[0])
	tr.header = ptproto.Fields(hb)

	for _, c := range sameReply {
		tr.replies[c] = b.ask(c)
	}
	for _, p := range tr.caps.Ports {
		if p.Loop == ptproto.LoopLink && containsStr(sessions, p.Name) {
			tr.echoes[p.Name] = b.ask("pt.echo " + p.Name + " 1")
		}
	}

	for _, name := range sessions {
		port, _ := tr.caps.Port(name)
		args := startArgs[name]
		if name == "eth" {
			args = fmt.Sprintf(args, freePort(t))
		}
		if r := b.ask("pt.start " + name + " " + args); r != "OK started "+name {
			t.Errorf("%s: pt.start %s %s: %q", b.name, name, args, r)
			continue
		}
		n := 4
		if name == "relay" {
			n = 3
		}
		var prev ptproto.Frame
		for i := 0; i < n; i++ {
			f := b.frame(name)
			seq, rx, miss := u32(t, f, "seq"), u32(t, f, "rx"), u32(t, f, "miss")
			if i == 0 && (seq != 1 || rx != 0 || miss != 0) {
				t.Errorf("%s %s: first frame should be seq=1 rx=0 miss=0: %q", b.name, name, f.Raw)
			}
			if port.Loop == ptproto.LoopCtrl || port.Loop == ptproto.LoopSelf {
				if i > 0 {
					ps := u32(t, prev, "seq")
					if seq != ps+1 || rx != ps || miss != 0 {
						t.Errorf("%s %s: after echoing %d the next frame should be seq=%d rx=%d miss=0: %q",
							b.name, name, ps, ps+1, ps, f.Raw)
					}
				}
				cmd, ok := tr.caps.EchoCommand(f)
				if !ok {
					t.Fatalf("%s %s: no echo command for a ctrl frame", b.name, name)
				}
				if r := b.ask(cmd); r != "OK echo "+name+" "+strconv.Itoa(int(seq)) {
					t.Errorf("%s: %s: %q", b.name, cmd, r)
				}
			}
			prev = f
		}
		tr.keys[name] = keys(prev.Fields)
		if r := b.ask("pt.stop " + name); r != "OK stopped "+name {
			t.Errorf("%s: pt.stop %s: %q", b.name, name, r)
		}
	}

	mirror(t, b)

	for _, target := range targets {
		args := runArgs[target]
		if args == nil {
			args = []string{""}
		}
		for _, a := range args {
			tr.runs[strings.TrimSpace(target+" "+a)] = runReply(t, b, target, a)
		}
	}
	tr.cal = b.ask("pt.cal")

	// The control line switched and back: replies, then a frame at the new rate.
	tr.line = append(tr.line, b.ask("pt.start rs232 baud=19200 period=200"))
	if f := b.nudgedFrame("rs232"); u32(t, f, "baud") != 19200 {
		t.Errorf("%s: rs232 frame after the switch: %q", b.name, f.Raw)
	}
	tr.line = append(tr.line, b.ask("pt.stop rs232"))

	if r := b.ask("pt.stop all"); r != "OK stopped all" {
		t.Errorf("%s: pt.stop all: %q", b.name, r)
	}
	for _, l := range b.all {
		if len(l) > lineMax {
			t.Errorf("%s: %d-byte line over the %d limit: %q", b.name, len(l), lineMax, l)
		}
	}
	return tr
}

// mirror is the full-rate run: every byte sent to the eth port comes back.
func mirror(t *testing.T, b *board) {
	port := freePort(t)
	if r := b.ask(fmt.Sprintf("pt.start eth mode=mirror port=%d period=200", port)); r != "OK started eth" {
		t.Errorf("%s: mirror start: %q", b.name, r)
		return
	}
	var conn net.Conn
	var err error
	for i := 0; i < 100; i++ {
		if conn, err = net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Errorf("%s: could not connect to the mirror: %v", b.name, err)
		return
	}
	defer conn.Close()

	sent := make([]byte, 64*1024)
	rand.Read(sent)
	go func() {
		for off := 0; off < len(sent); off += 1024 {
			conn.Write(sent[off : off+1024])
		}
	}()
	// The board pushes frames while this runs; drain them so its stdout never blocks.
	stop, drained := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(drained)
		for {
			select {
			case <-stop:
				return
			case l, ok := <-b.lines:
				if !ok {
					return
				}
				b.all = append(b.all, l)
			}
		}
	}()
	got := make([]byte, len(sent))
	conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	_, err = io.ReadFull(conn, got)
	close(stop)
	<-drained
	if err != nil {
		t.Errorf("%s: mirror read: %v", b.name, err)
	} else if !bytes.Equal(got, sent) {
		t.Errorf("%s: mirror sent back different bytes", b.name)
	}

	f := b.frame("eth")
	if v, _ := ptproto.Get(f.Fields, "mode"); v != "mirror" {
		t.Errorf("%s: mirror frame says mode=%s: %q", b.name, v, f.Raw)
	}
	if r := b.ask("pt.stop eth"); r != "OK stopped eth" {
		t.Errorf("%s: pt.stop eth: %q", b.name, r)
	}
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestDemoSpeaksTheFixtureProtocol(t *testing.T) {
	needBuild(t)
	fixPath, demoPath := fixtureSim, demoSim

	demo := start(t, "demo", demoPath)
	demoCaps, _ := demo.caps()
	var sessions, targets []string
	for _, p := range demoCaps.Ports {
		targets = append(targets, p.Runs...)
		if want, ok := demoKind[p.Name]; ok && p.Kind == want {
			continue
		}
		if p.Kind != ptproto.KindSession {
			t.Errorf("demo port %s is kind=%s; only %v may differ", p.Name, p.Kind, demoKind)
			continue
		}
		if _, ok := startArgs[p.Name]; !ok {
			t.Errorf("demo port %s has no start command in this test", p.Name)
			continue
		}
		sessions = append(sessions, p.Name)
	}

	fix := converse(t, start(t, "fixture", fixPath), sessions, targets)
	dem := converse(t, demo, sessions, targets)

	// Identity: the demo says it is one and follows the firmware's protocol version.
	fv, _ := ptproto.Get(fix.id, "porttool")
	dv, _ := ptproto.Get(dem.id, "porttool")
	if fv == "" || fv != dv {
		t.Errorf("pt.id porttool=: fixture %q, demo %q - FX_PROTOCOL_VERSION must follow PORTTOOL_VERSION", fv, dv)
	}
	if fix.caps.Version != dem.caps.Version {
		t.Errorf("pt.caps porttool=: fixture %q, demo %q", fix.caps.Version, dem.caps.Version)
	}
	for _, c := range []struct {
		what  string
		pairs []ptproto.Pair
		want  bool
	}{{"fixture pt.id", fix.id, false}, {"fixture pt.caps", fix.header, false},
		{"demo pt.id", dem.id, true}, {"demo pt.caps", dem.header, true}} {
		if _, has := ptproto.Get(c.pairs, "demo"); has != c.want {
			t.Errorf("%s: demo= present is %v, want %v", c.what, has, c.want)
		}
	}
	if got, want := diff(keys(fix.id), keys(dem.id)), []string{"rx_dropped", "rx_errors"}; !equal(got, want) {
		t.Errorf("pt.id fields the demo lacks: %v, want %v", got, want)
	}
	for _, k := range []string{"hold", "led"} {
		a, _ := ptproto.Get(fix.header, k)
		b, _ := ptproto.Get(dem.header, k)
		if a != b {
			t.Errorf("pt.caps %s=: fixture %q, demo %q", k, a, b)
		}
	}

	// Shape: every demo port is the firmware's port, with a subset of its
	// parameters and pt.run targets.
	for _, d := range dem.caps.Ports {
		f, ok := fix.caps.Port(d.Name)
		if !ok {
			t.Errorf("demo port %s is not in the fixture firmware", d.Name)
			continue
		}
		for _, r := range d.Runs {
			if !containsStr(f.Runs, r) {
				t.Errorf("%s: demo target %s is not a fixture target %v", d.Name, r, f.Runs)
			}
		}
		if _, ok := demoKind[d.Name]; ok && (d.Board != f.Board || d.Term != f.Term) {
			t.Errorf("%s differs: fixture %s %s, demo %s %s", d.Name, f.Board, f.Term, d.Board, d.Term)
		}
	}
	for _, name := range sessions {
		d, _ := dem.caps.Port(name)
		f, ok := fix.caps.Port(name)
		if !ok {
			continue
		}
		if d.Board != f.Board || d.Block != f.Block || d.Term != f.Term || d.Channels != f.Channels ||
			d.Loop != f.Loop || d.Kind != f.Kind {
			t.Errorf("%s shape differs:\n  fixture %+v\n  demo    %+v", name,
				[]any{f.Board, f.Block, f.Term, f.Channels, f.Loop, f.Kind},
				[]any{d.Board, d.Block, d.Term, d.Channels, d.Loop, d.Kind})
		}
		if !equal(d.TerminalLabels(), f.TerminalLabels()) {
			t.Errorf("%s terminal labels: fixture %v, demo %v", name, f.TerminalLabels(), d.TerminalLabels())
		}
		for _, p := range d.Params {
			if !containsStr(f.Params, p) {
				t.Errorf("%s: demo parameter %s is not a fixture parameter %v", name, p, f.Params)
			}
		}
	}

	// Replies and refusals: word for word.
	for _, c := range sameReply {
		if fix.replies[c] != dem.replies[c] {
			t.Errorf("%s:\n  fixture %q\n  demo    %q", c, fix.replies[c], dem.replies[c])
		}
	}
	for name, r := range fix.echoes {
		if dem.echoes[name] != r {
			t.Errorf("pt.echo %s 1 on a link port:\n  fixture %q\n  demo    %q", name, r, dem.echoes[name])
		}
	}

	// Frames: the same fields, apart from the listed differences.
	for _, name := range sessions {
		extra := diff(dem.keys[name], fix.keys[name])
		missing := diff(fix.keys[name], dem.keys[name])
		if !equal(extra, sorted(demoExtra[name])) || !equal(missing, sorted(demoMissing[name])) {
			t.Errorf("!%s fields: demo adds %v (allowed %v), lacks %v (allowed %v)", name,
				extra, demoExtra[name], missing, demoMissing[name])
		}
	}
	// pt.run replies: the same fields, apart from the listed differences.
	for cmd, fk := range fix.runs {
		target, _, _ := strings.Cut(cmd, " ")
		dk := dem.runs[cmd]
		if fk == nil || dk == nil {
			continue // already reported
		}
		if extra, missing := diff(dk, fk), diff(fk, dk); len(extra) > 0 || !equal(missing, sorted(runMissing[target])) {
			t.Errorf("pt.run %s fields: demo adds %v, lacks %v (allowed %v)", cmd, extra, missing, runMissing[target])
		}
	}
	calRe := regexp.MustCompile(`^OK cal=[0-9A-F]{112}$`)
	if !calRe.MatchString(fix.cal) || !calRe.MatchString(dem.cal) {
		t.Errorf("pt.cal: fixture %q, demo %q - both should be 56 bytes in hex", fix.cal, dem.cal)
	}
	if !equal(fix.line, dem.line) {
		t.Errorf("rs232 rate switch replies: fixture %q, demo %q", fix.line, dem.line)
	}
	t.Logf("compared %d sessions and %d pt.run targets on both: %s %s", len(sessions), len(targets),
		strings.Join(sessions, " "), strings.Join(targets, " "))
}

// TestDemoRunsEveryFullTestItem: every user-level item of the catalogue, its
// targets and the criteria steps of the default plan, run on both boards; a
// check's field missing from the reply or frame fails it (decision 127),
// except an if_present check on the demo (STG-07).
func TestDemoRunsEveryFullTestItem(t *testing.T) {
	needBuild(t)
	fixPath, demoPath := fixtureSim, demoSim
	plan, err := ptplan.Load("../../../src/plans/station6-poweron.json")
	if err != nil {
		t.Fatal(err)
	}
	steps := map[string]ptplan.Step{}
	for _, s := range plan.Steps {
		steps[s.ID] = s
	}
	for _, c := range []struct{ name, path string }{{"fixture", fixPath}, {"demo", demoPath}} {
		b := start(t, c.name, c.path)
		caps, _ := b.caps()
		n := 0
		for _, it := range ptitems.Catalogue {
			if !it.In(ptitems.User) {
				continue
			}
			if _, ok := caps.Port(it.Port); !ok {
				t.Errorf("%s: user-level item %s: port %s not in pt.caps", c.name, it.ID, it.Port)
				continue
			}
			for _, tg := range it.Targets {
				if tg.Run != "" {
					runReply(t, b, tg.Run, "")
					continue
				}
				var kv []string
				for k, v := range tg.Set {
					kv = append(kv, k+"="+v)
				}
				sort.Strings(kv)
				sessionFields(t, b, tg.Session, kv)
			}
			for _, id := range it.Criteria {
				s, ok := steps[id]
				if !ok || s.OnRef() || (s.Type != ptplan.TypePtRun && s.Type != ptplan.TypePtSession) {
					continue
				}
				// A step whose mode the board does not offer is not run (ptlib
				// offered): the demo has no eth / usb sink or source (A.9).
				if p, _ := caps.Port(s.Port); s.Type == ptplan.TypePtSession && s.Params["mode"] != "" {
					if lim, ok := p.Limits["mode"]; ok {
						if okv, _ := lim.Accepts(s.Params["mode"]); !okv {
							if c.name == "fixture" {
								t.Errorf("fixture: %s step %s: mode=%s not offered", it.ID, id, s.Params["mode"])
							}
							continue
						}
					}
				}
				var got map[string]bool
				if s.Type == ptplan.TypePtRun {
					got = runReply(t, b, s.Target, strings.Join(s.ParamArgs(), " "))
				} else {
					args := s.ParamArgs()
					for i, a := range args {
						if s.Port == "eth" && strings.HasPrefix(a, "port=") {
							args[i] = fmt.Sprintf("port=%d", freePort(t))
						}
					}
					got = sessionFields(t, b, s.Port, args)
				}
				if got == nil {
					continue
				}
				for _, ck := range s.Checks {
					f, _, _ := strings.Cut(ck.Field, "[")
					if strings.HasPrefix(f, "_") || strings.HasPrefix(f, "peer_") || got[f] {
						continue
					}
					if c.name == "demo" && ck.IfPresent {
						continue
					}
					t.Errorf("%s: %s step %s judges %s, which the board did not report", c.name, it.ID, id, f)
				}
				n++
			}
		}
		t.Logf("%s: %d criteria steps of the user-level items checked", c.name, n)
	}
}

// sessionFields starts a session, returns the fields of its first frame and stops it.
func sessionFields(t *testing.T, b *board, port string, args []string) map[string]bool {
	t.Helper()
	cmd := strings.TrimSpace("pt.start " + port + " " + strings.Join(args, " "))
	if r := b.ask(cmd); r != "OK started "+port {
		t.Errorf("%s: %s: %q", b.name, cmd, r)
		return nil
	}
	f := b.frame(port)
	if r := b.ask("pt.stop " + port); r != "OK stopped "+port {
		t.Errorf("%s: pt.stop %s: %q", b.name, port, r)
	}
	return keys(f.Fields)
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
