package profile

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

const refSchema = "rowshape_ref_test"

// seedReferences builds the foreign-key shapes that decide whether the disposable
// database enforces what production does: a self-reference with ON DELETE CASCADE
// (which also proves load order is irrelevant), a composite key, and a NOT VALID
// key.
func seedReferences(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	ctx := context.Background()
	stmts := []string{
		`DROP SCHEMA IF EXISTS ` + refSchema + ` CASCADE`,
		`CREATE SCHEMA ` + refSchema,
		`CREATE TABLE ` + refSchema + `.categories (
			id        int PRIMARY KEY,
			parent_id int REFERENCES ` + refSchema + `.categories(id) ON DELETE CASCADE,
			name      text NOT NULL
		)`,
		`CREATE TABLE ` + refSchema + `.orders (
			tenant_id int NOT NULL,
			id        int NOT NULL,
			PRIMARY KEY (tenant_id, id)
		)`,
		`CREATE TABLE ` + refSchema + `.line_items (
			id        int PRIMARY KEY,
			tenant_id int NOT NULL,
			order_id  int NOT NULL,
			CONSTRAINT li_order_fkey FOREIGN KEY (tenant_id, order_id)
			  REFERENCES ` + refSchema + `.orders (tenant_id, id) ON DELETE RESTRICT ON UPDATE CASCADE
		)`,
		`INSERT INTO ` + refSchema + `.categories SELECT g, NULLIF(g-1, 0), 'c' || g FROM generate_series(1, 100) g`,
		`INSERT INTO ` + refSchema + `.orders SELECT 1, g FROM generate_series(1, 100) g`,
		`INSERT INTO ` + refSchema + `.line_items SELECT g, 1, g FROM generate_series(1, 100) g`,
		`ANALYZE ` + refSchema + `.categories`,
		`ANALYZE ` + refSchema + `.line_items`,
	}
	for _, s := range stmts {
		if _, err := conn.Exec(ctx, s); err != nil {
			t.Fatalf("seed references (%s): %v", s, err)
		}
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+refSchema+` CASCADE`)
	})
}

// TestPullRecordsReferenceIdentity: without the constraint name a consumer cannot
// tell a composite key from two independent single-column keys, and rebuilding it
// as two constrains something production does not.
func TestPullRecordsReferenceIdentity(t *testing.T) {
	conn := adminConn(t)
	seedReferences(t, conn)

	f, err := Fast(context.Background(), conn, Options{Schemas: []string{refSchema}})
	if err != nil {
		t.Fatalf("Fast: %v", err)
	}

	refs := f.Tables[refSchema+".line_items"].References
	if len(refs) != 2 {
		t.Fatalf("composite key recorded as %d entries, want 2 (one per column pair)", len(refs))
	}
	for _, ref := range refs {
		if ref.Name != "li_order_fkey" {
			t.Errorf("reference on %s has name %q, want li_order_fkey — the entries cannot be grouped without it",
				ref.Column, ref.Name)
		}
		if ref.OnDelete != "restrict" {
			t.Errorf("on_delete = %q, want restrict", ref.OnDelete)
		}
		if ref.OnUpdate != "cascade" {
			t.Errorf("on_update = %q, want cascade", ref.OnUpdate)
		}
	}

	self := f.Tables[refSchema+".categories"].References
	if len(self) != 1 {
		t.Fatalf("self-reference recorded as %d entries, want 1", len(self))
	}
	if self[0].Name == "" {
		t.Error("self-reference has no constraint name")
	}
	if self[0].OnDelete != "cascade" {
		t.Errorf("self-reference on_delete = %q, want cascade", self[0].OnDelete)
	}
}

// TestPullRecordsNotValidReference: a NOT VALID key is not enforced against
// existing rows, so a consumer that validates it makes the target reject data
// production holds.
func TestPullRecordsNotValidReference(t *testing.T) {
	conn := adminConn(t)
	seedReferences(t, conn)

	ctx := context.Background()
	for _, s := range []string{
		`CREATE TABLE ` + refSchema + `.legacy (id int PRIMARY KEY, cat_id int)`,
		`INSERT INTO ` + refSchema + `.legacy SELECT g, 9999 FROM generate_series(1, 10) g`,
		`ALTER TABLE ` + refSchema + `.legacy ADD CONSTRAINT legacy_cat_fkey
		   FOREIGN KEY (cat_id) REFERENCES ` + refSchema + `.categories(id) NOT VALID`,
		`ANALYZE ` + refSchema + `.legacy`,
	} {
		if _, err := conn.Exec(ctx, s); err != nil {
			t.Fatalf("seed not-valid (%s): %v", s, err)
		}
	}

	f, err := Fast(ctx, conn, Options{Schemas: []string{refSchema}})
	if err != nil {
		t.Fatalf("Fast: %v", err)
	}
	refs := f.Tables[refSchema+".legacy"].References
	if len(refs) != 1 {
		t.Fatalf("want one reference, got %d", len(refs))
	}
	if refs[0].Validated == nil || *refs[0].Validated {
		t.Errorf("NOT VALID key recorded as validated (%v); the target would reject data production holds", refs[0].Validated)
	}

	// A validated key must NOT carry the field, so ordinary fixtures stay quiet.
	for _, ref := range f.Tables[refSchema+".categories"].References {
		if ref.Validated != nil {
			t.Errorf("validated key carries validated:%v; the field should be absent", *ref.Validated)
		}
	}
}
