package ptpanel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"PortTool/internal/simboard"
)

// Which COM port is which, remembered between runs.
//
// The panel cannot work this out on its own and neither can anybody looking at
// the labels: on 2026-09-08 a CH340 RS485 adapter and a CANable2 were
// indistinguishable from their VID/PID. So a person tells it once, and it does
// not ask again.
//
// Deliberately its own small file rather than a field in local_config.json:
// that file belongs to IAPTool and is edited by hand, and a panel writing to
// it on every connect would fight whoever is editing it.
type portMap struct {
	Control string             `json:"control"`
	Peers   map[string]peerRef `json:"peers"`
}

type peerRef struct {
	COM  string `json:"com"`
	Baud int    `json:"baud,omitempty"`
}

// PortMapFile overrides where the mapping is kept. Set by tests; empty means
// beside the executable.
var PortMapFile string

var portMapMu sync.Mutex

func portMapPath() string {
	if PortMapFile != "" {
		return PortMapFile
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "porttool_ports.json")
	}
	return "porttool_ports.json"
}

// loadPortMap never fails in a way the caller has to handle: a missing or
// unreadable file just means nothing is remembered yet, which is exactly the
// first-run state. Refusing to start the panel over it would be worse than
// asking the question again.
func loadPortMap() portMap {
	portMapMu.Lock()
	defer portMapMu.Unlock()

	var m portMap
	b, err := os.ReadFile(portMapPath())
	if err != nil {
		return portMap{Peers: map[string]peerRef{}}
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return portMap{Peers: map[string]peerRef{}}
	}
	if m.Peers == nil {
		m.Peers = map[string]peerRef{}
	}
	return m
}

func savePortMap(m portMap) {
	portMapMu.Lock()
	defer portMapMu.Unlock()

	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	// A write failure is ignored on purpose: it costs the convenience of being
	// remembered and nothing else, and a station whose panel refused to run
	// because a preferences file is read-only would be a worse trade.
	_ = os.WriteFile(portMapPath(), append(b, '\n'), 0o644)
}

// rememberControl records the control port after a connect succeeded - after,
// so a port that could not be opened is not the one offered next time.
//
// The simulated board is never remembered. It is not an adapter somebody wired
// up, and writing it here overwrites the answer to a question only a person at
// the bench can answer - which is exactly what happened on 2026-09-08: a run of
// the browser test against sim replaced a bench's rs232/rs485/can mapping with
// {"control":"sim"}, and the next person at that bench had to work out which
// COM was which all over again.
func rememberControl(com string) {
	if simboard.IsSim(com) {
		return
	}
	m := loadPortMap()
	if m.Control == com {
		return
	}
	m.Control = com
	savePortMap(m)
}

// rememberPeer records a link peer once it is bound, keyed by the board port it
// answers for. Keyed that way rather than by COM name because the question a
// person is answering is "which adapter is on RS485", not "what is COM16 for".
func rememberPeer(boardPort, com string, baud int) {
	m := loadPortMap()
	if cur, ok := m.Peers[boardPort]; ok && cur.COM == com && cur.Baud == baud {
		return
	}
	m.Peers[boardPort] = peerRef{COM: com, Baud: baud}
	savePortMap(m)
}

// RememberedPeer is the adapter recorded for a board port, for a plan step
// whose peer is "serial" - in the panel and in `porttool run` alike.
func RememberedPeer(boardPort string) (com string, baud int, ok bool) {
	p, ok := loadPortMap().Peers[boardPort]
	if !ok || p.COM == "" {
		return "", 0, false
	}
	return p.COM, p.Baud, true
}

// forgetPeer drops a binding when it is unbound, so the panel does not keep
// offering an adapter somebody deliberately took away.
func forgetPeer(boardPort string) {
	m := loadPortMap()
	if _, ok := m.Peers[boardPort]; !ok {
		return
	}
	delete(m.Peers, boardPort)
	savePortMap(m)
}

// savedJSON is what the page reads to prefill its choices.
func savedJSON() map[string]any {
	m := loadPortMap()
	peers := map[string]any{}
	for k, v := range m.Peers {
		peers[k] = map[string]any{"com": v.COM, "baud": v.Baud}
	}
	return map[string]any{"control": m.Control, "peers": peers}
}
