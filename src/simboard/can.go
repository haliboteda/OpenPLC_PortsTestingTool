package simboard

// The CAN analyser window's far end on the simulated board (decision 111,
// PORTTOOL-FLOW.md C.3.7): there is no CANable, so the wire is played here.
// While it is open the simulator's CAN stops answering itself (sim.canloop 0),
// every frame the board reports sending (!can.tx ok=1) arrives here as if the
// adapter had received it, and every frame sent from here goes into the board
// as sim.canrx. The bit rate is not modelled.

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"PortTool/src/ptproto"
	"PortTool/src/slcan"
)

// CAN is the simulated analyser end; it has slcan.Adapter's methods.
type CAN struct {
	w      *wired
	mu     sync.Mutex
	frames chan slcan.Frame
	closed bool
}

// OpenCAN opens the analyser end on a simulated board opened by OpenPort.
func OpenCAN(port io.ReadWriteCloser, bitrate int) (*CAN, error) {
	w, ok := port.(*wired)
	if !ok {
		return nil, errors.New("not the simulated board")
	}
	if _, ok := slcan.BitrateCode(bitrate); !ok {
		return nil, fmt.Errorf("%d bit/s has no SLCAN code", bitrate)
	}
	c := &CAN{w: w, frames: make(chan slcan.Frame, 64)}
	if old := w.can.Swap(c); old != nil {
		old.shut()
	}
	w.inject("sim.canloop 0")
	return c, nil
}

// Send puts a frame on the simulated board's CAN.
func (c *CAN) Send(f slcan.Frame) error {
	if f.Ext || f.ID > 0x7FF || len(f.Data) > 8 {
		return errors.New("the simulated board takes standard frames of up to 8 bytes")
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return errors.New("the analyser end is closed")
	}
	c.w.inject(fmt.Sprintf("sim.canrx %X %X", f.ID, f.Data))
	return nil
}

// Frames delivers what the board sent.
func (c *CAN) Frames() <-chan slcan.Frame { return c.frames }

// Bells is always 0: nothing here can refuse or see a bus error.
func (c *CAN) Bells() int { return 0 }

// Close gives the simulator its self-loopback back.
func (c *CAN) Close() error {
	if c.w.can.CompareAndSwap(c, nil) {
		c.w.inject("sim.canloop 1")
	}
	c.shut()
	return nil
}

func (c *CAN) shut() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		close(c.frames)
	}
}

// sent passes a !can.tx the board reported on; a full buffer drops it, as a
// busy adapter would.
func (c *CAN) sent(f ptproto.Frame) {
	if v, _ := ptproto.Get(f.Fields, "ok"); v != "1" {
		return
	}
	idv, _ := ptproto.Get(f.Fields, "id")
	id, err := strconv.ParseUint(strings.TrimPrefix(idv, "0x"), 16, 32)
	if err != nil {
		return
	}
	var data []byte
	if d, _ := ptproto.Get(f.Fields, "data"); d != "-" {
		if data, err = hex.DecodeString(d); err != nil {
			return
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	select {
	case c.frames <- slcan.Frame{ID: uint32(id), Data: data}:
	default:
	}
}
