package profile

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/profile/hll"
)

// hllDistinct estimates a column's distinct count by streaming it through a
// client-side HyperLogLog (RFC §7.3): each value is hashed into the sketch and
// discarded, so no values are retained (INV-NO-ROWS) and no server extension is
// needed. The result is a `measured` fact — a full pass over the data with a
// bounded, published error — which beats the `estimated` pg_stats value and can
// license a PASS under §7.4.
//
// This is the expensive path; the fast-mode profiler pays it only for the
// dangerous columns auto-escalation selects (P1b-T3).
func (r *reader) hllDistinct(ctx context.Context, schema, table, column string) (*fixture.Fact[int64], error) {
	from := pgx.Identifier{schema, table}.Sanitize()
	c := pgx.Identifier{column}.Sanitize()
	// Cast to text so every column type hashes through one code path. The query
	// streams from a server-side cursor; pgx reads rows incrementally, so the
	// emitter's memory stays bounded regardless of table size.
	q := "SELECT (" + c + ")::text FROM " + from + " WHERE " + c + " IS NOT NULL"

	rows, err := r.tx.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sketch := hll.New()
	// The number of values the sketch was actually shown. It is an EXACT upper bound
	// on the distinct count — n values cannot contain more than n distinct ones — and
	// it costs an increment, so there is no reason to publish an estimate that
	// violates it.
	var seen int64
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		sketch.AddString(v)
		seen++
		// v goes out of scope each iteration — no value is retained.
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// HyperLogLog errs in BOTH directions (~1.6% at precision 14), so on a
	// high-cardinality column its estimate routinely lands above the number of values
	// there are: a 40,000-row table came back as 41,305 distinct. That is not merely
	// imprecise, it is impossible, and a consumer computing selectivity as
	// distinct/rows gets a ratio above 1 — which can flip an index-usefulness or
	// fan-out judgement.
	//
	// Clamping to the observed count, not to the table's row count: the sketch only
	// saw non-NULL values, so this is the tighter bound and it is right even for a
	// mostly-null column.
	value := int64(sketch.Count())
	if value > seen {
		value = seen
	}

	// `error` is NOT cleared and the confidence stays `measured`. The clamp removes an
	// impossible reading; it does not turn an estimate into an exact count, and saying
	// otherwise would license a PASS this fact cannot support (§7.4). Nor does
	// value == seen imply uniqueness — INV-UNIQUENESS forbids inferring `unique` from
	// anything but an exact probe, and nothing here writes it.
	return &fixture.Fact[int64]{
		Value:      value,
		Confidence: fixture.Measured,
		Via:        "hll",
		Error:      round6(hll.RelativeError()),
	}, nil
}
