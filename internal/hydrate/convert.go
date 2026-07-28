package hydrate

import "math"

// toI64 converts a float to int64 with a DEFINED result for every input.
//
// Go's spec leaves float-to-int conversion "implementation-specific" when the
// value is out of the target type's range, and the implementations genuinely
// differ: amd64 yields INT64_MIN, arm64 saturates to MaxInt64. That is the same
// architecture-divergence family as the FMA-fusion bug the determinism-matrix CI
// job was built for — INV-DETERMINISM promises byte-identical hydrate output on
// ANY platform, and a bare int64(f) silently breaks that promise the moment a
// fixture carries a value big enough to overflow.
//
// The inputs here are fixture-supplied and therefore not trustworthy: Scale is
// unbounded above, histogram bounds are read straight from the document, and
// geomLerp can overflow to +Inf on a fixture whose p95 is far above its p50.
//
// The mapping is deliberately total and boring:
//
//	NaN        -> 0            (no meaningful magnitude; 0 is the neutral row count)
//	+Inf, >max -> MaxInt64     (saturate rather than wrap)
//	-Inf, <min -> MinInt64
//
// Saturation is the right direction for the callers: every one of them feeds a
// count or a bound that is subsequently clamped to a sane range, so a saturated
// value lands at the clamp's edge instead of wrapping to a wildly wrong sign.
func toI64(f float64) int64 {
	switch {
	case math.IsNaN(f):
		return 0
	case f >= math.MaxInt64:
		// >= rather than >: float64 cannot represent MaxInt64 exactly, and the
		// nearest representable value is above it, so the boundary itself is
		// already out of range.
		return math.MaxInt64
	case f <= math.MinInt64:
		return math.MinInt64
	default:
		return int64(f)
	}
}
