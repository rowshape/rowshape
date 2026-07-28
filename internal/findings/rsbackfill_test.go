package findings

import (
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/validate"
)

func backfillFixture(nullFrac float64) *fixture.Fixture {
	return &fixture.Fixture{
		RowshapeFixture: fixture.FormatVersion,
		Meta:            fixture.Meta{Engine: fixture.Engine{Name: "postgres", Version: "16"}},
		Tables: map[string]fixture.Table{
			"public.users": {
				Rows: fixture.Fact[int64]{Value: 50_000_000, Confidence: fixture.Exact},
				Columns: map[string]fixture.Column{
					"email":      {Type: "text"},
					"normalized": {Type: "text", Nullable: true, NullFraction: &fixture.Fact[float64]{Value: nullFrac, Confidence: fixture.Estimated}},
				},
			},
			"public.small": {
				Rows:    fixture.Fact[int64]{Value: 100, Confidence: fixture.Exact},
				Columns: map[string]fixture.Column{"x": {Type: "text"}},
			},
		},
	}
}

func findingFor(t *testing.T, f *fixture.Fixture, sql, code string) *struct {
	Title, Detail string
	Evidence      map[string]any
} {
	t.Helper()
	res := validate.BuildResult(f, &validate.Capture{
		Success:    true,
		Statements: []validate.Statement{{SQL: sql}},
	}, validate.Registered(), false)
	for _, fnd := range res.Findings {
		if fnd.Code != code {
			continue
		}
		ev, _ := fnd.Evidence.(map[string]any)
		return &struct {
			Title, Detail string
			Evidence      map[string]any
		}{fnd.Title, fnd.Detail, ev}
	}
	return nil
}

// TestUnbatchedBackfillIsFlagged is the regression for a hazard the catalog
// exempted BY CONSTRUCTION.
//
// RS-PERF-002 fires only when a statement has NO WHERE clause. The real-world
// unbatched backfill always has one, so it was exempt and returned PASS.
func TestUnbatchedBackfillIsFlagged(t *testing.T) {
	f := backfillFixture(0.9) // 90% of rows still need backfilling
	got := findingFor(t, f, "UPDATE users SET normalized = lower(email) WHERE normalized IS NULL;", "RS-PERF-010")
	if got == nil {
		t.Fatal("the canonical unbatched backfill produced no finding")
	}
	// null_fraction IS the selectivity of an IS NULL predicate, so the estimate
	// should come from real facts rather than an upper bound.
	if !strings.Contains(got.Title, "45.0M") {
		t.Errorf("want ~45M affected rows (0.9 x 50M) from null_fraction, got title %q", got.Title)
	}
	if got.Evidence["selectivity"] != "null_fraction" {
		t.Errorf("evidence must record how selectivity was derived, got %v", got.Evidence)
	}
}

// A predicate the fixture PROVES is narrow must not be flagged, or the rule
// becomes noise on every qualified statement.
func TestNarrowPredicateIsNotFlagged(t *testing.T) {
	f := backfillFixture(0.0001) // only 5,000 of 50M rows match
	if got := findingFor(t, f, "UPDATE users SET normalized = lower(email) WHERE normalized IS NULL;", "RS-PERF-010"); got != nil {
		t.Errorf("a predicate the fixture proves narrow must not be flagged, got %q", got.Title)
	}
}

// IS NOT NULL is the complement of null_fraction and must be sized as such:
// against a 99%-null column it matches ~1% of the table, not 99%.
func TestIsNotNullUsesTheComplement(t *testing.T) {
	f := backfillFixture(0.99)
	got := findingFor(t, f, "UPDATE users SET email = lower(email) WHERE normalized IS NOT NULL;", "RS-PERF-010")
	if got == nil {
		// 1% of 50M is 500k, which is still above the batching threshold.
		t.Fatal("500k affected rows is above the threshold and must be flagged")
	}
	if !strings.Contains(got.Title, "500k") {
		t.Errorf("IS NOT NULL must use the COMPLEMENT of null_fraction (~500k, not ~49.5M), got %q", got.Title)
	}
	if got.Evidence["selectivity"] != "null_fraction" {
		t.Errorf("the estimate should be derived, got %v", got.Evidence)
	}
}

// When the fixture cannot size the predicate, the finding reports the table's
// row count as an UPPER BOUND rather than falling silent.
func TestUnknownSelectivityReportsAnUpperBound(t *testing.T) {
	f := backfillFixture(0.5)
	got := findingFor(t, f, "UPDATE users SET normalized = lower(email) WHERE email LIKE 'a%';", "RS-PERF-010")
	if got == nil {
		t.Fatal("an unsizable predicate on a large table must not fall silent")
	}
	if got.Evidence["selectivity"] != "unknown" {
		t.Errorf("evidence must say the selectivity is unknown, got %v", got.Evidence)
	}
	if !strings.Contains(got.Detail, "cannot tell") {
		t.Errorf("the detail must admit it cannot size the predicate, got %q", got.Detail)
	}
}

// A compound predicate changes selectivity in ways this cannot model; reporting
// a confident number for one conjunct would be worse than declining.
func TestCompoundPredicateIsNotEstimated(t *testing.T) {
	f := backfillFixture(0.9)
	got := findingFor(t, f, "UPDATE users SET normalized = lower(email) WHERE normalized IS NULL AND email IS NOT NULL;", "RS-PERF-010")
	if got == nil {
		t.Fatal("expected an upper-bound finding")
	}
	if got.Evidence["selectivity"] != "unknown" {
		t.Errorf("a compound predicate must not be estimated from one conjunct, got %v", got.Evidence)
	}
}

// An unresolved table name silently downgraded a mass DML to PASS. An absent
// fact is a reason to decline, never a reason to certify.
func TestUnresolvedTableDeclinesRatherThanCertifies(t *testing.T) {
	f := backfillFixture(0.5)
	got := findingFor(t, f, "UPDATE no_such_table SET x = 1 WHERE y IS NULL;", "RS-PERF-010")
	if got == nil {
		t.Fatal("an unresolvable table must produce a cannot-decide finding, not silence")
	}
	if got.Evidence["unresolved_table"] == nil {
		t.Errorf("evidence must name the unresolved table, got %v", got.Evidence)
	}
}

// Small tables and unqualified statements stay out of this rule's way.
func TestBackfillRuleDoesNotOverfire(t *testing.T) {
	f := backfillFixture(0.9)
	if got := findingFor(t, f, "UPDATE small SET x = 1 WHERE x IS NULL;", "RS-PERF-010"); got != nil {
		t.Errorf("a 100-row table must not be flagged, got %q", got.Title)
	}
	// No WHERE at all is RS-PERF-002's; the two must not double-flag.
	if got := findingFor(t, f, "UPDATE users SET normalized = 'x';", "RS-PERF-010"); got != nil {
		t.Errorf("the unqualified form belongs to RS-PERF-002, got %q", got.Title)
	}
	if findingFor(t, f, "UPDATE users SET normalized = 'x';", "RS-PERF-002") == nil {
		t.Error("the unqualified form must still produce RS-PERF-002")
	}
}

// TestSingleRowUpdateByUniqueKeyIsNotFlagged is the regression from the
// false-positive sweep.
//
// `UPDATE users SET status = 'x' WHERE id = 42` — a single-row update by primary
// key, the most ordinary statement there is — was reported as a 50M-row
// backfill, because the predicate was unsizable and the rule fell back to the
// table's row count as an upper bound. An upper bound is the right default for an
// UNKNOWN predicate; it is the wrong answer for one the fixture can prove matches
// at most one row.
func TestSingleRowUpdateByUniqueKeyIsNotFlagged(t *testing.T) {
	f := backfillFixture(0.5)
	// Give the table a proven-unique key.
	tbl := f.Tables["public.users"]
	tbl.Columns["id"] = fixture.Column{
		Type:   "bigint",
		Unique: &fixture.Fact[bool]{Value: true, Confidence: fixture.Exact},
	}
	f.Tables["public.users"] = tbl

	for _, sql := range []string{
		"UPDATE users SET email = 'x' WHERE id = 42;",
		"DELETE FROM users WHERE id = 42;",
		// An AND can only narrow further, so proving one conjunct bounds it.
		"UPDATE users SET email = 'x' WHERE id = 42 AND status IS NULL;",
	} {
		if got := findingFor(t, f, sql, "RS-PERF-010"); got != nil {
			t.Errorf("%q matches at most one row and must not be flagged, got %q", sql, got.Title)
		}
	}
}

// The silencing must rest on a PROVEN fact, and must not swallow the cases it
// was never meant to cover.
func TestUniqueEqualitySilencingIsNarrow(t *testing.T) {
	f := backfillFixture(0.5)
	tbl := f.Tables["public.users"]
	// Uniqueness only ESTIMATED: not proof, so the silencing must not apply.
	tbl.Columns["guess"] = fixture.Column{
		Type:   "bigint",
		Unique: &fixture.Fact[bool]{Value: true, Confidence: fixture.Estimated},
	}
	tbl.Columns["plain"] = fixture.Column{Type: "bigint"}
	f.Tables["public.users"] = tbl

	mustFlag := []string{
		"UPDATE users SET email = 'x' WHERE guess = 42;",  // estimated, not proven
		"UPDATE users SET email = 'x' WHERE plain = 42;",  // not unique at all
		"UPDATE users SET email = 'x' WHERE plain >= 42;", // not an equality
		"UPDATE users SET email = 'x' WHERE plain != 42;", // an operator containing "="
	}
	for _, sql := range mustFlag {
		if got := findingFor(t, f, sql, "RS-PERF-010"); got == nil {
			t.Errorf("%q must still be flagged — the silencing is only for a PROVEN unique equality", sql)
		}
	}
}
