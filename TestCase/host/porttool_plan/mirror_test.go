package testcase

import (
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"PortTool/src/ptboard"
	"PortTool/src/ptplan"
	"PortTool/src/ptreport"
	"PortTool/src/ptseq"
)

// boardMirror stands in for the board's side of the link in mode=mirror:
// every byte the peer writes comes back unchanged.
type boardMirror struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	closed bool
}

func newBoardMirror() *boardMirror {
	m := &boardMirror{}
	m.cond = sync.NewCond(&m.mu)
	return m
}

func (m *boardMirror) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return 0, io.ErrClosedPipe
	}
	m.buf = append(m.buf, p...)
	m.cond.Broadcast()
	return len(p), nil
}

func (m *boardMirror) Read(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for len(m.buf) == 0 && !m.closed {
		m.cond.Wait()
	}
	if len(m.buf) == 0 {
		return 0, io.EOF
	}
	n := copy(p, m.buf)
	m.buf = m.buf[n:]
	return n, nil
}

func (m *boardMirror) Close() error {
	m.mu.Lock()
	m.closed = true
	m.cond.Broadcast()
	m.mu.Unlock()
	return nil
}

// A mode=mirror step floods through its peer and judges the peer_* numbers
// alongside the board's frame (PORTTOOL-FLOW.md B.9, PRODUCTION-FRAMEWORK.md
// in $PROD).
const mirrorPlan = `{"schema":1,"name":"usb mirror","limit_version":"2026-10-11","steps":[
  {"id":"usb-mirror","type":"PtSession","port":"usb","frames":3,"timeout_ms":8000,
   "params":{"mode":"mirror","period":100},"peer":{"usb":true},
   "checks":[{"field":"state","op":"eq","value":"cfg"},
             {"field":"peer_sent","op":"range","min":1},
             {"field":"peer_lost","op":"eq","value":"0"},
             {"field":"peer_bad","op":"eq","value":"0"},
             {"field":"peer_kbps","op":"range","min":1}]}]}`

func TestMirrorStepJudgesWhatThePeerMeasured(t *testing.T) {
	plan, err := ptplan.Parse([]byte(mirrorPlan))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	done := make(chan struct{})
	defer close(done)
	var fake *scriptBoard
	fake = newScriptBoard(t, func(cmd string, nth int) ([]string, []string) {
		if lines, ok := standardReplies(cmd); ok {
			return lines, nil
		}
		if strings.HasPrefix(cmd, "pt.start usb") {
			go func() {
				for i := 1; ; i++ {
					select {
					case <-done:
						return
					case <-time.After(100 * time.Millisecond):
					}
					fake.emit("!usb t=1 seq=1 rx=0 miss=0 state=cfg enum=0 mode=mirror" +
						" rx_bytes=0 tx_bytes=0 kbps=0 busy=0 drop=0")
				}
			}()
			return []string{"OK started usb"}, nil
		}
		return []string{"ERR unexpected " + cmd}, nil
	})
	board := ptboard.New(fake, 0)
	t.Cleanup(func() { board.Close() })

	mirror := newBoardMirror()
	open := func(kind, addr string, baud int) (io.ReadWriteCloser, error) { return mirror, nil }
	findCDC := func() (string, error) { return "COM99", nil }
	runner := &ptseq.Runner{OpenPeer: open, FindCDC: findCDC,
		Board: board, Sleep: func(time.Duration) {}, ToolVersion: "test"}
	rep, err := runner.Run(plan)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	st := rep.Steps[0]
	if st.Outcome != ptreport.OutcomePass {
		for _, a := range st.Attempts {
			t.Logf("attempt %d: %s %s %v", a.N, a.Outcome, a.Err, a.Extra)
			for _, c := range a.Checks {
				t.Logf("  %s -> %q %v %s", c.Check.Describe(), c.Got, c.Pass, c.Why)
			}
		}
		t.Fatalf("outcome %s, want PASS", st.Outcome)
	}
	extra := st.Attempts[len(st.Attempts)-1].Extra
	for _, k := range []string{"peer_kbps", "peer_lost", "peer_rtt_avg_us"} {
		if extra[k] == "" {
			t.Errorf("the report does not keep %s: %v", k, extra)
		}
	}
}
