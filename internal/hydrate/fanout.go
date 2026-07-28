package hydrate

import (
	"math"

	"github.com/rowshape/rowshape/internal/fixture"
)

// assignForeignKeys returns, for each of childN child rows, the parent ordinal
// (in [0, parentN)) it references. The assignment reproduces the fan-out
// DISTRIBUTION shape — its p50/p95/max children-per-parent — not merely the mean
// (RFC §6.6). A hydrator that only matched the mean would turn a long-tailed
// cascade into a uniform one and miss exactly the migration pathology fan-out
// exists to capture.
//
// declaredChild is the child table's PRODUCTION row count. It is needed because
// `max` is a count of children, and at --scale 0.01 there are a hundredth as many
// children to give away: a whale owning 40% of production's rows still owns 40%
// of the hydrated ones, which is 40% of a much smaller number.
func assignForeignKeys(seed int64, table string, ref fixture.Reference, childN, parentN, declaredChild int64) []int64 {
	out := make([]int64, childN)
	if childN <= 0 {
		return out
	}
	if parentN <= 0 {
		return out // no parents to point at; leave zeros
	}
	if ref.Fanout == nil {
		// No fan-out fact: spread children roughly uniformly across parents.
		for i := int64(0); i < childN; i++ {
			out[i] = i % parentN
		}
		return out
	}

	counts := targetCounts(parentN, childN, declaredChild, ref.Fanout)
	orphanGroups := orphanGroupCount(counts, childN, ref.OrphanFraction)

	// Fill child slots parent-by-parent following the constructed counts. This is
	// deterministic and reproduces the per-parent count distribution exactly.
	//
	// The first orphanGroups slots are handed ordinals at or beyond parentN, which
	// parentIDValue maps to ids no parent has. counts is ascending, so these are
	// the SMALLEST groups: orphans in real data are stragglers, not whales, and
	// taking from the bottom leaves p50/p95/max where they were.
	idx := int64(0)
	for p := int64(0); p < int64(len(counts)) && idx < childN; p++ {
		ordinal := p
		if p < orphanGroups {
			ordinal = parentN + p // no parent has this id
		}
		for k := int64(0); k < counts[p] && idx < childN; k++ {
			out[idx] = ordinal
			idx++
		}
	}
	// Any leftover children (from rounding) wrap onto the busiest parent so the
	// tail, not the body, absorbs the remainder.
	for ; idx < childN; idx++ {
		out[idx] = parentN - 1
	}
	return out
}

// targetCounts builds the per-parent child-count array, ascending, summing to
// exactly childN.
//
// What the fixture's facts actually say (they are measured by `pull` with
// GROUP BY on the foreign key, so the population is parents that have at least
// one child): among those parents, the mean is `mean`, the median `p50`, the 95th
// percentile `p95`, and the single largest `max`.
//
// The previous model read far more into them than that. It sampled a curve that
// interpolated p95 -> max across the whole top 5% of parents, i.e. it assumed 5%
// of parents were near-whales. For a real fixture (p95 4, max 7942) that band
// alone claimed ~1M children against a childN of 20k, so normalising the total
// multiplied everything by ~0.02 and rounded every typical parent from 2 down to
// ZERO. The mean survived; the shape did not. Measured: declared p50 2 / p95 4 /
// max 7942 hydrated as p50 0 / p95 0 / max 1244, with 95% of parents childless.
//
// The facts say nothing about the 96th-99th percentiles. The least-assumption
// reading is that they sit near p95 and exactly ONE parent is the whale, which is
// what production data looks like. So the shape is built directly and the total
// is reconciled by nudging the body, rather than by scaling a wrong shape until
// it fits.
func targetCounts(parentN, childN, declaredChild int64, fo *fixture.Fanout) []int64 {
	counts := make([]int64, parentN)

	mean, p50, p95, mx := fanoutFacts(fo, childN, parentN)

	// How many parents have any children at all. mean = childN/nonEmpty by
	// definition of the population pull measured, so this inverts it — and it is
	// what leaves the rest childless, which a fixture with mean > childN/parentN
	// implies and the old model never represented.
	nonEmpty := toI64(math.Round(float64(childN) / mean))
	nonEmpty = clamp(nonEmpty, 1, parentN)

	// `max` is a count of children, so it scales with the number of children being
	// handed out. At scale 1 this is the fixture's max; at 0.01 it is a hundredth
	// of it — the whale keeps its SHARE of a smaller pie.
	scale := 1.0
	if declaredChild > 0 {
		scale = float64(childN) / float64(declaredChild)
	}
	whale := clamp(toI64(math.Round(mx*scale)), 1, childN)

	// Ascending shape over the non-empty parents:
	//   bottom 95%     — the curve from a plausible floor through p50 to p95
	//   95% .. top-1   — near p95 (the facts claim nothing more)
	//   top            — the whale
	bodyEnd := toI64(math.Floor(0.95 * float64(nonEmpty)))
	bodyEnd = clamp(bodyEnd, 0, maxInt64(nonEmpty-1, 0))

	for i := int64(0); i < nonEmpty; i++ {
		switch {
		case i == nonEmpty-1:
			counts[i] = whale
		case i < bodyEnd:
			q := (float64(i) + 0.5) / float64(bodyEnd) // position within the body
			counts[i] = maxInt64(1, toI64(math.Round(bodyQuantile(q, p50, p95))))
		default:
			counts[i] = maxInt64(1, toI64(math.Round(p95)))
		}
	}

	reconcile(counts, nonEmpty, childN)
	return counts
}

// fanoutFacts reads the fixture's fan-out with fallbacks, keeping the ordering
// mean/p50 <= p95 <= max coherent so the shape below is monotonic.
func fanoutFacts(fo *fixture.Fanout, childN, parentN int64) (mean, p50, p95, mx float64) {
	mean, p50, p95, mx = fo.Mean, fo.P50, fo.P95, fo.Max
	if p50 <= 0 {
		p50 = mean
	}
	if mean <= 0 {
		mean = p50
	}
	if mean <= 0 {
		// No usable fact: fall back to spreading evenly.
		mean = math.Max(1, float64(childN)/float64(parentN))
		p50 = mean
	}
	if p95 < p50 {
		p95 = p50
	}
	if mx < p95 {
		mx = p95
	}
	return mean, p50, p95, mx
}

// bodyQuantile is the shape of the bottom 95%: a floor rising to p50 by the
// median, then to p95 at the top of the body.
//
// The p50 -> p95 half is GEOMETRIC, not linear, and the difference is not
// cosmetic. These distributions are heavy-tailed: between the median and the 95th
// percentile most parents sit near the median and the curve only turns up at the
// end. A linear ramp instead averages the band at (p50+p95)/2 — for the
// pathology fixture (p50 5, p95 100) that is 52 per parent, so the constructed
// shape claims ~32k children against a childN of 20k, and reconcile then eats the
// body down to 1 to fit. Measured, when this was linear: declared p50 5 hydrated
// as p50 1.
func bodyQuantile(q, p50, p95 float64) float64 {
	// Reflect p95 about p50 for a plausible lower tail, never below 1 — every
	// parent in this population has at least one child by definition.
	// float64() for the same fusion reason as lerp: 2*p50-p95 is a multiply
	// feeding a subtract, which fuses to FMS on the same architectures.
	floor := math.Max(1, float64(2*p50)-p95)
	if q <= 0.5 {
		return lerp(floor, p50, q/0.5)
	}
	return geomLerp(p50, p95, (q-0.5)/0.5)
}

// geomLerp interpolates geometrically: a·(b/a)^t. It grows slowly near a and
// accelerates toward b, which is the shape of a heavy tail. Falls back to linear
// if either endpoint is non-positive.
func geomLerp(a, b, t float64) float64 {
	if a <= 0 || b <= 0 {
		return lerp(a, b, t)
	}
	return a * math.Pow(b/a, t)
}

// reconcile adjusts the body so the counts sum to exactly childN.
//
// The shape above is built from the facts, not fitted to the total, so it will
// not sum to childN on its own. The residual is spread across the body — the
// least-constrained part, and the part a ±1 leaves quantile-stable — rather than
// by rescaling everything, which is what destroyed the median before. The whale
// and the near-p95 band are left alone: they are the facts the fixture actually
// asserts.
func reconcile(counts []int64, nonEmpty, childN int64) {
	sum := int64(0)
	for i := int64(0); i < nonEmpty; i++ {
		sum += counts[i]
	}
	if sum == childN || nonEmpty == 0 {
		return
	}

	// Body = everything below the whale. With a single non-empty parent there is
	// no body, so the whale absorbs it.
	body := maxInt64(nonEmpty-1, 0)
	if body == 0 {
		counts[0] = childN
		return
	}

	for sum < childN {
		// Hand out round-robin from the bottom so the median moves last.
		for i := int64(0); i < body && sum < childN; i++ {
			counts[i]++
			sum++
		}
	}
	for sum > childN {
		// Take back from the busiest body parents first, never below 1: dropping a
		// parent to zero would remove it from the population the facts describe.
		moved := false
		for i := body - 1; i >= 0 && sum > childN; i-- {
			if counts[i] > 1 {
				counts[i]--
				sum--
				moved = true
			}
		}
		if !moved {
			// The body cannot give any more: the whale takes the remainder. This is
			// a fixture whose max alone exceeds the children available at this
			// scale.
			counts[nonEmpty-1] -= sum - childN
			if counts[nonEmpty-1] < 1 {
				counts[nonEmpty-1] = 1
			}
			return
		}
	}
}

// lerp is a linear interpolation between a and b at t in [0, 1].
//
// The float64() conversion prevents the compiler fusing the multiply and add
// into a single FMA, which it does on arm64/ppc64/s390x but not amd64. Without
// it the same fixture hydrates differently per architecture (INV-DETERMINISM).
// See engine.go's sampleHistogram for the measurements and D-011.
func lerp(a, b, t float64) float64 {
	return a + float64((b-a)*t)
}

func clamp(v, lo, hi int64) int64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// orphanGroupCount returns how many of the (ascending) count groups must point at
// a parent that does not exist, so the hydrated orphan_fraction matches the
// fixture's.
//
// hydrate ignored orphan_fraction entirely, so P1-T7's clause "satisfy declared
// constraints UNLESS orphan_fraction>0 demands otherwise" was unimplemented: the
// corpus case that exists for "the VALIDATE that trips on pre-existing orphans"
// hydrated a database with none, and VALIDATE CONSTRAINT would have succeeded on
// it. The FAIL was right, but it came from reading the fixture, not from running
// anything.
//
// This does NOT fight the fan-out. `pull` measures fan-out with GROUP BY on the
// foreign key (internal/profile/fanout.go), so its "parents" are FK VALUES, not
// parent rows — an orphan group is simply a value with no match. The group-size
// distribution is untouched; some groups just point elsewhere. What changes is
// how many real parents end up childless, which the facts already imply.
func orphanGroupCount(counts []int64, childN int64, of *fixture.Fact[float64]) int64 {
	if of == nil || of.Value <= 0 || childN <= 0 {
		return 0
	}
	want := toI64(math.Round(of.Value * float64(childN)))
	if want <= 0 {
		return 0
	}
	var rows, groups int64
	for i := 0; i < len(counts) && rows < want; i++ {
		if counts[i] <= 0 {
			continue
		}
		rows += counts[i]
		groups++
	}
	// Never orphan every group: a reference whose children ALL dangle is not what
	// a fraction below 1 describes, and it would leave no fan-out to speak of.
	if groups >= int64(len(counts)) {
		groups = int64(len(counts)) - 1
	}
	if groups < 0 {
		groups = 0
	}
	return groups
}
