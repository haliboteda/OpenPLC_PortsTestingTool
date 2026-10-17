// Package ptitems is the test-item catalogue: every test item of every port,
// which firmware target runs it, which stages it appears in, what peer it
// needs and which plan steps hold its criteria.
// $PROD/docs/modules/M4/PORTTOOL-FLOW.md C.3.6; items from
// $PROD/maps/fixture-test-stages/issues/STG-03-what-each-port-tests-at-each-level.md
//
// Judging is not here: Criteria names plan steps, and ptcheck evaluates them
// (decision 77).
package ptitems

import "PortTool/src/ptproto"

// Stage is a test type an item can appear in. R&D lists every item; the other
// three are configurations picked from it (decision 97). Safety has no ports.
type Stage string

const (
	RD    Stage = "rd"
	Func  Stage = "func"
	Aging Stage = "aging"
	User  Stage = "user"
)

// Peer is what sits on the other end of an item.
type Peer string

const (
	PeerNone  Peer = "none"
	PeerPC    Peer = "pc"
	PeerRef   Peer = "ref" // the reference board
	PeerHuman Peer = "human"
)

// Answer is what a person contributes to an item.
type Answer string

const (
	AnswerNumber Answer = "number" // types a reading; judged like any other value
	AnswerYesNo  Answer = "yes_no" // confirms what they saw or heard
	AnswerWait   Answer = "wait"   // acts; the board's reading ends the question (decision 128)
)

// Target is one thing the firmware is asked to do: a session on a port with
// fixed parameters, or a one-shot pt.run target.
type Target struct {
	Session string            `json:"session,omitempty"`
	Set     map[string]string `json:"set,omitempty"`
	Run     string            `json:"run,omitempty"`
}

// Item is one test item. Name and What are strings.json keys.
type Item struct {
	ID      string   `json:"id"`
	Port    string   `json:"port"`
	Channel int      `json:"channel,omitempty"` // 0 = the whole port
	Name    string   `json:"name"`
	What    string   `json:"what"`
	Targets []Target `json:"targets"`
	// Params are the session parameters a person may change on this item;
	// everything else is fixed by Targets or left at the board's default.
	Params []string `json:"params,omitempty"`
	Once   bool     `json:"once,omitempty"`
	// OnceRD: once in R&D only; the fixture tests repeat it for the duration
	// (decision 151, PORTTOOL-FLOW.md C.3.6).
	OnceRD bool    `json:"once_rd,omitempty"`
	Stages []Stage `json:"stages"`
	Peers  []Peer  `json:"peers"` // first is the default
	// Exclusive: items of one port sharing a group cannot be picked together,
	// because they are one session in different modes (decision 102).
	Exclusive string `json:"exclusive,omitempty"`
	// Ref is what the reference board runs while this item runs, when the
	// peer is the reference board: the second board connection (decision 103).
	Ref []Target `json:"ref,omitempty"`
	// Human says what a person gives when the peer is a person: a number
	// judged by ptcheck, or a yes/no (decision 103, STG-19).
	Human    Answer   `json:"human,omitempty"`
	Criteria []string `json:"criteria"`          // plan step ids; empty = limits not decided yet
	Pending  string   `json:"pending,omitempty"` // strings.json key: firmware change still to come
	// WatchS keeps a criteria-less session open this many seconds and lists
	// its event frames on the card, instead of stopping at the first frame
	// (decision 105).
	WatchS int `json:"watch_s,omitempty"`
	// Load marks the AO load item: its "out1" / "out2" params are each AO's
	// target in that channel's registered unit, required, no default
	// (decisions 129, 134, STG-38), kept on the PC and sent as DAC mV from the
	// board's own k, b (PORTTOOL-FLOW.md C.3.6).
	Load bool `json:"load,omitempty"`
	// Live: with a person as the peer the session stays open, the card follows
	// every frame and sets outputs, until the person ends it (decision 108,
	// PORTTOOL-FLOW.md C.3.6 "实时卡片").
	Live bool `json:"live,omitempty"`
	// Group draws several items as one card with a row per channel: AI / AO
	// reading, output and calibration (decision 114). Each member still runs
	// and is judged on its own, so configurations and records keep their ids.
	Group string `json:"group,omitempty"`
	// Recover: a person pulls the port's cable while it carries traffic and
	// plugs it back; judged by its criteria step when the plan has one, else
	// recorded (decision 150, PORTTOOL-FLOW.md C.3.6 「断开恢复」).
	Recover bool `json:"recover,omitempty"`
}

// Parameters kept on the PC; never sent to the board as they are.
const (
	// AO load targets, one per channel, each in its AO's registered unit (mA or mV).
	ParamOut1 = "out1"
	ParamOut2 = "out2"
	// Relay switching: how ticked relays run together and how the card was
	// filled; the board takes ch / close / open / count (decision 123).
	ParamOrder   = "order"   // par | ser
	ParamBy      = "by"      // gap: holds typed | time: holds worked out from minutes and count
	ParamMinutes = "minutes" // per relay, the time its count should take (by=time)
)

// retiredParams are parameters an item no longer takes. Saved configurations
// and page memories may still carry them, so they are dropped on read rather
// than refused: 按时长 splits each cycle half and half, no ratio (decision 123).
var retiredParams = map[string]map[string]bool{"relay.cycle": {"ratio": true}}

// renamedParams are retired parameters whose value moves to new ones on read:
// the AO load's shared "ma" fills both channels' targets (STG-38).
var renamedParams = map[string]map[string][]string{"aout.load": {"ma": {ParamOut1, ParamOut2}}}

// retiredItems are items dropped from the catalogue. Saved configurations and
// page memories may still name them, so they are dropped on read rather than
// refused: the window CRC is covered by the full sweep (decision 141).
var retiredItems = map[string]bool{"sdram.crc": true}

// RetiredItem says an item was dropped from the catalogue.
func RetiredItem(id string) bool { return retiredItems[id] }

// Retired says an item's parameter was dropped from the catalogue.
func Retired(id, k string) bool { return retiredParams[id][k] || renamedParams[id][k] != nil }

// RenamedTo is the parameters a retired one's value moves to; nil when dropped.
func RenamedTo(id, k string) []string { return renamedParams[id][k] }

// DropRetired removes an item's retired parameters from params in place; a
// renamed one's value fills its new parameters that are not set.
func DropRetired(id string, params map[string]string) {
	for k, v := range params {
		if !Retired(id, k) {
			continue
		}
		for _, n := range RenamedTo(id, k) {
			if _, set := params[n]; !set {
				params[n] = v
			}
		}
		delete(params, k)
	}
}

// LoadChannel is the AO an AO load target parameter belongs to; "" for any other.
func LoadChannel(k string) string {
	switch k {
	case ParamOut1:
		return "AO1"
	case ParamOut2:
		return "AO2"
	}
	return ""
}

// HostParam says a parameter is kept on the PC rather than taken by the firmware.
func HostParam(k string) bool {
	switch k {
	case ParamOut1, ParamOut2, ParamOrder, ParamBy, ParamMinutes:
		return true
	}
	return false
}

// Asks says whether the item waits for a person's answer by default; such items
// run one by one before the timed part of a stage (decision 102).
func (it Item) Asks() bool { return len(it.Peers) > 0 && it.Peers[0] == PeerHuman }

// RDPeers are the peers an item offers in R&D: never the reference board, R&D
// is done by hand (decision 133, PORTTOOL-FLOW.md C.3.6 「默认对端」).
func (it Item) RDPeers() []Peer {
	var out []Peer
	for _, p := range it.Peers {
		if p != PeerRef {
			out = append(out, p)
		}
	}
	return out
}

// DefaultPeer is the peer a configuration item runs with when nobody picked
// one: an item that offers the reference board and another peer takes the
// reference board while it is connected and its other peer otherwise; every
// other item takes its first peer (decision 112, PORTTOOL-FLOW.md C.3.6). The
// page's cfgDefaultPeer (web/js/stagecfg.js) mirrors this.
func DefaultPeer(it Item, refConnected bool) Peer {
	if len(it.Peers) == 0 {
		return ""
	}
	if it.offers(PeerRef) && !refConnected {
		for _, p := range it.Peers {
			if p != PeerRef {
				return p
			}
		}
	}
	return it.Peers[0]
}

func (it Item) offers(p Peer) bool {
	for _, q := range it.Peers {
		if q == p {
			return true
		}
	}
	return false
}

// In says whether the item appears in a stage.
func (it Item) In(st Stage) bool {
	for _, s := range it.Stages {
		if s == st {
			return true
		}
	}
	return false
}

// PortInfo is how a port is named on screen.
type PortInfo struct {
	Port  string `json:"port"`
	Label string `json:"label"` // strings.json key
}

// NotItems are ports pt.caps reports that are deliberately not test items,
// with the reason (a strings.json key).
var NotItems = map[string]string{
	// Shown as the board time under the control port instead (STG-03).
	"rtc": "lib.notitem.rtc",
}

var (
	all3   = []Stage{RD, Func, Aging, User}
	rdOnly = []Stage{RD}
	rdFunc = []Stage{RD, Func} // SDRAM read/write: the demo has no retention session (decision 139)
)

func session(port string, kv ...string) Target {
	t := Target{Session: port}
	if len(kv) > 0 {
		t.Set = map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			t.Set[kv[i]] = kv[i+1]
		}
	}
	return t
}

func run(target string) Target { return Target{Run: target} }

func item(port, id string, ch int, t []Target, stages []Stage, peers []Peer, crit ...string) Item {
	full := port + "." + id
	if crit == nil {
		crit = []string{}
	}
	return Item{
		ID: full, Port: port, Channel: ch,
		Name: "lib.item." + full + ".name", What: "lib.item." + full + ".what",
		Targets: t, Stages: stages, Peers: peers, Criteria: crit,
	}
}

func once(it Item) Item                    { it.Once = true; return it }
func onceRD(it Item) Item                  { it.OnceRD = true; return it }
func onRef(it Item, t ...Target) Item      { it.Ref = t; return it }
func human(it Item, a Answer) Item         { it.Human = a; return it }
func excl(it Item, g string) Item          { it.Exclusive = g; return it }
func pend(it Item, key string) Item        { it.Pending = key; return it }
func params(it Item, names ...string) Item { it.Params = names; return it }
func watch(it Item, secs int) Item         { it.WatchS = secs; return it }
func live(it Item) Item                    { it.Live = true; return it }
func group(it Item, g string) Item         { it.Group = g; return it }
func load(it Item) Item {
	it.Load = true
	it.Params = append(it.Params, ParamOut1, ParamOut2)
	return it
}

// Ports is the catalogue's port order and labels. Labels already used by the
// panel are reused.
var Ports = []PortInfo{
	{"eth", "port_label.eth"}, {"usb", "port_label.usb"}, {"sd", "port_label.sd"},
	{"sdram", "lib.port.sdram"}, {"reset", "port_label.reset"}, {"led", "port_label.led"},
	{"din", "port_label.din"}, {"ain", "port_label.ain"}, {"aout", "port_label.aout"},
	{"rs232", "lib.port.rs232"}, {"rs485", "lib.port.rs485"}, {"can", "lib.port.can"},
	{"knx", "lib.port.knx"}, {"dout", "port_label.dout"}, {"relay", "port_label.relay"},
	{"temp", "port_label.temp"},
}

func analog(port, readID, calID string, ch int) []Item {
	sfx := string(rune('0' + ch))
	peers := []Peer{PeerHuman}
	// A reference AO drives an AI, a reference AI reads an AO; which one is
	// registered on the reference board's settings page (decision 135).
	peers = []Peer{PeerRef, PeerHuman}
	t := []Target{session(port, "ch", sfx)}
	// No editable session parameter: AO outputs current only (decision 98), so
	// the DAC mV follows from the card's mA points and the board's k, b (C.3.2).
	// The criteria step holds the ±1 % FS limit per point (ptlib/analog.go).
	crit := "ai" + sfx + "-read"
	if port == "aout" {
		crit = "ao" + sfx + "-out"
	}
	read := human(item(port, readID, ch, t, all3, peers, crit), AnswerNumber)
	read.Group = port + map[string]string{"ain": ".read", "aout": ".out"}[port]
	if port == "ain" {
		read = onRef(read, session("aout"))
	} else {
		read = onRef(read, session("ain"))
	}
	return []Item{read, group(human(item(port, calID, ch, t, rdOnly, []Peer{PeerHuman}), AnswerNumber), port+".cal")}
}

// Catalogue is every item, in the order cards are shown.
var Catalogue = func() []Item {
	none, pc, hum := []Peer{PeerNone}, []Peer{PeerPC}, []Peer{PeerHuman}
	refHum := []Peer{PeerRef, PeerHuman}
	items := []Item{
		once(item("eth", "link", 0, []Target{run("eth.link")}, all3, pc, "eth-link")),
		// Download, upload, then the mirror run: each direction's rate on its own (decision 151).
		pend(params(item("eth", "xfer", 0, []Target{session("eth", "mode", "mirror")}, all3, pc, "eth-down", "eth-up", "eth-throughput"), "port", "ip"), "lib.pending.speed_limit"),
		excl(once(item("usb", "info", 0, []Target{session("usb", "mode", "info")}, all3, pc, "usb-enum")), "usb.mode"),
		pend(excl(item("usb", "xfer", 0, []Target{session("usb", "mode", "mirror")}, all3, pc, "usb-down", "usb-up", "usb-link"), "usb.mode"), "lib.pending.speed_limit"),
		once(item("sd", "probe", 0, []Target{run("sd.probe")}, all3, none, "sd-card")),
		item("sd", "rw", 0, []Target{run("sd.integrity"), run("sd.speed")}, all3, none, "sd-integrity", "sd-speed"),
		once(item("sdram", "probe", 0, []Target{run("sdram.probe")}, all3, none, "sdram-buses")),
		onceRD(item("sdram", "sweep", 0, []Target{run("sdram.sweep")}, all3, none, "sdram-stress")),
		// The retention session, write / sit / read back (decision 151); the functional
		// test runs one whole cycle (decision 139, ptlib holdFrames).
		params(item("sdram", "hold", 0, []Target{session("sdram")}, rdFunc, none, "sdram-refresh"), "wait", "period"),
		human(once(item("reset", "cause", 0, []Target{run("reset.cause")}, rdOnly, hum, "reset-cause")), AnswerYesNo),
		human(item("led", "blink", 0, []Target{run("led.blink")}, rdOnly, hum, "indicator-drive"), AnswerYesNo),
		// A person walks it in R&D (decision 133); the fixture tests walk the
		// reference DOs the registration wires to each DI (decision 135).
		live(onRef(excl(params(item("din", "level", 0, []Target{session("din", "mode", "level")}, all3, refHum,
			"digital-in", "digital-in-2", "digital-in-3", "digital-in-4", "digital-in-5", "digital-in-6", "digital-in-7", "digital-in-8"), "ch", "period"), "din.mode"),
			session("dout", "mode", "hold"))),
		live(excl(item("din", "quad", 0, []Target{session("din", "mode", "quad")}, rdOnly, hum), "din.mode")),
		group(live(item("ain", "live", 0, []Target{session("ain")}, rdOnly, hum)), "ain.read"),
	}
	items = append(items, analog("ain", "read1", "cal1", 1)...)
	items = append(items, analog("ain", "read2", "cal2", 2)...)
	items = append(items, analog("aout", "out1", "cal1", 1)...)
	items = append(items, analog("aout", "out2", "cal2", 2)...)
	items = append(items,
		group(live(item("aout", "manual", 0, []Target{session("aout")}, rdOnly, hum)), "aout.out"),
		group(human(onRef(load(item("aout", "load", 0, []Target{session("aout")}, []Stage{RD, Aging}, []Peer{PeerRef, PeerHuman}, "ao1-out", "ao2-out")), session("ain", "ch", "1,2")), AnswerNumber), "aout.out"),
		// baud switches the whole control line for the session (decision 104, PORTTOOL-FLOW.md B.7).
		params(item("rs232", "peer", 0, []Target{session("rs232")}, all3, pc, "rs232-loopback"), "baud", "period"),
		params(item("rs485", "peer", 0, []Target{session("rs485")}, all3, pc, "rs485"), "baud", "period"),
		once(item("rs485", "pins", 0, []Target{run("rs485.pins")}, rdOnly, none, "rs485-pins")),
		onRef(excl(params(item("can", "peer", 0, []Target{session("can", "mode", "normal")}, all3, []Peer{PeerRef, PeerPC}, "can-peer", "can-pc"), "baud", "period"), "can.mode"),
			session("can", "mode", "echo")), // STG-02 row 6
		excl(params(item("can", "loop", 0, []Target{session("can", "mode", "loopback")}, rdOnly, none), "baud"), "can.mode"),
		excl(params(item("can", "ext", 0, []Target{session("can", "mode", "extloop")}, rdOnly, none, "can"), "baud"), "can.mode"),
		// The CAN analyser window: board in mode=bus, this PC's CANable on the same bus (decision 111).
		live(excl(params(item("can", "bus", 0, []Target{session("can", "mode", "bus")}, rdOnly, pc), "baud"), "can.mode")),
		once(item("knx", "bus", 0, []Target{run("knx.pullup")}, rdOnly, none)),
		excl(params(item("knx", "loop", 0, []Target{session("knx", "mode", "loopback")}, rdOnly, none, "knx"), "period"), "knx.mode"),
		// ga / src / val only act in mode=frames ($BOOT TestCase/porttool/porttool_knx.c).
		watch(excl(params(item("knx", "frames", 0, []Target{session("knx", "mode", "frames")}, rdOnly, none), "ga", "src", "val", "period"), "knx.mode"), 60),
		// The KNX analyser window: board in mode=frames, this PC's KNX USB interface on the same bus (decision 151 #8).
		live(excl(item("knx", "live", 0, []Target{session("knx", "mode", "frames", "ga", "none")}, rdOnly, pc), "knx.mode")),
		// The reference DI the registration wires to each DO, direct or through a
		// relay (decisions 133, 135, ptlib/chain.go), or a person's multimeter in R&D.
		human(onRef(excl(params(item("dout", "load", 0, []Target{session("dout", "mode", "hold", "duty", "255")}, all3, []Peer{PeerRef, PeerHuman}, "high-side-drop"), "ch", "period"), "dout.mode"),
			session("din", "mode", "level")), AnswerNumber),
		// Toggles are counted by a person; PWM is checked on a scope (STG-33).
		human(excl(params(item("dout", "blink", 0, []Target{session("dout", "mode", "blink", "duty", "255")}, rdOnly, hum, "dout-blink", "dout-blink-seen"), "period"), "dout.mode"), AnswerNumber),
		human(excl(params(item("dout", "square", 0, []Target{session("dout", "mode", "hold", "duty", "128")}, rdOnly, hum), "ch", "res", "duty", "freq"), "dout.mode"), AnswerYesNo),
		live(excl(item("dout", "manual", 0, []Target{session("dout", "mode", "hold")}, rdOnly, hum), "dout.mode")),
		// Every relay its own holds and count (decision 123); judged by the cycles
		// its load fed back (decision 130): typed by a person in R&D, counted by
		// the reference DI through DO -> relay in the fixture tests (decision 133).
		human(onRef(params(item("relay", "cycle", 0, []Target{session("relay", "mode", "cycle")}, all3, []Peer{PeerRef, PeerHuman}),
			"ch", "close", "open", "count", ParamOrder, ParamBy, ParamMinutes), session("din", "mode", "level")), AnswerNumber),
		params(item("temp", "read", 0, []Target{session("temp")}, all3, none, "temperature"), "period"),
	)
	// Pull and plug back, one per communication port, R&D only (decision 150).
	for _, r := range []struct {
		port string
		set  []string
		peer []Peer
	}{
		{"eth", []string{"mode", "echo"}, pc}, {"usb", []string{"mode", "echo"}, pc}, {"rs232", nil, pc},
		{"rs485", nil, pc}, {"can", []string{"mode", "normal"}, pc}, {"knx", []string{"mode", "loopback"}, none},
	} {
		it := once(human(item(r.port, "recover", 0, []Target{session(r.port, append(r.set, "period", RecoverPeriod)...)}, rdOnly, r.peer, r.port+"-recover"), AnswerWait))
		it.Recover = true
		items = append(items, it)
	}
	return items
}()

// RecoverPeriod is the recover items' frame period in ms: the resolution of
// the recovery time they record.
const RecoverPeriod = "500"

// RecoverPending is a recover item's note when the plan has no criteria for it:
// recorded only (decision 150).
const RecoverPending = "lib.pending.recover_limit"

// ForCaps is the catalogue as this board can run it: items whose port the
// board did not report are dropped, and so are ports with no item left.
func ForCaps(c ptproto.Caps) ([]PortInfo, []Item) {
	var items []Item
	have := map[string]bool{}
	for _, it := range Catalogue {
		if _, ok := c.Port(it.Port); ok {
			items = append(items, it)
			have[it.Port] = true
		}
	}
	var ports []PortInfo
	for _, p := range Ports {
		if have[p.Port] {
			ports = append(ports, p)
		}
	}
	return ports, items
}
