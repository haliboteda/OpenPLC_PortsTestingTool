package ptlib

// The KNX analyser window (decision 151 #8, PORTTOOL-FLOW.md C.3.6 "KNX 分析",
// B.12): the board runs knx mode=frames ga=none, this PC's KNX USB interface
// sits on the same bus, and every telegram either side sends is matched
// against what the other side receives. Unlike the CAN window, a missing PC
// end leaves the board's half usable. Nothing is judged.

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"PortTool/src/knxusb"
	"PortTool/src/ptboard"
	"PortTool/src/ptproto"
	"PortTool/src/ptreport"
)

// KNXPeer is this PC's end of the KNX bus (knxusb.Interface).
type KNXPeer interface {
	Send(knxusb.Telegram) error
	Events() <-chan knxusb.Event
	Name() string
	EMI() string
	Close() error
}

// KNXUnavailable says why the PC's end is not open. Why is one of the
// KNXWhy* codes; the page words them.
type KNXUnavailable struct{ Why, Detail string }

func (e KNXUnavailable) Error() string { return "KNX USB interface: " + e.Why + " " + e.Detail }

// Why the PC's end of the KNX window is closed.
const (
	KNXWhySim         = "sim"         // the simulated board has no KNX bus
	KNXWhyUnsupported = "unsupported" // no HID access on this system (not Windows, Linux or macOS)
	KNXWhyNone        = "none"        // no interface found
	KNXWhyPick        = "pick"        // several found, none picked in the connection block
	KNXWhyOpen        = "open"        // found, would not open or answer
	KNXWhyLost        = "lost"        // went away during the session
)

// What the KNX window can do, beside LiveEnd. Side is CANBoard or CANPC.
const (
	LiveKNXSend  = "knx_send"  // Side writes Value to GA, every Every ms, Count times
	LiveKNXStop  = "knx_stop"  // stop Side's repeated send
	LiveKNXClear = "knx_clear" // forget the table and the counts
)

const (
	knxMatchWait = 2 * time.Second
	knxRowsMax   = 500
	knxEveryMin  = 100 // ms; a telegram is about 20 ms on TP1 at 9600 bit/s
)

// KNXRow is one telegram one side sent or received.
type KNXRow struct {
	MS    int64  `json:"ms"`
	Side  string `json:"side"` // board | pc
	Dir   string `json:"dir"`  // tx | rx
	Src   string `json:"src,omitempty"`
	Dst   string `json:"dst,omitempty"`
	APCI  string `json:"apci,omitempty"` // write | read | response | ""
	Value string `json:"value,omitempty"`
	Raw   string `json:"raw,omitempty"`   // board: the octets as reported
	CRC   string `json:"crc,omitempty"`   // board rx: raw | inv | bad
	Match string `json:"match,omitempty"` // tx: wait | ok | bad | missing | unsent; rx: ok | self
	Got   string `json:"got,omitempty"`
	Con   string `json:"con,omitempty"` // pc tx: ok | fail, the interface's L_Data.con
}

// KNXBoard is the board's side: the !knx frame's counters (B.12).
type KNXBoard struct {
	Bus      string `json:"bus"` // ok | dead | odd
	VCC      int    `json:"vcc"`
	Tx       int    `json:"tx"`
	Rx       int    `json:"rx"`
	CRCRaw   int    `json:"crc_raw"`
	CRCInv   int    `json:"crc_inv"`
	CRCBad   int    `json:"crc_bad"`
	Acks     int    `json:"acks"`
	AckOther int    `json:"ack_other"`
	Partial  int    `json:"partial"`
	BadChars int    `json:"bad_chars"`
	Pulses   int    `json:"pulses"`
	Dropped  int    `json:"dropped"`
	WAvg     int    `json:"w_avg"`
	DAvg     int    `json:"d_avg"`
	Every    int    `json:"every,omitempty"`
	Left     int    `json:"left,omitempty"`
}

// KNXPC is this PC's side.
type KNXPC struct {
	Open    bool   `json:"open"`
	Name    string `json:"name,omitempty"`
	EMI     string `json:"emi,omitempty"`
	Tx      int    `json:"tx"`
	Rx      int    `json:"rx"`
	ConOK   int    `json:"con_ok"`
	ConFail int    `json:"con_fail"`
	Why     string `json:"why,omitempty"` // KNXWhy* while not open
	Detail  string `json:"detail,omitempty"`
	Every   int    `json:"every,omitempty"`
	Left    int    `json:"left,omitempty"`
}

// KNXView is what the KNX window draws.
type KNXView struct {
	Board   KNXBoard `json:"board"`
	PC      KNXPC    `json:"pc"`
	Matched int      `json:"matched"`
	Missing int      `json:"missing"`
	Bad     int      `json:"bad"`
	Rows    []KNXRow `json:"rows"`
	Dropped int      `json:"dropped,omitempty"`
}

// knxKey is what two sides must agree on: destination and TPDU.
func knxKey(t knxusb.Telegram) (dst, body string) {
	return fmt.Sprintf("%v:%04X", t.Group, t.Dst), hex.EncodeToString(t.TPDU)
}

type knxRepeat struct {
	ga      uint16
	srcText string // board: the source address to set first, "" keeps the session's
	val     byte
	every   time.Duration
	left    int
	next    time.Time
}

// ParseKNXSend reads the window's group address and value (0-63).
func ParseKNXSend(ga, value string) (uint16, byte, error) {
	a, err := knxusb.ParseGA(ga)
	if err != nil {
		return 0, 0, err
	}
	v, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || v < 0 || v > 63 {
		return 0, 0, errors.New("the value is 0 to 63")
	}
	return a, byte(v), nil
}

// boardTelegram reads a !knx.tx / !knx.rx event: the octets that passed
// their check, decoded here rather than trusting the board's own unpacking.
func boardTelegram(f ptproto.Frame) (knxusb.Telegram, string, bool) {
	raw := get(f, "raw")
	b, err := hex.DecodeString(strings.ReplaceAll(raw, ".", ""))
	if err != nil {
		return knxusb.Telegram{}, raw, false
	}
	if get(f, "crc") == "inv" {
		for i := range b {
			b[i] = ^b[i]
		}
	}
	t, err := knxusb.ParseTP1(b)
	return t, raw, err == nil
}

func (e *Engine) runKNXWin(ctx context.Context, j *job) (ptreport.Outcome, string, string) {
	if e.NoLive {
		return ptreport.OutcomeSkipped, ReasonLiveFull, ""
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
	if err := send("pt.start knx mode=frames ga=none period=500"); err != nil {
		_ = send("pt.stop knx")
		o, d := e.recordErr(j, att, err)
		return o, "", d
	}

	v := KNXView{Rows: []KNXRow{}}
	var peer KNXPeer
	if e.KNX == nil {
		v.PC.Why = KNXWhyNone
	} else if p, err := e.KNX(); err != nil {
		var why KNXUnavailable
		if errors.As(err, &why) {
			v.PC.Why, v.PC.Detail = why.Why, why.Detail
		} else {
			v.PC.Why, v.PC.Detail = KNXWhyOpen, err.Error()
		}
	} else {
		peer = p
		v.PC.Open, v.PC.Name, v.PC.EMI = true, p.Name(), p.EMI()
	}
	shut := func() {
		_ = send("pt.stop knx")
		if peer != nil {
			_ = peer.Close()
			peer = nil
		}
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
	// The board hears its own telegram on the same pair; those rows are marked, not matched.
	match := frameMatcher{wait: knxMatchWait}
	rep := map[string]*knxRepeat{}

	show := func() {
		c := v
		c.Rows = append([]KNXRow(nil), v.Rows...)
		c.Board.Every, c.Board.Left, c.PC.Every, c.PC.Left = 0, 0, 0, 0
		if r := rep[CANBoard]; r != nil {
			c.Board.Every, c.Board.Left = int(r.every/time.Millisecond), r.left
		}
		if r := rep[CANPC]; r != nil {
			c.PC.Every, c.PC.Left = int(r.every/time.Millisecond), r.left
		}
		e.change(j.item.ID, func(s *State) { s.Live = &LiveState{Kind: "knx", KNX: &c} })
	}
	add := func(r KNXRow) int {
		r.MS = time.Since(began).Milliseconds()
		v.Rows = append(v.Rows, r)
		if over := len(v.Rows) - knxRowsMax; over > 0 {
			v.Rows = v.Rows[over:]
			v.Dropped += over
			match.dropped(over)
		}
		return len(v.Rows) - 1
	}
	rowOf := func(side, dir string, t knxusb.Telegram) KNXRow {
		return KNXRow{Side: side, Dir: dir, Src: knxusb.IAText(t.Src), Dst: t.DstText(), APCI: t.APCI(), Value: t.Value()}
	}
	sent := func(side string, t knxusb.Telegram, r KNXRow, ok bool) {
		r.Match = "wait"
		if !ok {
			r.Match = "unsent"
		}
		if ok && peer == nil {
			r.Match = "" // nobody on the other side to wait for
		}
		i := add(r)
		dst, tp := knxKey(t)
		if side == CANBoard && ok {
			match.sentOwn(i, dst, tp, t.Src)
		}
		if r.Match != "wait" {
			return
		}
		if o, ok := match.sent(i, side, dst, tp); ok {
			v.Rows[i].Match, v.Rows[o].Match = "ok", "ok"
			v.Matched++
		}
	}
	received := func(side string, t knxusb.Telegram, r KNXRow) {
		dst, tp := knxKey(t)
		if side == CANBoard && match.heardOwn(dst, tp, t.Src) {
			r.Match = "self"
			add(r)
			return
		}
		i := add(r)
		if p, ok := match.received(i, side, dst, tp, r.Value); ok {
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
	sendFrom := func(side string, r *knxRepeat) error {
		if side == CANPC {
			if peer == nil {
				return errors.New("this PC's KNX USB interface is not open")
			}
			t := knxusb.GroupWrite(0, r.ga, r.val)
			err := peer.Send(t)
			if err == nil {
				v.PC.Tx++
			}
			row := rowOf(CANPC, "tx", t)
			row.Src = "" // the interface puts its own address in
			sent(CANPC, t, row, err == nil)
			return err
		}
		cmd := fmt.Sprintf("pt.set knx tx=%s:%d", knxusb.GAText(r.ga), r.val)
		if r.srcText != "" {
			cmd = fmt.Sprintf("pt.set knx src=%s tx=%s:%d", r.srcText, knxusb.GAText(r.ga), r.val)
		}
		// The board reports the telegram itself as !knx.tx; the row comes from that.
		return send(cmd)
	}
	end := func(o ptreport.Outcome) (ptreport.Outcome, string, string) {
		shut()
		att.Outcome = o
		att.DurationMS = time.Since(att.StartedAt).Milliseconds()
		att.SetExtra("knx_matched", strconv.Itoa(v.Matched))
		att.SetExtra("knx_missing", strconv.Itoa(v.Missing))
		att.SetExtra("knx_bad", strconv.Itoa(v.Bad))
		att.SetExtra("knx_crc_bad", strconv.Itoa(v.Board.CRCBad))
		if v.PC.Name != "" {
			att.SetExtra("knx_interface", v.PC.Name+" "+v.PC.EMI)
		}
		e.addAttempts(j, att)
		show()
		return o, "", ""
	}

	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	show()
	for {
		var pcEvents <-chan knxusb.Event
		if peer != nil {
			pcEvents = peer.Events()
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
			f := ev.Frame
			switch f.Port {
			case "knx.tx":
				t, raw, ok := boardTelegram(f)
				r := KNXRow{Side: CANBoard, Dir: "tx", Raw: raw}
				if ok {
					r = rowOf(CANBoard, "tx", t)
					r.Raw = raw
				}
				sent(CANBoard, t, r, ok)
			case "knx.rx":
				if get(f, "n") == "1" {
					continue // an acknowledge octet; counted in the !knx frame
				}
				t, raw, ok := boardTelegram(f)
				r := KNXRow{Side: CANBoard, Dir: "rx", Raw: raw, CRC: get(f, "crc")}
				if !ok {
					add(r)
					break
				}
				crc := r.CRC
				r = rowOf(CANBoard, "rx", t)
				r.Raw, r.CRC = raw, crc
				received(CANBoard, t, r)
			case "knx":
				b := &v.Board
				b.Bus = get(f, "bus")
				num := func(k string) int { n, _ := strconv.Atoi(get(f, k)); return n }
				b.VCC, b.Tx, b.Rx = num("vcc"), num("tx_frames"), num("rx_frames")
				b.CRCRaw, b.CRCInv, b.CRCBad = num("crc_raw"), num("crc_inv"), num("crc_bad")
				b.Acks, b.AckOther, b.Partial = num("acks"), num("ack_other"), num("partial")
				b.BadChars, b.Pulses, b.Dropped = num("bad"), num("pulses"), num("dropped")
				b.WAvg, b.DAvg = num("w_avg"), num("d_avg")
			default:
				continue
			}
			show()
		case pe, ok := <-pcEvents:
			if !ok {
				// The board's half stays usable.
				_ = peer.Close()
				peer = nil
				v.PC.Open, v.PC.Why = false, KNXWhyLost
				delete(rep, CANPC)
				show()
				continue
			}
			if pe.Kind == knxusb.KindCon {
				// The confirmation settles the oldest unconfirmed send it describes.
				for i := range v.Rows {
					r := &v.Rows[i]
					if r.Side != CANPC || r.Dir != "tx" || r.Con != "" || r.Match == "unsent" ||
						r.Dst != pe.T.DstText() || r.APCI != pe.T.APCI() || r.Value != pe.T.Value() {
						continue
					}
					r.Con = "fail"
					v.PC.ConFail++
					if pe.OK {
						r.Con = "ok"
						v.PC.ConOK++
						v.PC.ConFail--
					}
					break
				}
			} else {
				v.PC.Rx++
				received(CANPC, pe.T, rowOf(CANPC, "rx", pe.T))
			}
			show()
		case now := <-tick.C:
			changed := match.waiting()
			expire(now)
			for side, r := range rep {
				if now.Before(r.next) {
					continue
				}
				_ = sendFrom(side, r)
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
			case LiveKNXSend:
				ga, val, err := ParseKNXSend(r.GA, r.Value)
				var src uint16
				if err == nil && r.Side == CANBoard && strings.TrimSpace(r.Src) != "" {
					src, err = knxusb.ParseIA(r.Src)
				}
				if err == nil && r.Side != CANBoard && r.Side != CANPC {
					err = errors.New("no such side " + r.Side)
				}
				if err == nil && r.Every != 0 && (r.Every < knxEveryMin || r.Count < 1 || r.Count > canCountMax) {
					err = fmt.Errorf("repeating needs an interval of at least %d ms and 1 to %d times", knxEveryMin, canCountMax)
				}
				if err != nil {
					m.reply <- err
					continue
				}
				x := &knxRepeat{ga: ga, val: val}
				if r.Side == CANBoard && strings.TrimSpace(r.Src) != "" {
					x.srcText = knxusb.IAText(src)
				}
				delete(rep, r.Side)
				err = sendFrom(r.Side, x)
				if err == nil && r.Every > 0 && r.Count > 1 {
					x.every = time.Duration(r.Every) * time.Millisecond
					x.left, x.next = r.Count-1, time.Now().Add(x.every)
					rep[r.Side] = x
				}
				show()
				m.reply <- err
			case LiveKNXStop:
				delete(rep, r.Side)
				show()
				m.reply <- nil
			case LiveKNXClear:
				pc := v.PC
				pc.Tx, pc.Rx, pc.ConOK, pc.ConFail = 0, 0, 0, 0
				v = KNXView{Board: v.Board, PC: pc, Rows: []KNXRow{}}
				match.clear()
				show()
				m.reply <- nil
			default:
				m.reply <- errors.New("unknown action " + r.Op)
			}
		}
	}
}
