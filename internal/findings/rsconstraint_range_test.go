package findings

import (
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/verdict"
)

// rangeFixture builds a table whose column carries the given extremes at the
// given confidence.
func rangeFixture(conf fixture.Confidence, min, max any) *fixture.Fixture {
	return &fixture.Fixture{
		Meta: fixture.Meta{ID: "t", Engine: fixture.Engine{Name: "postgres", Version: "16"}},
		Tables: map[string]fixture.Table{
			"app.orders": {
				Rows: fixture.Fact[int64]{Value: 60000, Confidence: fixture.Estimated},
				Columns: map[string]fixture.Column{
					"customer_id": {Type: "bigint", Range: &fixture.Range{Min: min, Max: max, Confidence: conf}},
				},
			},
		},
	}
}

// TestSampledRangeCannotCertifyAbsence is the wrong-PASS regression, and it is the
// case ordinary capping cannot reach: capping caps findings that EXIST, but here
// the weak fact makes the finding NOT EXIST, and a missing finding is a PASS
// nothing downstream can touch.
//
// Reproduced live: a column whose true maximum was 60,000 was recorded from a
// TABLESAMPLE as 59,773, so `CHECK (customer_id <= 59900)` looked satisfiable, no
// finding was emitted, and the verdict was PASS — while the source database
// refused the statement outright.
func TestSampledRangeCannotCertifyAbsence(t *testing.T) {
	f := rangeFixture(fixture.Estimated, 1, 59920)
	got, ok := checkConflict(f, "app.orders", "customer_id <= 59950")
	if !ok {
		t.Fatal("no finding for a bound the sampled extremes cannot rule out; this is the wrong PASS")
	}
	if got.Severity != verdict.SeverityWarn {
		t.Errorf("severity = %q, want warn — nothing proves a conflict, but nothing rules one out either", got.Severity)
	}
	if !strings.Contains(got.Detail, "--exact") {
		t.Errorf("detail does not name the command that resolves it: %s", got.Detail)
	}
	if len(got.DependsOn) != 1 || !strings.HasSuffix(got.DependsOn[0], ".range") {
		t.Errorf("depends_on = %v, want the range it actually read", got.DependsOn)
	}
}

// TestExactRangeCertifiesAbsence: an exact range is a real answer. Warning on it
// too would penalize precisely the fixtures that did the expensive thing right,
// and would make the warning meaningless.
func TestExactRangeCertifiesAbsence(t *testing.T) {
	f := rangeFixture(fixture.Exact, 1, 59920)
	if _, ok := checkConflict(f, "app.orders", "customer_id <= 59950"); ok {
		t.Error("exact extremes produced a finding for a bound they positively rule out")
	}
}

// TestSampledRangeStillReportsARealConflict: when the SAMPLED extremes already
// violate the constraint, that is proof, not a doubt — the sample is a lower bound
// on the spread, so a violation it found is real. It must stay a FAIL.
func TestSampledRangeStillReportsARealConflict(t *testing.T) {
	f := rangeFixture(fixture.Estimated, 1, 59920)
	got, ok := checkConflict(f, "app.orders", "customer_id <= 59900")
	if !ok {
		t.Fatal("no finding for extremes that already violate the bound")
	}
	if got.Severity != verdict.SeverityError {
		t.Errorf("severity = %q, want error — the sample already found a violating row", got.Severity)
	}
}

// TestSampledRangeHasNoSafeDirection: there is no "comfortably outside the range
// so it must be fine" escape, and that is deliberate. A sampled MINIMUM can only
// be too HIGH, so even `customer_id >= 0` against a sampled minimum of 5 may be
// violated by a lower row the sample never saw. A sample gives no bound on how far
// past its extremes the real data goes, so any threshold rule would be a guess
// dressed up as one — the class of reasoning INV-CONFIDENCE-CAPPING forbids.
func TestSampledRangeHasNoSafeDirection(t *testing.T) {
	f := rangeFixture(fixture.Estimated, 5, 59920)
	got, ok := checkConflict(f, "app.orders", "customer_id >= 0")
	if !ok {
		t.Fatal("a sampled minimum was treated as ruling out a lower bound; it cannot")
	}
	if got.Severity != verdict.SeverityWarn {
		t.Errorf("severity = %q, want warn", got.Severity)
	}
}

// TestRangeWithoutConfidenceIsNotTreatedAsSampled: a fixture written before the
// field carries no confidence. It must not be read as `estimated` and warned about
// wholesale — the capping engine reads such a range as `absent`, which already
// prevents it licensing a PASS through the normal path.
func TestRangeWithoutConfidenceIsNotTreatedAsSampled(t *testing.T) {
	f := rangeFixture("", 1, 59920)
	if _, ok := checkConflict(f, "app.orders", "customer_id <= 59950"); ok {
		t.Error("a range with no recorded confidence was treated as sampled")
	}
}
