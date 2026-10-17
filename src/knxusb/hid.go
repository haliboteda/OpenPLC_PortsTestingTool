package knxusb

import (
	"errors"
	"fmt"
)

// ErrUnsupported: this build cannot reach HID devices on this system; only
// Windows, Linux and macOS have a HID layer here.
var ErrUnsupported = errors.New("KNX USB interfaces are not supported on this system yet")

// DeviceInfo is one HID device that looks like a KNX USB interface.
type DeviceInfo struct {
	Path string `json:"path"`
	Name string `json:"name"`
	VID  uint16 `json:"vid"`
	PID  uint16 `json:"pid"`
}

// Label is how a person tells interfaces apart.
func (d DeviceInfo) Label() string {
	n := d.Name
	if n == "" {
		n = "HID"
	}
	return fmt.Sprintf("%s (%04X:%04X)", n, d.VID, d.PID)
}

// HID is an open HID device: whole reports of ReportLen bytes, report ID first.
type HID interface {
	ReadReport() ([]byte, error) // blocks until a report comes or Close
	WriteReport([]byte) error
	Close() error
}

// List names the HID devices with a vendor-defined usage page and 64-byte
// input and output reports, the shape the KNX USB interface specification
// gives; whether one really is a KNX interface is only known once Open asks.
func List() ([]DeviceInfo, error) { return listHID() }

// OpenHID opens one device from List.
func OpenHID(path string) (HID, error) { return openHID(path) }

// descShape is what a HID report descriptor says about the device.
type descShape struct {
	page   uint16      // first usage page, the top-level collection's
	in     map[int]int // report ID -> bytes, report ID octet included
	out    map[int]int
	parsed bool
}

// parseDescriptor walks a HID report descriptor's short items.
func parseDescriptor(d []byte) descShape {
	s := descShape{in: map[int]int{}, out: map[int]int{}}
	var page, size, count, id uint32
	pageSet := false
	type glob struct{ page, size, count, id uint32 }
	var stack []glob
	inBits, outBits := map[int]int{}, map[int]int{}
	for i := 0; i < len(d); {
		b := d[i]
		if b == 0xFE { // long item
			if i+1 >= len(d) {
				return s
			}
			i += 3 + int(d[i+1])
			continue
		}
		n := int(b & 0x03)
		if n == 3 {
			n = 4
		}
		if i+1+n > len(d) {
			return s
		}
		var v uint32
		for k := 0; k < n; k++ {
			v |= uint32(d[i+1+k]) << (8 * k)
		}
		typ, tag := (b>>2)&0x03, b>>4
		switch typ {
		case 0: // main
			switch tag {
			case 0x8:
				inBits[int(id)] += int(size * count)
			case 0x9:
				outBits[int(id)] += int(size * count)
			}
		case 1: // global
			switch tag {
			case 0x0:
				page = v
				if !pageSet {
					s.page, pageSet = uint16(v), true
				}
			case 0x7:
				size = v
			case 0x8:
				id = v
			case 0x9:
				count = v
			case 0xA:
				stack = append(stack, glob{page, size, count, id})
			case 0xB:
				if len(stack) > 0 {
					g := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					page, size, count, id = g.page, g.size, g.count, g.id
				}
			}
		}
		i += 1 + n
	}
	for k, bits := range inBits {
		s.in[k] = bits/8 + boolInt(k != 0)
	}
	for k, bits := range outBits {
		s.out[k] = bits/8 + boolInt(k != 0)
	}
	s.parsed = true
	return s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// knxShaped: vendor-defined usage page, a 64-byte input and output report.
func knxShaped(page uint16, inLen, outLen int) bool {
	return page >= 0xFF00 && inLen == ReportLen && outLen == ReportLen
}

func (s descShape) knxShaped() bool {
	has := func(m map[int]int) bool {
		for _, n := range m {
			if n == ReportLen {
				return true
			}
		}
		return false
	}
	return s.parsed && knxShaped(s.page, boolInt(has(s.in))*ReportLen, boolInt(has(s.out))*ReportLen)
}
