// Package ptcal turns a set of calibration points into a correction.
//
// It is the only place that arithmetic happens, for the same reason ptcheck is
// the only place a limit meets a reading: the panel walks an operator through
// the points and a production run will do it unattended, and the two must not
// be able to disagree about what the same measurements mean.
//
// The board is not involved. It reports what it intended to put out and a
// meter says what actually came out; everything from there is the host's
// (DECISIONS.md 22 and 38). Nothing here writes a coefficient back to a board:
// station 10 programs the sector-15 image internal/calstore produces (CAL-04).
//
// # Why a straight line
//
// A gain error and an offset error need different corrections, which is the
// whole reason several points are measured rather than one. A straight line is
// the smallest model that separates them, and it is the only shape defensible
// without measurements from real hardware to look at.
//
// *** So the residuals are reported, and they are the point. *** If the real
// converter is not linear, MaxResidual says so out loud instead of the fit
// quietly absorbing the curvature into a gain that is wrong everywhere. That
// is what makes fitting a line now safe: it cannot hide having been the wrong
// choice.
package ptcal

import (
	"errors"
	"math"
)

// Point is one measurement: what the board intended to produce, and what an
// instrument saw. Units are the caller's, and both fields must share them.
type Point struct {
	// Want is what the board said it was putting out, after its own
	// quantising - not what was asked for. Asking for 10.000 mA and the
	// hardware landing on 9.998 is not an error to correct; it is the number
	// the meter has to be compared against.
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
	// at. Judge this before trusting Gain and Offset: a residual far larger
	// than the instrument's own error means the response is not a straight
	// line and no straight line will correct it.
	MaxResidual   float64 `json:"max_residual"`
	MaxResidualAt float64 `json:"max_residual_at"`

	// RMSResidual is the same story averaged, for a limit that should not turn
	// on one unlucky point.
	RMSResidual float64 `json:"rms_residual"`

	// Exact is true when there were exactly two points. Two points define a
	// line, so every residual is zero and means nothing at all - a fit that
	// looks perfect because it could not have looked otherwise. Anything
	// judging residuals has to know which case it is holding.
	Exact bool `json:"exact"`
}

// ErrTooFewPoints is returned for fewer than two usable points: one point
// cannot tell a gain error from an offset error, and returning some arbitrary
// tie-break would be worse than refusing.
var ErrTooFewPoints = errors.New("a straight line needs at least two points")

// ErrNoSpread is returned when every point asked for the same value. The line
// is vertical - there is no gain to recover, however many points there are.
var ErrNoSpread = errors.New("every point has the same intended value, so no gain can be recovered")

// LinearFit computes the least-squares line through the points.
//
// Points whose numbers are not finite are dropped rather than poisoning the
// sums: a skipped reading arrives as NaN from the panel, and one of those
// would otherwise turn the whole fit into NaN with nothing to say why.
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

// Correct answers the question a calibrated board has to answer: to actually
// produce `want`, what should it be told to produce?
//
// Inverting the fit rather than applying it - a fit says what comes out when
// you ask for something, and correcting means going the other way.
func (f Fit) Correct(want float64) float64 {
	if f.Gain == 0 {
		return want
	}
	return (want - f.Offset) / f.Gain
}

// Apply is the forward direction: what this fit predicts will come out.
// Used to show a person how far a board was off before correction.
func (f Fit) Apply(want float64) float64 {
	return f.Gain*want + f.Offset
}

// Ideal reports whether a fit is close enough to unity gain and zero offset
// that correcting is not worth doing. `gainTol` is a fraction (0.01 is 1 %)
// and `offsetTol` is in the caller's units.
//
// It exists so a station can say "this board needs no calibration" as a
// finding rather than writing coefficients that are all but 1 and 0 - which
// would make every board look calibrated and hide the ones that really needed
// it.
func (f Fit) Ideal(gainTol, offsetTol float64) bool {
	return math.Abs(f.Gain-1) <= gainTol && math.Abs(f.Offset) <= offsetTol
}

func isNum(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}
