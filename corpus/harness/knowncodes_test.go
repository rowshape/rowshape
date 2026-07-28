package harness

import (
	"sort"
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/findings"
)

// TestKnownCodesMatchRegistry pins that the corpus vocabulary and the finding
// catalog agree.
//
// KnownCodes is deliberately hand-maintained rather than derived: the corpus is
// what checks the catalog, so deriving its vocabulary FROM the catalog would let
// a typo in a new code validate itself. That leaves one real drift risk in the
// other direction — adding a family to the registry and forgetting it here,
// which surfaces as a confusing "unknown finding code" on a case that is
// actually correct. This closes it.
func TestKnownCodesMatchRegistry(t *testing.T) {
	inRegistry := map[string]bool{}
	for _, code := range findings.Codes() {
		i := strings.LastIndex(code, "-")
		if i < 0 {
			t.Errorf("registry code %q has no numeric suffix", code)
			continue
		}
		inRegistry[code[:i]] = true
	}
	if len(inRegistry) == 0 {
		t.Fatal("registry reported no codes — the walk is broken and this test would pass vacuously")
	}

	var missing, extra []string
	for fam := range inRegistry {
		if !KnownCodes[fam] {
			missing = append(missing, fam)
		}
	}
	for fam := range KnownCodes {
		if !inRegistry[fam] {
			extra = append(extra, fam)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)

	if len(missing) > 0 {
		t.Errorf("families in the registry but not in KnownCodes: %v — a corpus case naming one would be "+
			"rejected as an unknown code", missing)
	}
	if len(extra) > 0 {
		t.Errorf("families in KnownCodes with no registered code: %v — either the code was removed or the "+
			"name is a typo that would silently validate", extra)
	}
}
