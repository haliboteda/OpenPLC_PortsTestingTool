package stlink_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"PortTool/src/stlink"
	"PortTool/src/stlink/stlinktest"
)

func fake(t *testing.T) *stlinktest.Flash {
	t.Helper()
	f, restore := stlinktest.Install(t.TempDir())
	t.Cleanup(restore)
	return f
}

func open(t *testing.T) stlink.Programmer {
	t.Helper()
	p, err := stlink.Open()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Root area, metadata and the marker are what sector 15 holds besides the
// calibration area.
func seedSector15(f *stlinktest.Flash) (root, meta, marker []byte) {
	root = bytes.Repeat([]byte{0x4F}, 160)
	meta = bytes.Repeat([]byte{0x4D}, 224)
	marker = bytes.Repeat([]byte{0x53}, 32)
	f.Put(stlink.Sector15+0x2000, root)
	f.Put(stlink.Sector15+0x4000, meta)
	f.Put(0x081FFFE0, marker)
	return
}

func image(fill byte) []byte {
	img := bytes.Repeat([]byte{0xFF}, stlink.CalAreaSize)
	copy(img, bytes.Repeat([]byte{fill}, 56))
	return img
}

func TestFactoryResetKeepsBootloaderAndCalibrationOnly(t *testing.T) {
	f := fake(t)
	seedSector15(f)
	boot := bytes.Repeat([]byte{0xB0}, 4096)
	f.Put(stlink.FlashBase, boot)
	f.Put(stlink.FlashBase+stlink.SectorSize, bytes.Repeat([]byte{0xA0}, 4096)) // app
	f.Put(stlink.Sector15, image(0x33)[:64])

	var saved []byte
	if err := open(t).FactoryReset(func(b []byte) error { saved = append([]byte(nil), b...); return nil }); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(saved, image(0x33)) {
		t.Fatal("the calibration area handed to keep is not what the board held")
	}
	if !bytes.Equal(f.At(stlink.FlashBase, len(boot)), boot) {
		t.Fatal("bootloader changed")
	}
	if !bytes.Equal(f.At(stlink.Sector15, stlink.CalAreaSize), image(0x33)) {
		t.Fatal("calibration area not written back")
	}
	for _, at := range []int{stlink.FlashBase + stlink.SectorSize, stlink.Sector15 + 0x2000, stlink.Sector15 + 0x4000, 0x081FFFE0} {
		if !bytes.Equal(f.At(at, 32), bytes.Repeat([]byte{0xFF}, 32)) {
			t.Fatalf("0x%X not erased", at)
		}
	}
	if !contains(f.Calls, "-e 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15") {
		t.Fatalf("not erased as sectors 1-15: %v", f.Calls)
	}
}

func TestFactoryResetErasesNothingWhenTheCopyIsNotSaved(t *testing.T) {
	f := fake(t)
	f.Put(stlink.Sector15, image(0x44)[:64])
	err := open(t).FactoryReset(func([]byte) error { return errors.New("disk full") })
	if err == nil {
		t.Fatal("went on without a saved copy")
	}
	for _, c := range f.Calls {
		if strings.Contains(c, "-e") {
			t.Fatalf("erased without a saved copy: %s", c)
		}
	}
}

func TestFlashBootloaderLeavesSector15(t *testing.T) {
	f := fake(t)
	seedSector15(f)
	f.Put(stlink.Sector15, image(0x55)[:64])
	if err := open(t).FlashBootloader("boot.hex"); err == nil {
		t.Fatal("the fake has no boot.hex; the write should fail")
	}
	if !contains(f.Calls, "-e 0 1 2 3 4 5 6 7 8 9 10 11 12 13 14") {
		t.Fatalf("not erased as sectors 0-14: %v", f.Calls)
	}
	if !bytes.Equal(f.At(stlink.Sector15, 64), image(0x55)[:64]) {
		t.Fatal("sector 15 changed")
	}
}

func TestProgrammerErrorStopsTheOperation(t *testing.T) {
	f := fake(t)
	f.FailOn = "-e"
	if err := open(t).FactoryReset(func([]byte) error { return nil }); err == nil {
		t.Fatal("an erase error did not stop the factory reset")
	}
	for _, c := range f.Calls {
		if strings.Contains(c, "-w") {
			t.Fatalf("wrote after a failed erase: %s", c)
		}
	}
}

func contains(calls []string, op string) bool {
	for _, c := range calls {
		if strings.HasSuffix(c, op) {
			return true
		}
	}
	return false
}
