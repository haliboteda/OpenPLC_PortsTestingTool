package ptlib

import (
	"testing"
	"time"

	"PortTool/src/ptitems"
	"PortTool/src/ptplan"
	"PortTool/src/ptproto"
)

// One channel of the DI walk (decision 126): three matching frames in a row
// pass, another input high for dinWrong fails, all low waits.
func TestDinJudge(t *testing.T) {
	t0 := time.Unix(0, 0)
	d := dinJudge{}
	for i, want := range []string{"", "", DinOK} {
		if got := d.frame(true, true, t0.Add(time.Duration(i)*50*time.Millisecond)); got != want {
			t.Fatalf("matching frame %d: %q, want %q", i+1, got, want)
		}
	}

	d = dinJudge{}
	if got := d.frame(false, true, t0); got != "" {
		t.Fatalf("first wrong frame decided %q", got)
	}
	if got := d.frame(false, false, t0.Add(time.Second)); got != "" {
		t.Fatalf("all low decided %q", got)
	}
	// All low in between restarts the wrong-input clock: the person moving a wire.
	if got := d.frame(false, true, t0.Add(1500*time.Millisecond)); got != "" {
		t.Fatalf("wrong again decided %q", got)
	}
	if got := d.frame(false, true, t0.Add(1500*time.Millisecond+dinWrong-time.Millisecond)); got != "" {
		t.Fatalf("wrong for less than %s decided %q", dinWrong, got)
	}
	if got := d.frame(false, true, t0.Add(1500*time.Millisecond+dinWrong)); got != DinWrong {
		t.Fatalf("wrong for %s: %q, want %q", dinWrong, got, DinWrong)
	}

	d = dinJudge{}
	d.frame(true, true, t0)
	d.frame(true, true, t0)
	if got := d.frame(false, false, t0); got != "" || d.streak != 0 {
		t.Fatalf("a low frame must break the streak: %q, streak %d", got, d.streak)
	}
}

// Inputs already passed and this one never count as wrong (the person is still
// moving the wire); anything else does.
func TestDinOther(t *testing.T) {
	for _, c := range []struct {
		v    string
		mask uint64
		want bool
	}{{"0x00", 0x02, false}, {"0x01", 0x03, false}, {"0x03", 0x03, false}, {"0x06", 0x03, true}, {"0x04", 0x02, true}, {"junk", 0, false}} {
		if got := dinOther(c.v, c.mask); got != c.want {
			t.Errorf("dinOther(%s, %#x) = %v, want %v", c.v, c.mask, got, c.want)
		}
	}
}

// A person's DI level is walked and judged (decision 128), not a live card.
func TestDinWalkInEveryTestType(t *testing.T) {
	plan, err := ptplan.Load("../plans/station6-poweron.json")
	if err != nil {
		t.Fatal(err)
	}
	pick := []Pick{{ID: "din.level", On: true, Peer: ptitems.PeerHuman}}
	jobs, err := build(ptitems.Catalogue, plan, ptproto.Caps{}, pick)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range jobs {
		if j.item.ID != "din.level" {
			continue
		}
		if !j.dinWalk || j.live {
			t.Errorf("walk %v live %v", j.dinWalk, j.live)
		}
		if len(j.steps) != 8 || j.steps[0].ID != "digital-in" || j.steps[7].ID != "digital-in-8" {
			t.Errorf("walk steps %d, want digital-in .. digital-in-8", len(j.steps))
		}
	}
	// The full test's cards list the walk's criteria under the person.
	if p := CriteriaPeers(ptitems.Catalogue[indexOf(t, "din.level")], plan)["digital-in-3"]; !peerIn(p, ptitems.PeerHuman) {
		t.Errorf("digital-in-3 peers %v, want the person among them", p)
	}
}

func indexOf(t *testing.T, id string) int {
	for i, it := range ptitems.Catalogue {
		if it.ID == id {
			return i
		}
	}
	t.Fatalf("%s not in the catalogue", id)
	return -1
}

func peerIn(ps []ptitems.Peer, p ptitems.Peer) bool {
	for _, q := range ps {
		if q == p {
			return true
		}
	}
	return false
}
