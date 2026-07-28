// Package estimate extrapolates a migration operation's cost to production scale
// along a known, engine-version-conditional cost model (RFC §9.1/§9.2).
//
// The load-bearing rules (INV-DURATIONS-BUCKETS):
//
//   - Extrapolation is model-based, not linear. Each operation class carries a
//     cost model (O(1), O(n), O(n log n)); naive linear scaling is wrong and
//     will be caught (RFC §9.1).
//   - Declared rows drive extrapolation — the rows the operation will touch in
//     production, NOT the count a developer hydrated at --scale (RFC §9).
//   - Cost models are engine-version-conditional, which is why
//     meta.engine.version is mandatory. Extrapolation REFUSES when the version
//     is absent rather than assume a recent default (RFC §9.1, versioned.go).
//   - Estimates are reported as buckets with the basis attached, never as point
//     estimates (RFC §9.2), and the estimate's confidence never exceeds the
//     confidence of `rows` (RFC §7.4).
package estimate

import (
	"math"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/verdict"
)

// Model is the cost-growth model an operation scales along (RFC §9.1). Its
// string value is what a verdict Estimate reports in `model`.
type Model string

const (
	// Constant is O(1): a catalog-only change whose cost does not grow with rows.
	Constant Model = "constant"
	// Linear is O(n): a table rewrite or a full-table validation scan.
	Linear Model = "linear"
	// NLogN is O(n log n): a b-tree index build.
	NLogN Model = "n_log_n"
)

// OpClass is a migration operation's cost class (RFC §9.1 table). The class is
// known statically from the operation; for the version-conditional operations
// (ADD COLUMN ... DEFAULT) it is resolved against the engine version first
// (versioned.go).
type OpClass int

const (
	// CatalogOnly is a metadata-only change: RENAME, DROP without rewrite, or a
	// PG11+ non-volatile DEFAULT fast-pathed into the catalog. O(1).
	CatalogOnly OpClass = iota
	// TableRewrite rewrites every row: ADD COLUMN with a volatile default, a type
	// change, or a pre-11 DEFAULT add. O(n), ACCESS EXCLUSIVE for the rewrite.
	TableRewrite
	// ConstraintValidation is a full-table scan validating a constraint, incl.
	// SET NOT NULL (which full-scans regardless of version). O(n).
	ConstraintValidation
	// BTreeBuild builds a b-tree index and holds an exclusive lock for the build.
	// O(n log n).
	BTreeBuild
	// IndexConcurrently builds an index in two passes and holds NO exclusive lock
	// (CREATE INDEX CONCURRENTLY). O(n log n), ~2x the single-pass work.
	IndexConcurrently
)

// Model returns the cost-growth model of an operation class (RFC §9.1).
func (o OpClass) Model() Model {
	switch o {
	case CatalogOnly:
		return Constant
	case TableRewrite, ConstraintValidation:
		return Linear
	case BTreeBuild, IndexConcurrently:
		return NLogN
	default:
		return Linear
	}
}

// HoldsExclusiveLock reports whether the operation holds an ACCESS EXCLUSIVE lock
// for its duration. CREATE INDEX CONCURRENTLY does not; that is the whole reason
// it exists (RFC §9.1). Catalog-only changes take the lock only momentarily.
func (o OpClass) HoldsExclusiveLock() bool {
	switch o {
	case IndexConcurrently, CatalogOnly:
		return false
	default:
		return true
	}
}

// passFactor scales the measured basis for operations that do more work than a
// single pass. CREATE INDEX CONCURRENTLY scans the table twice (RFC §9.1).
func (o OpClass) passFactor() float64 {
	if o == IndexConcurrently {
		return 2
	}
	return 1
}

// Extrapolate scales a measured basis (basisRows rewritten/scanned in basisMs)
// to declaredRows along the operation's cost model and returns a verdict Estimate
// carrying the duration bucket AND the basis, never a point estimate (RFC §9.2).
//
// rowsConfidence is the confidence of the fixture's `rows` fact. The estimate's
// confidence is the weaker of `estimated` (an extrapolation is never better than
// estimated — reality is non-linear in ways a fixture cannot capture) and
// rowsConfidence, so it never exceeds the confidence of `rows` (RFC §7.4/§9.2).
func Extrapolate(op OpClass, basisRows, basisMs, declaredRows int64, rowsConfidence fixture.Confidence) verdict.Estimate {
	predicted := predictMs(op.Model(), basisRows, basisMs, declaredRows) * op.passFactor()
	return verdict.Estimate{
		Bucket:       Bucket(predicted),
		Model:        string(op.Model()),
		BasisRows:    basisRows,
		BasisMs:      basisMs,
		DeclaredRows: declaredRows,
		Confidence:   string(fixture.Min(fixture.Estimated, rowsConfidence)),
	}
}

// predictMs projects the basis measurement to declaredRows along the model. A
// non-positive or absent basis yields 0 (nothing measured to scale). Catalog-only
// (constant) work does not grow with rows, so it stays at the basis cost.
func predictMs(m Model, basisRows, basisMs, declaredRows int64) float64 {
	if basisMs <= 0 {
		return 0
	}
	if declaredRows <= 0 {
		return float64(basisMs)
	}
	switch m {
	case Constant:
		return float64(basisMs)
	case NLogN:
		if basisRows <= 1 {
			// No usable basis width; fall back to linear on declared rows.
			return float64(basisMs) * float64(declaredRows)
		}
		basisWork := float64(basisRows) * math.Log2(float64(basisRows))
		declWork := float64(declaredRows) * math.Log2(math.Max(2, float64(declaredRows)))
		return float64(basisMs) * (declWork / basisWork)
	default: // Linear
		if basisRows <= 0 {
			return float64(basisMs) * float64(declaredRows)
		}
		return float64(basisMs) * (float64(declaredRows) / float64(basisRows))
	}
}

// reindexBytesPerMs is a conservative index-rebuild throughput (~50 MB/s) used
// to bucket a REINDEX from the index's on-disk size, since a rebuild's cost is
// dominated by writing the index out (RFC §6.5). It is a defensible
// order-of-magnitude constant, not a stopwatch.
// The .0 is load-bearing: written as `50 * 1024 * 1024 / 1000` this is an
// untyped INTEGER constant expression, so the division truncates to 52428
// rather than 52428.8 — a silent 0.0015% error in a constant whose whole
// purpose is to be a defensible round number.
const reindexBytesPerMs = 50 * 1024 * 1024 / 1000.0

// BucketFromBytes buckets an index rebuild by its on-disk size in bytes — the
// basis for a REINDEX, whose work scales with the (possibly bloated) index size
// rather than a row count.
func BucketFromBytes(bytes int64) string {
	if bytes <= 0 {
		return verdict.BucketInstant
	}
	return Bucket(float64(bytes) / reindexBytesPerMs)
}

// Bucket classifies a predicted duration in milliseconds into the five verdict
// duration buckets (RFC §9.2): instant <100ms, fast <1s, noticeable 1–10s,
// slow 10–60s, outage >60s.
func Bucket(ms float64) string {
	switch {
	case ms < 100:
		return verdict.BucketInstant
	case ms < 1_000:
		return verdict.BucketFast
	case ms < 10_000:
		return verdict.BucketNoticeable
	case ms < 60_000:
		return verdict.BucketSlow
	default:
		return verdict.BucketOutage
	}
}
