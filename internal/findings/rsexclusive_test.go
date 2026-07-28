package findings

import (
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/validate"
)

func exclusiveFixture() *fixture.Fixture {
	return &fixture.Fixture{
		RowshapeFixture: fixture.FormatVersion,
		Meta:            fixture.Meta{Engine: fixture.Engine{Name: "postgres", Version: "16"}},
		Tables: map[string]fixture.Table{
			"public.orders": {
				Rows: fixture.Fact[int64]{Value: 200_000_000, Confidence: fixture.Exact},
				Columns: map[string]fixture.Column{
					"id":     {Type: "bigint", Nullable: false},
					"amount": {Type: "integer", Nullable: false, Range: &fixture.Range{Min: 1, Max: 1000}},
				},
				Indexes: []fixture.Index{{Name: "idx_orders_amount", Bytes: 1 << 30}},
			},
		},
	}
}

func codesFor(t *testing.T, f *fixture.Fixture, sql string) []string {
	t.Helper()
	var sc []validate.Statement
	for _, s := range validate.SplitStatements(sql) {
		sc = append(sc, validate.Statement{SQL: s})
	}
	res := validate.BuildResult(f, &validate.Capture{Success: true, Statements: sc}, validate.Registered(), false)
	var out []string
	for _, fnd := range res.Findings {
		out = append(out, fnd.Code)
	}
	return out
}

func has(codes []string, want string) bool {
	for _, c := range codes {
		if c == want {
			return true
		}
	}
	return false
}

// TestSucceedingOperationsAreStillFlagged closes the catalog's largest
// structural gap: every analyzer before rsExclusive asked "will this FAIL?",
// while the product promises an answer to "is this SAFE to run on production?".
// A statement that succeeds after holding ACCESS EXCLUSIVE on a 200M-row table
// for twenty minutes is a successful outage, and each of these returned PASS.
func TestSucceedingOperationsAreStillFlagged(t *testing.T) {
	f := exclusiveFixture()
	cases := []struct{ name, sql, want string }{
		{"CHECK on valid data", "ALTER TABLE orders ADD CONSTRAINT c CHECK (amount > 0);", "RS-CONSTRAINT-020"},
		{"FK with no orphans", "ALTER TABLE orders ADD CONSTRAINT f FOREIGN KEY (id) REFERENCES users (id);", "RS-CONSTRAINT-020"},
		{"ADD PRIMARY KEY", "ALTER TABLE orders ADD PRIMARY KEY (id);", "RS-LOCK-002"},
		{"DROP INDEX", "DROP INDEX idx_orders_amount;", "RS-INDEX-002"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			codes := codesFor(t, f, c.sql)
			if !has(codes, c.want) {
				t.Fatalf("%q produced %v, want %s — a succeeding operation that locks the table for its "+
					"duration must not be certified clean", c.sql, codes, c.want)
			}
		})
	}
}

// The SAFE forms must not be flagged, or the tool cries wolf on correct work.
func TestSafeFormsAreNotFlagged(t *testing.T) {
	f := exclusiveFixture()
	cases := []struct{ name, sql, mustNot string }{
		{"CHECK with NOT VALID", "ALTER TABLE orders ADD CONSTRAINT c CHECK (amount > 0) NOT VALID;", "RS-CONSTRAINT-020"},
		{"FK with NOT VALID", "ALTER TABLE orders ADD CONSTRAINT f FOREIGN KEY (id) REFERENCES users (id) NOT VALID;", "RS-CONSTRAINT-020"},
		{"DROP INDEX CONCURRENTLY", "DROP INDEX CONCURRENTLY idx_orders_amount;", "RS-INDEX-002"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if has(codesFor(t, f, c.sql), c.mustNot) {
				t.Errorf("%q must NOT produce %s — that is the recommended safe form", c.sql, c.mustNot)
			}
		})
	}
}

// The findings must be SIZED from the fixture's declared rows: the whole point
// is that the sandbox reveals nothing about production duration.
func TestExclusiveFindingsAreSizedFromDeclaredRows(t *testing.T) {
	f := exclusiveFixture()
	var sc []validate.Statement
	for _, s := range validate.SplitStatements("ALTER TABLE orders ADD CONSTRAINT c CHECK (amount > 0);") {
		sc = append(sc, validate.Statement{SQL: s})
	}
	res := validate.BuildResult(f, &validate.Capture{Success: true, Statements: sc}, validate.Registered(), false)
	for _, fnd := range res.Findings {
		if fnd.Code != "RS-CONSTRAINT-020" {
			continue
		}
		if !strings.Contains(fnd.Title, "200.0M") {
			t.Errorf("title must be sized from declared rows, got %q", fnd.Title)
		}
		if len(fnd.DependsOn) != 1 || !strings.HasSuffix(fnd.DependsOn[0], ".rows") {
			t.Errorf("must cite the row count it rests on, got %v", fnd.DependsOn)
		}
		return
	}
	t.Fatal("no RS-CONSTRAINT-020 produced")
}

// An unresolved index name must not leave a dangling provenance path.
func TestDropIndexUnresolvedCitesNothing(t *testing.T) {
	res := validate.BuildResult(exclusiveFixture(), &validate.Capture{
		Success:    true,
		Statements: []validate.Statement{{SQL: "DROP INDEX no_such_index;"}},
	}, validate.Registered(), false)
	for _, fnd := range res.Findings {
		if fnd.Code == "RS-INDEX-002" && len(fnd.DependsOn) != 0 {
			t.Errorf("an unresolved index must cite no fact, got %v", fnd.DependsOn)
		}
	}
}
