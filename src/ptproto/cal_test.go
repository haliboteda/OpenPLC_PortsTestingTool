package ptproto

import (
	"errors"
	"strings"
	"testing"

	"PortTool/src/calarea"
)

var testUID = [3]uint32{0x00340041, 0x31355114, 0x39303538}

func testImage() []byte {
	return calarea.Image(testUID, [calarea.Channels]calarea.Channel{
		{Gain: 1.01, Offset: -2.5}, {Gain: 0.99, Offset: 0.01}, {Gain: 1, Offset: 0.2}, {Gain: 1.02, Offset: -0.1},
	})
}

// The record pt.calw carries reads back through ParseCal as the same values,
// so the two directions agree on the byte order.
func TestCalWriteCommandRoundTripsThroughParseCal(t *testing.T) {
	img := testImage()
	cmd, err := CalWriteCommand(img)
	if err != nil {
		t.Fatal(err)
	}
	hexPart, ok := strings.CutPrefix(cmd, "pt.calw ")
	if !ok || len(hexPart) != 2*calarea.Size || strings.ToUpper(hexPart) != hexPart {
		t.Fatalf("command shape: %q", cmd)
	}
	cal, err := ParseCal("cal=" + hexPart)
	if err != nil || !cal.Present || cal.UID != testUID {
		t.Fatalf("parse back: %+v %v", cal, err)
	}
	_, ch, _ := calarea.Decode(img)
	if cal.Ch != ch {
		t.Fatalf("coefficients: %+v, want %+v", cal.Ch, ch)
	}
}

// One line, inside the board's receive limit with its terminator: no chunks.
func TestCalWriteCommandFitsOneLine(t *testing.T) {
	cmd, err := CalWriteCommand(testImage())
	if err != nil {
		t.Fatal(err)
	}
	if n := len(cmd) + len("\r\n"); n > CommandLineMax {
		t.Fatalf("pt.calw is %d bytes with its terminator, limit %d", n, CommandLineMax)
	}
}

func TestCalWriteCommandRefusesImagesTheFormatDoesNotAllow(t *testing.T) {
	short := testImage()[:calarea.Size]

	tail := testImage()
	tail[calarea.AreaSize-1] = 0x00

	blank := calarea.Image(testUID, [calarea.Channels]calarea.Channel{})
	for i := range blank[:calarea.Size] {
		blank[i] = 0xFF
	}

	badCRC := testImage()
	badCRC[20] ^= 0x01

	for what, img := range map[string][]byte{
		"a 56-byte image":         short,
		"data after the record":   tail,
		"an erased record":        blank,
		"a record with a bad CRC": badCRC,
	} {
		if cmd, err := CalWriteCommand(img); err == nil {
			t.Errorf("%s: accepted as %q", what, cmd)
		}
	}
	if _, err := CalWriteCommand(badCRC); !errors.Is(err, calarea.ErrCorrupt) {
		t.Errorf("bad CRC: %v, want calarea.ErrCorrupt", err)
	}
}
