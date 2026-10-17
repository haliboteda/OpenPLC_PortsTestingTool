package ptproto

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"PortTool/src/calarea"
)

// Cal is pt.cal decoded: what calibration the board carries in flash.
type Cal struct {
	// Present is false when the area was never written. That is a board
	// state, not an error.
	Present bool
	UID     [3]uint32
	Ch      [calarea.Channels]calarea.Channel
}

// ParseCal reads the body of pt.cal's OK line ("cal=<hex>", marker already
// stripped by Classify). The firmware reports the area's bytes raw and
// calarea.Decode is the only reader of the format - see PORTTOOL-FLOW.md A.4.
//
// A corrupt area returns an error wrapping calarea.ErrCorrupt, so a caller can
// tell it from a reply that is malformed.
func ParseCal(body string) (Cal, error) {
	v, ok := Get(Fields(body), "cal")
	if !ok {
		return Cal{}, fmt.Errorf("pt.cal reply has no cal=: %q", body)
	}
	if len(v) != 2*calarea.Size {
		return Cal{}, fmt.Errorf("pt.cal reply has %d hex digits, want %d", len(v), 2*calarea.Size)
	}
	raw, err := hex.DecodeString(v)
	if err != nil {
		return Cal{}, fmt.Errorf("pt.cal reply is not hex: %w", err)
	}

	uid, ch, err := calarea.Decode(raw)
	switch {
	case errors.Is(err, calarea.ErrBlank):
		return Cal{}, nil
	case err != nil:
		return Cal{}, fmt.Errorf("pt.cal: %w", err)
	}
	return Cal{Present: true, UID: uid, Ch: ch}, nil
}

// CommandLineMax is PORTTOOL_LINE_MAX: the longest line the board takes,
// terminator included ($BOOT/TestCase/porttool/porttool.h).
const CommandLineMax = 256

// CalWriteCommand is the pt.calw line that writes image, an 8 KiB
// calarea.bin, into the board's sector 15 (decision 142). Only the 56-byte
// record travels: the format has the rest erased, which is checked here.
// PORTTOOL-FLOW.md A.3, A.4.
func CalWriteCommand(image []byte) (string, error) {
	if len(image) != calarea.AreaSize {
		return "", fmt.Errorf("calibration image is %d bytes, not %d", len(image), calarea.AreaSize)
	}
	if !bytes.Equal(image[calarea.Size:], bytes.Repeat([]byte{0xFF}, calarea.AreaSize-calarea.Size)) {
		return "", fmt.Errorf("calibration image has data after its %d-byte record", calarea.Size)
	}
	if _, _, err := calarea.Decode(image); err != nil {
		return "", fmt.Errorf("calibration image: %w", err)
	}
	return "pt.calw " + strings.ToUpper(hex.EncodeToString(image[:calarea.Size])), nil
}
