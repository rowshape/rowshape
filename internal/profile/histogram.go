package profile

import (
	"context"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/rowshape/rowshape/internal/fixture"
)

// histogramBuckets is the number of equi-depth buckets emitted for a skewed
// column. Downsampling the planner's ~100-bucket histogram keeps a fixture small
// (RFC §3.3) while preserving the skew.
const histogramBuckets = 16

// histogramSkewFactor is how non-uniform an equi-depth histogram must be before
// it is worth emitting. A uniform column's buckets are all about the same width;
// a skewed one has a few very wide buckets (sparse tail) and many narrow ones
// (dense body). Only skew earns a histogram — a summary statistic already
// captures a uniform column (RFC §6.2).
const histogramSkewFactor = 4.0

// measureHistogram emits an equi-depth histogram for a numeric column when — and
// only when — its distribution is skewed (RFC §6.2). The bounds are computed
// directly from the data with percentile_cont over the sample, NOT read from
// pg_stats.histogram_bounds: the planner's bounds exclude most_common_vals, so
// they would miss a value-concentration skew (one value owning most rows) — the
// canonical case §6.2 exists for. Bounds are real values, so ApplyPrivacy gates
// them at standard+ and drops them under strict (RFC §8.2).
func (r *reader) measureHistogram(ctx context.Context, from, column, typ string) (*fixture.Histogram, error) {
	bounds, err := r.equiDepthBounds(ctx, from, column, typ, histogramBuckets)
	if err != nil {
		return nil, err
	}
	if len(bounds) < 4 {
		return nil, nil // too few distinct values to describe a shape
	}
	if !isSkewed(bounds) {
		return nil, nil // uniform: mean/range already say everything
	}

	out := make([]any, len(bounds))
	for i, b := range bounds {
		out[i] = round6(b)
	}
	return &fixture.Histogram{Buckets: len(bounds) - 1, Bounds: out}, nil
}

// equiDepthBounds computes buckets+1 equi-depth bounds by asking Postgres for the
// value at each i/buckets quantile. Because it runs over the actual data, a
// value-concentration skew shows up as many quantiles landing on the same dense
// region — exactly the skew a histogram must capture.
func (r *reader) equiDepthBounds(ctx context.Context, from, column, typ string, buckets int) ([]float64, error) {
	fractions := make([]float64, buckets+1)
	for i := 0; i <= buckets; i++ {
		fractions[i] = float64(i) / float64(buckets)
	}
	c := pgx.Identifier{column}.Sanitize()
	// numericCast, not a bare ::float8: money cannot cast to a float directly, and a
	// money column would abort the pull here exactly as it did in rangeStat.
	q := "SELECT percentile_cont($1::float8[]) WITHIN GROUP (ORDER BY " + numericCast(c, typ) + ") FROM " + from + " WHERE " + c + " IS NOT NULL"

	var bounds []float64
	if err := r.tx.QueryRow(ctx, q, fractions).Scan(&bounds); err != nil {
		return nil, err
	}
	// Repeated bounds are kept: a run of equal bounds is the signature of a
	// dominant value and must survive so the skew is recorded.
	return bounds, nil
}

// isSkewed reports whether an equi-depth histogram's bucket widths vary enough to
// be worth capturing. Each bucket holds ~the same number of rows, so a wide
// bucket means a sparse value region and a narrow one a dense region; large
// width variation is exactly the skew a histogram exists to record.
func isSkewed(bounds []float64) bool {
	widths := make([]float64, 0, len(bounds)-1)
	for i := 1; i < len(bounds); i++ {
		w := bounds[i] - bounds[i-1]
		if w < 0 {
			w = -w
		}
		widths = append(widths, w)
	}
	sort.Float64s(widths)
	median := widths[len(widths)/2]
	maxW := widths[len(widths)-1]
	if median <= 0 {
		// A zero-width median means many identical bounds (a dominant value) —
		// that is itself strong skew.
		return maxW > 0
	}
	return maxW/median >= histogramSkewFactor
}
