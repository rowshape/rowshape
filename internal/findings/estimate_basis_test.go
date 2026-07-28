package findings

import (
	"testing"

	"github.com/rowshape/rowshape/internal/estimate"
	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/validate"
)

// TestEstimateForRefusesWithoutAMeasuredBasis pins that a missing row count on
// the hydrated path produces NO estimate rather than a fast one.
//
// The fallback `if rows1 <= 0 { rows1 = declaredRows }` was unconditional. On
// the ephemeral path that made the linear ratio exactly 1.0, so the prediction
// equalled ms1 — which defaults to 1ms — and a rewrite of a 50M-row table was
// reported as `instant`. That is the same failure the tableKnown gate above it
// exists to prevent: it closed the "table missing from the fixture" door while
// this left the "row count missing from the capture" door open.
func TestEstimateForRefusesWithoutAMeasuredBasis(t *testing.T) {
	const declared = 50_000_000

	t.Run("hydrated path, table absent from the capture: no estimate", func(t *testing.T) {
		c := &validate.Capture{
			// Populated => hydrated path. "public.other" was hydrated; the table
			// under analysis was not.
			TableRows:  map[string]int64{"public.other": 1000},
			Statements: []validate.Statement{{SQL: "ALTER TABLE t ...", DurationMs: 1}},
		}
		got := estimateFor(c, 0, estimate.TableRewrite, "public.t", declared, fixture.Exact, true, true)
		if got != nil {
			t.Errorf("want no estimate without a measured basis, got %+v — a 50M-row rewrite must never report from a 1ms hydrated run", got)
		}
	})

	t.Run("ground-truth path, empty capture map: declared rows are real", func(t *testing.T) {
		c := &validate.Capture{
			// Empty => --target: the fixture's declared rows ARE the rows the
			// statement ran against, so the fallback is sound here.
			TableRows:  nil,
			Statements: []validate.Statement{{SQL: "ALTER TABLE t ...", DurationMs: 250}},
		}
		got := estimateFor(c, 0, estimate.TableRewrite, "public.t", declared, fixture.Exact, true, true)
		if got == nil {
			t.Fatal("the ground-truth path has a real basis and must still produce an estimate")
		}
	})

	t.Run("hydrated path with a real basis still estimates", func(t *testing.T) {
		c := &validate.Capture{
			TableRows:  map[string]int64{"public.t": 10_000},
			Statements: []validate.Statement{{SQL: "ALTER TABLE t ...", DurationMs: 100}},
		}
		got := estimateFor(c, 0, estimate.TableRewrite, "public.t", declared, fixture.Exact, true, true)
		if got == nil {
			t.Fatal("a measured basis must produce an estimate")
		}
		if got.Bucket == "instant" {
			t.Errorf("extrapolating 10k rows/100ms to %d rows must not be instant, got %+v", declared, got)
		}
	})
}

// TestDegenerateBasisProducesNoEstimate: hydratedRowCount floors to 1 row, so at
// a small --scale a table hydrates to a handful of rows. Extrapolating 50M
// declared rows from a 1-row basis is a 50,000,000x ratio applied to a
// measurement that is entirely noise — and it landed on `outage` for EVERY table.
// Measured before the fix: basisRows=1 -> outage, basisRows=10 -> outage.
//
// A tool that reports every migration as an outage below some scale is not being
// cautious, it is being useless: it teaches people the estimate means nothing.
func TestDegenerateBasisProducesNoEstimate(t *testing.T) {
	const declared = 50_000_000
	for _, basis := range []int64{1, 10, 100} {
		c := &validate.Capture{
			TableRows:  map[string]int64{"public.t": basis},
			Statements: []validate.Statement{{SQL: "ALTER TABLE t ...", DurationMs: 5}},
		}
		if est := estimateFor(c, 0, estimate.TableRewrite, "public.t", declared, fixture.Exact, true, true); est != nil {
			t.Errorf("basisRows=%d extrapolated to %d must produce NO estimate, got bucket %q",
				basis, declared, est.Bucket)
		}
	}
}

// A basis that IS large enough must still estimate, or the guard has swallowed
// the feature it was meant to protect.
func TestAdequateBasisStillEstimates(t *testing.T) {
	c := &validate.Capture{
		TableRows:  map[string]int64{"public.t": 50_000},
		Statements: []validate.Statement{{SQL: "ALTER TABLE t ...", DurationMs: 120}},
	}
	if est := estimateFor(c, 0, estimate.TableRewrite, "public.t", 5_000_000, fixture.Exact, true, true); est == nil {
		t.Error("a 50k-row basis extrapolated 100x is a reasonable estimate and must be produced")
	}
}

// Nothing MEASURED means no basis. DurationMs <= 0 used to default to 1ms, which
// dressed a non-measurement up as a measurement and then scaled it by the row
// ratio.
func TestUnmeasuredStatementProducesNoEstimate(t *testing.T) {
	c := &validate.Capture{
		TableRows:  map[string]int64{"public.t": 50_000},
		Statements: []validate.Statement{{SQL: "ALTER TABLE t ...", DurationMs: 0}},
	}
	if est := estimateFor(c, 0, estimate.TableRewrite, "public.t", 5_000_000, fixture.Exact, true, true); est != nil {
		t.Errorf("an unmeasured statement has no basis; got bucket %q", est.Bucket)
	}
}
