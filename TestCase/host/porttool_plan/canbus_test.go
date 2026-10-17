package testcase

// The CAN analyser window (decision 111, PORTTOOL-FLOW.md C.3.6 "CAN 分析",
// B.10): the board in can mode=bus and this PC's end on the same bus; every
// frame one side sends is matched against what the other side receives.
//
// Bench: one simulated board, its CAN wired to the panel-side analyser end
// (simboard.OpenCAN), plus a scripted analyser end for the frames that go
// missing or arrive changed. It does not see a real CANable, a real
// transceiver or a bit rate.

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"PortTool/src/ptboard"
	"PortTool/src/ptitems"
	"PortTool/src/ptlib"
	"PortTool/src/ptplan"
	"PortTool/src/ptreport"
	"PortTool/src/ptseq"
	"PortTool/src/simboard"
	"PortTool/src/slcan"
)

func openSimRaw(t *testing.T) (*ptboard.Board, io.ReadWriteCloser) {
	t.Helper()
	a, err := simboard.OpenPort("sim", 0)
	if errors.Is(err, simboard.ErrNotBuilt) {
		t.Skip("simulated board not built: tools/build.sh sim")
	}
	if err != nil {
		t.Fatal(err)
	}
	b := ptboard.New(a, 0)
	t.Cleanup(func() { b.Close() })
	return b, a
}

func canRun(t *testing.T, b *ptboard.Board, peer func(int) (ptlib.CANPeer, error), noLive bool) (*ptlib.Engine, context.CancelFunc, <-chan ptreport.Report) {
	t.Helper()
	e := &ptlib.Engine{
		Board: b, Plan: ptplan.Plan{}, Items: []ptitems.Item{catItem(t, "can.bus")}, Hold: &fakeHold{b: b},
		Runner: func() *ptseq.Runner { return &ptseq.Runner{Board: b, OpenPeer: refusePeer, FindCDC: noCDC} },
		Ask:    func(ctx context.Context, q ptlib.Question) (ptlib.Answer, error) { return ptlib.Answer{}, nil },
		CAN:    peer, NoLive: noLive,
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan ptreport.Report, 1)
	go func() {
		rep, err := e.Run(ctx, []ptlib.Pick{{ID: "can.bus", On: true, Peer: ptitems.PeerPC}}, 0)
		if err != nil {
			t.Error(err)
		}
		done <- rep
	}()
	return e, cancel, done
}

func waitCAN(t *testing.T, e *ptlib.Engine, what string, ok func(*ptlib.CANView) bool) *ptlib.CANView {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if lv := stateOf(e, "can.bus").Live; lv != nil && lv.CAN != nil && ok(lv.CAN) {
			return lv.CAN
		}
		time.Sleep(50 * time.Millisecond)
	}
	if lv := stateOf(e, "can.bus").Live; lv != nil && lv.CAN != nil {
		t.Fatalf("can.bus: %s never showed; last %+v", what, *lv.CAN)
	}
	t.Fatalf("can.bus: %s never showed; no window", what)
	return nil
}

func TestCANWindowMatchesBothDirectionsOnTheSimulatedBus(t *testing.T) {
	b, raw := openSimRaw(t)
	var bauds []int
	e, _, done := canRun(t, b, func(bps int) (ptlib.CANPeer, error) {
		bauds = append(bauds, bps)
		return simboard.OpenCAN(raw, bps)
	}, false)
	waitCAN(t, e, "the window open at 500 kbit/s", func(v *ptlib.CANView) bool { return v.Baud == 500000 && v.Board.State == "active" })

	if err := e.Live("can.bus", ptlib.LiveReq{Op: ptlib.LiveCANSend, Side: ptlib.CANBoard, CANID: "123", Data: "11 22 33"}); err != nil {
		t.Fatal(err)
	}
	v := waitCAN(t, e, "the board's frame received by the analyser", func(v *ptlib.CANView) bool { return v.Matched == 1 })
	// Either row may come first: the board's report and the analyser's copy are two streams.
	got := map[string]ptlib.CANRow{}
	for _, r := range v.Rows {
		got[r.Side+" "+r.Dir] = r
	}
	if tx, rx := got["board tx"], got["pc rx"]; len(v.Rows) != 2 || tx.Match != "ok" || rx.Match != "ok" ||
		rx.ID != "0x123" || rx.Data != "11 22 33" || rx.DLC != 3 {
		t.Fatalf("rows after the board sent: %+v", v.Rows)
	}

	if err := e.Live("can.bus", ptlib.LiveReq{Op: ptlib.LiveCANSend, Side: ptlib.CANPC, CANID: "7FF", Data: "AA"}); err != nil {
		t.Fatal(err)
	}
	v = waitCAN(t, e, "the analyser's frame received by the board", func(v *ptlib.CANView) bool { return v.Matched == 2 })
	found := false
	for _, r := range v.Rows {
		found = found || (r.Side == "board" && r.Dir == "rx" && r.ID == "0x7FF" && r.Data == "AA" && r.Match == "ok")
	}
	if !found {
		t.Fatalf("no matched board rx row for 0x7FF: %+v", v.Rows)
	}

	// Repeated from the board: three frames, each matched.
	if err := e.Live("can.bus", ptlib.LiveReq{Op: ptlib.LiveCANSend, Side: ptlib.CANBoard, CANID: "100", Data: "", Every: 50, Count: 3}); err != nil {
		t.Fatal(err)
	}
	waitCAN(t, e, "three repeats matched", func(v *ptlib.CANView) bool { return v.Matched == 5 && v.Board.Left == 0 })

	for _, bad := range []ptlib.LiveReq{
		{Op: ptlib.LiveCANSend, Side: ptlib.CANBoard, CANID: "800", Data: "11"},
		{Op: ptlib.LiveCANSend, Side: ptlib.CANBoard, CANID: "123", Data: "112233445566778899"},
		{Op: ptlib.LiveCANSend, Side: ptlib.CANPC, CANID: "123", Data: "11", Every: 10, Count: 5},
		{Op: ptlib.LiveCANBaud, Baud: 9600},
	} {
		if err := e.Live("can.bus", bad); err == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}

	if err := e.Live("can.bus", ptlib.LiveReq{Op: ptlib.LiveCANBaud, Baud: 250000}); err != nil {
		t.Fatal(err)
	}
	waitCAN(t, e, "both ends at 250 kbit/s, table cleared", func(v *ptlib.CANView) bool { return v.Baud == 250000 && len(v.Rows) == 0 })
	if err := e.Live("can.bus", ptlib.LiveReq{Op: ptlib.LiveEnd}); err != nil {
		t.Fatal(err)
	}
	rep := waitDone(t, done)
	if o := outcomes(rep)["can.bus"]; o != ptlib.Recorded {
		t.Fatalf("can.bus ended by the person is %s, want RECORDED", o)
	}
	sent := strings.Join(rep.Steps[0].Attempts[0].Sent, "\n")
	for _, want := range []string{"pt.start can mode=bus baud=500000", "pt.set can tx=123:112233", "pt.start can mode=bus baud=250000", "pt.stop can"} {
		if !strings.Contains(sent, want) {
			t.Errorf("sent %q, want %q among it", sent, want)
		}
	}
	if len(bauds) != 2 || bauds[0] != 500000 || bauds[1] != 250000 {
		t.Errorf("the analyser end was opened at %v, want [500000 250000]", bauds)
	}
	if v := ptlib.PortVerdicts([]ptitems.Item{catItem(t, "can.bus")}, e.States())["can"]; v == ptlib.PortPass || v == ptlib.PortFail {
		t.Errorf("the analyser window decided the CAN port: %s", v)
	}
}

// scriptedCAN is an analyser end that receives nothing by itself; what it
// "hears" is pushed by the test.
type scriptedCAN struct {
	frames chan slcan.Frame
	sent   []slcan.Frame
}

func (s *scriptedCAN) Send(f slcan.Frame) error    { s.sent = append(s.sent, f); return nil }
func (s *scriptedCAN) Frames() <-chan slcan.Frame { return s.frames }
func (s *scriptedCAN) Bells() int                 { return 3 }
func (s *scriptedCAN) Close() error               { return nil }

func TestCANWindowSaysWhatWentMissingAndWhatArrivedChanged(t *testing.T) {
	b, _ := openSimRaw(t)
	sim(b, "sim.canloop 0") // a real bus does not hand a node its own frame back
	t.Cleanup(func() { sim(b, "sim.canloop 1") })
	pc := &scriptedCAN{frames: make(chan slcan.Frame, 8)}
	e, cancel, done := canRun(t, b, func(int) (ptlib.CANPeer, error) { return pc, nil }, false)
	waitCAN(t, e, "the window open", func(v *ptlib.CANView) bool { return v.Board.State != "" })

	// Nobody on the analyser side hears it: missing after a second.
	if err := e.Live("can.bus", ptlib.LiveReq{Op: ptlib.LiveCANSend, Side: ptlib.CANBoard, CANID: "321", Data: "01"}); err != nil {
		t.Fatal(err)
	}
	waitCAN(t, e, "the unheard frame marked missing", func(v *ptlib.CANView) bool { return v.Missing == 1 && v.Rows[0].Match == "missing" })

	// The analyser hears the ID with other bytes: changed, and the bytes said.
	if err := e.Live("can.bus", ptlib.LiveReq{Op: ptlib.LiveCANSend, Side: ptlib.CANBoard, CANID: "321", Data: "02"}); err != nil {
		t.Fatal(err)
	}
	waitCAN(t, e, "the second send on the table", func(v *ptlib.CANView) bool { return len(v.Rows) == 2 })
	pc.frames <- slcan.Frame{ID: 0x321, Data: []byte{0x03}}
	v := waitCAN(t, e, "the changed frame marked bad", func(v *ptlib.CANView) bool { return v.Bad == 1 })
	for _, r := range v.Rows {
		if r.Dir == "tx" && r.Data == "02" && (r.Match != "bad" || r.Got != "03") {
			t.Errorf("the changed frame's row: %+v", r)
		}
	}
	if v.PC.Bells != 3 {
		t.Errorf("the adapter's bells are %d, want 3", v.PC.Bells)
	}
	cancel()
	if o := outcomes(waitDone(t, done))["can.bus"]; o != ptreport.OutcomeStopped {
		t.Fatalf("stopping the run leaves can.bus %s, want STOPPED", o)
	}
}

func TestCANWindowNeedsItsAnalyserEndAndSitsOutTheFullTest(t *testing.T) {
	b, _ := openSimRaw(t)
	_, _, done := canRun(t, b, func(int) (ptlib.CANPeer, error) { return nil, errors.New("no CANable") }, false)
	select {
	case rep := <-done:
		if o := outcomes(rep)["can.bus"]; o != ptreport.OutcomeError || !strings.HasPrefix(rep.Steps[0].Reason, ptlib.ReasonNoCANPeer) {
			t.Fatalf("no analyser end gives %s / %s, want ERROR / %s", o, rep.Steps[0].Reason, ptlib.ReasonNoCANPeer)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("the item waited without its analyser end")
	}

	_, _, done = canRun(t, b, func(int) (ptlib.CANPeer, error) { t.Error("opened in the full test"); return nil, nil }, true)
	select {
	case rep := <-done:
		if o := outcomes(rep)["can.bus"]; o != ptreport.OutcomeSkipped {
			t.Fatalf("the full test gives can.bus %s, want SKIPPED", o)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("the full test waited on the analyser window")
	}
}
