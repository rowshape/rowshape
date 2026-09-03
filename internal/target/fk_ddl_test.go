package target

import (
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
)

// TestDeferredConstraintsEmitsForeignKeys is the wrong-PASS regression. Dropping
// foreign keys meant an INSERT of a row whose parent does not exist returned PASS
// with exit 0 while the source database refused it.
func TestDeferredConstraintsEmitsForeignKeys(t *testing.T) {
	f := &fixture.Fixture{
		Tables: map[string]fixture.Table{
			"app.orders": {
				Rows:    fixture.Fact[int64]{Value: 10},
				Columns: map[string]fixture.Column{"id": {Type: "bigint"}, "customer_id": {Type: "bigint"}},
				References: []fixture.Reference{
					{Column: "customer_id", To: "app.customers.id", Name: "orders_customer_fkey", OnDelete: "cascade"},
				},
			},
		},
	}
	stmt := findStatement(t, DeferredConstraints(f), "orders_customer_fkey")
	for _, want := range []string{
		`ALTER TABLE "app"."orders" ADD CONSTRAINT "orders_customer_fkey"`,
		`FOREIGN KEY ("customer_id")`,
		`REFERENCES "app"."customers" ("id")`,
		`ON DELETE CASCADE`,
	} {
		if !strings.Contains(stmt, want) {
			t.Errorf("statement missing %q:\n%s", want, stmt)
		}
	}
}

// TestForeignKeyReferentialActions: a migration that deletes parent rows behaves
// materially differently under CASCADE than under RESTRICT, so the recorded action
// has to survive. no_action is the default and is omitted rather than spelled out.
func TestForeignKeyReferentialActions(t *testing.T) {
	cases := []struct {
		onDelete, onUpdate string
		want, absent       string
	}{
		{"cascade", "", "ON DELETE CASCADE", "ON UPDATE"},
		{"restrict", "", "ON DELETE RESTRICT", "ON UPDATE"},
		{"set_null", "", "ON DELETE SET NULL", "ON UPDATE"},
		{"set_default", "", "ON DELETE SET DEFAULT", "ON UPDATE"},
		{"no_action", "no_action", "FOREIGN KEY", "ON DELETE"},
		{"", "cascade", "ON UPDATE CASCADE", "ON DELETE"},
	}
	for _, tc := range cases {
		t.Run(tc.onDelete+"/"+tc.onUpdate, func(t *testing.T) {
			stmts := addForeignKeys("app.orders", []fixture.Reference{
				{Column: "c", To: "app.p.id", Name: "fk", OnDelete: tc.onDelete, OnUpdate: tc.onUpdate},
			})
			if len(stmts) != 1 {
				t.Fatalf("want one statement, got %v", stmts)
			}
			if !strings.Contains(stmts[0], tc.want) {
				t.Errorf("missing %q: %s", tc.want, stmts[0])
			}
			if strings.Contains(stmts[0], tc.absent) {
				t.Errorf("unexpected %q: %s", tc.absent, stmts[0])
			}
		})
	}
}

// TestForeignKeyGroupsCompositeByName: a composite key arrives as one Reference
// per column pair. Rebuilt as separate single-column keys it constrains something
// production does not — (a) alone and (b) alone must each match a parent row,
// rather than the pair matching one.
func TestForeignKeyGroupsCompositeByName(t *testing.T) {
	stmts := addForeignKeys("app.line_items", []fixture.Reference{
		{Column: "order_id", To: "app.orders.id", Name: "li_order_fkey"},
		{Column: "tenant_id", To: "app.orders.tenant_id", Name: "li_order_fkey"},
	})
	if len(stmts) != 1 {
		t.Fatalf("composite key split into %d constraints:\n%s", len(stmts), strings.Join(stmts, "\n"))
	}
	if !strings.Contains(stmts[0], `FOREIGN KEY ("order_id", "tenant_id")`) {
		t.Errorf("column order or grouping wrong: %s", stmts[0])
	}
	if !strings.Contains(stmts[0], `REFERENCES "app"."orders" ("id", "tenant_id")`) {
		t.Errorf("referenced column order or grouping wrong: %s", stmts[0])
	}
}

// TestForeignKeySeparateConstraintsStaySeparate: two DIFFERENT keys on one table
// must not be merged just because they point at the same parent.
func TestForeignKeySeparateConstraintsStaySeparate(t *testing.T) {
	stmts := addForeignKeys("app.t", []fixture.Reference{
		{Column: "a", To: "app.p.id", Name: "t_a_fkey"},
		{Column: "b", To: "app.p.id", Name: "t_b_fkey"},
	})
	if len(stmts) != 2 {
		t.Fatalf("want two constraints, got %d:\n%s", len(stmts), strings.Join(stmts, "\n"))
	}
}

// TestForeignKeyUnnamedReferencesStaySeparate: a fixture written before `name`
// existed, or a hand-authored one, has no constraint name. Those must still become
// constraints, and must not be merged with each other.
func TestForeignKeyUnnamedReferencesStaySeparate(t *testing.T) {
	stmts := addForeignKeys("app.t", []fixture.Reference{
		{Column: "a", To: "app.p.id"},
		{Column: "b", To: "app.q.id"},
	})
	if len(stmts) != 2 {
		t.Fatalf("want two constraints, got %d:\n%s", len(stmts), strings.Join(stmts, "\n"))
	}
	for _, s := range stmts {
		if strings.Contains(s, "\x00") {
			t.Errorf("internal grouping key leaked into SQL: %s", s)
		}
	}
}

// TestForeignKeyPreservesNotValid: a NOT VALID key is not enforced against
// existing rows, so validating it here would make the target reject data
// production holds.
func TestForeignKeyPreservesNotValid(t *testing.T) {
	no := false
	stmts := addForeignKeys("app.t", []fixture.Reference{
		{Column: "a", To: "app.p.id", Name: "fk", Validated: &no},
	})
	if len(stmts) != 1 || !strings.HasSuffix(stmts[0], " NOT VALID") {
		t.Errorf("NOT VALID dropped: %v", stmts)
	}
}

// TestForeignKeySkipsUnusableReference: a `to` that is not schema.table.column
// cannot be rebuilt, and guessing at it would point the key somewhere production
// does not.
func TestForeignKeySkipsUnusableReference(t *testing.T) {
	for _, to := range []string{"", "users", "users.id"} {
		if stmts := addForeignKeys("app.t", []fixture.Reference{{Column: "a", To: to, Name: "fk"}}); len(stmts) != 0 {
			t.Errorf("reference to %q produced %v", to, stmts)
		}
	}
}

// TestForeignKeysComeAfterTables: the original objection to emitting foreign keys
// was that they "need dependency-ordered loading". Adding them after the rows
// removes the requirement — which is what makes self-references and cycles work.
func TestForeignKeysComeAfterTables(t *testing.T) {
	f := &fixture.Fixture{
		Tables: map[string]fixture.Table{
			// A cycle: each table references the other.
			"app.a": {
				Rows:       fixture.Fact[int64]{Value: 5},
				Columns:    map[string]fixture.Column{"id": {Type: "bigint"}, "b_id": {Type: "bigint", Nullable: true}},
				References: []fixture.Reference{{Column: "b_id", To: "app.b.id", Name: "a_b_fkey"}},
			},
			"app.b": {
				Rows:       fixture.Fact[int64]{Value: 5},
				Columns:    map[string]fixture.Column{"id": {Type: "bigint"}, "a_id": {Type: "bigint", Nullable: true}},
				References: []fixture.Reference{{Column: "a_id", To: "app.a.id", Name: "b_a_fkey"}},
			},
		},
	}
	// No foreign key may appear in DDL, which runs before any row exists.
	for _, s := range DDL(f) {
		if strings.Contains(s, "FOREIGN KEY") {
			t.Errorf("foreign key emitted in CREATE TABLE, reintroducing load-order dependence: %s", s)
		}
	}
	if got := len(DeferredConstraints(f)); got != 2 {
		t.Errorf("cycle produced %d constraints, want 2", got)
	}
}
