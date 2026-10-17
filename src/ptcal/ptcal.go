// Package ptcal turns a set of calibration points into a correction.
//
// It is the only place that arithmetic happens, so the panel and a production
// run cannot disagree about the same measurements. The board reports what it
// intended to put out, a meter what came out; the rest is the host's
// (DECISIONS.md 22 and 38). Nothing here writes to a board: src/calstore
// produces the sector-15 image (CAL-04).
//
// A straight line is the smallest model that separates gain from offset; the
// residuals are reported so a non-linear converter shows instead of hiding in
// the gain.
package ptcal

import (
	"errors"
	"math"
)

// Point is one measurement: what the board intended to produce, and what an
// instrument saw. Units are the caller's, and both fields must share them.
type Point struct {
	// Want is what the board said it was putting out, after its own
	// quantising - not what was asked for.
	Want float64
	// Got is what the instrument measured.
	Got float64
}

// Fit is a straight line mapping what the board intends to what it actually
// produces: Got ≈ Gain*Want + Offset.
type Fit struct {
	Gain   float64 `json:"gain"`
	Offset float64 `json:"offset"`

	// Points is how many measurements went in.
	Points int `json:"points"`

	// MaxResidual is the largest distance between a measured point and the
	// line, in the caller's units, and MaxResidualAt is the Want it happened
	// at. Far larger than the instrument's own error means the response is
	// not a straight line.
	MaxResidual   float64 `json:"max_residual"`
	MaxResidualAt float64 `json:"max_residual_at"`

	// RMSResidual is the same story averaged, for a limit that should not turn
	// on one unlucky point.
	RMSResidual float64 `json:"rms_residual"`

	// Exact is true when there were exactly two points, so every residual is
	// zero and means nothing.
	Exact bool `json:"exact"`
}

// ErrTooFewPoints is returned for fewer than two usable points: one point
// cannot tell a gain error from an offset error.
var ErrTooFewPoints = errors.New("a straight line needs at least two points")

// ErrNoSpread is returned when every point asked for the same value. The line
// is vertical - there is no gain to recover, however many points there are.
var ErrNoSpread = errors.New("every point has the same intended value, so no gain can be recovered")

// LinearFit computes the least-squares line through the points. Non-finite
// points are dropped: a skipped reading arrives from the panel as NaN.
func LinearFit(points []Point) (Fit, error) {
	usable := make([]Point, 0, len(points))
	for _, p := range points {
		if isNum(p.Want) && isNum(p.Got) {
			usable = append(usable, p)
		}
	}
	if len(usable) < 2 {
		return Fit{}, ErrTooFewPoints
	}

	n := float64(len(usable))
	var sx, sy float64
	for _, p := range usable {
		sx += p.Want
		sy += p.Got
	}
	mx, my := sx/n, sy/n

	var sxx, sxy float64
	for _, p := range usable {
		dx := p.Want - mx
		sxx += dx * dx
		sxy += dx * (p.Got - my)
	}
	if sxx == 0 {
		return Fit{}, ErrNoSpread
	}

	f := Fit{
		Points: len(usable),
		Exact:  len(usable) == 2,
	}
	f.Gain = sxy / sxx
	f.Offset = my - f.Gain*mx

	var sumSq float64
	for _, p := range usable {
		r := p.Got - (f.Gain*p.Want + f.Offset)
		sumSq += r * r
		if math.Abs(r) > math.Abs(f.MaxResidual) {
			f.MaxResidual = r
			f.MaxResidualAt = p.Want
		}
	}
	f.RMSResidual = math.Sqrt(sumSq / n)
	return f, nil
}

func isNum(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}
