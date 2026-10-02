package calstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"PortTool/internal/calarea"
)

const uid = "003400413135511439303538"

func passed(gain, offset float64, unit string) Channel {
	ok := true
	return Channel{Unit: unit, Gain: gain, Offset: offset, Pass: &ok,
		Points: []Point{{Want: 0, Got: offset}, {Want: 10, Got: 10*gain + offset}}}
}

func TestParseUIDMatchesPtID(t *testing.T) {
	w, err := ParseUID(uid)
	if err != nil {
		t.Fatal(err)
	}
	if w != [3]uint32{0x00340041, 0x31355114, 0x39303538} {
		t.Fatalf("got %08X", w)
	}
	if _, err := ParseUID("00340041"); err == nil {
		t.Fatal("a short uid must be refused")
	}
}

// The image appears only once all four channels are measured and pass, and
// it decodes back to the archived coefficients for this board.
func TestImageNeedsAllFourChannels(t *testing.T) {
	Dir = t.TempDir()
	defer func() { Dir = "" }()
	now := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)

	var res Result
	var err error
	for i, n := range calarea.Names {
		res, err = Store(uid, n, passed(1+float64(i)/100, float64(i)/10, calarea.Units[i]), Meta{PortTool: "test"}, now.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if i < 3 && res.Image != "" {
			t.Fatalf("image written with only %d channels", i+1)
		}
	}
	if res.Image == "" {
		t.Fatalf("no image after all four: %+v", res)
	}
	img, _ := os.ReadFile(res.Image)
	if len(img) != calarea.AreaSize {
		t.Fatalf("image is %d bytes", len(img))
	}
	words, ch, err := calarea.Decode(img)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := ParseUID(uid)
	if words != want || ch[calarea.AO2].Gain != float32(1.03) {
		t.Fatalf("decoded uid %08X ch %+v", words, ch)
	}
}

// Re-measuring keeps the previous archive and image, and a failing channel
// leaves no image that disagrees with the archive.
func TestRemeasureKeepsOldFilesAndFailingDropsImage(t *testing.T) {
	Dir = t.TempDir()
	defer func() { Dir = "" }()
	now := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	for i, n := range calarea.Names {
		if _, err := Store(uid, n, passed(1, 0, calarea.Units[i]), Meta{}, now); err != nil {
			t.Fatal(err)
		}
	}
	bad := passed(1, 0, "mA")
	no := false
	bad.Pass = &no
	res, err := Store(uid, "AO1", bad, Meta{}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if res.KeptArchive == "" || res.KeptImage == "" {
		t.Fatalf("old files not kept: %+v", res)
	}
	if res.Image != "" || len(res.Failing) != 1 || res.Failing[0] != "AO1" {
		t.Fatalf("a failing channel must leave no image: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(res.Dir, imageName)); !os.IsNotExist(err) {
		t.Fatal("calarea.bin still there after a failing re-measure")
	}
	var arc Archive
	data, _ := os.ReadFile(res.Archive)
	if err := json.Unmarshal(data, &arc); err != nil || len(arc.Channels) != 4 {
		t.Fatalf("archive must keep the channels not re-measured: %v %d", err, len(arc.Channels))
	}
}

func TestWrongUnitIsRefused(t *testing.T) {
	Dir = t.TempDir()
	defer func() { Dir = "" }()
	if _, err := Store(uid, "AI1", passed(1, 0, "mA"), Meta{}, time.Now()); err == nil {
		t.Fatal("AI1 is fitted in mV; mA must be refused")
	}
}
