// Package portmap remembers which COM port is which between runs.
//
// Nothing can work this out on its own, and neither can anybody looking at the
// labels: on 2026-09-08 a CH340 RS485 adapter and a CANable2 were
// indistinguishable from their VID/PID. So a person tells the panel once, and
// both the panel and `porttool run` read the answer back.
//
// Its own small file beside the executable, porttool_ports.json.
package portmap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"PortTool/internal/simboard"
)

type portMap struct {
	Control string             `json:"control"`
	Peers   map[string]peerRef `json:"peers"`
}

type peerRef struct {
	COM  string `json:"com"`
	Baud int    `json:"baud,omitempty"`
}

// File overrides where the mapping is kept; empty means beside the executable.
// Tests set it, so a test run never rewrites a bench's real mapping.
var File string

var mu sync.Mutex

func path() string {
	if File != "" {
		return File
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "porttool_ports.json")
	}
	return "porttool_ports.json"
}

// load never fails in a way the caller has to handle: a missing or unreadable
// file means nothing is remembered yet, which is the first-run state.
func load() portMap {
	mu.Lock()
	defer mu.Unlock()
	var m portMap
	b, err := os.ReadFile(path())
	if err != nil || json.Unmarshal(b, &m) != nil {
		return portMap{Peers: map[string]peerRef{}}
	}
	if m.Peers == nil {
		m.Peers = map[string]peerRef{}
	}
	return m
}

func save(m portMap) {
	mu.Lock()
	defer mu.Unlock()
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	// A write failure costs the convenience of being remembered and nothing
	// else; refusing to run over a read-only preferences file would be worse.
	_ = os.WriteFile(path(), append(b, '\n'), 0o644)
}

// SetControl records the control port after a connect succeeded.
//
// The simulated board is never remembered: on 2026-09-08 a browser-test run
// against sim replaced a bench's whole mapping with {"control":"sim"}.
func SetControl(com string) {
	if simboard.IsSim(com) {
		return
	}
	m := load()
	if m.Control == com {
		return
	}
	m.Control = com
	save(m)
}

// SetPeer records a link peer once it is bound, keyed by the board port it
// answers for: the question a person answers is "which adapter is on RS485".
func SetPeer(boardPort, com string, baud int) {
	m := load()
	if cur, ok := m.Peers[boardPort]; ok && cur.COM == com && cur.Baud == baud {
		return
	}
	m.Peers[boardPort] = peerRef{COM: com, Baud: baud}
	save(m)
}

// Peer is the adapter recorded for a board port, for a plan step whose peer is
// "serial" - in the panel and in `porttool run` alike.
func Peer(boardPort string) (com string, baud int, ok bool) {
	p, ok := load().Peers[boardPort]
	if !ok || p.COM == "" {
		return "", 0, false
	}
	return p.COM, p.Baud, true
}

// ForgetPeer drops a binding when it is unbound, so an adapter somebody
// deliberately took away is not offered again.
func ForgetPeer(boardPort string) {
	m := load()
	if _, ok := m.Peers[boardPort]; !ok {
		return
	}
	delete(m.Peers, boardPort)
	save(m)
}

// Saved is what the page reads to prefill its choices.
func Saved() map[string]any {
	m := load()
	peers := map[string]any{}
	for k, v := range m.Peers {
		peers[k] = map[string]any{"com": v.COM, "baud": v.Baud}
	}
	return map[string]any{"control": m.Control, "peers": peers}
}
