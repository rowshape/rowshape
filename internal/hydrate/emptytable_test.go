package hydrate

import (
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
)

// TestEmptyTableHydratesEmpty: emptiness is a fact the fixture recorded, and
// overriding it invented a row that does not exist. The consequence was a wrong
// FAIL on the most common safe migration there is — `ALTER TABLE <empty> ADD
// COLUMN ... NOT NULL` succeeds in production precisely because there are no rows
// to fail, and returned FAIL here.
func TestEmptyTableHydratesEmpty(t *testing.T) {
	if got := hydratedRowCount(0, 1.0, 0); got != 0 {
		t.Errorf("a table declared rows:0 hydrated %d row(s), want 0", got)
	}
	if got := hydratedRowCount(0, 1.0, 50); got != 0 {
		t.Errorf("with --max-rows, empty table hydrated %d row(s), want 0", got)
	}
	// A negative declared count is not a row count either.
	if got := hydratedRowCount(-1, 1.0, 0); got != 0 {
		t.Errorf("negative declared count hydrated %d row(s), want 0", got)
	}
}

// TestNonEmptyTableStillFloorsAtOne preserves the original intent: `--scale 0.001`
// against a small table must still exercise it rather than rounding to nothing.
func TestNonEmptyTableStillFloorsAtOne(t *testing.T) {
	if got := hydratedRowCount(100, 0.001, 0); got != 1 {
		t.Errorf("scaled-to-zero non-empty table hydrated %d row(s), want 1", got)
	}
	if got := hydratedRowCount(1, 1.0, 0); got != 1 {
		t.Errorf("single-row table hydrated %d row(s), want 1", got)
	}
}

// TestEmptyTableEmitsNoInsert: the row count is only half of it — the generated
// table must carry no rows, so no INSERT is written and COPY sends nothing.
func TestEmptyTableEmitsNoInsert(t *testing.T) {
	f := &fixture.Fixture{
		Tables: map[string]fixture.Table{
			"app.audit_scratch": {
				Rows: fixture.Fact[int64]{Value: 0},
				Columns: map[string]fixture.Column{
					"id":   {Type: "integer"},
					"note": {Type: "text", Nullable: true},
				},
			},
		},
	}
	res, err := Generate(f, Options{Seed: 1})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if n := len(res.Tables[0].Rows); n != 0 {
		t.Fatalf("empty table generated %d row(s): %v", n, res.Tables[0].Rows)
	}

	// And the plan must not claim rows it will not create.
	for _, p := range PlanRows(f, Options{Seed: 1}) {
		if p.Hydrated != 0 {
			t.Errorf("plan claims %d hydrated row(s) for an empty table", p.Hydrated)
		}
	}
}
