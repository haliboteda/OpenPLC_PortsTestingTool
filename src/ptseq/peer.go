package ptseq

// The far end of a loop=link session (eth, usb, rs485) for one step: the
// firmware will not answer those over the control channel (DECISIONS.md 9).

import (
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"PortTool/src/ptecho"
	"PortTool/src/ptplan"
	"PortTool/src/ptproto"
	"PortTool/src/ptreport"
	"PortTool/src/serialx"
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

// peerPlan tracks which peers a step still has to get up; retried because a
// freshly powered board has no DHCP address or CDC pipe yet.
type peerPlan struct {
	com     string
	usb     bool
	tcp     string
	baud    int
	until   time.Time
	open    peerOpener
	findCDC func() (string, error)

	// What each opened peer was asked for, so a dead one can be asked for again.
	asked map[*ptecho.Peer]func()
}

// peerOpener is how a peer's channel comes into existence. Injected so a test
// never opens whatever serial port the machine running it happens to have.
type peerOpener func(kind, addr string, baud int) (io.ReadWriteCloser, error)

// realPeerOpener is the production one.
func realPeerOpener(kind, addr string, baud int) (io.ReadWriteCloser, error) {
	if kind == "tcp" {
		return net.DialTimeout("tcp", addr, peerDialTimeout)
	}
	return serialx.Open(addr, baud)
}

// peerGrace is how long a step waits past its frame count for a peer that is
// not up yet. A duration, not frames, so a session whose frames stopped still
// ends on a verdict rather than a TIMEOUT; 10 s covers a 5-6 s DHCP lease.
const peerGrace = 10 * time.Second

func newPeerPlan(step ptplan.Step, com string, comBaud int, open peerOpener, findCDC func() (string, error), now time.Time) *peerPlan {
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
		com:     com,
		usb:     step.Peer.USB,
		tcp:     step.Peer.TCP,
		baud:    baud,
		until:   now.Add(peerGrace),
		open:    open,
		findCDC: findCDC,
		asked:   map[*ptecho.Peer]func(){},
	}
}

// rearmDead stops the peers whose channel closed (a USB CDC port that left
// with its cable, a TCP connection the board dropped) and asks for them again;
// it returns the ones still up.
func (pp *peerPlan) rearmDead(peers []*ptecho.Peer, att *ptreport.Attempt) []*ptecho.Peer {
	var up []*ptecho.Peer
	for _, p := range peers {
		if !p.Done() {
			up = append(up, p)
			continue
		}
		p.Stop()
		note(att, "peer", p.Name+" closed, opening it again")
		if again := pp.asked[p]; again != nil {
			again()
			delete(pp.asked, p)
		}
	}
	return up
}

// tryOpen makes one attempt at everything still outstanding and returns
// whatever came up; a peer that is up is cleared from the plan. Only the last
// failure reason is kept, not one per frame.
func (pp *peerPlan) tryOpen(f ptproto.Frame, att *ptreport.Attempt) []*ptecho.Peer {
	var out []*ptecho.Peer

	if pp.com != "" {
		if p, err := pp.openSerial(pp.com, att); err == nil {
			out = append(out, p)
			com := pp.com
			pp.remember(p, func() { pp.com = com })
			pp.com = ""
		}
	}
	if pp.usb {
		// ⚠️ Looked up here, not written in the plan: the CDC port only exists
		// once the session has started the board's USB stack.
		if name, err := pp.findCDC(); err != nil {
			att.SetExtra("peer_usb", err.Error())
		} else if p, err := pp.openSerial(name, att); err == nil {
			out = append(out, p)
			pp.remember(p, func() { pp.usb = true })
			pp.usb = false
		}
	}
	if pp.tcp != "" {
		if p := pp.openTCP(f, att); p != nil {
			out = append(out, p)
			tcp := pp.tcp
			pp.remember(p, func() { pp.tcp = tcp })
			pp.tcp = ""
		}
	}
	return out
}

func (pp *peerPlan) remember(p *ptecho.Peer, again func()) {
	if pp.asked != nil {
		pp.asked[p] = again
	}
}

// outstanding reports whether any peer this step asked for is still not up;
// the collect loop keeps waiting past the frame count while it is.
func (pp *peerPlan) outstanding(now time.Time) bool {
	if pp.com == "" && !pp.usb && pp.tcp == "" {
		return false
	}
	return now.Before(pp.until)
}

func (pp *peerPlan) openSerial(name string, att *ptreport.Attempt) (*ptecho.Peer, error) {
	// One attempt here, not eight: the caller retries on the next frame, and
	// blocking the collect loop for seconds would eat the step's own timeout.
	port, err := pp.open("serial", name, pp.baud)
	if err != nil {
		att.SetExtra("peer_error_"+name, err.Error())
		return nil, err
	}
	note(att, "peer", fmt.Sprintf("%s answering at %d baud", name, pp.baud))
	return ptecho.New(name, port, nil), nil
}

func (pp *peerPlan) openTCP(f ptproto.Frame, att *ptreport.Attempt) *ptecho.Peer {
	addr, err := peerTCPAddr(pp.tcp, f)
	if err != nil {
		att.SetExtra("peer_error_tcp", err.Error())
		return nil
	}
	conn, err := pp.open("tcp", addr, pp.baud)
	if err != nil {
		att.SetExtra("peer_error_tcp", fmt.Sprintf("%s: %v", addr, err))
		return nil
	}
	// "connected", not "answering": a TUN-mode VPN accepts a connect to any
	// address; recordPeerStats settles whether anything answered.
	note(att, "peer", "tcp "+addr+" connected")
	return ptecho.New("tcp "+addr, conn, nil)
}

// peerTCPAddr works out where to dial. "auto" reads the session's own ip= and
// port= fields: the address comes from DHCP.
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

// recordPeerStats writes what actually crossed each peer onto the attempt, so
// a miss can be told apart as this machine's or the link's. silent says the
// board sends nothing on this link (mode=sink), so nothing coming back is expected.
func recordPeerStats(peers []*ptecho.Peer, att *ptreport.Attempt, silent bool) {
	for _, p := range peers {
		s := p.Stats()
		v := fmt.Sprintf("received %d bytes, echoed %d bytes", s.RxBytes, s.TxBytes)
		if s.Err != "" {
			v += ", error " + s.Err
		}
		// Opened but heard nothing: a TUN-mode VPN or proxy took the connect;
		// the board's own conn=0 would read as a board fault.
		if s.RxBytes == 0 && !silent && strings.HasPrefix(s.Name, "tcp ") {
			v += " - nothing came back, so this opened onto something that is " +
				"not the board: check that no VPN or proxy holds the default " +
				"route, and that this machine is on the board's subnet"
		}
		note(att, "peer_"+s.Name, v)
	}
}
