package profile

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/rowshape/rowshape/internal/fixture"
)

// Fast profiling constants. The sample is deterministic (a fixed REPEATABLE
// seed, or the whole of a small table), so a fixture's digest is stable across
// runs against an unchanged database (RFC §13).
const (
	sampleTargetRows = 20000 // rows a large-table TABLESAMPLE aims for
	sampleSeed       = 42    // fixed seed makes TABLESAMPLE reproducible
	valueSampleCap   = 500   // sampled text/json values pulled per column
)

// Fast reads structure (like ReadStructure) and then adds fast-mode column
// profiling: null fractions and distinct counts from pg_stats, numeric/temporal
// ranges and text/bytea length stats from a deterministic sample, and format
// classes inferred from sampled values (RFC §6, §7.3). Most facts land at
// `estimated`. Uniqueness is NEVER inferred from the sample (INV-UNIQUENESS).
func Fast(ctx context.Context, conn *pgx.Conn, opts Options) (*fixture.Fixture, error) {
	return read(ctx, conn, opts, true)
}

// profileTable augments an already-structured table with fast-mode column facts.
func (r *reader) profileTable(ctx context.Context, t tableRef, tbl *fixture.Table) error {
	stats, err := r.columnStats(ctx, t.schema, t.name)
	if err != nil {
		return err
	}
	rows := tbl.Rows.Value

	// In exact mode every stat is computed over the whole table; otherwise large
	// tables are sampled deterministically.
	from, _ := sampleClause(t.schema, t.name, t.reltuples)
	if r.exact {
		from = pgx.Identifier{t.schema, t.name}.Sanitize()

		// Exact mode scans the whole table anyway, so count it and record the row
		// count at `exact` instead of leaving the planner's estimate in place
		// (CR-T28). The upgrade happens ONLY here, on the full-pass branch: a
		// sampled, cost-capped or aborted pass must never produce `exact`, because
		// an exact row count is what lets a finding certify PASS.
		//
		// If the count fails the error propagates: silently keeping `estimated`
		// would be safe for the verdict but would hide a broken exact pass the
		// user is paying minutes-to-hours for.
		n, err := r.exactRowCount(ctx, t.schema, t.name)
		if err != nil {
			return err
		}
		tbl.Rows = fixture.Fact[int64]{Value: n, Confidence: fixture.Exact}
		rows = n
	}

	for name, col := range tbl.Columns {
		category := categorize(col.Type)

		// Exact mode: null counts are exact and distinct is measured via a full
		// HLL pass. Fast mode: both come from the planner's stats (estimated).
		if r.exact {
			if err := r.exactColumn(ctx, t.schema, t.name, name, &col); err != nil {
				return err
			}
		} else if st, ok := stats[name]; ok {
			// null_fraction is emitted only for nullable columns: a NOT NULL column
			// is structurally 0% null, while a *nullable* column at 0% null is the
			// load-bearing case §6.1 warns about (passes staging, fails prod). This
			// keeps a 200-table fixture committable (§3.3) without losing the fact
			// that matters. These facts carry no via — `estimated` already means
			// "from the planner's stats" — so they emit as compact bare scalars.
			if col.Nullable {
				nf := round6(st.nullFrac)
				col.NullFraction = &fixture.Fact[float64]{Value: nf, Confidence: fixture.Estimated}
			}
			if d, known := distinctFromStats(st.nDistinct, rows); known {
				col.Distinct = &fixture.Fact[int64]{Value: d, Confidence: fixture.Estimated}
			}
		}

		switch category {
		case "numeric", "temporal":
			// Numeric/temporal columns may carry a range (§6.2). Text and bytea
			// MUST NOT (§6.1) — that is why range is only reached here.
			rng, err := r.rangeStat(ctx, from, name, category)
			if err != nil {
				return err
			}
			col.Range = rng
			// A skewed numeric column also earns a histogram — the only field that
			// captures skew (§6.2). Privacy-gated at standard+ (§8.2).
			if category == "numeric" {
				hist, err := r.measureHistogram(ctx, from, name)
				if err != nil {
					return err
				}
				col.Histogram = hist
			}
		case "text":
			samples, err := r.valueSample(ctx, from, name)
			if err != nil {
				return err
			}
			col.Length = lengthStatsFromStrings(samples)
			d, known := distinctValue(col.Distinct)
			col.Format = inferTextFormat(samples, d, known)
			// Under permissive, gather a candidate value set + frequencies from
			// the sample. ApplyPrivacy makes the final call (k-threshold, §8.2);
			// nothing is gathered under standard/strict, so values can't leak.
			if r.privacy == PrivacyPermissive {
				col.Values, col.Frequencies = valueSetFromSample(samples)
				col.SampleN = len(samples)
			}
		case "bytea":
			// bytea gets length stats only, never a range (§6.1). opaque is the
			// honest format for opaque bytes.
			col.Length, err = r.byteaLengthStat(ctx, from, name)
			if err != nil {
				return err
			}
			col.Format = fmtOpaque
		case "json":
			samples, err := r.valueSample(ctx, from, name)
			if err != nil {
				return err
			}
			col.Format = fmtJSONBShape
			if strings.EqualFold(col.Type, "json") {
				col.Format = fmtJSON
			}
			col.Shape = jsonSkeleton(samples)
		case "uuid":
			col.Format = fmtUUID
		}

		switch {
		case r.exact:
			// Exact mode probes uniqueness for every column without a catalog proof
			// — the full treatment (RFC §7.3). The existence probe short-circuits on
			// the first duplicate, so a non-unique column is cheap; only a genuinely
			// unique column pays for a full scan.
			if col.Unique == nil {
				uf, err := r.probeUniqueExistence(ctx, t.schema, t.name, name)
				if err != nil {
					return err
				}
				col.Unique = uf
			}
		case shouldEscalate(col, rows):
			// Fast/targeted mode: auto-escalate a dangerous column — looks unique but
			// unproven — to a full pass, unless it is over the cost ceiling (RFC
			// §7.3 / §14.5, P1b-T3/T4).
			if r.overEscalationCap(rows) {
				r.warnf("skipped uniqueness escalation on %s.%s: table has ~%d rows, over the --max-escalation-rows cap of %d; leaving `unique` unproven (omitted) rather than full-scanning",
					t.qualified, name, rows, r.maxEscalationRows)
			} else {
				if err := r.escalateColumn(ctx, t.schema, t.name, name, &col); err != nil {
					return err
				}
				r.escalated = append(r.escalated, t.qualified+"."+name)
			}
		}

		tbl.Columns[name] = col
	}

	// Measure the fan-out distribution and orphan_fraction for every FK — the
	// moat fields that must be aggregated over data, not read from the catalog
	// (RFC §6.6, P1-T11).
	if err := r.measureReferences(ctx, t, tbl); err != nil {
		return err
	}

	// A partitioned parent declares its partition shape (RFC §14.2, P1-T12), and
	// its declared rows come from the partitions (the parent stores none itself).
	parts, err := r.measurePartitions(ctx, t)
	if err != nil {
		return err
	}
	tbl.Partitions = parts
	if parts != nil {
		total, err := r.partitionTotalRows(ctx, t.oid)
		if err != nil {
			return err
		}
		tbl.Rows = fixture.Fact[int64]{Value: total, Confidence: fixture.Estimated}
	}
	return nil
}

// colStat holds the pg_stats facts used in fast mode.
type colStat struct {
	nullFrac  float64
	nDistinct float64
}

// columnStats reads null_frac and n_distinct for every column of a table from
// pg_stats. Reading the planner's stats requires no scan of user data.
func (r *reader) columnStats(ctx context.Context, schema, table string) (map[string]colStat, error) {
	const q = `SELECT attname, null_frac, n_distinct FROM pg_stats WHERE schemaname = $1 AND tablename = $2`
	rows, err := r.tx.Query(ctx, q, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]colStat{}
	for rows.Next() {
		var name string
		var st colStat
		if err := rows.Scan(&name, &st.nullFrac, &st.nDistinct); err != nil {
			return nil, err
		}
		out[name] = st
	}
	return out, rows.Err()
}

// rangeStat computes min/max (and, for numeric, mean) over the sample. All are
// read as aggregates — no row values enter the profiler (INV-NO-ROWS).
func (r *reader) rangeStat(ctx context.Context, from, col, category string) (*fixture.Range, error) {
	c := pgx.Identifier{col}.Sanitize()
	if category == "numeric" {
		q := fmt.Sprintf(`SELECT min((%s)::double precision), max((%s)::double precision), avg((%s)::double precision) FROM %s`, c, c, c, from)
		var lo, hi, mean *float64
		if err := r.tx.QueryRow(ctx, q).Scan(&lo, &hi, &mean); err != nil {
			return nil, err
		}
		if lo == nil && hi == nil {
			return nil, nil
		}
		rng := &fixture.Range{}
		if mean != nil {
			m := round6(*mean)
			rng.Mean = &m
		}
		if lo != nil {
			rng.Min = *lo
		}
		if hi != nil {
			rng.Max = *hi
		}
		return rng, nil
	}
	// temporal: min/max only (RFC §6.1 temporal range carries no mean).
	q := fmt.Sprintf(`SELECT min(%s), max(%s) FROM %s`, c, c, from)
	var lo, hi *time.Time
	if err := r.tx.QueryRow(ctx, q).Scan(&lo, &hi); err != nil {
		return nil, err
	}
	if lo == nil && hi == nil {
		return nil, nil
	}
	rng := &fixture.Range{}
	if lo != nil {
		rng.Min = lo.UTC().Format(time.RFC3339)
	}
	if hi != nil {
		rng.Max = hi.UTC().Format(time.RFC3339)
	}
	return rng, nil
}

// byteaLengthStat computes octet-length statistics for a bytea column. Only
// length is permitted — never a value range (§6.1).
func (r *reader) byteaLengthStat(ctx context.Context, from, col string) (*fixture.Length, error) {
	c := pgx.Identifier{col}.Sanitize()
	q := fmt.Sprintf(`SELECT min(octet_length(%s)), max(octet_length(%s)), avg(octet_length(%s)) FROM %s`, c, c, c, from)
	var lo, hi *int64
	var mean *float64
	if err := r.tx.QueryRow(ctx, q).Scan(&lo, &hi, &mean); err != nil {
		return nil, err
	}
	if lo == nil && hi == nil && mean == nil {
		return nil, nil
	}
	return &fixture.Length{Min: lo, Max: hi, Mean: mean}, nil
}

// valueSample pulls a bounded sample of non-null values for a text/json column,
// cast to text so it scans cleanly. The values are used transiently to classify
// format and build JSON skeletons, then discarded — they never leave as values
// (RFC §13 sampled SELECT; INV-NO-ROWS).
func (r *reader) valueSample(ctx context.Context, from, col string) ([]string, error) {
	c := pgx.Identifier{col}.Sanitize()
	q := fmt.Sprintf(`SELECT (%s)::text FROM %s WHERE %s IS NOT NULL LIMIT %d`, c, from, c, valueSampleCap)
	rows, err := r.tx.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// sampleClause returns the FROM target for sampling. Large tables use a
// deterministic TABLESAMPLE (fixed seed); small tables are read whole so their
// aggregates are exact-over-sample and order-independent.
func sampleClause(schema, table string, reltuples float64) (string, bool) {
	qt := pgx.Identifier{schema, table}.Sanitize()
	if reltuples > float64(sampleTargetRows) {
		p := 100.0 * float64(sampleTargetRows) / reltuples
		if p < 0.01 {
			p = 0.01
		}
		return fmt.Sprintf("%s TABLESAMPLE SYSTEM (%s) REPEATABLE (%d)", qt, strconvFloat(p), sampleSeed), true
	}
	return qt, false
}

// strconvFloat formats a sampling percentage compactly and locale-independently.
func strconvFloat(f float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.4f", f), "0"), ".")
}

// round6 rounds to 6 significant-ish decimal places, clearing float32 noise
// from pg_stats values without affecting real precision at fraction scale.
func round6(f float64) float64 {
	return math.Round(f*1e6) / 1e6
}

// distinctFromStats converts pg_stats.n_distinct into an absolute distinct
// count. A positive value is absolute; a negative value is a ratio of the row
// count; zero means "unknown" and yields no fact.
func distinctFromStats(nDistinct float64, rows int64) (int64, bool) {
	switch {
	case nDistinct == 0:
		return 0, false
	case nDistinct > 0:
		return int64(math.Round(nDistinct)), true
	default:
		d := int64(math.Round(-nDistinct * float64(rows)))
		if d < 0 {
			d = 0
		}
		return d, true
	}
}

// distinctValue unpacks an optional distinct fact for format inference.
func distinctValue(f *fixture.Fact[int64]) (int64, bool) {
	if f == nil {
		return 0, false
	}
	return f.Value, true
}

// valueSetFromSample derives a candidate value set and parallel frequencies from
// a sample, for low-cardinality columns under permissive privacy. Values are
// sorted for a deterministic, stable digest (RFC §11). Frequencies are the
// sample proportions (estimates of the true frequency). It returns nil when the
// column has too many distinct values to be a value set — the k-threshold and
// the distinct<=50 gate are enforced later by ApplyPrivacy.
func valueSetFromSample(samples []string) ([]string, []float64) {
	if len(samples) == 0 {
		return nil, nil
	}
	counts := map[string]int{}
	for _, v := range samples {
		counts[v]++
	}
	if len(counts) > permissiveMaxDistinct {
		return nil, nil
	}
	values := make([]string, 0, len(counts))
	for v := range counts {
		values = append(values, v)
	}
	sort.Strings(values)
	freqs := make([]float64, len(values))
	for i, v := range values {
		freqs[i] = round6(float64(counts[v]) / float64(len(samples)))
	}
	return values, freqs
}

// lengthStatsFromStrings computes character-length min/max/mean/p95 over a
// sample of strings.
func lengthStatsFromStrings(vals []string) *fixture.Length {
	if len(vals) == 0 {
		return nil
	}
	lengths := make([]int, 0, len(vals))
	sum := 0
	for _, v := range vals {
		n := utf8.RuneCountInString(v)
		lengths = append(lengths, n)
		sum += n
	}
	sort.Ints(lengths)
	min64 := int64(lengths[0])
	max64 := int64(lengths[len(lengths)-1])
	mean := round6(float64(sum) / float64(len(lengths)))
	p95 := int64(percentile(lengths, 0.95))
	return &fixture.Length{Min: &min64, Max: &max64, Mean: &mean, P95: &p95}
}

// percentile returns the value at quantile q of a sorted int slice (nearest-rank).
func percentile(sorted []int, q float64) int {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(q*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// categorize maps a Postgres type name (from format_type) onto the profiling
// category that decides which facts are legal — crucially, text and bytea are
// separated out so a range can never be computed for them (§6.1).
func categorize(typ string) string {
	t := strings.ToLower(strings.TrimSpace(typ))
	switch {
	case t == "bytea":
		return "bytea"
	case t == "json" || t == "jsonb":
		return "json"
	case t == "uuid":
		return "uuid"
	case t == "boolean":
		return "bool"
	case strings.HasPrefix(t, "timestamp") || t == "date" || strings.HasPrefix(t, "time"):
		return "temporal"
	case t == "text" || strings.Contains(t, "char") || strings.Contains(t, "varying") || t == "citext" || t == "name":
		return "text"
	case t == "smallint" || t == "integer" || t == "bigint" || t == "real" ||
		t == "double precision" || strings.HasPrefix(t, "numeric") ||
		strings.HasPrefix(t, "decimal") || t == "money" || t == "smallserial" ||
		t == "serial" || t == "bigserial":
		return "numeric"
	default:
		return "other"
	}
}
