package calstore

import (
	"testing"
	"time"
)

// An unregistered board reads as the product default; a registration is kept
// per UID and only changes the channels it names (decision 102).
func TestRangesDefaultAndPersistPerUID(t *testing.T) {
	Dir = t.TempDir()
	defer func() { Dir = "" }()

	r, reg, err := LoadRanges(uid)
	if err != nil || reg || r["AI1"] != RangeVoltage || r["AI2"] != RangeCurrent || r["AO1"] != RangeVoltage || r["AO2"] != RangeAO020 {
		t.Fatalf("default: %v %v %v", r, reg, err)
	}
	if _, err := SaveRanges(uid, Ranges{"AI1": RangeCurrent}, time.Now()); err != nil {
		t.Fatal(err)
	}
	r, reg, _ = LoadRanges(uid)
	if !reg || r["AI1"] != RangeCurrent || r["AO2"] != RangeAO020 {
		t.Fatalf("after registering AI1: %v %v", r, reg)
	}
	if other, reg, _ := LoadRanges("0021003A3135511439303539"); reg || other["AI1"] != RangeVoltage {
		t.Fatal("another board must not inherit the registration")
	}
	// AO may be voltage on other products (decision 134); AI has no 4-20 range.
	if _, err := SaveRanges(uid, Ranges{"AO1": RangeVoltage}, time.Now()); err != nil {
		t.Fatalf("AO registered as voltage: %v", err)
	}
	if _, err := SaveRanges(uid, Ranges{"AI1": RangeAO420}, time.Now()); err == nil {
		t.Fatal("AI cannot be registered as 4-20 mA")
	}
}

// AI1 soldered for current is stored in mA; the unit must match the range.
func TestStoreFollowsTheRange(t *testing.T) {
	Dir = t.TempDir()
	defer func() { Dir = "" }()
	ch := fitted(1, 0, "mA")
	ch.Range = RangeCurrent
	if _, err := Store(uid, "AI1", ch, Meta{}, time.Now()); err != nil {
		t.Fatalf("AI1 in current range, mA: %v", err)
	}
	ch.Unit = "mV"
	if _, err := Store(uid, "AI1", ch, Meta{}, time.Now()); err == nil {
		t.Fatal("mV in the current range must be refused")
	}
	if lo, fs := RangeAO420.Scale(); lo != 4 || fs != 20 {
		t.Fatalf("4-20 mA scale = %v, %v", lo, fs)
	}
}
