package testcase

// Upload and download measured apart, and the SDRAM sweep's rates
// (decision 151, PORTTOOL-FLOW.md B.9 「上下行分开测」 and C.3.6 in $PROD).
//
// Bench: a scripted fake board and an in-memory peer; nothing on the network.

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"PortTool/src/ptboard"
	"PortTool/src/ptitems"
	"PortTool/src/ptlib"
	"PortTool/src/ptplan"
	"PortTool/src/ptproto"
	"PortTool/src/ptreport"
	"PortTool/src/ptseq"
)

// linkPeer is the PC's end of the link. In source mode it hands out what the
// board pushed; Write counts what the PC sent.
type linkPeer struct {
	source  bool
	written atomic.Int64
	once    sync.Once
	done    chan struct{}
}

func newLinkPeer(source bool) *linkPeer { return &linkPeer{source: source, done: make(chan struct{})} }

func (p *linkPeer) Read(b []byte) (int, error) {
	if !p.source {
		<-p.done
		return 0, io.EOF
	}
	select {
	case <-p.done:
		return 0, io.EOF
	case <-time.After(5 * time.Millisecond):
	}
	return copy(b, strings.Repeat("x", 512)), nil
}

func (p *linkPeer) Write(b []byte) (int, error) {
	select {
	case <-p.done:
		return 0, io.ErrClosedPipe
	default:
	}
	p.written.Add(int64(len(b)))
	time.Sleep(time.Millisecond)
	return len(b), nil
}

func (p *linkPeer) Close() error { p.once.Do(func() { close(p.done) }); return nil }

// rateBoard emits eth frames whose counter climbs 62500 bytes per 500 ms of
// board time: 1000 kbit/s.
func rateBoard(t *testing.T, mode, counter string) *ptboard.Board {
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	var fake *scriptBoard
	fake = newScriptBoard(t, func(cmd string, nth int) ([]string, []string) {
		if lines, ok := standardReplies(cmd); ok {
			return lines, nil
		}
		if strings.HasPrefix(cmd, "pt.start eth mode="+mode) {
			go func() {
				for i := 0; ; i++ {
					select {
					case <-done:
						return
					case <-time.After(50 * time.Millisecond):
					}
					rx, tx := 0, 0
					if counter == "rx_bytes" {
						rx = 62500 * i
					} else {
						tx = 62500 * i
					}
					fake.emit(fmt.Sprintf("!eth t=%d seq=0 rx=0 miss=0 ip=127.0.0.1 link=1 conn=1 mode=%s port=5000"+
						" rx_bytes=%d tx_bytes=%d kbps=0 drop=0", 1000+500*i, mode, rx, tx))
				}
			}()
			return []string{"OK started eth"}, nil
		}
		return []string{"ERR unexpected " + cmd}, nil
	})
	b := ptboard.New(fake, 0)
	t.Cleanup(func() { b.Close() })
	return b
}

func rateStep(mode, counter string) ptplan.Plan {
	plan, err := ptplan.Parse([]byte(fmt.Sprintf(`{"schema":1,"name":"rates","limit_version":"t","steps":[
  {"id":"eth-%s","type":"PtSession","port":"eth","frames":3,"timeout_ms":8000,
   "params":{"mode":"%s","port":5000,"period":500},"peer":{"tcp":"auto"},
   "checks":[{"field":"%s","op":"range","min":1}]}]}`, mode, mode, counter)))
	if err != nil {
		panic(err)
	}
	return plan
}

func runRate(t *testing.T, mode, counter string, peer *linkPeer) ptreport.Attempt {
	t.Helper()
	runner := &ptseq.Runner{Board: rateBoard(t, mode, counter), Sleep: func(time.Duration) {}, ToolVersion: "test",
		OpenPeer: func(kind, addr string, baud int) (io.ReadWriteCloser, error) { return peer, nil },
		FindCDC:  noCDC}
	rep, err := runner.Run(rateStep(mode, counter))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	st := rep.Steps[0]
	att := st.Attempts[len(st.Attempts)-1]
	if st.Outcome != ptreport.OutcomePass {
		t.Fatalf("outcome %s (%s), want PASS: %v", st.Outcome, att.Err, att.Extra)
	}
	return att
}

func TestSinkStepPushesAndRecordsTheDownloadRate(t *testing.T) {
	peer := newLinkPeer(false)
	att := runRate(t, "sink", "rx_bytes", peer)
	if peer.written.Load() == 0 {
		t.Errorf("mode=sink: the PC pushed nothing at the board")
	}
	if got := att.Extra[ptseq.RateDown]; got != "1000" {
		t.Errorf("down_kbps = %q, want 1000 (62500 bytes per 500 ms of board time): %v", got, att.Extra)
	}
}

func TestSourceStepDrainsAndRecordsTheUploadRate(t *testing.T) {
	peer := newLinkPeer(true)
	att := runRate(t, "source", "tx_bytes", peer)
	if n := peer.written.Load(); n != 0 {
		t.Errorf("mode=source: the PC sent %d bytes back, want it only to read", n)
	}
	if got := att.Extra[ptseq.RateUp]; got != "1000" {
		t.Errorf("up_kbps = %q, want 1000: %v", got, att.Extra)
	}
}

func TestSweepRecordsWriteAndReadRate(t *testing.T) {
	fake := newScriptBoard(t, func(cmd string, nth int) ([]string, []string) {
		if lines, ok := standardReplies(cmd); ok {
			return lines, nil
		}
		if cmd == "pt.run sdram.sweep" {
			return []string{"OK sdram.sweep ready=1 patterns=4 words_each=16777216 mismatches=0" +
				" first_bad=0x00000000 bad_pattern=0x00000000 write_ms=1376 verify_ms=4586"}, nil
		}
		return []string{"ERR unexpected " + cmd}, nil
	})
	b := ptboard.New(fake, 0)
	t.Cleanup(func() { b.Close() })
	plan, err := ptplan.Parse([]byte(`{"schema":1,"name":"sweep","limit_version":"t","steps":[
  {"id":"sdram-stress","type":"PtRun","target":"sdram.sweep","timeout_ms":2000,
   "checks":[{"field":"mismatches","op":"eq","value":"0"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	rep, err := (&ptseq.Runner{Board: b, Sleep: func(time.Duration) {}, ToolVersion: "test"}).Run(plan)
	if err != nil {
		t.Fatal(err)
	}
	extra := rep.Steps[0].Attempts[0].Extra
	// 4 x 16777216 x 4 bytes over 1376 ms and 4586 ms.
	if extra[ptseq.RateSDRAMWr] != "195.1" || extra[ptseq.RateSDRAMRd] != "58.5" {
		t.Errorf("sweep rates write=%q read=%q, want 195.1 and 58.5 MB/s", extra[ptseq.RateSDRAMWr], extra[ptseq.RateSDRAMRd])
	}
}

// In R&D the sweep runs once whatever the duration; elsewhere it repeats
// (decision 151, ptitems OnceRD).
func TestOnceRDRunsOnceInRDOnly(t *testing.T) {
	for _, rd := range []bool{true, false} {
		b, fake := libBoard(t)
		e, _ := libEngine(t, b, ptlib.Answer{})
		for i := range e.Items {
			if e.Items[i].ID == "sdram.sweep" {
				e.Items[i].OnceRD = true
			}
		}
		e.RD = rd
		if _, err := e.Run(context.Background(), []ptlib.Pick{pick("sdram.sweep", true), pick("temp.read", true)}, 600*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		n := count(fake.commands(), "pt.run sdram.sweep")
		if rd && n != 1 {
			t.Errorf("R&D: sdram.sweep ran %d times within the duration, want once", n)
		}
		if !rd && n < 2 {
			t.Errorf("fixture test: sdram.sweep ran %d times within the duration, want it repeated", n)
		}
	}
}

// A criteria step whose mode the board does not offer is not run: the demo
// has no sink / source (PORTTOOL-FLOW.md A.9, C.3.6 「一项跑哪几步」).
func TestStepWithAModeTheBoardLacksIsNotRun(t *testing.T) {
	plan, err := ptplan.Parse([]byte(`{"schema":1,"name":"modes","limit_version":"t","steps":[
  {"id":"din-quad","type":"PtSession","port":"din","frames":1,"timeout_ms":2000,"params":{"mode":"quad"},
   "checks":[{"field":"v","op":"eq","value":"0xFF"}]},
  {"id":"din-all","type":"PtSession","port":"din","frames":1,"timeout_ms":2000,"params":{"mode":"level"},
   "checks":[{"field":"v","op":"eq","value":"0xFF"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	caps, err := ptproto.ParseCaps([]string{
		"OK porttool=0.9.0 ports=1 lines=3",
		"OK port=din board=upper kind=session blk=D term=D02-D09 channels=8 loop=ctrl params=ch,mode,period running=0",
		"OK vals=din ch=1,2,3,4,5,6,7,8 mode=level period=200",
		"OK limits=din ch:1..8 mode:level period:50..",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, fake := libBoard(t)
	e, _ := libEngine(t, b, ptlib.Answer{})
	e.Plan, e.Caps = plan, caps
	it := ptitems.Item{ID: "din.both", Port: "din", Peers: []ptitems.Peer{ptitems.PeerNone},
		Stages: []ptitems.Stage{ptitems.RD}, Criteria: []string{"din-quad", "din-all"}}
	e.Items = []ptitems.Item{it}
	rep, err := e.Run(context.Background(), []ptlib.Pick{pick("din.both", true)}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(rep)["din.both"]; got != ptreport.OutcomePass {
		t.Errorf("din.both: %s, want PASS from the step the board offers", got)
	}
	for _, c := range fake.commands() {
		if strings.Contains(c, "mode=quad") {
			t.Errorf("the step whose mode the board lacks was sent: %q", c)
		}
	}
}
