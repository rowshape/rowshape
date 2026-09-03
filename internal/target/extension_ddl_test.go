package target

import (
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
)

// extensionFixture is shaped the way a real pull of an extension-using schema
// emits: a citext column, an index whose operator class comes from pg_trgm, and a
// covering unique index whose payload must stay out of the key.
func extensionFixture() *fixture.Fixture {
	return &fixture.Fixture{
		Extensions: map[string]fixture.Extension{
			"citext":  {Schema: "public"},
			"pg_trgm": {Schema: "public"},
		},
		Types: map[string]fixture.UserType{
			"app.mood": {Kind: "enum", Labels: []string{"sad", "ok"}, LabelCount: 2},
		},
		Tables: map[string]fixture.Table{
			"app.orders": {
				Rows: fixture.Fact[int64]{Value: 100},
				Columns: map[string]fixture.Column{
					"id":         {Type: "bigint"},
					"email":      {Type: "citext", Nullable: true},
					"total":      {Type: "numeric(12,2)", Nullable: true},
					"created_at": {Type: "timestamp with time zone"},
				},
				Indexes: []fixture.Index{
					{Name: "orders_email_trgm", Method: "gin", Columns: []string{"email"},
						Keys: []string{`email public.gin_trgm_ops`}},
					{Name: "orders_cover", Method: "btree", Columns: []string{"id", "created_at"},
						Include: []string{"total"}, Unique: true},
				},
			},
		},
	}
}

// TestDDLCreatesRequiredExtensions: without these statements the whole DDL
// transaction failed on the first citext column with `type "citext" does not
// exist`, so `validate` could not run at all on the schema.
func TestDDLCreatesRequiredExtensions(t *testing.T) {
	stmts := DDL(extensionFixture())

	var extAt, typeAt, tableAt = -1, -1, -1
	seen := map[string]bool{}
	for i, s := range stmts {
		switch {
		case strings.HasPrefix(s, "CREATE EXTENSION"):
			if extAt < 0 {
				extAt = i
			}
			for _, name := range []string{"citext", "pg_trgm"} {
				if strings.Contains(s, `"`+name+`"`) {
					seen[name] = true
				}
			}
		case strings.HasPrefix(s, "CREATE TYPE") && typeAt < 0:
			typeAt = i
		case strings.HasPrefix(s, "CREATE TABLE") && tableAt < 0:
			tableAt = i
		}
	}

	for _, name := range []string{"citext", "pg_trgm"} {
		if !seen[name] {
			t.Errorf("no CREATE EXTENSION for %q in:\n%s", name, strings.Join(stmts, "\n"))
		}
	}
	// Ordering is load-bearing, not cosmetic: an extension IS how some types arrive,
	// so it has to precede both CREATE TYPE and CREATE TABLE.
	if extAt < 0 || typeAt < 0 || tableAt < 0 {
		t.Fatalf("expected extension, type and table statements; got:\n%s", strings.Join(stmts, "\n"))
	}
	if extAt > typeAt || extAt > tableAt {
		t.Errorf("CREATE EXTENSION at %d must precede CREATE TYPE (%d) and CREATE TABLE (%d)", extAt, typeAt, tableAt)
	}
}

// TestDDLExtensionSchema: a type name in a column can be schema-qualified, so
// installing the extension elsewhere leaves that name unresolvable.
func TestDDLExtensionSchema(t *testing.T) {
	f := extensionFixture()
	f.Extensions = map[string]fixture.Extension{"vector": {Schema: "ext"}}

	stmts := DDL(f)
	joined := strings.Join(stmts, "\n")
	if !strings.Contains(joined, `CREATE EXTENSION IF NOT EXISTS "vector" WITH SCHEMA "ext"`) {
		t.Errorf("extension not installed into its recorded schema:\n%s", joined)
	}
	// CREATE EXTENSION ... WITH SCHEMA requires the schema to exist first, and an
	// extension can live in a schema no table does.
	schemaAt, extAt := -1, -1
	for i, s := range stmts {
		if strings.Contains(s, `CREATE SCHEMA IF NOT EXISTS "ext"`) && schemaAt < 0 {
			schemaAt = i
		}
		if strings.HasPrefix(s, "CREATE EXTENSION") && extAt < 0 {
			extAt = i
		}
	}
	if schemaAt < 0 {
		t.Fatalf("extension schema never created:\n%s", joined)
	}
	if schemaAt > extAt {
		t.Errorf("CREATE SCHEMA at %d must precede CREATE EXTENSION at %d", schemaAt, extAt)
	}
}

// TestDDLNoExtensionsWhenNoneRequired: a fixture from a schema that uses no
// extensions must not emit a stray statement, so ordinary fixtures do not change.
func TestDDLNoExtensionsWhenNoneRequired(t *testing.T) {
	f := extensionFixture()
	f.Extensions = nil
	for _, s := range DDL(f) {
		if strings.HasPrefix(s, "CREATE EXTENSION") {
			t.Errorf("unexpected extension statement: %s", s)
		}
	}
}

// TestSecondaryIndexKeepsIncludeOutOfKey: this is the wrong-PASS case. Folding the
// payload in made `UNIQUE (id, created_at) INCLUDE (total)` a three-column unique
// index, which enforces strictly LESS than production — so the disposable database
// accepted rows the source database rejects.
func TestSecondaryIndexKeepsIncludeOutOfKey(t *testing.T) {
	stmt := findStatement(t, SecondaryIndexes(extensionFixture()), "orders_cover")

	key := stmt[strings.Index(stmt, "(")+1 : strings.Index(stmt, ")")]
	if strings.Contains(key, "total") {
		t.Errorf("INCLUDE column is in the index key, widening what it enforces: %s", stmt)
	}
	if !strings.Contains(stmt, `INCLUDE ("total")`) {
		t.Errorf("INCLUDE payload dropped: %s", stmt)
	}
	if !strings.HasPrefix(stmt, "CREATE UNIQUE INDEX") {
		t.Errorf("covering index lost its uniqueness: %s", stmt)
	}
}

// TestSecondaryIndexKeepsOperatorClass: an index recorded with a non-default
// operator class must be rebuilt with it, or the build fails outright
// (`data type text has no default operator class for access method "gin"`) and the
// disposable database silently lacks an index production has.
func TestSecondaryIndexKeepsOperatorClass(t *testing.T) {
	stmt := findStatement(t, SecondaryIndexes(extensionFixture()), "orders_email_trgm")
	if !strings.Contains(stmt, "gin_trgm_ops") {
		t.Errorf("operator class dropped from the index: %s", stmt)
	}
}

func findStatement(t *testing.T, stmts []string, name string) string {
	t.Helper()
	for _, s := range stmts {
		if strings.Contains(s, `"`+name+`"`) {
			return s
		}
	}
	t.Fatalf("no statement for %q in:\n%s", name, strings.Join(stmts, "\n"))
	return ""
}
