package hydrate

import (
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
)

func hugeFixture(rows int64, cols int) *fixture.Fixture {
	c := map[string]fixture.Column{}
	for i := 0; i < cols; i++ {
		c[string(rune('a'+i))] = fixture.Column{Type: "bigint"}
	}
	return &fixture.Fixture{
		RowshapeFixture: fixture.FormatVersion,
		Meta:            fixture.Meta{Engine: fixture.Engine{Name: "postgres", Version: "16"}},
		Tables: map[string]fixture.Table{
			"public.huge": {
				Rows:    fixture.Fact[int64]{Value: rows, Confidence: fixture.Exact},
				Columns: c,
			},
		},
	}
}

// TestHydrateRefusesAnImpossibleFixture is the regression for an OOM that named
// nothing useful.
//
// Generate builds the entire result in memory and target.Load makes another full
// copy, while validate and hydrate both default --max-rows to 0 — NO CAP. A
// fixture legitimately declaring rows: 1000000000 attempted a billion-row
// in-memory materialization by default.
func TestHydrateRefusesAnImpossibleFixture(t *testing.T) {
	_, err := Generate(hugeFixture(1_000_000_000, 5), Options{Seed: 1, Scale: 1})
	if err == nil {
		t.Fatal("a billion-row fixture must be refused, not attempted")
	}
	// The error has to be actionable: the projected size and the way out.
	for _, want := range []string{"5.0B", "--max-rows", "--scale"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must mention %q, got: %v", want, err)
		}
	}
	// It must also name the table responsible, so a 500-table fixture is diagnosable.
	if !strings.Contains(err.Error(), "public.huge") {
		t.Errorf("the refusal must name the largest table, got: %v", err)
	}
}

// Refusing rather than TRUNCATING is deliberate: silently capping would change
// the duration estimates and therefore the verdict, without telling anyone.
func TestBudgetRefusesRatherThanTruncating(t *testing.T) {
	f := hugeFixture(1_000_000_000, 5)
	res, err := Generate(f, Options{Seed: 1, Scale: 1})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if res != nil {
		t.Error("a refused hydrate must return no partial result")
	}
}

// The documented ways out must actually work.
func TestBudgetEscapeHatches(t *testing.T) {
	f := hugeFixture(1_000_000_000, 5)

	t.Run("--max-rows", func(t *testing.T) {
		if _, err := Generate(f, Options{Seed: 1, Scale: 1, MaxRows: 1000}); err != nil {
			t.Errorf("--max-rows must bring it under budget, got %v", err)
		}
	})
	t.Run("--scale", func(t *testing.T) {
		if _, err := Generate(f, Options{Seed: 1, Scale: 0.000001}); err != nil {
			t.Errorf("--scale must bring it under budget, got %v", err)
		}
	})
	t.Run("explicitly unbounded", func(t *testing.T) {
		// Negative means the caller has accepted the risk. Use a small fixture so
		// the test does not actually allocate gigabytes.
		if _, err := Generate(hugeFixture(10, 2), Options{Seed: 1, Scale: 1, MaxCells: -1}); err != nil {
			t.Errorf("MaxCells<0 must disable the guard, got %v", err)
		}
	})
}

// A realistic fixture must not trip the guard, or the default is wrong.
//
// checkBudget is called directly rather than through Generate: the point is the
// BUDGET DECISION, and actually materializing 40M cells to assert it makes the
// test take eleven seconds for no added confidence.
func TestBudgetDoesNotTripOnRealisticFixtures(t *testing.T) {
	f := hugeFixture(2_000_000, 20) // 40M cells, under the 50M default
	names := sortedKeys(f.Tables)
	counts := map[string]int64{"public.huge": 2_000_000}
	if err := checkBudget(f, names, counts, 0); err != nil {
		t.Errorf("a realistic fixture must pass the budget without flags, got %v", err)
	}
}

// An absurd declared count must not overflow into a negative and slip past.
func TestBudgetHandlesOverflow(t *testing.T) {
	f := hugeFixture(1<<62, 8)
	if _, err := Generate(f, Options{Seed: 1, Scale: 1}); err == nil {
		t.Error("an overflowing cell count must still be refused")
	}
}
