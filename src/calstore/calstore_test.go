package calstore

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"PortTool/src/calarea"
)

const uid = "003400413135511439303538"

func fitted(gain, offset float64, unit string) Channel {
	return Channel{Unit: unit, Gain: gain, Offset: offset,
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

// The image appears only once all four channels are measured, and
// it decodes back to the archived coefficients for this board.
func TestImageNeedsAllFourChannels(t *testing.T) {
	Dir = t.TempDir()
	defer func() { Dir = "" }()
	now := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)

	var res Result
	var err error
	for i, n := range calarea.Names {
		res, err = Store(uid, n, fitted(1+float64(i)/100, float64(i)/10, DefaultRanges()[n].Unit()), Meta{PortTool: "test"}, now.Add(time.Duration(i)*time.Second))
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

// Re-measuring keeps the previous archive and image, and a channel with a
// large residual still gets a fresh image: calibration does not judge the
// residual (decision 99).
func TestRemeasureKeepsOldFilesAndLargeResidualStillWritesImage(t *testing.T) {
	Dir = t.TempDir()
	defer func() { Dir = "" }()
	now := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	for _, n := range calarea.Names {
		if _, err := Store(uid, n, fitted(1, 0, DefaultRanges()[n].Unit()), Meta{}, now); err != nil {
			t.Fatal(err)
		}
	}
	rough := fitted(1.2, 0.5, DefaultRanges()["AO1"].Unit())
	rough.MaxResidual = 3
	pct := 15.0
	rough.ResidualPctFS = &pct
	res, err := Store(uid, "AO1", rough, Meta{}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if res.KeptArchive == "" || res.KeptImage == "" {
		t.Fatalf("old files not kept: %+v", res)
	}
	if res.Image == "" {
		t.Fatalf("a large residual must not hold back the image: %+v", res)
	}
	img, _ := os.ReadFile(res.Image)
	_, ch, err := calarea.Decode(img)
	if err != nil || ch[calarea.AO1].Gain != float32(1.2) || ch[calarea.AO1].Offset != float32(0.5) {
		t.Fatalf("image does not carry the re-measured AO1: %v %+v", err, ch)
	}
	var arc Archive
	data, _ := os.ReadFile(res.Archive)
	if err := json.Unmarshal(data, &arc); err != nil || len(arc.Channels) != 4 {
		t.Fatalf("archive must keep the channels not re-measured: %v %d", err, len(arc.Channels))
	}
	if got := arc.Channels["AO1"]; got.ResidualPctFS == nil || *got.ResidualPctFS != 15 {
		t.Fatalf("the residual is still archived: %+v", got)
	}
}

func TestWrongUnitIsRefused(t *testing.T) {
	Dir = t.TempDir()
	defer func() { Dir = "" }()
	if _, err := Store(uid, "AI1", fitted(1, 0, "mA"), Meta{}, time.Now()); err == nil {
		t.Fatal("AI1 on its voltage range is fitted in mV; mA must be refused")
	}
}
