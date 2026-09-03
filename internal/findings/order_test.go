package findings

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
)

// fixtureWithCascadingChildren builds one parent and n long-tailed cascading
// children. n must be > 1 for map-order nondeterminism to be observable at all.
func fixtureWithCascadingChildren(n int) *fixture.Fixture {
	f := &fixture.Fixture{
		RowshapeFixture: fixture.FormatVersion,
		Tables:          map[string]fixture.Table{},
	}
	f.Tables["public.parent"] = fixture.Table{
		Rows: fixture.Fact[int64]{Value: 1000, Confidence: fixture.Exact},
	}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("public.child_%02d", i)
		f.Tables[name] = fixture.Table{
			Rows: fixture.Fact[int64]{Value: 100000, Confidence: fixture.Exact},
			References: []fixture.Reference{{
				Column:   "parent_id",
				To:       "public.parent.id",
				OnDelete: "cascade",
				// long-tailed: Max >= 100 and Max >= Mean*10
				Fanout: &fixture.Fanout{Mean: 5, P50: 2, P95: 200, Max: 5000},
			}},
		}
	}
	return f
}

// TestCascadeFanoutFindingsAreOrdered pins that the analyzer emits findings in a
// stable order.
//
// It used to range f.Tables directly, so a parent with several long-tailed
// cascading children produced its findings in Go's randomized map order — the
// same fixture and migration yielding a different Findings slice run to run. The
// verdict is shaped as a signable in-toto predicate, so an unstable slice means
// an unstable attestation over identical inputs.
//
// The test runs the analyzer repeatedly IN ONE PROCESS. Go randomizes map
// iteration per range statement, not per process, so repetition is what exposes
// the bug — a single call would have looked deterministic and passed against the
// broken code.
func TestCascadeFanoutFindingsAreOrdered(t *testing.T) {
	f := fixtureWithCascadingChildren(12)

	key := func() string {
		got := cascadeFanoutFindings(f, "public.parent")
		if len(got) == 0 {
			t.Fatal("no findings produced — the fixture no longer exercises the analyzer")
		}
		var b strings.Builder
		for _, fnd := range got {
			b.WriteString(fnd.Code)
			b.WriteByte('|')
			b.WriteString(fnd.Detail)
			b.WriteByte('\n')
		}
		return b.String()
	}

	first := key()
	for i := 0; i < 200; i++ {
		if got := key(); got != first {
			t.Fatalf("finding order is not stable (iteration %d)\nfirst:\n%s\ngot:\n%s", i, first, got)
		}
	}

	// The order must also be the SORTED one, not merely repeatable.
	got := cascadeFanoutFindings(f, "public.parent")
	var prev string
	for _, fnd := range got {
		if prev != "" && fnd.Detail < prev {
			t.Errorf("findings are repeatable but not sorted: %q came after %q", fnd.Detail, prev)
		}
		prev = fnd.Detail
	}
}

// findIndex resolved an index name by ranging f.Tables, so with the same index
// name present in two schemas it returned an arbitrary one — and that table name
// feeds the finding's DependsOn, making the recorded provenance unstable too.
func TestFindIndexIsDeterministicAcrossSchemas(t *testing.T) {
	f := &fixture.Fixture{
		RowshapeFixture: fixture.FormatVersion,
		Tables:          map[string]fixture.Table{},
	}
	// Same index name in many schemas: Postgres permits this.
	for i := 0; i < 12; i++ {
		f.Tables[fmt.Sprintf("s%02d.t", i)] = fixture.Table{
			Rows:    fixture.Fact[int64]{Value: 10, Confidence: fixture.Exact},
			Indexes: []fixture.Index{{Name: "idx_shared", Bytes: int64(1000 + i)}},
		}
	}

	firstTable, _, ok := findIndex(f, "idx_shared")
	if !ok {
		t.Fatal("index not found — the fixture no longer exercises findIndex")
	}
	for i := 0; i < 200; i++ {
		tname, _, ok := findIndex(f, "idx_shared")
		if !ok {
			t.Fatalf("iteration %d: index not found", i)
		}
		if tname != firstTable {
			t.Fatalf("findIndex is not stable: got %q then %q", firstTable, tname)
		}
	}
	if firstTable != "s00.t" {
		t.Errorf("findIndex returned %q; sorted order should yield the first schema, s00.t", firstTable)
	}
}
