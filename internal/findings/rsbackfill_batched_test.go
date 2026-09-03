package findings

import (
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/validate"
)

// batchFixture is backfillFixture plus the integer key a batch loop ranges over.
func batchFixture() *fixture.Fixture {
	f := backfillFixture(0.9)
	tbl := f.Tables["public.users"]
	tbl.Columns["id"] = fixture.Column{Type: "bigint"}
	f.Tables["public.users"] = tbl
	return f
}

func backfillCodes(t *testing.T, sql string) []string {
	t.Helper()
	c := &validate.Capture{Success: true, Statements: []validate.Statement{{SQL: sql}}}
	var out []string
	for _, f := range (rsBackfill{}).Analyze(batchFixture(), c) {
		out = append(out, f.Code)
	}
	return out
}

// The rule must not dead-end its own remediation. RS-PERF-010 tells the reader to
// batch over a bounded key range; if the batched form warns just as loudly, the
// advice is unfollowable — and an agent reading the WARN cannot close its loop.
func TestBatchedBackfillIsNotFlagged(t *testing.T) {
	batched := []string{
		"UPDATE public.users SET normalized = 'x' WHERE normalized IS NULL AND id BETWEEN 1 AND 10000;",
		"UPDATE public.users SET normalized = 'x' WHERE id >= 1 AND id < 10001;",
		"UPDATE public.users SET normalized = 'x' WHERE id > 0 AND id <= 10000 AND normalized IS NULL;",
		// Placeholder bounds: the window is a run-time choice, not a claim that
		// the statement may rewrite the table.
		"UPDATE public.users SET normalized = 'x' WHERE id >= $1 AND id < $2;",
		"UPDATE public.users SET normalized = 'x' WHERE id BETWEEN :lo AND :hi;",
		"DELETE FROM public.users WHERE id >= 1 AND id < 5000;",
	}
	for _, sql := range batched {
		if codes := backfillCodes(t, sql); len(codes) != 0 {
			t.Errorf("batched statement produced %v, want no finding:\n  %s", codes, sql)
		}
	}
}

// The recognizer must not become a way to silence the rule. A one-sided bound
// still matches most of the table, an OR can widen the window back out, and a
// window that is merely large is not batched.
func TestUnbatchedBackfillIsStillFlagged(t *testing.T) {
	unbatched := []string{
		// The canonical unbatched backfill this rule exists for.
		"UPDATE public.users SET normalized = 'x' WHERE normalized IS NULL;",
		// One-sided: everything above the bound.
		"UPDATE public.users SET normalized = 'x' WHERE id >= 1000;",
		"UPDATE public.users SET normalized = 'x' WHERE id < 40000000;",
		// An OR widens it back out.
		"UPDATE public.users SET normalized = 'x' WHERE (id >= 1 AND id < 10000) OR normalized IS NULL;",
		// Bounded, but the window itself is a mass write.
		"UPDATE public.users SET normalized = 'x' WHERE id BETWEEN 1 AND 40000000;",
		// A range on a column the fixture does not know is not a proven window.
		"UPDATE public.users SET normalized = 'x' WHERE nosuchcol >= 1 AND nosuchcol < 10;",
	}
	for _, sql := range unbatched {
		codes := backfillCodes(t, sql)
		found := false
		for _, c := range codes {
			if c == "RS-PERF-010" {
				found = true
			}
		}
		if !found {
			t.Errorf("produced %v, want RS-PERF-010 — this is not a bounded batch:\n  %s", codes, sql)
		}
	}
}

// The window, not the table, is what a bounded statement's size rests on.
func TestBoundedRangeSpans(t *testing.T) {
	tbl := batchFixture().Tables["public.users"]
	cases := []struct {
		where     string
		bounded   bool
		span      int64
		spanKnown bool
	}{
		{"WHERE id BETWEEN 1 AND 100", true, 100, true},
		{"WHERE id >= 0 AND id < 10", true, 10, true},
		{"WHERE id >= $1 AND id < $2", true, 0, false},
		{"WHERE id >= 5", false, 0, false},
		{"WHERE normalized IS NULL", false, 0, false},
	}
	for _, c := range cases {
		b, span, known := boundedRange(tbl, c.where, strings.ToUpper(c.where))
		if b != c.bounded || known != c.spanKnown || (known && span != c.span) {
			t.Errorf("%s: got (bounded=%v span=%d known=%v), want (%v, %d, %v)",
				c.where, b, span, known, c.bounded, c.span, c.spanKnown)
		}
	}
}
