package testcase

// The KNX analyser window (decision 151 #8, PORTTOOL-FLOW.md C.3.6 "KNX 分析",
// B.12): the board in knx mode=frames ga=none, this PC's KNX USB interface on
// the same bus.
//
// Bench: one simulated board, whose KNX stub hands the board's own telegram
// back to it plus an ACK, and a scripted PC end. It does not see a real KNX
// USB interface, a real bus or the TP1 timing.

import (
	"context"
	"testing"
	"time"

	"PortTool/src/knxusb"
	"PortTool/src/ptboard"
	"PortTool/src/ptitems"
	"PortTool/src/ptlib"
	"PortTool/src/ptplan"
	"PortTool/src/ptreport"
	"PortTool/src/ptseq"
)

func knxRun(t *testing.T, b *ptboard.Board, peer func() (ptlib.KNXPeer, error)) (*ptlib.Engine, <-chan ptreport.Report) {
	t.Helper()
	e := &ptlib.Engine{
		Board: b, Plan: ptplan.Plan{}, Items: []ptitems.Item{catItem(t, "knx.live")}, Hold: &fakeHold{b: b},
		Runner: func() *ptseq.Runner { return &ptseq.Runner{Board: b, OpenPeer: refusePeer, FindCDC: noCDC} },
		Ask:    func(ctx context.Context, q ptlib.Question) (ptlib.Answer, error) { return ptlib.Answer{}, nil },
		KNX:    peer,
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan ptreport.Report, 1)
	go func() {
		rep, err := e.Run(ctx, []ptlib.Pick{{ID: "knx.live", On: true, Peer: ptitems.PeerPC}}, 0)
		if err != nil {
			t.Error(err)
		}
		done <- rep
	}()
	return e, done
}

func waitKNX(t *testing.T, e *ptlib.Engine, what string, ok func(*ptlib.KNXView) bool) *ptlib.KNXView {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if lv := stateOf(e, "knx.live").Live; lv != nil && lv.KNX != nil && ok(lv.KNX) {
			return lv.KNX
		}
		time.Sleep(50 * time.Millisecond)
	}
	if lv := stateOf(e, "knx.live").Live; lv != nil && lv.KNX != nil {
		t.Fatalf("knx.live: %s never showed; last %+v", what, *lv.KNX)
	}
	t.Fatalf("knx.live: %s never showed; no window", what)
	return nil
}

func TestKNXWindowBoardHalfWithoutAnInterface(t *testing.T) {
	b, _ := openSimRaw(t)
	e, done := knxRun(t, b, func() (ptlib.KNXPeer, error) {
		return nil, ptlib.KNXUnavailable{Why: ptlib.KNXWhySim}
	})
	waitKNX(t, e, "the window with the PC side off", func(v *ptlib.KNXView) bool { return !v.PC.Open && v.PC.Why == ptlib.KNXWhySim })

	if err := e.Live("knx.live", ptlib.LiveReq{Op: ptlib.LiveKNXSend, Side: ptlib.CANBoard, GA: "1/2/3", Value: "5", Src: "1.1.250"}); err != nil {
		t.Fatal(err)
	}
	v := waitKNX(t, e, "the board's telegram and its own echo", func(v *ptlib.KNXView) bool { return len(v.Rows) >= 2 })
	tx, rx := v.Rows[0], v.Rows[1]
	if tx.Side != "board" || tx.Dir != "tx" || tx.Src != "1.1.250" || tx.Dst != "1/2/3" || tx.APCI != "write" || tx.Value != "5" || tx.Match != "" {
		t.Errorf("board tx row %+v", tx)
	}
	if rx.Dir != "rx" || rx.Match != "self" || rx.CRC != "raw" {
		t.Errorf("the board's echo is not marked as its own: %+v", rx)
	}
	if err := e.Live("knx.live", ptlib.LiveReq{Op: ptlib.LiveKNXSend, Side: ptlib.CANPC, GA: "1/2/3", Value: "1"}); err == nil {
		t.Error("the PC side sent with no interface open")
	}
	for _, bad := range []ptlib.LiveReq{
		{Op: ptlib.LiveKNXSend, Side: ptlib.CANBoard, GA: "32/0/0", Value: "1"},
		{Op: ptlib.LiveKNXSend, Side: ptlib.CANBoard, GA: "1/2/3", Value: "64"},
		{Op: ptlib.LiveKNXSend, Side: ptlib.CANBoard, GA: "1/2/3", Value: "1", Every: 50, Count: 3},
	} {
		if err := e.Live("knx.live", bad); err == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
	waitKNX(t, e, "the board's counters", func(v *ptlib.KNXView) bool { return v.Board.Tx >= 1 && v.Board.Acks >= 1 })
	if err := e.Live("knx.live", ptlib.LiveReq{Op: ptlib.LiveEnd}); err != nil {
		t.Fatal(err)
	}
	if o := outcomes(waitDone(t, done))["knx.live"]; o != ptlib.Recorded {
		t.Fatalf("knx.live ended by the person is %s, want RECORDED", o)
	}
}

// scriptedKNX is a PC end that hears only what the test feeds it.
type scriptedKNX struct {
	ev   chan knxusb.Event
	sent []knxusb.Telegram
}

func (s *scriptedKNX) Send(t knxusb.Telegram) error { s.sent = append(s.sent, t); return nil }
func (s *scriptedKNX) Events() <-chan knxusb.Event  { return s.ev }
func (s *scriptedKNX) Name() string                 { return "scripted" }
func (s *scriptedKNX) EMI() string                  { return "cEMI" }
func (s *scriptedKNX) Close() error                 { return nil }

func TestKNXWindowMatchesAndConfirms(t *testing.T) {
	b, _ := openSimRaw(t)
	pc := &scriptedKNX{ev: make(chan knxusb.Event, 8)}
	e, done := knxRun(t, b, func() (ptlib.KNXPeer, error) { return pc, nil })
	waitKNX(t, e, "the window with the PC side open", func(v *ptlib.KNXView) bool { return v.PC.Open && v.PC.EMI == "cEMI" })

	if err := e.Live("knx.live", ptlib.LiveReq{Op: ptlib.LiveKNXSend, Side: ptlib.CANBoard, GA: "1/2/3", Value: "5"}); err != nil {
		t.Fatal(err)
	}
	pc.ev <- knxusb.Event{Kind: knxusb.KindInd, T: knxusb.GroupWrite(0xFFFA, 0x0A03, 5)}
	waitKNX(t, e, "the board's telegram matched on the PC side", func(v *ptlib.KNXView) bool { return v.Matched == 1 })

	if err := e.Live("knx.live", ptlib.LiveReq{Op: ptlib.LiveKNXSend, Side: ptlib.CANPC, GA: "1/2/4", Value: "1"}); err != nil {
		t.Fatal(err)
	}
	if len(pc.sent) != 1 || pc.sent[0].DstText() != "1/2/4" || pc.sent[0].Value() != "1" {
		t.Fatalf("the PC sent %+v", pc.sent)
	}
	pc.ev <- knxusb.Event{Kind: knxusb.KindCon, T: pc.sent[0], OK: true}
	// The simulated board has no bus, so the PC's telegram never reaches it.
	v := waitKNX(t, e, "the PC's telegram confirmed and missing on the board", func(v *ptlib.KNXView) bool {
		return v.PC.ConOK == 1 && v.Missing == 1
	})
	for _, r := range v.Rows {
		if r.Side == "pc" && r.Dir == "tx" && (r.Con != "ok" || r.Match != "missing") {
			t.Errorf("PC tx row %+v", r)
		}
	}
	close(pc.ev) // the interface goes away: the board's half stays
	waitKNX(t, e, "the PC side marked lost", func(v *ptlib.KNXView) bool { return v.PC.Why == ptlib.KNXWhyLost })
	if err := e.Live("knx.live", ptlib.LiveReq{Op: ptlib.LiveEnd}); err != nil {
		t.Fatal(err)
	}
	if o := outcomes(waitDone(t, done))["knx.live"]; o != ptlib.Recorded {
		t.Fatalf("knx.live is %s, want RECORDED", o)
	}
}
