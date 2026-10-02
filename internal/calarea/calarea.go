// Package calarea builds and reads the per-board calibration area the fixture
// writes into flash at 0x081E0000.
//
// Mirror of the bootloader's IAPServer/calib_area.h, which owns the format;
// this repo's selfcheck (calarea step) checks they agree. Byte table and meaning of the coefficients:
// $PROD/docs/modules/M1/SECTOR-15.md, "校准值区的格式".
package calarea

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
)

const (
	Addr     = 0x081E0000
	AreaSize = 8 * 1024

	Magic    = 0x4C41434F // "OCAL"
	Version  = 1
	Channels = 4

	// Size is sizeof(calib_area_t).
	Size = 56
)

// Channel order is part of the format.
const (
	AI1 = iota // mV
	AI2        // mA
	AO1        // mA
	AO2        // mA
)

// Names and Units are the channels as plan files and the archive spell them,
// in format order. The unit is the one the coefficients are fitted in.
var (
	Names = [Channels]string{"AI1", "AI2", "AO1", "AO2"}
	Units = [Channels]string{"mV", "mA", "mA", "mA"}
)

// Index returns a channel's position in the area, by name.
func Index(name string) (int, bool) {
	for i, n := range Names {
		if n == name {
			return i, true
		}
	}
	return 0, false
}

// Channel holds one fit: measured ~= Gain*nominal + Offset.
type Channel struct {
	Gain   float32
	Offset float32
}

type area struct {
	Magic    uint32
	Version  uint16
	Channels uint16
	UID      [3]uint32 // w0 w1 w2
	Ch       [Channels]Channel
	CRC32    uint32
}

// Encode returns the 56-byte record for a board.
func Encode(uid [3]uint32, ch [Channels]Channel) []byte {
	a := area{Magic: Magic, Version: Version, Channels: Channels, UID: uid, Ch: ch}
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, a)
	out := b.Bytes()
	binary.LittleEndian.PutUint32(out[Size-4:], crc32.ChecksumIEEE(out[:Size-4]))
	return out
}

// Image returns the whole 8 KiB area: the record, then erased flash.
func Image(uid [3]uint32, ch [Channels]Channel) []byte {
	img := bytes.Repeat([]byte{0xFF}, AreaSize)
	copy(img, Encode(uid, ch))
	return img
}

var (
	ErrBlank   = errors.New("calibration area was never written")
	ErrCorrupt = errors.New("calibration area has a bad CRC or an unknown version")
)

// Decode reads a record back, e.g. from a sector read over JLINK.
func Decode(b []byte) (uid [3]uint32, ch [Channels]Channel, err error) {
	if len(b) < Size {
		return uid, ch, ErrCorrupt
	}
	var a area
	_ = binary.Read(bytes.NewReader(b[:Size]), binary.LittleEndian, &a)
	if a.Magic != Magic {
		return uid, ch, ErrBlank
	}
	if a.Version != Version || a.Channels != Channels || crc32.ChecksumIEEE(b[:Size-4]) != a.CRC32 {
		return uid, ch, ErrCorrupt
	}
	return a.UID, a.Ch, nil
}
