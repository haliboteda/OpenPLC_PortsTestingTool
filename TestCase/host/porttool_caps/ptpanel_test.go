package testcase

// The third part of T4-01: the panel's HTTP surface end to end against the fake
// board. The page is built only from /api/state, so a mistake here renders wrong
// rather than failing.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"PortTool/src/portmap"
	"PortTool/src/ptpanel"
)

func newPanel(t *testing.T) (*httptest.Server, *fakeBoard) {
	t.Helper()
	// Never the bench's real porttool_ports.json.
	portmap.File = filepath.Join(t.TempDir(), "porttool_ports.json")
	t.Cleanup(func() { portmap.File = "" })
	fake := newFakeBoard(t)
	p := ptpanel.New()
	p.Open = func(name string, baud int) (io.ReadWriteCloser, error) { return fake, nil }
	srv := httptest.NewServer(p.Handler())
	t.Cleanup(func() { srv.Close(); p.Close() })
	return srv, fake
}

func postJSON(t *testing.T, srv *httptest.Server, path string, body any) map[string]any {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = strings.NewReader(string(b))
	}
	resp, err := srv.Client().Post(srv.URL+path, "application/json", rdr)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("POST %s returned unreadable JSON: %v", path, err)
	}
	return out
}

func getJSON(t *testing.T, srv *httptest.Server, path string) map[string]any {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("GET %s returned unreadable JSON: %v", path, err)
	}
	return out
}

func TestPanelServesItsOwnPage(t *testing.T) {
	srv, _ := newPanel(t)
	resp, err := srv.Client().Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	// The page is embedded in the binary; a missing go:embed would show up
	// here as a 404 rather than at build time.
	if !strings.Contains(string(body), "端口测试工装") {
		t.Error("the embedded page is not what was served")
	}
}

func TestPanelBuildsItselfFromWhatTheBoardReports(t *testing.T) {
	srv, _ := newPanel(t)

	if st := getJSON(t, srv, "/api/state"); st["connected"] != false {
		t.Fatal("a fresh panel should not claim to be connected")
	}

	st := postJSON(t, srv, "/api/connect", map[string]any{"port": "COM_TEST"})
	if st["connected"] != true {
		t.Fatalf("connect did not take: %v", st)
	}
	if want := goldenVersion(t); st["firmware"] != want {
		t.Errorf("firmware = %v, want %s", st["firmware"], want)
	}
	if msg, _ := st["capsError"].(string); msg != "" {
		t.Errorf("capsError = %q, want empty", msg)
	}

	ports, _ := st["ports"].([]any)
	if len(ports) < 3 {
		t.Fatalf("panel got %d ports, want the whole list the board reported", len(ports))
	}

	byName := map[string]map[string]any{}
	for _, p := range ports {
		m := p.(map[string]any)
		byName[m["name"].(string)] = m
	}

	din := byName["din"]
	if din == nil {
		t.Fatal("din missing from the panel state")
	}
	if din["channels"].(float64) != 8 || din["kind"] != "session" || din["loop"] != "ctrl" {
		t.Errorf("din = %v", din)
	}
	// The checkbox labels the page shows come from here, not from anything the
	// panel knows about this board.
	terms, _ := din["terminals"].([]any)
	if len(terms) != 8 || terms[0] != "D02" || terms[7] != "D09" {
		t.Errorf("din terminals = %v, want D02..D09", terms)
	}
	relayTerms, _ := byName["relay"]["terminals"].([]any)
	if len(relayTerms) != 6 || relayTerms[0] != "B01+B02" {
		t.Errorf("relay terminals = %v, want the contact pairs", relayTerms)
	}

	// One piece of hardware, one row: can's bring-up entries would ride on it as
	// targets=, the shape rs485 has (DECISIONS.md 17).
	can := byName["can"]
	if can["kind"] != "session" {
		t.Errorf("can kind = %v, want session", can["kind"])
	}
	// No deep entries in caps, so the panel has no button that takes the board away
	// (DECISIONS.md 40).
	if tg, _ := can["targets"].([]any); len(tg) != 0 {
		t.Errorf("can targets = %v, want none", tg)
	}
}

func TestPanelSplitsPerChannelValues(t *testing.T) {
	srv, _ := newPanel(t)
	st := postJSON(t, srv, "/api/connect", map[string]any{"port": "COM_TEST"})

	byName := map[string]map[string]any{}
	for _, p := range st["ports"].([]any) {
		m := p.(map[string]any)
		byName[m["name"].(string)] = m
	}

	// Split per channel so the page can draw one control per channel.
	dout := byName["dout"]
	if dout == nil {
		t.Fatal("dout missing")
	}
	per, _ := dout["perChannel"].(map[string]any)
	duty, _ := per["duty"].(map[string]any)
	if len(duty) != 8 {
		t.Errorf("dout duty split into %d channels, want 8: %v", len(duty), per)
	}

	// relay's on= is per channel too, and it goes through the same path.
	relayPer, _ := byName["relay"]["perChannel"].(map[string]any)
	if _, ok := relayPer["on"]; !ok {
		t.Errorf("relay on= did not arrive per channel: %v", relayPer)
	}

	// freq is per channel too: eight independent frequencies need eight controls.
	freq, _ := per["freq"].(map[string]any)
	if len(freq) != 8 {
		t.Errorf("dout freq split into %d channels, want 8: %v", len(freq), per)
	}

	// A parameter that is one value for the whole port must NOT be split, or
	// the page would draw eight controls for something with one setting.
	if _, ok := per["period"]; ok {
		t.Error("period is one value for the port but arrived per channel")
	}
}

func TestPanelRefreshesCapsAfterACommand(t *testing.T) {
	srv, _ := newPanel(t)
	postJSON(t, srv, "/api/connect", map[string]any{"port": "COM_TEST"})

	// The transcript's second pt.caps has din running: the panel must re-read caps
	// after a command, or the running state shown goes stale.
	res := postJSON(t, srv, "/api/command",
		map[string]any{"cmd": "pt.start din ch=1,3,5 period=200"})
	if res["error"] != nil {
		t.Fatalf("pt.start returned an error: %v", res["error"])
	}
	st, _ := res["state"].(map[string]any)
	if st == nil {
		t.Fatal("the command reply carried no refreshed state")
	}
	for _, p := range st["ports"].([]any) {
		m := p.(map[string]any)
		if m["name"] == "din" {
			if m["running"] != true {
				t.Error("din still reads as stopped after pt.start; caps was not re-read")
			}
			vals, _ := m["values"].(map[string]any)
			if vals["ch"] != "1,3,5" {
				t.Errorf("din ch = %v, want the 1,3,5 that was asked for", vals["ch"])
			}
		}
	}
}

func TestPanelPassesARefusalThroughVerbatim(t *testing.T) {
	srv, _ := newPanel(t)
	postJSON(t, srv, "/api/connect", map[string]any{"port": "COM_TEST"})

	res := postJSON(t, srv, "/api/command", map[string]any{"cmd": "pt.start din ch=1,9"})
	reason, _ := res["refused"].(string)
	if reason == "" {
		t.Fatalf("a refused command produced no refusal: %v", res)
	}
	// Naming the parameter and the range is the whole value of the message.
	if !strings.Contains(reason, "ch=") || !strings.Contains(reason, "must be channels") {
		t.Errorf("refusal = %q, want the board's own explanation", reason)
	}
}

func TestPanelRejectsCommandsWithNoBoard(t *testing.T) {
	srv, _ := newPanel(t)
	res := postJSON(t, srv, "/api/command", map[string]any{"cmd": "pt.caps"})
	if res["error"] == nil {
		t.Error("a command with nothing connected should say so")
	}
}

func TestPanelAnswersTheEchoLoopByItself(t *testing.T) {
	srv, fake := newPanel(t)
	postJSON(t, srv, "/api/connect", map[string]any{"port": "COM_TEST"})

	// A loop=ctrl frame has to be answered with the number it carried, or the board's
	// counter reports only misses.
	fake.emit("!din t=48213 seq=7 rx=6 miss=0 v=0x16 ch1=0")

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, c := range fake.commands() {
			if c == "pt.echo din 7" {
				goto answered
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the panel never answered the frame; it sent %v", fake.commands())

answered:
	// A loop=link port takes its reply off the link under test; answering it here
	// would count a pass on a dead link.
	fake.emit("!rs485 t=48300 seq=4 rx=3 miss=0 tx=PT")
	time.Sleep(300 * time.Millisecond)
	for _, c := range fake.commands() {
		if strings.HasPrefix(c, "pt.echo rs485") {
			t.Errorf("the panel answered a loop=link port: %q", c)
		}
	}

	// Switching it off is what proves the counter is real: with it on, a
	// healthy loop is all anyone ever sees.
	st := postJSON(t, srv, "/api/autoecho", map[string]any{"on": false})
	if st["autoEcho"] != false {
		t.Fatalf("autoEcho did not switch off: %v", st["autoEcho"])
	}
	fake.emit("!din t=48400 seq=8 rx=7 miss=0 v=0x16 ch1=0")
	time.Sleep(300 * time.Millisecond)
	for _, c := range fake.commands() {
		if c == "pt.echo din 8" {
			t.Errorf("still answering after being switched off: %q", c)
		}
	}
}

// fakeLink is the far end of a link port: a pipe the test writes into as if it
// were the board putting bytes on the RS485 pair, and reads back out of to see
// what the panel sent in reply.
type fakeLink struct {
	toPanel    *io.PipeReader
	toPanelW   *io.PipeWriter
	fromPanel  *io.PipeReader
	fromPanelW *io.PipeWriter
}

func newFakeLink() *fakeLink {
	tr, tw := io.Pipe()
	fr, fw := io.Pipe()
	return &fakeLink{toPanel: tr, toPanelW: tw, fromPanel: fr, fromPanelW: fw}
}

func (f *fakeLink) Read(p []byte) (int, error)  { return f.toPanel.Read(p) }
func (f *fakeLink) Write(p []byte) (int, error) { return f.fromPanelW.Write(p) }
func (f *fakeLink) Close() error {
	_ = f.toPanelW.Close()
	return f.fromPanelW.Close()
}

func TestPanelRepeatsOnTheLinkUnderTest(t *testing.T) {
	board := newFakeBoard(t)
	link := newFakeLink()
	// Never the bench's real porttool_ports.json.
	portmap.File = filepath.Join(t.TempDir(), "porttool_ports.json")
	t.Cleanup(func() { portmap.File = "" })

	p := ptpanel.New()
	p.Open = func(name string, baud int) (io.ReadWriteCloser, error) {
		if name == "COM_LINK" {
			return link, nil
		}
		return board, nil
	}
	srv := httptest.NewServer(p.Handler())
	t.Cleanup(func() { srv.Close(); p.Close() })

	postJSON(t, srv, "/api/connect", map[string]any{"port": "COM_CTRL"})

	st := postJSON(t, srv, "/api/link",
		map[string]any{"port": "rs485", "com": "COM_LINK"})
	links, _ := st["links"].(map[string]any)
	if _, ok := links["rs485"]; !ok {
		t.Fatalf("rs485 did not bind: %v", st)
	}

	// The far end sends back exactly what the board put on the pair; the board compares the two.
	go func() { _, _ = io.WriteString(link.toPanelW, "7\n") }()

	// io.Pipe has no read deadline, so the read runs on its own goroutine and
	// the test waits on a channel instead of blocking forever if nothing comes.
	back := make(chan string, 1)
	go func() {
		buf := make([]byte, 16)
		n, err := link.fromPanel.Read(buf)
		if err != nil && n == 0 {
			back <- ""
			return
		}
		back <- string(buf[:n])
	}()

	select {
	case seen := <-back:
		if strings.TrimSpace(seen) != "7" {
			t.Fatalf("the far end sent back %q, want exactly \"7\"", seen)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the far end sent nothing back, so the loop could never close")
	}

	// The connection block's row counts what its peer echoed, once a second
	// from /api/conn (decision 106).
	// The count is taken just after the write, so it is polled briefly.
	var conn map[string]any
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		conn = getJSON(t, srv, "/api/conn")
		cl, _ := conn["links"].(map[string]any)
		row, _ := cl["rs485"].(map[string]any)
		if row != nil && row["com"] == "COM_LINK" && row["echoed"].(float64) >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("/api/conn links = %v, want rs485 on COM_LINK with its echo counted", conn["links"])
		}
	}

	// A port whose reply comes back on the control port has no far end to bind.
	res := postJSON(t, srv, "/api/link",
		map[string]any{"port": "din", "com": "COM_LINK"})
	if res["error"] == nil {
		t.Error("binding a link adapter to a loop=ctrl port should be refused")
	}

	// Nor may the control port double as the far end.
	res = postJSON(t, srv, "/api/link",
		map[string]any{"port": "rs485", "com": "COM_CTRL"})
	if res["error"] == nil {
		t.Error("using the control port as its own far end should be refused")
	}

	st = postJSON(t, srv, "/api/unlink", map[string]any{"port": "rs485"})
	links, _ = st["links"].(map[string]any)
	if _, ok := links["rs485"]; ok {
		t.Error("rs485 still bound after unlink")
	}
}

func TestPanelStreamsFrames(t *testing.T) {
	srv, fake := newPanel(t)
	postJSON(t, srv, "/api/connect", map[string]any{"port": "COM_TEST"})

	req, _ := http.NewRequest("GET", srv.URL+"/api/events", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	go func() {
		time.Sleep(30 * time.Millisecond)
		fake.emit("!din t=48213 v=0x16 ch1=0 ch3=1 ch5=1")
	}()

	// The backlog replays first, so read until the frame turns up.
	buf := make([]byte, 4096)
	var seen strings.Builder
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			seen.Write(buf[:n])
			if strings.Contains(seen.String(), `"port":"din"`) {
				break
			}
		}
		if err != nil {
			break
		}
	}
	text := seen.String()
	if !strings.Contains(text, `"port":"din"`) {
		t.Fatalf("no din frame arrived on the stream; got:\n%s", text)
	}
	// The backlog matters as much as the live frames: a page opened after the
	// board has been talking has to see what it already said.
	if !strings.Contains(text, "porttool="+goldenVersion(t)) {
		t.Error("the stream did not replay the backlog to a page that opened late")
	}
	if !strings.Contains(text, `"kind":"frame"`) {
		t.Error("frames are not being labelled as frames")
	}
}
