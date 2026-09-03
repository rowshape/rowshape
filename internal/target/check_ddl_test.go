package target

import (
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
)

func boolPtr(b bool) *bool { return &b }

// checkFixture carries the CHECK shapes that decide whether the disposable
// database enforces what production does.
func checkFixture() *fixture.Fixture {
	return &fixture.Fixture{
		Tables: map[string]fixture.Table{
			"app.orders": {
				Rows: fixture.Fact[int64]{Value: 100},
				Columns: map[string]fixture.Column{
					"id":     {Type: "bigint"},
					"status": {Type: "text"},
					"qty":    {Type: "integer"},
				},
				Constraints: []fixture.Constraint{
					{Name: "orders_pkey", Kind: "primary_key", Columns: []string{"id"}},
					{Name: "orders_status_chk", Kind: "check",
						Expression: `(status = ANY (ARRAY['pending'::text, 'paid'::text]))`},
					{Name: "orders_qty_chk", Kind: "check", Expression: `(qty > 0)`},
				},
			},
		},
	}
}

// TestDeferredConstraintsEmitsChecks is the wrong-PASS regression. Dropping a
// CHECK meant an UPDATE writing a status the constraint forbids returned PASS with
// exit 0 while the source database refused it outright.
func TestDeferredConstraintsEmitsChecks(t *testing.T) {
	stmts := DeferredConstraints(checkFixture())
	joined := strings.Join(stmts, "\n")

	for _, want := range []string{"orders_status_chk", "orders_qty_chk"} {
		if !strings.Contains(joined, want) {
			t.Errorf("CHECK %q not applied to the target:\n%s", want, joined)
		}
	}
	for _, s := range stmts {
		if !strings.HasPrefix(s, "ALTER TABLE ") || !strings.Contains(s, "ADD CONSTRAINT ") {
			t.Errorf("not an ADD CONSTRAINT statement: %s", s)
		}
	}
}

// TestDeferredConstraintsSkipsInlineKinds: PRIMARY KEY and UNIQUE are emitted by
// createTable, so repeating them here would fail with `relation already exists`.
func TestDeferredConstraintsSkipsInlineKinds(t *testing.T) {
	for _, s := range DeferredConstraints(checkFixture()) {
		if strings.Contains(s, "orders_pkey") {
			t.Errorf("primary key re-added after CREATE TABLE already emitted it: %s", s)
		}
	}
}

// TestDeferredConstraintsSkipsOpaqueCheck: "opaque" is the placeholder
// privacy:strict leaves behind (§6.4), not a predicate. Reconstructing it would
// constrain hydrated data in a way production may not — the same call createType
// already makes for an opaque domain CHECK.
func TestDeferredConstraintsSkipsOpaqueCheck(t *testing.T) {
	f := checkFixture()
	f.Tables["app.orders"] = withConstraints(f.Tables["app.orders"], []fixture.Constraint{
		{Name: "orders_secret_chk", Kind: "check", Expression: "opaque"},
	})
	for _, s := range DeferredConstraints(f) {
		if strings.Contains(s, "opaque") {
			t.Errorf("opaque predicate reconstructed: %s", s)
		}
	}
}

// TestDeferredConstraintsPreservesNotValid: a NOT VALID constraint is not enforced
// against existing rows, so recreating it as validated would make the target
// reject data production holds — and a migration's own VALIDATE CONSTRAINT, which
// is the interesting statement, would have nothing left to validate.
func TestDeferredConstraintsPreservesNotValid(t *testing.T) {
	f := checkFixture()
	f.Tables["app.orders"] = withConstraints(f.Tables["app.orders"], []fixture.Constraint{
		{Name: "orders_legacy_chk", Kind: "check", Expression: `(qty < 1000)`, Validated: boolPtr(false)},
	})
	stmt := findStatement(t, DeferredConstraints(f), "orders_legacy_chk")
	if !strings.HasSuffix(stmt, " NOT VALID") {
		t.Errorf("NOT VALID dropped, making the target stricter than production: %s", stmt)
	}
	// A validated constraint must NOT pick it up.
	if s := findStatement(t, DeferredConstraints(f), "orders_qty_chk"); strings.Contains(s, "NOT VALID") {
		t.Errorf("validated constraint marked NOT VALID: %s", s)
	}
}

func withConstraints(tbl fixture.Table, extra []fixture.Constraint) fixture.Table {
	tbl.Constraints = append(append([]fixture.Constraint{}, tbl.Constraints...), extra...)
	return tbl
}
