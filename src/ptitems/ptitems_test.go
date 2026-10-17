package ptitems

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"PortTool/src/ptplan"
	"PortTool/src/ptproto"
)

const (
	goldenPath  = "../../TestCase/host/porttool_caps/caps_golden.txt"
	stringsPath = "../ptpanel/web/strings.json"
	planPath    = "../plans/station6-poweron.json"
)

// goldenCaps is the first pt.caps reply of the T4-01 transcript: what the real
// porttool.c reports.
func goldenCaps(t *testing.T) ptproto.Caps {
	t.Helper()
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read %s: %v (T4-01 writes it: TestCase/test.sh t4-01)", goldenPath, err)
	}
	chunk := strings.Split(string(raw), ">>> ")[1]
	var lines []string
	for _, l := range strings.Split(chunk, "\n")[1:] {
		l = strings.TrimRight(l, "\r")
		if l != "" && !strings.HasPrefix(l, "TEST ") {
			lines = append(lines, l)
		}
	}
	c, err := ptproto.ParseCaps(lines)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Every port the firmware reports is either a test item or listed in NotItems,
// so a port added to the firmware cannot silently miss the panel.
func TestEveryCapsPortIsCovered(t *testing.T) {
	c := goldenCaps(t)
	_, items := ForCaps(c)
	has := map[string]bool{}
	for _, it := range items {
		has[it.Port] = true
	}
	for _, p := range c.Ports {
		_, skip := NotItems[p.Name]
		if has[p.Name] == skip {
			t.Errorf("port %s: has items=%v, in NotItems=%v; want exactly one", p.Name, has[p.Name], skip)
		}
	}
	for _, it := range Catalogue {
		if _, ok := c.Port(it.Port); !ok {
			t.Errorf("%s: port %s is not in the golden caps", it.ID, it.Port)
		}
	}
}

// What an item asks the firmware to do has to be something the firmware takes.
// The reference board runs the same firmware, so its targets are checked
// against the same caps.
func TestTargetsFitTheFirmware(t *testing.T) {
	c := goldenCaps(t)
	for _, it := range Catalogue {
		for _, tg := range append(append([]Target{}, it.Targets...), it.Ref...) {
			checkTarget(t, c, it, tg)
		}
		p, _ := c.Port(it.Port)
		for _, k := range it.Params {
			if HostParam(k) {
				continue // kept on the PC (decisions 91, 106)
			}
			if !takes(p, k) {
				t.Errorf("%s: editable parameter %s is not one %s takes", it.ID, k, it.Port)
			}
		}
	}
}

func checkTarget(t *testing.T, c ptproto.Caps, it Item, tg Target) {
	t.Helper()
	switch {
	case tg.Run != "":
		p, _ := c.Port(it.Port)
		found := false
		for _, r := range p.Runs {
			found = found || r == tg.Run
		}
		if !found {
			t.Errorf("%s: %s is not a run target of %s", it.ID, tg.Run, it.Port)
		}
	case tg.Session != "":
		p, ok := c.Port(tg.Session)
		if !ok || p.Kind != ptproto.KindSession {
			t.Errorf("%s: %s is not a session port", it.ID, tg.Session)
			return
		}
		for k, v := range tg.Set {
			if !takes(p, k) {
				t.Errorf("%s: %s takes no %s", it.ID, tg.Session, k)
			} else if ok, why := p.Limits[k].Accepts(v); !ok {
				t.Errorf("%s: %s %s=%s: %s", it.ID, tg.Session, k, v, why)
			}
		}
	default:
		t.Errorf("%s: empty target", it.ID)
	}
}

func takes(p ptproto.Port, param string) bool {
	for _, x := range p.Params {
		if x == param {
			return true
		}
	}
	return false
}

func TestItemsAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, it := range Catalogue {
		if seen[it.ID] {
			t.Errorf("%s twice", it.ID)
		}
		seen[it.ID] = true
		if !it.In(RD) {
			t.Errorf("%s: not in R&D, which lists every item", it.ID)
		}
		if len(it.Peers) == 0 {
			t.Errorf("%s: no peer", it.ID)
		}
		for _, pe := range it.Peers {
			if pe == PeerRef && len(it.Ref) == 0 && it.Pending == "" {
				t.Errorf("%s: the reference board is a peer but nothing says what it runs", it.ID)
			}
			if pe == PeerHuman && it.Human == "" && !it.Live {
				t.Errorf("%s: a person is a peer but neither what they give nor a live card", it.ID)
			}
		}
		if it.Exclusive != "" && !strings.HasPrefix(it.Exclusive, it.Port+".") {
			t.Errorf("%s: exclusive group %s crosses ports", it.ID, it.Exclusive)
		}
	}
}

// Criteria are plan steps; a name the plan does not have would leave an item
// looking judged when nothing judges it.
func TestCriteriaNameRealPlanSteps(t *testing.T) {
	plan, err := ptplan.Load(planPath)
	if err != nil {
		t.Fatal(err)
	}
	steps := map[string]bool{}
	for _, s := range plan.Steps {
		steps[s.ID] = true
	}
	for _, it := range Catalogue {
		for _, id := range it.Criteria {
			if !steps[id] {
				t.Errorf("%s: criteria step %q is not in %s", it.ID, id, planPath)
			}
		}
	}
}

// catalogueKeys is every strings.json key the catalogue uses.
func catalogueKeys() []string {
	var k []string
	for _, p := range Ports {
		k = append(k, p.Label)
	}
	for _, it := range Catalogue {
		k = append(k, it.Name, it.What)
		if it.Pending != "" {
			k = append(k, it.Pending)
		}
	}
	for _, v := range NotItems {
		k = append(k, v)
	}
	return append(k, RecoverPending)
}

func TestEveryKeyIsInTheDictionary(t *testing.T) {
	raw, err := os.ReadFile(stringsPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Strings map[string]map[string]string `json:"strings"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, k := range catalogueKeys() {
		e, ok := doc.Strings[k]
		if !ok {
			t.Errorf("%s: not in strings.json", k)
			continue
		}
		for _, lang := range []string{"zh", "en", "de"} {
			if strings.TrimSpace(e[lang]) == "" {
				t.Errorf("%s: no %s text", k, lang)
			}
		}
	}
}

// Items that offer the reference board default to it only while it is
// connected, otherwise to their other peer; the rest keep their first peer
// (decision 112, PORTTOOL-FLOW.md C.3.6 「默认对端」).
func TestDefaultPeerFollowsTheConnection(t *testing.T) {
	byID := map[string]Item{}
	for _, it := range Catalogue {
		byID[it.ID] = it
	}
	for _, id := range []string{"din.level", "dout.load", "ain.read2", "aout.out1", "aout.out2"} {
		it, ok := byID[id]
		if !ok {
			t.Fatalf("%s is not in the catalogue", id)
		}
		if got := DefaultPeer(it, true); got != PeerRef {
			t.Errorf("%s with the reference board connected: %s, want ref", id, got)
		}
		if got := DefaultPeer(it, false); got != PeerHuman {
			t.Errorf("%s with no reference board: %s, want human", id, got)
		}
	}
	// CAN falls back to the PC: the reference board is parked (TODO-fixture.md).
	can := byID["can.peer"]
	if got := DefaultPeer(can, true); got != PeerRef {
		t.Errorf("can.peer with the reference board connected: %s, want ref", got)
	}
	if got := DefaultPeer(can, false); got != PeerPC {
		t.Errorf("can.peer with no reference board: %s, want pc", got)
	}
	if eth := byID["eth.xfer"]; DefaultPeer(eth, false) != eth.Peers[0] {
		t.Errorf("eth.xfer offers no reference board and keeps %s", eth.Peers[0])
	}
}

// AI and AO are two cards each (decision 114): every analog item sits in one
// of four groups, and each reading / calibration group covers both channels.
func TestAnalogItemsFormFourGroups(t *testing.T) {
	members := map[string][]string{}
	for _, it := range Catalogue {
		if it.Port != "ain" && it.Port != "aout" {
			continue
		}
		if it.Group == "" {
			t.Errorf("%s has no group", it.ID)
		}
		members[it.Group] = append(members[it.Group], it.ID)
	}
	want := map[string][]string{
		"ain.read": {"ain.live", "ain.read1", "ain.read2"}, "ain.cal": {"ain.cal1", "ain.cal2"},
		"aout.out": {"aout.out1", "aout.out2", "aout.manual", "aout.load"}, "aout.cal": {"aout.cal1", "aout.cal2"},
	}
	for g, ids := range want {
		got := map[string]bool{}
		for _, id := range members[g] {
			got[id] = true
		}
		for _, id := range ids {
			if !got[id] {
				t.Errorf("group %s lacks %s: %v", g, id, members[g])
			}
		}
	}
	if len(members) != len(want) {
		t.Errorf("groups %v", members)
	}
}

// Decision 151: the SDRAM sweep runs once in R&D only (decision 141 retired
// the window CRC), the long read/write is R&D's repeatable one; eth / usb measure download and
// upload before the mirror run.
func TestDecision138Items(t *testing.T) {
	byID := map[string]Item{}
	for _, it := range Catalogue {
		byID[it.ID] = it
	}
	if it := byID["sdram.sweep"]; it.Once || !it.OnceRD {
		t.Errorf("sdram.sweep: once=%v once_rd=%v, want once in R&D only", it.Once, it.OnceRD)
	}
	// Decision 141: the window CRC is gone from every test type.
	if _, ok := byID["sdram.crc"]; ok || !RetiredItem("sdram.crc") {
		t.Errorf("sdram.crc: still in the catalogue or not retired")
	}
	// Decision 139: also in the functional test, not in aging or user-level.
	if it := byID["sdram.hold"]; !it.In(RD) || !it.In(Func) || it.In(Aging) || it.In(User) {
		t.Errorf("sdram.hold: stages %v, want R&D and functional only", it.Stages)
	}
	if it := byID["sdram.hold"]; it.Once || it.OnceRD {
		t.Errorf("sdram.hold: want a repeatable item, got %+v", it)
	}
	for id, want := range map[string]string{"eth.xfer": "eth-down eth-up eth-throughput", "usb.xfer": "usb-down usb-up usb-link"} {
		if got := strings.Join(byID[id].Criteria, " "); got != want {
			t.Errorf("%s criteria %q, want %q", id, got, want)
		}
	}
}
