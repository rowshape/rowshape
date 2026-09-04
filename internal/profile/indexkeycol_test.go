package profile

import (
	"strings"
	"testing"
)

// TestIndexKeyCountColumnBoundary pins the PG 11 boundary for the index read.
//
// pg_index.indnkeyatts arrived with INCLUDE (covering indexes) in PostgreSQL 11.
// Reading it unconditionally made `pull`, `plan`, `verify` and the catalog read
// fail outright on PG 10 with `column ix.indnkeyatts does not exist` (42703),
// against a README and D-022 that both claim support from 10. It went unnoticed
// because the version matrix had never run (PR-T14); the first run caught it.
//
// This is the D-006/D-007 hazard in miniature — a catalog read that is right on
// one major and absent on another — so the boundary gets an assertion on BOTH
// sides rather than a comment.
func TestIndexKeyCountColumnBoundary(t *testing.T) {
	for _, c := range []struct {
		major int
		want  string
		why   string
	}{
		{10, "ix.indnatts", "no INCLUDE before 11, so every indexed column is a key column"},
		{11, "ix.indnkeyatts", "INCLUDE arrives in 11, and with it the column"},
		{12, "ix.indnkeyatts", ""},
		{18, "ix.indnkeyatts", ""},
		{0, "ix.indnkeyatts", "unknown version fails loudly on an old server rather than silently widening a key"},
	} {
		if got := indexKeyCountColumn(c.major); got != c.want {
			t.Errorf("indexKeyCountColumn(%d) = %q, want %q %s", c.major, got, c.want, c.why)
		}
	}
}

// The substitution has to reach BOTH uses. The query names the column twice —
// once in generate_series to walk the key positions, once in the select list to
// split key from payload — and replacing only one produces a query that still
// mentions indnkeyatts and still fails on 10.
func TestIndexQueryHasNoModernColumnOnPG10(t *testing.T) {
	const q = `FROM generate_series(1, ix.indnkeyatts) AS k(n)` + "\n" + `       ix.indnkeyatts,`
	got := strings.ReplaceAll(q, "ix.indnkeyatts", indexKeyCountColumn(10))
	if strings.Contains(got, "indnkeyatts") {
		t.Errorf("a PG 10 query still references indnkeyatts:\n%s", got)
	}
	if strings.Count(got, "ix.indnatts") != 2 {
		t.Errorf("expected both uses substituted, got:\n%s", got)
	}
}
