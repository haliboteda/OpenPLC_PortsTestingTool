package ptlib

import (
	"strings"
	"testing"
	"time"

	"PortTool/src/ptitems"
	"PortTool/src/ptplan"
	"PortTool/src/ptproto"
)

func rframe(t *testing.T, line string) *ptproto.Frame {
	t.Helper()
	kind, body := ptproto.Classify(line)
	f, ok := ptproto.ParseFrame(body)
	if kind != ptproto.LineFrame || !ok {
		t.Fatalf("not a frame: %s", line)
	}
	return &f
}

// feedWatch runs lines through a watch 500 ms apart and returns the events.
func feedWatch(t *testing.T, w *recoverWatch, at time.Time, lines ...string) []int {
	t.Helper()
	var out []int
	for i, l := range lines {
		out = append(out, w.see(rframe(t, l), at.Add(time.Duration(i)*500*time.Millisecond)))
	}
	return out
}

func last(evs []int) int { return evs[len(evs)-1] }

// Each port's own signals (PORTTOOL-FLOW.md C.3.6 「断开恢复」): what breaks,
// what says the line is back, what says the traffic caught up.
func TestRecoverWatchPerPort(t *testing.T) {
	t0 := time.Unix(100, 0)
	for _, c := range []struct {
		port              string
		warm, cut, back   []string
		recoverMS, downMS string
		lost              string
	}{
		{"eth",
			[]string{"!eth t=1000 seq=1 rx=0 miss=0 link=1 conn=1", "!eth t=1500 seq=2 rx=1 miss=0 link=1 conn=1"},
			[]string{"!eth t=2000 seq=2 rx=1 miss=1 link=0 conn=1"},
			[]string{"!eth t=2500 seq=2 rx=1 miss=2 link=0 conn=1", "!eth t=3000 seq=2 rx=1 miss=3 link=1 conn=1", "!eth t=3500 seq=3 rx=2 miss=0 link=1 conn=1"},
			"500", "1000", "3"},
		{"usb",
			[]string{"!usb t=1000 seq=1 rx=0 miss=0 state=cfg", "!usb t=1500 seq=2 rx=1 miss=0 state=cfg"},
			[]string{"!usb t=2000 seq=2 rx=1 miss=1 state=default"},
			[]string{"!usb t=2500 seq=2 rx=1 miss=2 state=cfg", "!usb t=3000 seq=3 rx=2 miss=0 state=cfg"},
			"500", "500", "2"},
		{"rs485",
			[]string{"!rs485 t=1000 seq=1 rx=0 miss=0", "!rs485 t=1500 seq=2 rx=1 miss=0"},
			[]string{"!rs485 t=2000 seq=2 rx=1 miss=1", "!rs485 t=2500 seq=2 rx=1 miss=2"},
			[]string{"!rs485 t=3000 seq=2 rx=1 miss=3", "!rs485 t=3500 seq=3 rx=2 miss=0"},
			"0", "1000", "3"},
		{"can",
			[]string{"!can t=1000 seq=1 rx=0 miss=0 lec=0", "!can t=1500 seq=2 rx=1 miss=0 lec=0"},
			[]string{"!can t=2000 seq=2 rx=1 miss=1 lec=3"},
			[]string{"!can t=2500 seq=2 rx=1 miss=2 lec=3", "!can t=3000 seq=3 rx=2 miss=0 lec=0"},
			"0", "1000", "2"},
		{"knx",
			[]string{"!knx t=1000 seq=1 rx=0 miss=0 bus=ok", "!knx t=1500 seq=2 rx=1 miss=0 bus=ok"},
			[]string{"!knx t=2000 seq=2 rx=1 miss=1 bus=dead"},
			[]string{"!knx t=2500 seq=2 rx=1 miss=2 bus=ok", "!knx t=3000 seq=3 rx=2 miss=0 bus=ok"},
			"500", "500", "2"},
	} {
		w := &recoverWatch{port: c.port}
		if ev := last(feedWatch(t, w, t0, c.warm...)); ev != rwWarmed {
			t.Errorf("%s: two caught-up frames did not warm it up (%d)", c.port, ev)
			continue
		}
		if ev := last(feedWatch(t, w, t0, c.cut...)); ev != rwBroke {
			t.Errorf("%s: the cut was not seen as a break (%d)", c.port, ev)
			continue
		}
		if ev := last(feedWatch(t, w, t0, c.back...)); ev != rwCaughtUp {
			t.Errorf("%s: never caught up (%d)", c.port, ev)
			continue
		}
		got := map[string]string{}
		for _, kv := range w.values() {
			got[kv[0]] = kv[1]
		}
		if got["recover_ms"] != c.recoverMS || got["down_ms"] != c.downMS || got["lost"] != c.lost || got["reset"] != "0" {
			t.Errorf("%s: %v, want recover_ms %s down_ms %s lost %s reset 0", c.port, got, c.recoverMS, c.downMS, c.lost)
		}
	}
}

// RS232's frames ride the cable under test: silence is the break, the first
// frame after it the line coming back, and its miss what was lost.
func TestRecoverWatchRS232Silence(t *testing.T) {
	t0 := time.Unix(100, 0)
	w := &recoverWatch{port: "rs232"}
	feedWatch(t, w, t0, "!rs232 t=1000 seq=1 rx=0 miss=0", "!rs232 t=1500 seq=2 rx=1 miss=0")
	if ev := w.see(nil, t0.Add(500*time.Millisecond+recoverQuiet-time.Millisecond)); ev != rwNone {
		t.Fatalf("a break before %s of silence (%d)", recoverQuiet, ev)
	}
	if ev := w.see(nil, t0.Add(500*time.Millisecond+recoverQuiet)); ev != rwBroke {
		t.Fatalf("%s of silence is not a break (%d)", recoverQuiet, ev)
	}
	evs := feedWatch(t, w, t0.Add(10*time.Second), "!rs232 t=6000 seq=2 rx=1 miss=9", "!rs232 t=6500 seq=3 rx=2 miss=0")
	if last(evs) != rwCaughtUp {
		t.Fatalf("never caught up: %v", evs)
	}
	got := map[string]string{}
	for _, kv := range w.values() {
		got[kv[0]] = kv[1]
	}
	if got["lost"] != "9" || got["recover_ms"] != "500" || got["down_ms"] != "4500" || got["reset"] != "0" {
		t.Errorf("%v, want lost 9, recover_ms 500, down_ms 4500, reset 0", got)
	}
}

// A board that restarted while the cable was out shows its tick falling back.
func TestRecoverWatchSeesARestart(t *testing.T) {
	t0 := time.Unix(100, 0)
	w := &recoverWatch{port: "rs485"}
	feedWatch(t, w, t0, "!rs485 t=90000 seq=1 rx=0 miss=0", "!rs485 t=90500 seq=2 rx=1 miss=0",
		"!rs485 t=91000 seq=2 rx=1 miss=2", "!rs485 t=1200 seq=2 rx=1 miss=0")
	for _, kv := range w.values() {
		if kv[0] == "reset" && kv[1] != "1" {
			t.Errorf("tick 91000 then 1200 is a restart: %v", w.values())
		}
	}
}

// Every recover item names its plan step; without it in the plan the item
// is record-only and says so (decision 150).
func TestRecoverItemsNameTheirCriteria(t *testing.T) {
	n := 0
	for _, it := range ptitems.Catalogue {
		if !it.Recover {
			continue
		}
		n++
		if strings.Join(it.Criteria, " ") != it.Port+"-recover" || RecordOnly(it) || !it.Once || len(it.Stages) != 1 {
			t.Errorf("%s: criteria %v, record-only %v, once %v, stages %v", it.ID, it.Criteria, RecordOnly(it), it.Once, it.Stages)
		}
		bare := ForPlan([]ptitems.Item{it}, ptplan.Plan{})[0]
		if !RecordOnly(bare) || bare.Pending != ptitems.RecoverPending {
			t.Errorf("%s with no plan step: record-only %v, pending %q", it.ID, RecordOnly(bare), bare.Pending)
		}
	}
	if n != 6 {
		t.Errorf("%d recover items, want one per communication port (6)", n)
	}
}
