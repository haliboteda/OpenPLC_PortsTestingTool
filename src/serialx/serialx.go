// Package serialx is the one place PortTool opens a serial port, so every
// port gets the same timeout and empty-read handling.
package serialx

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"go.bug.st/serial"
)

// DefaultBaud is what every port on these boards runs at: the RS232 log and
// command line, the RS485 terminal, and the USB CDC channel alike.
const DefaultBaud = 115200

// ReadTimeout bounds one Read on an idle port; without it the driver answers
// at once with zero bytes and the reader spins.
const ReadTimeout = 100 * time.Millisecond

// Open opens a port at 8N1.
func Open(name string, baud int) (serial.Port, error) {
	port, err := serial.Open(name, &serial.Mode{BaudRate: baud})
	if err != nil {
		return nil, err
	}
	// Error ignored: a port that will not take a read timeout still reads,
	// and SteadyReader is what makes an idle port safe.
	_ = port.SetReadTimeout(ReadTimeout)
	if port == nil {
		// Not observed, but a nil port with a nil error would surface much
		// later as a nil dereference in whoever writes to it first.
		return nil, errors.New("serial.Open returned nil port")
	}
	return port, nil
}

// PortInfo is one serial port as the operating system describes it.
type PortInfo struct {
	Name    string // "COM7", "/dev/ttyUSB0"
	Product string // what the adapter calls itself, empty if it says nothing
	Driver  string // Linux kernel driver, which names the chip (ch341-uart, pl2303)
	VID     string
	PID     string
	Serial  string
	IsUSB   bool
}

// Label is what to show a person choosing a port: the adapter behind the
// number, as far as the platform describes it.
func (p PortInfo) Label() string {
	var b strings.Builder
	b.WriteString(p.Name)
	if p.Product != "" {
		fmt.Fprintf(&b, " - %s", p.Product)
	}
	if p.Driver != "" {
		fmt.Fprintf(&b, " (%s)", p.Driver)
	}
	if p.VID != "" && p.PID != "" {
		fmt.Fprintf(&b, " [%s:%s]", p.VID, p.PID)
	}
	if p.Serial != "" {
		fmt.Fprintf(&b, " #%s", p.Serial)
	}
	return b.String()
}

// List reports every serial port the OS knows about, USB adapters described as
// fully as the platform allows - see enum_detailed.go and enum_darwin.go.
//
// Ports with no USB details are still listed.
func List() ([]PortInfo, error) {
	return listPorts()
}

// FindUSB names the one port with this USB VID:PID. none is the refusal when
// there is no such port; many words it when there are several, given their
// names comma-separated.
func FindUSB(vid, pid string, none error, many func(names string) error) (string, error) {
	ports, err := List()
	if err != nil {
		return "", err
	}
	var hits []string
	for _, p := range ports {
		if p.IsUSB && strings.EqualFold(p.VID, vid) && strings.EqualFold(p.PID, pid) {
			hits = append(hits, p.Name)
		}
	}
	switch len(hits) {
	case 0:
		return "", none
	case 1:
		return hits[0], nil
	default:
		return "", many(strings.Join(hits, ", "))
	}
}

// SteadyReader hides empty reads from whoever is above it: an idle port
// returns (0, nil), and bufio.Scanner gives up after a hundred of those
// (bufio.ErrNoProgress) on a port that is merely quiet. Every scanner over a
// serial port must read through it.
type SteadyReader struct {
	R io.Reader
}

func (s SteadyReader) Read(p []byte) (int, error) {
	for {
		n, err := s.R.Read(p)
		if n > 0 || err != nil {
			return n, err
		}
		// Nothing yet and nothing wrong. The read timeout above is what keeps
		// this from becoming a busy loop.
	}
}
