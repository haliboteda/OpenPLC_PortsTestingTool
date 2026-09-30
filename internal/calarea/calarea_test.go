package calarea_test

import (
	"encoding/binary"
	"errors"
	"testing"

	"PortTool/internal/calarea"
)

var uid = [3]uint32{0x00390029, 0x34325103, 0x39393938}

var fits = [calarea.Channels]calarea.Channel{
	{Gain: 0.998, Offset: 1.5},
	{Gain: 1.002, Offset: -0.01},
	{Gain: 0.99, Offset: 0.17},
	{Gain: 1, Offset: 0},
}

// The byte positions are the on-flash format the C side reads.
func TestLayout(t *testing.T) {
	b := calarea.Encode(uid, fits)
	if len(b) != calarea.Size {
		t.Fatalf("record is %d bytes, want %d", len(b), calarea.Size)
	}
	if got := binary.LittleEndian.Uint32(b[0:]); got != calarea.Magic {
		t.Errorf("magic at 0 = %#x", got)
	}
	if got := binary.LittleEndian.Uint16(b[4:]); got != calarea.Version {
		t.Errorf("version at 4 = %d", got)
	}
	if got := binary.LittleEndian.Uint32(b[8:]); got != uid[0] {
		t.Errorf("uid w0 at 8 = %#x", got)
	}
	img := calarea.Image(uid, fits)
	if len(img) != calarea.AreaSize || img[calarea.Size] != 0xFF || img[calarea.AreaSize-1] != 0xFF {
		t.Errorf("image is not the record followed by erased flash")
	}
}

func TestRoundTrip(t *testing.T) {
	gotUID, gotCh, err := calarea.Decode(calarea.Encode(uid, fits))
	if err != nil || gotUID != uid || gotCh != fits {
		t.Fatalf("round trip = %v %v %v", gotUID, gotCh, err)
	}
}

func TestRejects(t *testing.T) {
	erased := make([]byte, calarea.Size)
	for i := range erased {
		erased[i] = 0xFF
	}
	if _, _, err := calarea.Decode(erased); !errors.Is(err, calarea.ErrBlank) {
		t.Errorf("erased flash: %v, want ErrBlank", err)
	}
	b := calarea.Encode(uid, fits)
	b[20] ^= 1
	if _, _, err := calarea.Decode(b); !errors.Is(err, calarea.ErrCorrupt) {
		t.Errorf("flipped bit: %v, want ErrCorrupt", err)
	}
}
