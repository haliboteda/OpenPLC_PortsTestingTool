// Package calstore keeps each board's calibration on the PC, by UID, and
// writes the sector-15 calibration-area image station 10 programs over JLINK.
//
// The tooling firmware never writes flash (CAL-04), so files are all this
// produces. Layout, names and the rules for re-measuring:
// $PROD/docs/modules/M4/PORTTOOL-FLOW.md, C.3.2.
package calstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"

	"PortTool/internal/calarea"
)

// Dir overrides where the store lives. Tests set it; a bench never does.
var Dir string

const (
	archiveName = "calibration.json"
	imageName   = "calarea.bin"
	stampLayout = "20060102-150405"
	// Schema is the archive's own format version.
	Schema = 1
)

func root() string {
	if Dir != "" {
		return Dir
	}
	// Beside the executable, like runlogs/: where a bench looks for it.
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "calibration")
	}
	return "calibration"
}

// Point is one measurement: Want is the board's nominal value, Got the
// instrument's (measured ~= gain*nominal + offset, the area's direction).
type Point struct {
	Want float64 `json:"want"`
	Got  float64 `json:"got"`
}

// Channel is one fitted channel as archived.
type Channel struct {
	Unit          string  `json:"unit"`
	MeasuredAt    string  `json:"measured_at"`
	Points        []Point `json:"points"`
	Gain          float64 `json:"gain"`
	Offset        float64 `json:"offset"`
	MaxResidual   float64 `json:"max_residual"`
	MaxResidualAt float64 `json:"max_residual_at"`
	RMSResidual   float64 `json:"rms_residual"`
	// The limit it was judged by, and the verdict. Absent when the plan
	// states no limit for this channel: unjudged is not the same as passed.
	FullScale        *float64 `json:"full_scale,omitempty"`
	MaxResidualPctFS *float64 `json:"max_residual_pct_fs,omitempty"`
	ResidualPctFS    *float64 `json:"residual_pct_fs,omitempty"`
	Pass             *bool    `json:"pass,omitempty"`
}

// Archive is calibration.json.
type Archive struct {
	Schema       int                `json:"schema"`
	UID          string             `json:"uid"`
	PortTool     string             `json:"porttool"`
	Plan         string             `json:"plan"`
	LimitVersion string             `json:"limit_version"`
	Channels     map[string]Channel `json:"channels"`
}

// Meta is what an archive records about where a fit came from.
type Meta struct {
	PortTool, Plan, LimitVersion string
}

// Result says what a Store call left on disk.
type Result struct {
	Dir         string   `json:"dir"`
	Archive     string   `json:"archive"`
	KeptArchive string   `json:"kept_archive,omitempty"` // the previous archive, renamed
	Image       string   `json:"image,omitempty"`        // written only when all four pass
	KeptImage   string   `json:"kept_image,omitempty"`   // the previous image, renamed
	Missing     []string `json:"missing,omitempty"`      // channels not measured yet
	Failing     []string `json:"failing,omitempty"`      // channels over their limit
	Unjudged    []string `json:"unjudged,omitempty"`     // channels the plan sets no limit for
}

var uidRE = regexp.MustCompile(`^[0-9A-Fa-f]{24}$`)

// ParseUID turns pt.id's uid= value into the area's three words. The firmware
// prints w0, w1, w2 as %08lX each, in that order.
func ParseUID(s string) ([3]uint32, error) {
	var w [3]uint32
	if !uidRE.MatchString(s) {
		return w, fmt.Errorf("uid %q is not 24 hex digits", s)
	}
	for i := range w {
		v, err := strconv.ParseUint(s[i*8:i*8+8], 16, 32)
		if err != nil {
			return w, err
		}
		w[i] = uint32(v)
	}
	return w, nil
}

// Store adds or replaces one channel in a board's archive, keeping the old
// archive under a time stamp, and refreshes the image.
func Store(uid string, name string, ch Channel, meta Meta, now time.Time) (Result, error) {
	words, err := ParseUID(uid)
	if err != nil {
		return Result{}, err
	}
	idx, ok := calarea.Index(name)
	if !ok {
		return Result{}, fmt.Errorf("channel %q is not one of %v", name, calarea.Names)
	}
	if ch.Unit != calarea.Units[idx] {
		return Result{}, fmt.Errorf("channel %s is fitted in %s, not %s", name, calarea.Units[idx], ch.Unit)
	}

	dir := filepath.Join(root(), uid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{}, err
	}
	res := Result{Dir: dir, Archive: filepath.Join(dir, archiveName)}
	stamp := now.Format(stampLayout)

	arc := Archive{Channels: map[string]Channel{}}
	if data, err := os.ReadFile(res.Archive); err == nil {
		if err := json.Unmarshal(data, &arc); err != nil {
			return Result{}, fmt.Errorf("reading %s: %w", res.Archive, err)
		}
		// Kept, not overwritten: a re-measured board must still show what it
		// measured before (TODO「每块板校准」).
		kept := filepath.Join(dir, "calibration."+stamp+".json")
		if err := os.Rename(res.Archive, kept); err != nil {
			return Result{}, err
		}
		res.KeptArchive = kept
	}
	if arc.Channels == nil {
		arc.Channels = map[string]Channel{}
	}
	ch.MeasuredAt = now.Format(time.RFC3339)
	arc.Channels[name] = ch
	arc.Schema = Schema
	arc.UID = uid
	arc.PortTool, arc.Plan, arc.LimitVersion = meta.PortTool, meta.Plan, meta.LimitVersion

	data, err := json.MarshalIndent(arc, "", "  ")
	if err != nil {
		return Result{}, err
	}
	if err := os.WriteFile(res.Archive, append(data, '\n'), 0o644); err != nil {
		return Result{}, err
	}

	var coeffs [calarea.Channels]calarea.Channel
	for i, n := range calarea.Names {
		c, have := arc.Channels[n]
		switch {
		case !have:
			res.Missing = append(res.Missing, n)
		case c.Pass == nil:
			res.Unjudged = append(res.Unjudged, n)
		case !*c.Pass:
			res.Failing = append(res.Failing, n)
		}
		coeffs[i] = calarea.Channel{Gain: float32(c.Gain), Offset: float32(c.Offset)}
	}
	sort.Strings(res.Failing)

	img := filepath.Join(dir, imageName)
	if _, err := os.Stat(img); err == nil {
		// Whatever happens next, the old image no longer matches the archive.
		kept := filepath.Join(dir, "calarea."+stamp+".bin")
		if err := os.Rename(img, kept); err != nil {
			return Result{}, err
		}
		res.KeptImage = kept
	}
	if len(res.Missing)+len(res.Failing)+len(res.Unjudged) == 0 {
		if err := os.WriteFile(img, calarea.Image(words, coeffs), 0o644); err != nil {
			return Result{}, err
		}
		res.Image = img
	}
	return res, nil
}
