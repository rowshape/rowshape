package findings

import (
	"github.com/rowshape/rowshape/internal/estimate"
	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/validate"
	"github.com/rowshape/rowshape/internal/verdict"
)

// minBasisRows and maxExtrapolationRatio bound what counts as a usable basis.
//
// Neither is a precise science, and they are deliberately generous: the goal is
// to reject the degenerate cases (a table hydrated to one row, a scale so small
// the measurement is noise) rather than to second-guess a reasonable one. A
// 1,000-row basis extrapolated to 10M is a 10,000x ratio and still permitted;
// a 1-row basis extrapolated to anything is not.
const (
	minBasisRows          = 1000
	maxExtrapolationRatio = 10_000
)

// max64 returns the larger of two int64s.
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// estimateFor computes a finding's duration estimate for the statement at
// stmtIdx. When the capture carries a second-scale measurement for this
// statement (validate --calibrate), it fits the cost curve to the two measured
// points and returns a `measured` estimate (RFC §9.2); otherwise it extrapolates
// from the single measured basis, which stays `estimated`. Returns nil when there
// is no usable basis.
func estimateFor(c *validate.Capture, stmtIdx int, op estimate.OpClass, table string, declaredRows int64, rowsConf fixture.Confidence, tableKnown, hasVersion bool) *verdict.Estimate {
	// Refuse to extrapolate without an engine version (RFC §9.1). CR-T21 moved
	// this gate here from three separate `if hasVersion` wrappers in rslock,
	// rsindex and rsconstraint: the rule now has ONE enforcement point, so a
	// fourth analyzer cannot forget it. estimate.ForFixture was a parallel,
	// never-called implementation of the same gate and has been deleted.
	if !hasVersion {
		return nil
	}

	// Refuse to extrapolate for a table the fixture does not carry.
	//
	// Without this, an unknown table reads the zero value and the arithmetic
	// happily reports `instant` — a rewrite of no rows takes no time. That is
	// indistinguishable from a genuinely empty table, and it is exactly the wrong
	// answer for the likeliest cause: a name the fixture has no facts for (a typo,
	// a table never pulled, or an unqualified name in two schemas). Telling
	// someone an unknown migration is instant is worse than telling them nothing.
	//
	// This mirrors RFC §9.1's refusal to extrapolate without engine.version: when
	// the basis is missing, omit the estimate and say why. Callers surface the
	// finding either way — the lock is still real — and an absent dependency caps
	// it to WARN.
	if !tableKnown {
		return nil
	}

	rows1 := c.TableRows[table]
	if rows1 <= 0 {
		// The fallback is only sound on the GROUND-TRUTH path, where the fixture's
		// declared rows really are the rows the statement ran against. There
		// TableRows is left empty entirely (it is populated only when hydrating),
		// so an empty map is the discriminator.
		//
		// On the ephemeral path a populated map that is MISSING this table means
		// the table was not hydrated — there is no measured basis at all. Falling
		// back to declaredRows there made the linear ratio exactly 1.0, so the
		// prediction equalled ms1, which defaults to 1ms: a rewrite of a 50M-row
		// table was reported as `instant`. That is precisely the outcome the
		// tableKnown gate above exists to prevent; it closed the "table missing
		// from the fixture" door while this line left the "row count missing from
		// the capture" door open.
		if len(c.TableRows) > 0 {
			return nil
		}
		rows1 = declaredRows // ground-truth target: hydrated == real
	}
	// A basis this small cannot support an extrapolation, and pretending
	// otherwise produces a confident nonsense answer rather than no answer.
	//
	// hydratedRowCount floors to 1 row, so at a small --scale a table hydrates to
	// a handful of rows. Extrapolating 50M declared rows from a 1-row basis is a
	// 50,000,000x ratio applied to a measurement that is entirely noise — and it
	// lands on `outage` for EVERY table, measured: basisRows=1 -> outage,
	// basisRows=10 -> outage. A tool that reports every migration as an outage
	// below some scale is not being cautious, it is being useless, and it teaches
	// people that the estimate means nothing.
	if rows1 < minBasisRows && declaredRows/max64(rows1, 1) > maxExtrapolationRatio {
		return nil
	}

	// Nothing was actually MEASURED. DurationMs <= 0 means the statement was too
	// fast to time or was never timed; defaulting that to 1ms and then scaling it
	// by the row ratio dresses a non-measurement up as a basis.
	if stmtIdx < 0 || stmtIdx >= len(c.Statements) || c.Statements[stmtIdx].DurationMs <= 0 {
		return nil
	}
	ms1 := c.Statements[stmtIdx].DurationMs

	if cal := c.Calibration; cal != nil && stmtIdx >= 0 && stmtIdx < len(cal.StatementMs2) {
		rows2, ms2 := cal.TableRows[table], cal.StatementMs2[stmtIdx]
		if rows2 > 0 && rows2 != rows1 && ms2 > 0 {
			if est, err := estimate.Calibrate(op, estimate.Point{Rows: rows1, Ms: ms1}, estimate.Point{Rows: rows2, Ms: ms2}, declaredRows, rowsConf); err == nil {
				return &est
			}
		}
	}
	est := estimate.Extrapolate(op, rows1, ms1, declaredRows, rowsConf)
	return &est
}

// tableKnown reports whether the fixture carries facts for this table. It is the
// difference between "this table has no rows" and "we have never seen this
// table" — arithmetic on the zero value cannot tell them apart, and answers
// `instant` for both.
func tableKnown(f *fixture.Fixture, table string) bool {
	if f == nil {
		return false
	}
	_, ok := f.Tables[table]
	return ok
}
