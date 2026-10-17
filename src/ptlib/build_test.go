package ptlib

import (
	"strings"
	"testing"

	"PortTool/src/ptitems"
	"PortTool/src/ptplan"
	"PortTool/src/ptproto"
)

// An item's criteria hold one set of steps per peer; the picked peer chooses
// (PORTTOOL-FLOW.md C.3.6 "一项跑哪几步"). Checked against the shipped plan.
func TestStepsFollowThePeer(t *testing.T) {
	plan, err := ptplan.Load("../plans/station6-poweron.json")
	if err != nil {
		t.Fatal(err)
	}
	ids := func(id string, peer ptitems.Peer) string {
		jobs, err := build(ptitems.Catalogue, plan, ptproto.Caps{}, []Pick{{ID: id, On: true, Peer: peer}})
		if err != nil {
			t.Fatal(err)
		}
		for _, j := range jobs {
			if j.item.ID == id {
				var out []string
				for _, st := range j.steps {
					out = append(out, st.ID)
				}
				return j.fail + "|" + strings.Join(out, ",")
			}
		}
		return "missing"
	}
	cases := []struct {
		id   string
		peer ptitems.Peer
		want string
	}{
		{"can.peer", ptitems.PeerRef, "|can-peer-ref,can-peer"},
		{"can.peer", ptitems.PeerPC, "|can-pc"},
		// The chain reads DO through the relays, not plan steps (chain.go, decision 133).
		{"dout.load", ptitems.PeerRef, "|"},
		// A person measures each channel while it is on (drop.go, STG-32): only the drop step judges.
		{"dout.load", ptitems.PeerHuman, "|high-side-drop"},
		{"aout.out1", ptitems.PeerHuman, "|ao1-out"},
		// The reference board's own k, b give the reference value (analog.go, STG-31).
		{"aout.out1", ptitems.PeerRef, "|ao1-out"},
		// Any AI may be served by a reference AO the settings page wires to it (decision 135).
		{"ain.read1", ptitems.PeerRef, "|ai1-read"},
	}
	for _, c := range cases {
		if got := ids(c.id, c.peer); got != c.want {
			t.Errorf("%s with %s: %s, want %s", c.id, c.peer, got, c.want)
		}
	}
	if got := ids("din.level", ptitems.PeerRef); strings.Count(got, "digital-in-ref-") != 8 {
		t.Errorf("din.level with the reference board walks DO1-8 there: %s", got)
	}
}

// A configuration card lists the criteria of its peer only (PORTTOOL-FLOW.md
// C.3.6 layer 3): the same steps build picks.
func TestCriteriaPeersFollowBuild(t *testing.T) {
	plan, err := ptplan.Load("../plans/station6-poweron.json")
	if err != nil {
		t.Fatal(err)
	}
	item := func(id string) ptitems.Item {
		for _, it := range ptitems.Catalogue {
			if it.ID == id {
				return it
			}
		}
		t.Fatalf("%s is not in the catalogue", id)
		return ptitems.Item{}
	}
	got := func(id, step string) string {
		var s []string
		for _, p := range CriteriaPeers(item(id), plan)[step] {
			s = append(s, string(p))
		}
		return strings.Join(s, ",")
	}
	for _, c := range []struct{ id, step, want string }{
		{"can.peer", "can-peer", "ref"},
		{"can.peer", "can-pc", "pc"},
		{"dout.load", "high-side-drop", "human"},
		{"aout.out1", "ao1-out", "ref,human"},
		{"eth.link", "eth-link", "pc"},
	} {
		if g := got(c.id, c.step); g != c.want {
			t.Errorf("%s step %s: peers %q, want %q", c.id, c.step, g, c.want)
		}
	}
}
