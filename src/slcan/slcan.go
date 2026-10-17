// Package slcan drives a CANable 2.0 in SLCAN mode: the PC's CAN peer when
// the reference board is not used (decision 103). Same protocol and adapter
// as $TEST/tools/can_send.py's Analyser.
package slcan

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"PortTool/src/serialx"
)

// The CANable 2.0's USB ids, as can_send.py finds it.
const (
	VID = "16D0"
	PID = "117E"
)

// DefaultBitrate is the firmware's can session default.
const DefaultBitrate = 500000

// cmdTimeout is how long a configuration command waits for its acknowledgement.
const cmdTimeout = 500 * time.Millisecond

var bitrateCodes = map[int]string{
	10000: "S0", 20000: "S1", 50000: "S2", 100000: "S3", 125000: "S4",
	250000: "S5", 500000: "S6", 750000: "S7", 1000000: "S8",
}

// BitrateCode is the SLCAN command that sets bps.
func BitrateCode(bps int) (string, bool) {
	c, ok := bitrateCodes[bps]
	return c, ok
}

// Frame is one CAN data frame.
type Frame struct {
	ID   uint32
	Ext  bool
	Data []byte
}

// Encode renders a frame as an SLCAN transmit command, without the CR.
func Encode(f Frame) (string, error) {
	if len(f.Data) > 8 {
		return "", fmt.Errorf("a CAN frame carries at most 8 bytes, not %d", len(f.Data))
	}
	var b strings.Builder
	if f.Ext {
		if f.ID > 0x1FFFFFFF {
			return "", fmt.Errorf("extended id 0x%X is over 29 bits", f.ID)
		}
		fmt.Fprintf(&b, "T%08X", f.ID)
	} else {
		if f.ID > 0x7FF {
			return "", fmt.Errorf("standard id 0x%X is over 11 bits", f.ID)
		}
		fmt.Fprintf(&b, "t%03X", f.ID)
	}
	fmt.Fprintf(&b, "%d", len(f.Data))
	for _, d := range f.Data {
		fmt.Fprintf(&b, "%02X", d)
	}
	return b.String(), nil
}

// Decode reads a received data frame line ("t1234DEADBEEF"); ok is false for
// anything else.
func Decode(line string) (Frame, bool) {
	if line == "" {
		return Frame{}, false
	}
	idLen := 3
	ext := false
	switch line[0] {
	case 't':
	case 'T':
		idLen, ext = 8, true
	default:
		return Frame{}, false
	}
	if len(line) < 1+idLen+1 {
		return Frame{}, false
	}
	id, err := strconv.ParseUint(line[1:1+idLen], 16, 32)
	if err != nil {
		return Frame{}, false
	}
	n := int(line[1+idLen] - '0')
	if n < 0 || n > 8 {
		return Frame{}, false
	}
	hex := line[2+idLen:]
	// A trailing timestamp (4 hex digits) is allowed after the data.
	if len(hex) < 2*n {
		return Frame{}, false
	}
	data := make([]byte, n)
	for i := 0; i < n; i++ {
		v, err := strconv.ParseUint(hex[2*i:2*i+2], 16, 8)
		if err != nil {
			return Frame{}, false
		}
		data[i] = byte(v)
	}
	return Frame{ID: uint32(id), Ext: ext, Data: data}, true
}

// Adapter is an open SLCAN channel.
type Adapter struct {
	rw     io.ReadWriteCloser
	wmu    sync.Mutex
	frames chan Frame
	acks   chan bool // true: CR acknowledgement, false: BEL refusal
	done   chan struct{}

	mu    sync.Mutex
	bells int
}

// ErrRefused is a command the adapter answered with BEL.
var ErrRefused = errors.New("the adapter refused the command")

// Open configures the channel at bitrate and opens it, the sequence
// can_send.py's Analyser uses: close, set bit rate, read version, open.
func Open(rw io.ReadWriteCloser, bitrate int) (*Adapter, error) {
	code, ok := BitrateCode(bitrate)
	if !ok {
		return nil, fmt.Errorf("%d bit/s has no SLCAN code", bitrate)
	}
	a := &Adapter{rw: rw, frames: make(chan Frame, 256), acks: make(chan bool, 16), done: make(chan struct{})}
	go a.readLoop()

	// A channel left open by an earlier run refuses S; closing first is what
	// the Analyser does, and its answer does not matter.
	_ = a.cmd("C")
	if err := a.cmd(code); err != nil {
		return nil, a.fail(fmt.Errorf("setting %d bit/s: %w", bitrate, err))
	}
	_ = a.cmd("V")
	if err := a.cmd("O"); err != nil {
		return nil, a.fail(fmt.Errorf("opening the channel: %w", err))
	}
	return a, nil
}

func (a *Adapter) fail(err error) error {
	_ = a.rw.Close()
	<-a.done
	return err
}

// cmd sends one configuration command and waits for CR or BEL.
func (a *Adapter) cmd(text string) error {
	for drained := false; !drained; {
		select {
		case <-a.acks:
		default:
			drained = true
		}
	}
	if err := a.write(text); err != nil {
		return err
	}
	select {
	case ok := <-a.acks:
		if !ok {
			return ErrRefused
		}
		return nil
	case <-a.done:
		return errors.New("the adapter went away")
	case <-time.After(cmdTimeout):
		return fmt.Errorf("no answer to %q within %s", text, cmdTimeout)
	}
}

func (a *Adapter) write(text string) error {
	a.wmu.Lock()
	defer a.wmu.Unlock()
	_, err := io.WriteString(a.rw, text+"\r")
	return err
}

func (a *Adapter) readLoop() {
	defer close(a.done)
	defer close(a.frames)
	buf := make([]byte, 256)
	var line []byte
	for {
		n, err := a.rw.Read(buf)
		for _, c := range buf[:n] {
			switch c {
			case '\r':
				a.line(string(line))
				line = line[:0]
			case '\a':
				a.mu.Lock()
				a.bells++
				a.mu.Unlock()
				line = line[:0]
				select {
				case a.acks <- false:
				default:
				}
			case '\n':
			default:
				line = append(line, c)
			}
		}
		if err != nil {
			return
		}
	}
}

func (a *Adapter) line(l string) {
	switch {
	case l == "":
		select {
		case a.acks <- true:
		default:
		}
	case l[0] == 'V' || l[0] == 'v':
		select {
		case a.acks <- true:
		default:
		}
	case l == "z" || l == "Z":
		// Transmit acknowledgement on some firmware; nothing to do.
	default:
		if f, ok := Decode(l); ok {
			select {
			case a.frames <- f:
			default: // a reader that is not keeping up loses frames, not the link
			}
		}
	}
}

// Bells counts BEL characters: refused commands and bus errors.
func (a *Adapter) Bells() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.bells
}

// Send transmits one frame.
func (a *Adapter) Send(f Frame) error {
	text, err := Encode(f)
	if err != nil {
		return err
	}
	return a.write(text)
}

// Frames delivers received data frames; it is closed when the adapter goes away.
func (a *Adapter) Frames() <-chan Frame { return a.frames }

// Close closes the CAN channel and the port.
func (a *Adapter) Close() error {
	_ = a.write("C")
	err := a.rw.Close()
	<-a.done
	return err
}

// EchoStats is what an echo responder did.
type EchoStats struct {
	Received int // frames seen
	Replied  int // frames answered
	Other    int // frames too short to answer
}

// Echo answers like a board in can mode=echo: every frame of four bytes or
// more goes back on the same identifier and length with its first four bytes
// incremented as one big-endian number. See
// $BOOT/TestCase/porttool/porttool_can.c can_collect(). Stop it with the
// returned function, which reports what it did.
func (a *Adapter) Echo() (stop func() EchoStats) {
	quit := make(chan struct{})
	finished := make(chan EchoStats, 1)
	go func() {
		var st EchoStats
		defer func() { finished <- st }()
		for {
			select {
			case <-quit:
				return
			case f, ok := <-a.frames:
				if !ok {
					return
				}
				st.Received++
				if len(f.Data) < 4 {
					st.Other++
					continue
				}
				d := append([]byte(nil), f.Data...)
				v := uint32(d[0])<<24 | uint32(d[1])<<16 | uint32(d[2])<<8 | uint32(d[3])
				v++
				d[0], d[1], d[2], d[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
				if a.Send(Frame{ID: f.ID, Ext: f.Ext, Data: d}) == nil {
					st.Replied++
				}
			}
		}
	}()
	var once sync.Once
	var st EchoStats
	return func() EchoStats {
		once.Do(func() {
			close(quit)
			st = <-finished
		})
		return st
	}
}

// Find names the CANable's serial port, or says why it cannot.
func Find() (string, error) {
	return serialx.FindUSB(VID, PID,
		fmt.Errorf("no CANable found (USB %s:%s) - is it plugged in and in SLCAN firmware?", VID, PID),
		func(names string) error { return fmt.Errorf("more than one CANable (%s) - unplug all but one", names) })
}
