package knxusb

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// featureWait is how long a Bus Access Server feature query waits.
const featureWait = time.Second

// Interface is an open KNX USB interface speaking one EMI.
type Interface struct {
	dev  HID
	name string
	emi  EMI
	bus  *bool // bus connection status as the interface last said; nil unknown

	wmu    sync.Mutex
	mu     sync.Mutex
	feats  chan Packet
	events chan Event
	bad    int // reports or frames that could not be read
	closed chan struct{}
	once   sync.Once
}

// Open negotiates on dev: asks which EMIs it supports, picks cEMI, EMI2 or
// EMI1 in that order, makes it the active one, switches the interface to
// link layer mode, then asks for the bus connection status. A device that
// does not answer is not a KNX USB interface.
func Open(dev HID, name string) (*Interface, error) {
	k := &Interface{dev: dev, name: name, feats: make(chan Packet, 4), events: make(chan Event, 256), closed: make(chan struct{})}
	go k.read()
	v, err := k.feature(FeatSupportedEMI)
	if err != nil {
		_ = k.Close()
		return nil, fmt.Errorf("no answer to the supported-EMI query, so not a KNX USB interface: %w", err)
	}
	e, _, ok := SupportedEMI(v)
	if !ok {
		_ = k.Close()
		return nil, fmt.Errorf("the interface reports no EMI it supports (% X)", v)
	}
	k.emi = e
	if err := k.write(FeatureSet(FeatActiveEMI, byte(e))); err != nil {
		_ = k.Close()
		return nil, err
	}
	for _, f := range LinkLayerMode(e) {
		if err := k.write(Packet{Proto: ProtoTunnel, ID: byte(e), Body: f}); err != nil {
			_ = k.Close()
			return nil, err
		}
	}
	if v, err := k.feature(FeatBusStatus); err == nil && len(v) > 0 {
		up := v[0]&0x01 != 0
		k.bus = &up
	}
	return k, nil
}

// feature asks for one feature and returns its value.
func (k *Interface) feature(id byte) ([]byte, error) {
	if err := k.write(FeatureGet(id)); err != nil {
		return nil, err
	}
	deadline := time.After(featureWait)
	for {
		select {
		case p := <-k.feats:
			if p.ID == FeatResponse && len(p.Body) >= 1 && p.Body[0] == id {
				return p.Body[1:], nil
			}
		case <-deadline:
			return nil, errors.New("timed out")
		case <-k.closed:
			return nil, errors.New("the interface went away")
		}
	}
}

func (k *Interface) write(p Packet) error {
	k.wmu.Lock()
	defer k.wmu.Unlock()
	for _, r := range EncodeReports(p) {
		if err := k.dev.WriteReport(r); err != nil {
			return err
		}
	}
	return nil
}

func (k *Interface) read() {
	defer close(k.events)
	var asm Assembler
	for {
		r, err := k.dev.ReadReport()
		if err != nil {
			_ = k.Close()
			return
		}
		p, done, err := asm.Add(r)
		if err != nil {
			k.count()
			continue
		}
		if !done {
			continue
		}
		switch p.Proto {
		case ProtoFeature:
			if p.ID == FeatInfo && len(p.Body) >= 2 && p.Body[0] == FeatBusStatus {
				up := p.Body[1]&0x01 != 0
				k.mu.Lock()
				k.bus = &up
				k.mu.Unlock()
				continue
			}
			select {
			case k.feats <- p:
			default:
			}
		case ProtoTunnel:
			ev, ok, err := DecodeLData(EMI(p.ID), p.Body)
			if err != nil {
				k.count()
			}
			if !ok {
				continue
			}
			select {
			case k.events <- ev:
			default:
				k.count()
			}
		}
	}
}

func (k *Interface) count() {
	k.mu.Lock()
	k.bad++
	k.mu.Unlock()
}

// Send puts one telegram on the bus as L_Data.req; the interface answers
// with an L_Data.con on Events.
func (k *Interface) Send(t Telegram) error {
	b, err := EncodeReq(k.emi, t)
	if err != nil {
		return err
	}
	return k.write(Packet{Proto: ProtoTunnel, ID: byte(k.emi), Body: b})
}

// Events carries L_Data.ind and L_Data.con; closed when the device goes away.
func (k *Interface) Events() <-chan Event { return k.events }

// Name is the label it was opened under.
func (k *Interface) Name() string { return k.name }

// EMI is the frame format in use.
func (k *Interface) EMI() string { return k.emi.String() }

// BusUp is the bus connection status, nil when the interface never said.
func (k *Interface) BusUp() *bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.bus
}

// Unreadable counts reports and frames that could not be read or were dropped.
func (k *Interface) Unreadable() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.bad
}

// Close lets go of the device.
func (k *Interface) Close() error {
	var err error
	k.once.Do(func() {
		close(k.closed)
		err = k.dev.Close()
	})
	return err
}
