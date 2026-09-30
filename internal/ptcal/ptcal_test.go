package ptcal_test

import (
	"errors"
	"math"
	"testing"

	"PortTool/internal/ptcal"
)

// close is a builtin, so this is near.
func near(t *testing.T, got, want float64, what string) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// A board that is 2 % low with a 0.1 offset has to come back as exactly that,
// because those two errors need different corrections and the whole reason for
// measuring several points is to tell them apart.
func TestSeparatesGainFromOffset(t *testing.T) {
	pts := []ptcal.Point{}
	for _, w := range []float64{1, 5, 10, 15, 20} {
		pts = append(pts, ptcal.Point{Want: w, Got: 0.98*w + 0.1})
	}

	f, err := ptcal.LinearFit(pts)
	if err != nil {
		t.Fatal(err)
	}
	near(t, f.Gain, 0.98, "gain")
	near(t, f.Offset, 0.1, "offset")
	if f.Points != 5 {
		t.Errorf("points = %d, want 5", f.Points)
	}
	if f.Exact {
		t.Error("five points is not the two-point case")
	}
	if math.Abs(f.MaxResidual) > 1e-9 {
		t.Errorf("max residual = %v on points that are exactly on a line", f.MaxResidual)
	}
}

// *** The check the whole design rests on. *** Fitting a line to a response
// that is not one is only safe because the residual says so; if this ever
// stops holding, the fit can hide having been the wrong model.
func TestCurvatureShowsUpAsResidual(t *testing.T) {
	var pts []ptcal.Point
	for _, w := range []float64{0, 5, 10, 15, 20} {
		// A gentle bow: nothing a person would spot in the numbers.
		pts = append(pts, ptcal.Point{Want: w, Got: w + 0.01*w*w})
	}

	f, err := ptcal.LinearFit(pts)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(f.MaxResidual) < 0.2 {
		t.Errorf("max residual = %v - curvature this large has to be visible", f.MaxResidual)
	}
	if f.RMSResidual <= 0 {
		t.Error("rms residual stayed at zero on a curved response")
	}
	// And it has to say where, so somebody can go and look at that end of the
	// range rather than at the whole of it.
	if f.MaxResidualAt != 0 && f.MaxResidualAt != 20 {
		t.Errorf("worst point at %v, want one of the extremes", f.MaxResidualAt)
	}
}

// Two points define a line, so zero residual is arithmetic, not evidence.
// Anything judging residuals has to be able to tell the two apart.
func TestTwoPointsAreMarkedExact(t *testing.T) {
	f, err := ptcal.LinearFit([]ptcal.Point{
		{Want: 1, Got: 1.1},
		{Want: 20, Got: 20.2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !f.Exact {
		t.Error("two points must be marked Exact")
	}
	// Zero to within float noise: two points always lie on their own line, so
	// this is arithmetic rather than a measurement that agreed.
	if math.Abs(f.MaxResidual) > 1e-12 {
		t.Errorf("max residual = %v, want ~0 - two points always lie on their own line",
			f.MaxResidual)
	}
}

func TestOnePointIsRefused(t *testing.T) {
	_, err := ptcal.LinearFit([]ptcal.Point{{Want: 10, Got: 10.1}})
	if !errors.Is(err, ptcal.ErrTooFewPoints) {
		t.Errorf("err = %v, want ErrTooFewPoints", err)
	}
}

// Every point at the same setting cannot produce a gain however many there
// are, and answering with one anyway would be a coefficient built on nothing.
func TestNoSpreadIsRefused(t *testing.T) {
	_, err := ptcal.LinearFit([]ptcal.Point{
		{Want: 10, Got: 10.1},
		{Want: 10, Got: 10.2},
		{Want: 10, Got: 10.0},
	})
	if !errors.Is(err, ptcal.ErrNoSpread) {
		t.Errorf("err = %v, want ErrNoSpread", err)
	}
}

// A point the operator skipped arrives as NaN. It must be dropped, not spread
// through the sums - a NaN fit would report nothing about what went wrong.
func TestSkippedPointsAreDropped(t *testing.T) {
	f, err := ptcal.LinearFit([]ptcal.Point{
		{Want: 1, Got: 1},
		{Want: 10, Got: math.NaN()},
		{Want: 20, Got: 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.Points != 2 {
		t.Errorf("points = %d, want 2 usable", f.Points)
	}
	near(t, f.Gain, 1, "gain")
	if math.IsNaN(f.Gain) || math.IsNaN(f.Offset) {
		t.Error("a skipped point leaked into the fit")
	}
}

// Correcting is the inverse of the fit: ask for what the board must be told so
// the right thing comes out. Getting this backwards is a calibration that
// doubles the error instead of cancelling it, and the numbers look plausible
// either way - which is why it is checked rather than read.
func TestCorrectInvertsTheFit(t *testing.T) {
	f, err := ptcal.LinearFit([]ptcal.Point{
		{Want: 0, Got: 0.5},
		{Want: 20, Got: 20.5},
	})
	if err != nil {
		t.Fatal(err)
	}
	// This board reads half a unit high everywhere, so to get 10 out it has to
	// be told 9.5.
	near(t, f.Correct(10), 9.5, "correct(10)")
	// And going back the other way lands where it started.
	near(t, f.Apply(f.Correct(10)), 10, "apply(correct(10))")
}

func TestIdealBoardNeedsNoCoefficients(t *testing.T) {
	good, err := ptcal.LinearFit([]ptcal.Point{
		{Want: 1, Got: 1.001},
		{Want: 20, Got: 20.002},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !good.Ideal(0.01, 0.05) {
		t.Errorf("gain %v offset %v should count as needing no correction",
			good.Gain, good.Offset)
	}

	bad, err := ptcal.LinearFit([]ptcal.Point{
		{Want: 1, Got: 1.5},
		{Want: 20, Got: 24},
	})
	if err != nil {
		t.Fatal(err)
	}
	if bad.Ideal(0.01, 0.05) {
		t.Errorf("gain %v offset %v must not count as ideal", bad.Gain, bad.Offset)
	}
}
