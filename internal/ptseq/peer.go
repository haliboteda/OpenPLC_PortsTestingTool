package ptseq

// Being the far end of a loop=link session, for the duration of one step.
//
// eth, usb and rs485 are judged on whether the number the board put on the
// link came back unchanged, and the firmware refuses to answer those over the
// control channel on purpose (DECISIONS.md 9). So a plan step that names one
// of them has to say what answers it - and this is what acts on that.
//
// Before this existed, eth's session step shipped disabled and rs485 could
// only ever fail: the mechanism to answer them lived in the panel and in a
// separate `porttool answer` process, neither of which a production run can
// count on somebody starting by hand at the right moment.

import (
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"PortTool/internal/ptecho"
	"PortTool/internal/ptplan"
	"PortTool/internal/ptproto"
	"PortTool/internal/ptreport"
	"PortTool/internal/serialx"
)

// peerDialTimeout is how long a TCP peer waits for the board's listener. The
// board opens it as the session starts, so this only has to cover the moment
// between the first frame and the socket being ready.
const peerDialTimeout = 8 * time.Second

// note records one line about the peers on the attempt, so a report says what
// this machine did and not only what the board saw.
func note(att *ptreport.Attempt, key, value string) {
	if att.Extra == nil {
		att.Extra = map[string]string{}
	}
	// Several peers can report under the same kind of key; keep them all.
	for i := 0; ; i++ {
		k := key
		if i > 0 {
			k = fmt.Sprintf("%s_%d", key, i)
		}
		if _, taken := att.Extra[k]; !taken {
			att.Extra[k] = value
			return
		}
	}
}

// peerPlan tracks which peers a step still has to get up.
//
// Retrying matters because a board that has just been powered on is not ready:
// DHCP takes seconds, and the CDC pipe does not exist until the board has
// initialised its USB stack. A production station tests a board that has just
// been powered on, so one attempt is the wrong number of attempts.
type peerPlan struct {
	com   string
	usb   bool
	tcp   string
	baud  int
	until time.Time
	open  peerOpener
}

// peerOpener is how a peer's channel comes into existence. Injected so a test
// never opens whatever serial port the machine running it happens to have.
type peerOpener func(kind, addr string, baud int) (io.ReadWriteCloser, error)

// realPeerOpener is the production one.
func realPeerOpener(kind, addr string, baud int) (io.ReadWriteCloser, error) {
	if kind == "tcp" {
		return net.DialTimeout("tcp", addr, peerDialTimeout)
	}
	// One attempt, not eight: the caller retries on the next frame, and
	// blocking the collect loop for seconds would eat the step's own timeout.
	return serialx.Open(addr, baud)
}

// peerGrace is how long a step waits past its frame count for a peer that is
// not up yet.
//
// It has to be bounded, and it has to be a duration rather than a number of
// frames: a session whose frames have already stopped would never spend a
// frame-based budget, so the step would sit until its own timeout. A TIMEOUT
// says less than a failed limit does - "conn is 0" names the reading, "the
// step ran out of time" does not.
//
// Ten seconds is comfortably more than the 5-6 s a DHCP lease takes on this
// bench, and short enough that a plan naming a cable this machine has not got
// still finishes on a verdict.
const peerGrace = 10 * time.Second

func newPeerPlan(step ptplan.Step, com string, comBaud int, open peerOpener, now time.Time) *peerPlan {
	if step.Peer == nil {
		return &peerPlan{}
	}
	baud := step.Peer.Baud
	if baud == 0 {
		baud = comBaud
	}
	if baud == 0 {
		baud = serialx.DefaultBaud
	}
	return &peerPlan{
		com:   com,
		usb:   step.Peer.USB,
		tcp:   step.Peer.TCP,
		baud:  baud,
		until: now.Add(peerGrace),
		open:  open,
	}
}

// tryOpen makes one attempt at everything still outstanding and returns
// whatever came up. Each peer is cleared from the plan once it is up, so a
// later frame only retries what is still missing.
//
// A failure is not recorded on every frame: only the last reason is kept, or
// the report would carry one "no address yet" line per frame.
func (pp *peerPlan) tryOpen(f ptproto.Frame, att *ptreport.Attempt) []*ptecho.Peer {
	var out []*ptecho.Peer

	if pp.com != "" {
		if p, err := pp.openSerial(pp.com, att); err == nil {
			out = append(out, p)
			pp.com = ""
		}
	}
	if pp.usb {
		// ⚠️ Looked up here, not written in the plan: the CDC port only exists
		// once the session has started the board's USB stack.
		if name, err := ptecho.FindCDC(); err != nil {
			pp.lastReason(att, "peer_usb", err.Error())
		} else if p, err := pp.openSerial(name, att); err == nil {
			out = append(out, p)
			pp.usb = false
		}
	}
	if pp.tcp != "" {
		if p := pp.openTCP(f, att); p != nil {
			out = append(out, p)
			pp.tcp = ""
		}
	}
	return out
}

// outstanding reports whether any peer this step asked for is still not up.
//
// The collect loop uses it to keep waiting past the frame count: a board that
// has just been powered on takes seconds to get a DHCP address, and the four
// frames a plan asks for go by in two. Stopping at the frame count meant the
// peer never got a chance to exist, and the step failed on conn=0 - a verdict
// about this program's patience, not about the link.
func (pp *peerPlan) outstanding(now time.Time) bool {
	if pp.com == "" && !pp.usb && pp.tcp == "" {
		return false
	}
	return now.Before(pp.until)
}

// lastReason keeps one line per kind of failure rather than one per frame.
func (pp *peerPlan) lastReason(att *ptreport.Attempt, key, value string) {
	if att.Extra == nil {
		att.Extra = map[string]string{}
	}
	att.Extra[key] = value
}

func (pp *peerPlan) openSerial(name string, att *ptreport.Attempt) (*ptecho.Peer, error) {
	// One attempt here, not eight: the caller retries on the next frame, and
	// blocking the collect loop for seconds would eat the step's own timeout.
	port, err := pp.open("serial", name, pp.baud)
	if err != nil {
		pp.lastReason(att, "peer_error_"+name, err.Error())
		return nil, err
	}
	note(att, "peer", fmt.Sprintf("%s answering at %d baud", name, pp.baud))
	return ptecho.New(name, port, nil), nil
}

func (pp *peerPlan) openTCP(f ptproto.Frame, att *ptreport.Attempt) *ptecho.Peer {
	addr, err := peerTCPAddr(pp.tcp, f)
	if err != nil {
		pp.lastReason(att, "peer_error_tcp", err.Error())
		return nil
	}
	conn, err := pp.open("tcp", addr, pp.baud)
	if err != nil {
		pp.lastReason(att, "peer_error_tcp", fmt.Sprintf("%s: %v", addr, err))
		return nil
	}
	// "connected", not "answering". A connect that returns is not yet a peer:
	// a VPN or proxy holding the default route in TUN mode accepts a connect
	// to any address at all, including one nothing is listening on. Whether
	// anything answered is settled by recordPeerStats, from what crossed.
	note(att, "peer", "tcp "+addr+" connected")
	return ptecho.New("tcp "+addr, conn, nil)
}

// peerTCPAddr works out where to dial. "auto" reads the session's own ip= and
// port= fields, which is the only thing that can work: the address came from
// DHCP, so no plan file can know it in advance.
func peerTCPAddr(spec string, f ptproto.Frame) (string, error) {
	if !strings.EqualFold(spec, "auto") {
		return spec, nil
	}
	ip, ok := ptproto.Get(f.Fields, "ip")
	if !ok || ip == "" || ip == "0.0.0.0" || ip == "dhcp" {
		return "", fmt.Errorf("the session has no address yet (ip=%q)", ip)
	}
	port, ok := ptproto.Get(f.Fields, "port")
	if !ok || port == "" {
		return "", fmt.Errorf("the session reports no TCP port")
	}
	return net.JoinHostPort(ip, port), nil
}

// recordPeerStats writes what actually crossed each peer onto the attempt.
//
// Worth recording next to the board's own counters: if the board reports
// misses and the peer says it echoed nothing, the fault is on this machine,
// and if both say lines crossed then the link really carried them. Not having
// this is what made rs485 look like a firmware problem until the peer's own
// count showed it had answered every line.
func recordPeerStats(peers []*ptecho.Peer, att *ptreport.Attempt) {
	for _, p := range peers {
		s := p.Stats()
		v := fmt.Sprintf("received %d, echoed %d", s.Received, s.Echoed)
		if s.Err != "" {
			v += ", error " + s.Err
		}
		// A peer that opened and then heard nothing is the shape a TUN-mode
		// VPN or proxy makes: the connect succeeds because the tunnel accepts
		// everything, and the board never sees a connection. Said here because
		// the board's own counter can only report conn=0, which reads as a
		// board fault and cost a day chasing one on 2026-09-10.
		if s.Received == 0 && strings.HasPrefix(s.Name, "tcp ") {
			v += " - nothing came back, so this opened onto something that is " +
				"not the board: check that no VPN or proxy holds the default " +
				"route, and that this machine is on the board's subnet"
		}
		note(att, "peer_"+s.Name, v)
	}
}
