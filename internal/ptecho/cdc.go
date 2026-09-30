package ptecho

import (
	"fmt"
	"strings"

	"PortTool/internal/serialx"
)

// The board's CDC pipe enumerates as ST's STM32 Virtual ComPort.
//
// ⚠️ The PID matters, not just the vendor: the ST-Link's own virtual COM port
// is 0483:3754 and would otherwise match. Echoing into the debug probe instead
// of the board would look exactly like a dead pipe on a working board.
//
// 5740 is not a guess - it is the value station 6's plan file has been
// checking for since before this responder existed.
const (
	cdcVID = "0483"
	cdcPID = "5740"
)

func stmCDC(p serialx.PortInfo) bool {
	return p.IsUSB && strings.EqualFold(p.VID, cdcVID) &&
		strings.EqualFold(p.PID, cdcPID)
}

// FindCDC names the board's CDC port, or says why it cannot.
func FindCDC() (string, error) {
	ports, err := serialx.List()
	if err != nil {
		return "", err
	}
	var hits []string
	for _, p := range ports {
		if stmCDC(p) {
			hits = append(hits, p.Name)
		}
	}
	switch len(hits) {
	case 0:
		return "", fmt.Errorf("no %s:%s port found - is the usb session started "+
			"and the cable in? The port only appears once pt.start usb runs, "+
			"because that is when the board initialises its USB stack",
			cdcVID, cdcPID)
	case 1:
		return hits[0], nil
	default:
		// Two boards on one PC. Naming both and refusing beats picking one.
		return "", fmt.Errorf("more than one %s:%s port (%s) - name the one you "+
			"mean with --com", cdcVID, cdcPID, strings.Join(hits, ", "))
	}
}
