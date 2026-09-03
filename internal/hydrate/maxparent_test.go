package hydrate

import (
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
)

func colWithRange(min, max int64) fixture.Column {
	return fixture.Column{Type: "bigint", Range: &fixture.Range{Min: min, Max: max}}
}

// The property this file pins is the one the orphan path exists to guarantee:
// a deliberate orphan must carry an id NO parent row holds.
//
// It used to be pinned differently — against a scanning oracle that reproduced
// the ORIGINAL maxParentID, back when a numeric id wrapped (min + ord % span).
// A unique column no longer wraps (numericInRange's unique branch is min + ord),
// so that oracle encoded a model the engine no longer has, and pinning the new
// code to it would have asserted the old behavior was still correct. The
// behavioral guarantee is model-independent, so it is asserted directly instead.
func TestOrphanIDsCollideWithNoParent(t *testing.T) {
	ranges := []struct{ lo, hi int64 }{
		{0, 0}, {0, 1}, {1, 10}, {0, 99}, {-50, 50}, {1000, 1000000},
	}
	counts := []int64{1, 2, 7, 64, 1000}

	for _, r := range ranges {
		col := colWithRange(r.lo, r.hi)
		for _, n := range counts {
			// Every id a real parent row holds.
			held := map[int64]bool{}
			for ord := int64(0); ord < n; ord++ {
				if v, ok := numericInRange(col, ord, true).(int64); ok {
					held[v] = true
				}
			}
			// The orphans assignForeignKeys hands out (ordinals >= parentN).
			for k := int64(0); k < 5; k++ {
				orphan := maxParentID(col, n) + 1 + k
				if held[orphan] {
					t.Errorf("range [%d,%d] parentN=%d: orphan id %d is held by a real parent",
						r.lo, r.hi, n, orphan)
				}
			}
		}
	}
}

// A rangeless numeric key generates the ordinals themselves, which is the case
// the old scanning oracle got wrong (its unreachable !ok branch returned parentN
// where the true maximum is parentN-1).
func TestMaxParentIDWithoutARange(t *testing.T) {
	noRange := fixture.Column{Type: "bigint"}
	for _, n := range []int64{1, 2, 100} {
		held := map[int64]bool{}
		for ord := int64(0); ord < n; ord++ {
			if v, ok := numericInRange(noRange, ord, true).(int64); ok {
				held[v] = true
			}
		}
		if orphan := maxParentID(noRange, n) + 1; held[orphan] {
			t.Errorf("parentN=%d: orphan id %d is held by a real parent", n, orphan)
		}
	}
}

func TestMaxParentIDEmptyParent(t *testing.T) {
	if got := maxParentID(colWithRange(5, 10), 0); got != 0 {
		t.Errorf("maxParentID with no parent rows = %d, want 0", got)
	}
}
