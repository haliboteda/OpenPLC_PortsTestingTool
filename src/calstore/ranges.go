package calstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"PortTool/src/calarea"
)

// Range is what an analog channel is soldered for. Software cannot switch it,
// so R&D registers it per board (decisions 98, 102). PORTTOOL-FLOW.md C.3.2.
type Range string

const (
	RangeVoltage Range = "v"    // 0-10 V, coefficients in mV; AI, and AO on products that have it (decision 134)
	RangeCurrent Range = "i"    // AI 0-20 mA, coefficients in mA
	RangeAO020   Range = "i20"  // AO 0-20 mA
	RangeAO420   Range = "i420" // AO 4-20 mA
)

// Ranges maps a channel name (calarea.Names) to its registered range.
type Ranges map[string]Range

// DefaultRanges applies to any board, under test or reference, with nothing
// registered: AI1, AO1 voltage; AI2, AO2 current (decision 134 "默认档位").
func DefaultRanges() Ranges {
	return Ranges{"AI1": RangeVoltage, "AI2": RangeCurrent, "AO1": RangeVoltage, "AO2": RangeAO020}
}

// RangeOptions lists the ranges a channel can be soldered for.
func RangeOptions(channel string) []Range {
	switch channel {
	case "AI1", "AI2":
		return []Range{RangeVoltage, RangeCurrent}
	case "AO1", "AO2":
		return []Range{RangeAO020, RangeAO420, RangeVoltage}
	}
	return nil
}

// ValidFor says whether r is one of a channel's ranges.
func (r Range) ValidFor(channel string) bool {
	for _, o := range RangeOptions(channel) {
		if o == r {
			return true
		}
	}
	return false
}

// Current says the range carries a current; two ends of a wire match when
// both are current or both are voltage (decision 134).
func (r Range) Current() bool { return r != RangeVoltage }

// Unit is the unit readings and coefficients are in for this range.
func (r Range) Unit() string {
	if r == RangeVoltage {
		return "mV"
	}
	return "mA"
}

// Scale is the range's low end and full scale, in Unit. A 4-20 mA output's
// full scale is 20 mA too, so ±1 % FS means the same current on both AO ranges.
func (r Range) Scale() (lo, fs float64) {
	switch r {
	case RangeVoltage:
		return 0, 10000
	case RangeAO420:
		return 4, 20
	}
	return 0, 20
}

const rangesName = "ranges.json"

type rangesFile struct {
	Schema       int    `json:"schema"`
	UID          string `json:"uid"`
	RegisteredAt string `json:"registered_at"`
	Ranges       Ranges `json:"ranges"`
}

// LoadRanges returns a board's registered ranges, with the product default
// filling any channel never registered. registered is false when the board
// has no registration at all.
func LoadRanges(uid string) (r Ranges, registered bool, err error) {
	r = DefaultRanges()
	if _, err := ParseUID(uid); err != nil {
		return r, false, err
	}
	data, err := os.ReadFile(filepath.Join(root(), uid, rangesName))
	if errors.Is(err, os.ErrNotExist) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	var f rangesFile
	if err := json.Unmarshal(data, &f); err != nil {
		return r, false, fmt.Errorf("reading %s: %w", rangesName, err)
	}
	for _, n := range calarea.Names {
		if v, ok := f.Ranges[n]; ok && v.ValidFor(n) {
			r[n] = v
		}
	}
	return r, true, nil
}

// SaveRanges registers a board's ranges. Channels left out keep what was
// registered before (or the default). Returns the file written.
func SaveRanges(uid string, set Ranges, now time.Time) (string, error) {
	cur, _, err := LoadRanges(uid)
	if err != nil {
		return "", err
	}
	for n, v := range set {
		if !v.ValidFor(n) {
			return "", fmt.Errorf("channel %s cannot be soldered for range %q", n, v)
		}
		cur[n] = v
	}
	dir := filepath.Join(root(), uid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(rangesFile{Schema: Schema, UID: uid, RegisteredAt: now.Format(time.RFC3339), Ranges: cur}, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, rangesName)
	return path, os.WriteFile(path, append(data, '\n'), 0o644)
}

// Local is one channel of a board's archive, for showing next to the card.
type Local struct {
	Gain       float64 `json:"k"`
	Offset     float64 `json:"b"`
	Range      Range   `json:"range"`
	Unit       string  `json:"unit"`
	MeasuredAt string  `json:"measured_at"`
}

// Archived returns the channels a board's archive holds, and whether its
// sector-15 image exists.
func Archived(uid string) (map[string]Local, bool, error) {
	out := map[string]Local{}
	if _, err := ParseUID(uid); err != nil {
		return out, false, err
	}
	_, imgErr := os.Stat(ImagePath(uid))
	data, err := os.ReadFile(filepath.Join(root(), uid, archiveName))
	if errors.Is(err, os.ErrNotExist) {
		return out, imgErr == nil, nil
	}
	if err != nil {
		return out, false, err
	}
	var arc Archive
	if err := json.Unmarshal(data, &arc); err != nil {
		return out, false, fmt.Errorf("reading %s: %w", archiveName, err)
	}
	for n, c := range arc.Channels {
		out[n] = Local{Gain: c.Gain, Offset: c.Offset, Range: c.Range, Unit: c.Unit, MeasuredAt: c.MeasuredAt}
	}
	return out, imgErr == nil, nil
}
