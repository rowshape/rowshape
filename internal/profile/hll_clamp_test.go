package profile

import (
	"context"
	"testing"
)

const clampSchema = "rowshape_clamp_test"

// TestHLLDistinctNeverExceedsObservedValues: HyperLogLog errs in BOTH directions
// (~1.6% at precision 14), so on a high-cardinality column its estimate routinely
// lands above the number of values there are — a 40,000-row table came back as
// 41,305 distinct. That is not imprecise, it is impossible, and a consumer
// computing selectivity as distinct/rows gets a ratio above 1.
//
// A fully-distinct column is the case that provokes it, so that is what this
// seeds: every value unique, which puts the true cardinality exactly at the bound
// the estimate must not cross.
func TestHLLDistinctNeverExceedsObservedValues(t *testing.T) {
	conn := adminConn(t)
	ctx := context.Background()

	const rows = 40000
	stmts := []string{
		`DROP SCHEMA IF EXISTS ` + clampSchema + ` CASCADE`,
		`CREATE SCHEMA ` + clampSchema,
		`CREATE TABLE ` + clampSchema + `.t (id bigint NOT NULL, maybe bigint)`,
		// `maybe` is half NULL, so the clamp must use the values the sketch actually
		// saw rather than the table's row count.
		`INSERT INTO ` + clampSchema + `.t
		   SELECT g, CASE WHEN g % 2 = 0 THEN g ELSE NULL END FROM generate_series(1, 40000) g`,
		`ANALYZE ` + clampSchema + `.t`,
	}
	for _, s := range stmts {
		if _, err := conn.Exec(ctx, s); err != nil {
			t.Fatalf("seed (%s): %v", s, err)
		}
	}
	t.Cleanup(func() { _, _ = conn.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+clampSchema+` CASCADE`) })

	r := &reader{tx: conn}
	for _, tc := range []struct {
		column string
		bound  int64
	}{
		{"id", rows},
		{"maybe", rows / 2}, // only the non-NULL half reaches the sketch
	} {
		fact, err := r.hllDistinct(ctx, clampSchema, "t", tc.column)
		if err != nil {
			t.Fatalf("hllDistinct(%s): %v", tc.column, err)
		}
		if fact.Value > tc.bound {
			t.Errorf("%s: distinct = %d, which exceeds the %d values the sketch saw — an impossible fixture",
				tc.column, fact.Value, tc.bound)
		}
		// The clamp removes an impossible reading; it does not turn an estimate into
		// an exact count. Saying otherwise would license a PASS this fact cannot
		// support (§7.4).
		if fact.Confidence != "measured" {
			t.Errorf("%s: confidence = %q, want measured", tc.column, fact.Confidence)
		}
		if fact.Error == 0 {
			t.Errorf("%s: error dropped by the clamp; the uncertainty is still real", tc.column)
		}
	}
}
