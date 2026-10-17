package ptlib

// The CAN analyser window (decision 111, PORTTOOL-FLOW.md C.3.6 "CAN 分析",
// B.10): the board runs can mode=bus, this PC's CANable sits on the same bus,
// and every frame either side sends is matched against what the other side
// receives. A live item: it stays open until the person ends it, and nothing
// is judged.

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"PortTool/src/ptboard"
	"PortTool/src/ptproto"
	"PortTool/src/ptreport"
	"PortTool/src/slcan"
)

// CANPeer is the analyser end on this PC: a CANable (slcan.Adapter) on a real
// bench, the panel-side wire on the simulated board (simboard).
type CANPeer interface {
	Send(slcan.Frame) error
	Frames() <-chan slcan.Frame
	Bells() int
	Close() error
}

// ReasonNoCANPeer: the analyser end could not be opened.
const ReasonNoCANPeer = "can_no_adapter"

// What the analyser window can do, beside LiveEnd.
const (
	LiveCANSend  = "can_send"  // Side sends ID / Data, every Every ms, Count times (Every 0: once)
	LiveCANStop  = "can_stop"  // stop Side's repeated send
	LiveCANBaud  = "can_baud"  // both ends to Baud
	LiveCANClear = "can_clear" // forget the table and the counts
)

// CAN analyser sides.
const (
	CANBoard = "board"
	CANPC    = "pc"
)

const (
	canMatchWait = time.Second // how long a sent frame waits for the other side
	canRowsMax   = 500
	canEveryMin  = 50 // ms, the session period floor
	canCountMax  = 10000
)

// CANRow is one frame one side sent or received.
type CANRow struct {
	MS    int64  `json:"ms"`   // since the window opened, this PC's clock
	Side  string `json:"side"` // board | pc
	Dir   string `json:"dir"`  // tx | rx
	ID    string `json:"id"`   // "0x123"
	Ext   bool   `json:"ext,omitempty"`
	DLC   int    `json:"dlc"`
	Data  string `json:"data"`            // "11 22 33"
	Match string `json:"match,omitempty"` // tx: wait | ok | bad | missing | unsent; rx: ok when it answered a send
	Got   string `json:"got,omitempty"`   // bad: what the other side received instead
}

// CANSide is one side's counts and state.
type CANSide struct {
	Tx    int    `json:"tx"`
	Rx    int    `json:"rx"`
	Lost  int    `json:"lost,omitempty"`  // board: received but not reported (B.10)
	TEC   int    `json:"tec,omitempty"`   // board
	REC   int    `json:"rec,omitempty"`   // board
	LEC   int    `json:"lec,omitempty"`   // board, FDCAN PSR.LEC
	State string `json:"state,omitempty"` // board: active | warning | passive | off
	Bells int    `json:"bells,omitempty"` // pc: commands refused and bus errors the adapter rang for
	Every int    `json:"every,omitempty"` // repeating every this many ms
	Left  int    `json:"left,omitempty"`  // repeats still to go
}

// CANView is what the analyser window draws.
type CANView struct {
	Baud    int      `json:"baud"`
	Board   CANSide  `json:"board"`
	PC      CANSide  `json:"pc"`
	Matched int      `json:"matched"`
	Missing int      `json:"missing"`
	Bad     int      `json:"bad"`
	Rows    []CANRow `json:"rows"`
	Dropped int      `json:"dropped,omitempty"` // older rows let go to keep canRowsMax
}

type canFrame struct {
	id   uint32
	data []byte
}

func (f canFrame) idText() string { return fmt.Sprintf("0x%03X", f.id) }

func spaced(d []byte) string {
	parts := make([]string, len(d))
	for i, b := range d {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, " ")
}

// ParseCANFrame reads the window's ID (hex, up to 7FF) and data (hex bytes,
// spaces allowed, 0-8 bytes).
func ParseCANFrame(id, data string) (uint32, []byte, error) {
	s := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(id), "0x"), "0X")
	v, err := strconv.ParseUint(s, 16, 32)
	if s == "" || err != nil || v > 0x7FF {
		return 0, nil, errors.New("the ID is hexadecimal, 0 to 7FF")
	}
	d := strings.NewReplacer(" ", "", ",", "", ":", "").Replace(strings.TrimSpace(data))
	b, err := hex.DecodeString(d)
	if err != nil || len(b) > 8 {
		return 0, nil, errors.New("the data is 0 to 8 hexadecimal bytes")
	}
	return uint32(v), b, nil
}

type canRepeat struct {
	f     canFrame
	every time.Duration
	left  int
	next  time.Time
}

// runCANBus keeps the analyser window open until the person ends it.
func (e *Engine) runCANBus(ctx context.Context, j *job) (ptreport.Outcome, string, string) {
	if e.NoLive {
		return ptreport.OutcomeSkipped, ReasonLiveFull, ""
	}
	if e.CAN == nil {
		return ptreport.OutcomeError, ReasonNoCANPeer, ""
	}
	baud := 500000
	if v, err := strconv.Atoi(j.params["baud"]); err == nil {
		baud = v
	}
	att := ptreport.Attempt{N: 1, StartedAt: time.Now()}
	send := func(cmd string) error {
		att.Sent = append(att.Sent, cmd)
		lines, err := e.Board.Send(cmd, ptboard.ExpectOne, ptboard.DefaultTimeout)
		att.Raw = append(att.Raw, lines...)
		return err
	}
	events, unsub := e.Board.Subscribe(512)
	defer unsub()

	var peer CANPeer
	open := func(b int) error {
		p, err := e.CAN(b)
		if err != nil {
			return err
		}
		if err := send(fmt.Sprintf("pt.start can mode=bus baud=%d period=500", b)); err != nil {
			_ = p.Close()
			_ = send("pt.stop can")
			return err
		}
		peer = p
		return nil
	}
	shut := func() {
		_ = send("pt.stop can")
		if peer != nil {
			_ = peer.Close()
			peer = nil
		}
	}
	if err := open(baud); err != nil {
		var refused *ptboard.RefusedError
		if errors.As(err, &refused) {
			o, d := e.recordErr(j, att, err)
			return o, "", d
		}
		att.Outcome, att.Err = ptreport.OutcomeError, err.Error()
		e.addAttempts(j, att)
		return ptreport.OutcomeError, ReasonNoCANPeer, err.Error()
	}

	reqs := make(chan liveMsg)
	e.mu.Lock()
	if e.lives == nil {
		e.lives = map[string]chan liveMsg{}
	}
	e.lives[j.item.ID] = reqs
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.lives, j.item.ID)
		e.mu.Unlock()
	}()

	began := time.Now()
	v := CANView{Baud: baud, Rows: []CANRow{}}
	match := frameMatcher{wait: canMatchWait}
	rep := map[string]*canRepeat{}

	show := func() {
		c := v
		c.Rows = append([]CANRow(nil), v.Rows...)
		for _, side := range []string{CANBoard, CANPC} {
			s := &c.Board
			if side == CANPC {
				s = &c.PC
			}
			s.Every, s.Left = 0, 0
			if r := rep[side]; r != nil {
				s.Every, s.Left = int(r.every/time.Millisecond), r.left
			}
		}
		if peer != nil {
			c.PC.Bells = peer.Bells()
		}
		e.change(j.item.ID, func(s *State) { s.Live = &LiveState{Kind: "can", CAN: &c} })
	}
	add := func(r CANRow) int {
		r.MS = time.Since(began).Milliseconds()
		v.Rows = append(v.Rows, r)
		if over := len(v.Rows) - canRowsMax; over > 0 {
			v.Rows = v.Rows[over:]
			v.Dropped += over
			match.dropped(over)
		}
		return len(v.Rows) - 1
	}
	// sent records a frame one side put on the bus and starts waiting for the other.
	sent := func(side string, f canFrame, ok bool) {
		r := CANRow{Side: side, Dir: "tx", ID: f.idText(), DLC: len(f.data), Data: spaced(f.data), Match: "wait"}
		if !ok {
			r.Match = "unsent"
		}
		i := add(r)
		if !ok {
			return
		}
		if o, ok := match.sent(i, side, f.idText(), string(f.data)); ok {
			v.Rows[i].Match, v.Rows[o].Match = "ok", "ok"
			v.Matched++
		}
	}
	// received records a frame one side got and settles a send it answers.
	received := func(side string, f canFrame) {
		i := add(CANRow{Side: side, Dir: "rx", ID: f.idText(), DLC: len(f.data), Data: spaced(f.data)})
		if p, ok := match.received(i, side, f.idText(), string(f.data), spaced(f.data)); ok {
			v.Rows[p].Match, v.Rows[i].Match = "ok", "ok"
			v.Matched++
		}
	}
	expire := func(now time.Time) {
		for _, x := range match.expire(now) {
			if x.got != "" {
				v.Rows[x.row].Match, v.Rows[x.row].Got = "bad", x.got
				v.Bad++
			} else {
				v.Rows[x.row].Match = "missing"
				v.Missing++
			}
		}
	}
	sendFrom := func(side string, f canFrame) error {
		if side == CANPC {
			err := peer.Send(slcan.Frame{ID: f.id, Data: f.data})
			if err == nil {
				v.PC.Tx++
			}
			sent(CANPC, f, err == nil)
			return err
		}
		// The board reports the frame itself as !can.tx; the row comes from that.
		return send(fmt.Sprintf("pt.set can tx=%03X:%X", f.id, f.data))
	}

	end := func(o ptreport.Outcome) (ptreport.Outcome, string, string) {
		shut()
		att.Outcome = o
		att.DurationMS = time.Since(att.StartedAt).Milliseconds()
		att.SetExtra("can_matched", strconv.Itoa(v.Matched))
		att.SetExtra("can_missing", strconv.Itoa(v.Missing))
		att.SetExtra("can_bad", strconv.Itoa(v.Bad))
		e.addAttempts(j, att)
		show()
		return o, "", ""
	}

	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	show()
	for {
		var pcFrames <-chan slcan.Frame
		if peer != nil {
			pcFrames = peer.Frames()
		}
		select {
		case <-ctx.Done():
			return end(ptreport.OutcomeStopped)
		case ev, ok := <-events:
			if !ok {
				shut()
				o, d := e.recordErr(j, att, errors.New("the board stopped answering"))
				return o, "", d
			}
			if ev.Kind != ptproto.LineFrame {
				continue
			}
			switch ev.Frame.Port {
			case "can.tx":
				f, ok := boardCANFrame(ev.Frame)
				if ok {
					sent(CANBoard, f, get(ev.Frame, "ok") == "1")
				}
			case "can.rx":
				if f, ok := boardCANFrame(ev.Frame); ok {
					received(CANBoard, f)
				}
			case "can":
				v.Board.Tx, _ = strconv.Atoi(get(ev.Frame, "tx"))
				v.Board.Rx, _ = strconv.Atoi(get(ev.Frame, "rx_frames"))
				v.Board.Lost, _ = strconv.Atoi(get(ev.Frame, "lost"))
				v.Board.TEC, _ = strconv.Atoi(get(ev.Frame, "tec"))
				v.Board.REC, _ = strconv.Atoi(get(ev.Frame, "rec"))
				v.Board.LEC, _ = strconv.Atoi(get(ev.Frame, "lec"))
				v.Board.State = get(ev.Frame, "bus")
			default:
				continue
			}
			show()
		case f, ok := <-pcFrames:
			if !ok {
				peer = nil
				shut()
				att.Outcome, att.Err = ptreport.OutcomeError, "the CAN adapter went away"
				e.addAttempts(j, att)
				return ptreport.OutcomeError, ReasonNoCANPeer, att.Err
			}
			v.PC.Rx++
			received(CANPC, canFrame{id: f.ID, data: f.Data})
			show()
		case now := <-tick.C:
			changed := match.waiting()
			expire(now)
			for side, r := range rep {
				if now.Before(r.next) {
					continue
				}
				_ = sendFrom(side, r.f)
				r.left--
				r.next = now.Add(r.every)
				if r.left <= 0 {
					delete(rep, side)
				}
				changed = true
			}
			if changed {
				show()
			}
		case m := <-reqs:
			r := m.req
			switch r.Op {
			case LiveEnd:
				m.reply <- nil
				return end(Recorded)
			case LiveCANSend:
				id, data, err := ParseCANFrame(r.CANID, r.Data)
				if err == nil && r.Side != CANBoard && r.Side != CANPC {
					err = errors.New("no such side " + r.Side)
				}
				if err == nil && r.Every != 0 && (r.Every < canEveryMin || r.Count < 1 || r.Count > canCountMax) {
					err = fmt.Errorf("repeating needs an interval of at least %d ms and 1 to %d times", canEveryMin, canCountMax)
				}
				if err != nil {
					m.reply <- err
					continue
				}
				f := canFrame{id: id, data: data}
				delete(rep, r.Side)
				err = sendFrom(r.Side, f)
				if err == nil && r.Every > 0 && r.Count > 1 {
					every := time.Duration(r.Every) * time.Millisecond
					rep[r.Side] = &canRepeat{f: f, every: every, left: r.Count - 1, next: time.Now().Add(every)}
				}
				show()
				m.reply <- err
			case LiveCANStop:
				delete(rep, r.Side)
				show()
				m.reply <- nil
			case LiveCANClear:
				v = CANView{Baud: v.Baud, Board: CANSide{State: v.Board.State}, Rows: []CANRow{}}
				match.clear()
				show()
				m.reply <- nil
			case LiveCANBaud:
				if _, ok := slcan.BitrateCode(r.Baud); !ok || (r.Baud != 125000 && r.Baud != 250000 && r.Baud != 500000 && r.Baud != 1000000) {
					m.reply <- errors.New("the bit rate is 125000, 250000, 500000 or 1000000")
					continue
				}
				rep = map[string]*canRepeat{}
				shut()
				if err := open(r.Baud); err != nil {
					m.reply <- err
					att.Outcome, att.Err = ptreport.OutcomeError, err.Error()
					e.addAttempts(j, att)
					return ptreport.OutcomeError, ReasonNoCANPeer, err.Error()
				}
				v = CANView{Baud: r.Baud, Rows: []CANRow{}}
				match.clear()
				show()
				m.reply <- nil
			default:
				m.reply <- errors.New("unknown action " + r.Op)
			}
		}
	}
}

// boardCANFrame reads a !can.tx / !can.rx event (B.10).
func boardCANFrame(f ptproto.Frame) (canFrame, bool) {
	id, err := strconv.ParseUint(strings.TrimPrefix(get(f, "id"), "0x"), 16, 32)
	if err != nil {
		return canFrame{}, false
	}
	var data []byte
	if d := get(f, "data"); d != "-" {
		if data, err = hex.DecodeString(d); err != nil {
			return canFrame{}, false
		}
	}
	return canFrame{id: uint32(id), data: data}, true
}
