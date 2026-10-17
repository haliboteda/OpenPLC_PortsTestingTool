package ptecho

import (
	"fmt"

	"PortTool/src/serialx"
)

// The board's CDC pipe enumerates as ST's STM32 Virtual ComPort, the same
// VID:PID station 6's plan checks. ⚠️ The PID matters: the ST-Link's own
// virtual COM port is 0483:3754, and echoing into it looks like a dead pipe.
const (
	cdcVID = "0483"
	cdcPID = "5740"
)

// FindCDC names the board's CDC port, or says why it cannot.
func FindCDC() (string, error) {
	return serialx.FindUSB(cdcVID, cdcPID,
		fmt.Errorf("no %s:%s port found - is the usb session started "+
			"and the cable in? The port only appears once pt.start usb runs, "+
			"because that is when the board initialises its USB stack",
			cdcVID, cdcPID),
		// Two boards on one PC. Naming both and refusing beats picking one.
		func(names string) error {
			return fmt.Errorf("more than one %s:%s port (%s) - name the one you "+
				"mean with --com", cdcVID, cdcPID, names)
		})
}
