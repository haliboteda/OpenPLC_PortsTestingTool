//go:build windows || linux

package serialx

import (
	"runtime"
	"strings"

	"go.bug.st/serial/enumerator"
)

// On Windows and Linux the detailed enumeration is pure Go, so a cross-compile
// still gets the USB descriptors that tell the adapters apart.
func listPorts() ([]PortInfo, error) {
	details, err := enumerator.GetDetailedPortsList()
	if err != nil {
		return nil, err
	}
	out := make([]PortInfo, 0, len(details))
	for _, d := range details {
		if d == nil {
			continue
		}
		p := PortInfo{
			Name:    d.Name,
			Product: strings.TrimSpace(d.Product),
			VID:     d.VID,
			PID:     d.PID,
			Serial:  d.SerialNumber,
			IsUSB:   d.IsUSB,
		}
		if runtime.GOOS == "linux" {
			describeFromSysfs(&p)
		}
		out = append(out, p)
	}
	return out, nil
}
