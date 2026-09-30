// Package ptecho is the far end of a loop=link session.
//
// Three of the board's ports - eth, usb and rs485 - are judged by whether a
// number the board put on the link comes back unchanged. That makes a peer
// mandatory: without one the counter can never close, and the frames look
// exactly like broken wiring. The production guide has always said the station
// PC has to be that peer (PRODUCTION-TEST-GAP.md, "Golden endpoint").
//
// The three are the same problem wearing different cables, so this is one
// implementation, not three: the board sends a line, the peer sends that line
// straight back.
//
// ⚠️ Verbatim, never incremented or reformatted. The board compares what came
// back against what it sent, so touching the payload here would read as a dead
// link (DECISIONS.md 18).
//
// ⚠️ internal/ptpanel/link.go does the same job for the panel, against a
// serial adapter it opened itself. The two are deliberately NOT merged yet -
// the panel's version is wired into its own logging and lifecycle, and
// rewriting that is a change to working code nobody asked for. If they ever
// disagree about what "echo" means, a port would pass on the panel and fail on
// the line, so merging them is worth doing on purpose rather than by accident.
package ptecho

import (
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// Peer is one channel answering the board.
type Peer struct {
	// Name is what shows up in the log, e.g. "eth" or "rs485 COM16".
	Name string

	rw io.ReadWriteCloser

	rx      atomic.Uint64
	tx      atomic.Uint64
	lastErr atomic.Value // string

	once sync.Once
	done chan struct{}
}

// Stats is what a caller reports or judges.
type Stats struct {
	Name     string
	Received uint64
	Echoed   uint64
	Err      string
}

// New starts answering on rw. It returns immediately; the answering runs until
// rw closes or Stop is called.
//
// log may be nil. When given it is called once per line, which is how a person
// sees the bytes actually crossing the terminal rather than inferring them from
// the board's counters.
func New(name string, rw io.ReadWriteCloser, log func(string)) *Peer {
	p := &Peer{Name: name, rw: rw, done: make(chan struct{})}
	p.lastErr.Store("")

	go func() {
		defer close(p.done)
		p.pump(rw, log)
	}()
	return p
}

// pump reads lines and sends each one back.
//
// ⚠️ Deliberately not bufio.Scanner. A serial port opened without a read
// timeout returns (0, nil) when the line is idle, and Scanner treats a run of
// those as "multiple Read calls return no data or error" and gives up. Idle is
// the NORMAL state of a link whose session has not started yet, so a peer
// built on Scanner dies before the board ever speaks - which reads as a dead
// link on good wiring. Observed on 2026-09-09 against the RS485 terminal.
//
// ⚠️ Lines, which is all a plan asks of this end: station6-poweron.json runs
// usb and eth with mode=echo only, because echo is the one mode whose verdict
// the board can reach by itself. A sink or source session puts a stream with
// no newline in it on the wire, and this loop would accumulate it to the 64 KiB
// cap and drop the rest - the panel's far end (internal/ptpanel/link.go) reads
// by blocks for exactly that reason. Teach this one modes when a plan needs
// them, not before.
func (p *Peer) pump(rw io.Reader, log func(string)) {
	w, canWrite := rw.(io.Writer)
	if !canWrite {
		p.lastErr.Store("channel cannot be written to")
		return
	}

	var line []byte
	tmp := make([]byte, 512)

	for {
		n, err := rw.Read(tmp)
		for i := 0; i < n; i++ {
			c := tmp[i]
			if c != '\n' && c != '\r' {
				// A single frame cannot be this long; anything longer is noise
				// on an unterminated line, and must not grow without bound.
				if len(line) < 64*1024 {
					line = append(line, c)
				}
				continue
			}
			if len(line) == 0 {
				continue
			}
			text := string(line)
			line = line[:0]

			p.rx.Add(1)
			if log != nil {
				log(p.Name + " <- " + text)
			}
			// Verbatim. The board compares what came back against what it
			// sent, so changing it here would look exactly like a dead link.
			if _, werr := io.WriteString(w, text+"\n"); werr != nil {
				p.lastErr.Store(werr.Error())
				if log != nil {
					log(p.Name + " ！送不回去：" + werr.Error())
				}
				return
			}
			p.tx.Add(1)
			if log != nil {
				log(p.Name + " -> " + text)
			}
		}

		if err != nil {
			// Close() from Stop lands here, which is the ordinary way this ends.
			if err != io.EOF {
				p.lastErr.Store(err.Error())
				if log != nil {
					log(p.Name + " ！读不下去：" + err.Error())
				}
			}
			return
		}
		if n == 0 {
			// Silence, not end of stream. See the warning above.
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// Stats reports what has crossed so far.
func (p *Peer) Stats() Stats {
	s := Stats{Name: p.Name, Received: p.rx.Load(), Echoed: p.tx.Load()}
	if e, ok := p.lastErr.Load().(string); ok {
		s.Err = e
	}
	return s
}

// Stop closes the channel and waits for the answering to finish.
func (p *Peer) Stop() {
	p.once.Do(func() {
		_ = p.rw.Close()
		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
			// A serial read that will not unblock must not hang a production
			// run. The port is closed either way.
		}
	})
}
