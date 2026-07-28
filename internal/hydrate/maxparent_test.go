package hydrate

import (
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
)

// maxParentIDScan is the ORIGINAL scanning implementation, kept here as the
// oracle. maxParentID was replaced with a closed form to remove a per-cell
// O(parentN) scan; this pins that the replacement is behavior-preserving rather
// than merely faster.
//
// Note the original's `!ok` branch is unreachable (numericInRange returns the
// ordinal itself when bounds are absent, so the assertion always succeeds). It
// is reproduced faithfully anyway — an oracle that "fixes" the original on the
// way past would not be an oracle.
func maxParentIDScan(col fixture.Column, parentN int64) int64 {
	var max int64
	for ord := int64(0); ord < parentN; ord++ {
		v, ok := numericInRange(col, ord).(int64)
		if !ok {
			return parentN
		}
		if ord == 0 || v > max {
			max = v
		}
		if lo, hi, ok := numericBounds(col); ok && ord >= hi-lo {
			break
		}
	}
	return max
}

func colWithRange(min, max int64) fixture.Column {
	return fixture.Column{Type: "bigint", Range: &fixture.Range{Min: min, Max: max}}
}

func TestMaxParentIDMatchesTheScan(t *testing.T) {
	ranges := []struct{ lo, hi int64 }{
		{1, 5000}, {0, 0}, {1, 1}, {1, 10}, {5, 7},
		{-100, 100}, {0, 3}, {1000, 1000000},
	}
	counts := []int64{1, 2, 3, 4, 5, 9, 10, 11, 50, 100, 999, 1000, 1001}

	for _, r := range ranges {
		col := colWithRange(r.lo, r.hi)
		for _, n := range counts {
			want := maxParentIDScan(col, n)
			got := maxParentID(col, n)
			if got != want {
				t.Errorf("range{%d,%d} parentN=%d: closed form = %d, scan = %d", r.lo, r.hi, n, got, want)
			}
		}
	}

	// The rangeless case: the one the scan handled worst and the one the dead
	// branch would have answered wrongly.
	noRange := fixture.Column{Type: "bigint"}
	for _, n := range counts {
		want := maxParentIDScan(noRange, n)
		got := maxParentID(noRange, n)
		if got != want {
			t.Errorf("no range, parentN=%d: closed form = %d, scan = %d", n, got, want)
		}
	}
}

// The point of the change: a rangeless key must not cost a scan. A parentN large
// enough to be visibly slow under the old code must return immediately.
func TestMaxParentIDIsConstantTime(t *testing.T) {
	noRange := fixture.Column{Type: "bigint"}
	const huge = 500_000_000 // the old scan would run this many iterations
	if got := maxParentID(noRange, huge); got != huge-1 {
		t.Errorf("maxParentID(no range, %d) = %d, want %d", huge, got, huge-1)
	}
}

func TestMaxParentIDEdgeCases(t *testing.T) {
	col := colWithRange(1, 10)
	if got := maxParentID(col, 0); got != 0 {
		t.Errorf("parentN=0 must be 0, got %d", got)
	}
	if got := maxParentID(col, -5); got != 0 {
		t.Errorf("negative parentN must be 0, got %d", got)
	}
}
