package testcase

// A board under script. Unlike the fake in porttool_caps, which replays one
// recorded transcript, this one lets a test decide per command what to answer
// and which frames to push - which is what an executor has to be exercised
// against: the same command answering differently on a retry, frames arriving
// after the reply, and a port that stops sending halfway.

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

// scriptBoard answers whatever handler says. handler is called with the
// command and how many times that exact command has been seen before, so a
// test can make the first attempt fail and the second pass.
type scriptBoard struct {
	pr *io.PipeReader
	pw *io.PipeWriter

	handler func(cmd string, nth int) (reply []string, frames []string)

	mu    sync.Mutex
	buf   strings.Builder
	count map[string]int
	seen  []string
}

func newScriptBoard(t *testing.T, handler func(cmd string, nth int) ([]string, []string)) *scriptBoard {
	t.Helper()
	pr, pw := io.Pipe()
	s := &scriptBoard{pr: pr, pw: pw, handler: handler, count: map[string]int{}}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func (s *scriptBoard) Read(p []byte) (int, error) { return s.pr.Read(p) }

func (s *scriptBoard) Write(p []byte) (int, error) {
	s.buf.Write(p)
	rest := s.buf.String()
	for {
		i := strings.IndexAny(rest, "\r\n")
		if i < 0 {
			break
		}
		cmd := strings.TrimSpace(rest[:i])
		rest = strings.TrimLeft(rest[i+1:], "\r\n")
		if cmd != "" {
			s.answer(cmd)
		}
	}
	s.buf.Reset()
	s.buf.WriteString(rest)
	return len(p), nil
}

func (s *scriptBoard) answer(cmd string) {
	s.mu.Lock()
	nth := s.count[cmd]
	s.count[cmd]++
	s.seen = append(s.seen, cmd)
	h := s.handler
	s.mu.Unlock()

	reply, frames := h(cmd, nth)
	// The pipe is synchronous and the board's reader is on the other side, so
	// writing has to happen off this call.
	go func() {
		for _, l := range reply {
			s.emit(l)
		}
		for _, l := range frames {
			s.emit(l)
		}
	}()
}

func (s *scriptBoard) emit(line string) { _, _ = io.WriteString(s.pw, line+"\r\n") }

func (s *scriptBoard) Close() error { return s.pw.Close() }

func (s *scriptBoard) commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

func (s *scriptBoard) sawCommand(want string) bool {
	for _, c := range s.commands() {
		if c == want {
			return true
		}
	}
	return false
}

// standardReplies answers what every run needs regardless of what it is
// testing: the executor identifies the board, and always releases the port.
func standardReplies(cmd string) ([]string, bool) {
	switch {
	case cmd == "pt.id":
		return []string{"OK uid=003400413135511439303538 porttool=0.4.0"}, true
	case cmd == "pt.caps":
		return capsReply(), true
	case strings.HasPrefix(cmd, "pt.stop "):
		return []string{"OK " + strings.TrimPrefix(cmd, "pt.stop ") + " stopped"}, true
	}
	return nil, false
}

// capsReply is a subset of a real pt.caps: one loop=ctrl session, one
// loop=link session and one kind=run row, which is enough to exercise the caps
// cross-check for both shapes a plan step can name.
func capsReply() []string {
	body := []string{
		"OK port=din board=upper kind=session blk=D term=D02-D09 channels=8 loop=ctrl params=ch,mode,period running=0",
		"OK vals=din ch=1,2,3,4,5,6,7,8 mode=level period=200",
		"OK limits=din ch:1..8 mode:level|quad period:50..",
		"OK port=rs485 board=upper kind=session blk=C term=C10,C11 channels=1 loop=link params=baud,period running=0",
		"OK vals=rs485 baud=115200 period=3000",
		"OK terms=rs485 C10+C11",
		"OK limits=rs485 baud:9600|19200|38400|57600|115200 period:50..",
		"OK port=sdram board=bridge kind=run blk=- term=U6 channels=1 loop=none runs=sdram.probe,sdram.sweep,sdram.retention targets=sdram.capacity,sdram.crc",
	}
	header := fmt.Sprintf("OK porttool=0.9.0 ports=3 lines=%d", len(body))
	return append([]string{header}, body...)
}
