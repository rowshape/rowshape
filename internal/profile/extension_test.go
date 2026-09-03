package profile

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/rowshape/rowshape/internal/fixture"
)

const extSchema = "rowshape_ext_test"

// seedExtensions builds a schema that depends on extensions two different ways —
// through a column TYPE (citext, plain and as an array) and through an index
// OPERATOR CLASS (gin_trgm_ops) — plus the index shapes whose rendering the
// per-column pg_get_indexdef form silently drops.
//
// It skips rather than fails when the extensions are not installable, because a
// stripped-down server that lacks contrib is a property of the environment, not a
// defect in the code under test.
func seedExtensions(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	ctx := context.Background()
	for _, ext := range []string{"citext", "pg_trgm"} {
		if _, err := conn.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS `+ext); err != nil {
			t.Skipf("extension %s not available on this server: %v", ext, err)
		}
	}
	stmts := []string{
		`DROP SCHEMA IF EXISTS ` + extSchema + ` CASCADE`,
		`CREATE SCHEMA ` + extSchema,
		`CREATE TABLE ` + extSchema + `.t (
			id      integer PRIMARY KEY,
			email   citext,
			tags    citext[],
			status  text NOT NULL DEFAULT 'pending',
			seen_at timestamptz NOT NULL,
			payload jsonb NOT NULL DEFAULT '{}'::jsonb
		)`,
		// Needs pg_trgm and nothing about a column type says so.
		`CREATE INDEX t_email_trgm ON ` + extSchema + `.t USING gin (email gin_trgm_ops)`,
		// A non-default ordering, which the per-column indexdef form does not render.
		`CREATE INDEX t_seen_desc ON ` + extSchema + `.t (seen_at DESC NULLS LAST)`,
		// A covering unique index: `total` is payload, NOT part of what it enforces.
		`CREATE UNIQUE INDEX t_cover ON ` + extSchema + `.t (id, seen_at) INCLUDE (status)`,
		// The default opclass case, which must stay bare so ordinary fixtures do not churn.
		`CREATE INDEX t_payload ON ` + extSchema + `.t USING gin (payload)`,
		`INSERT INTO ` + extSchema + `.t (id, email, tags, seen_at)
		   SELECT g, ('u' || g || '@example.com')::citext, ARRAY['a']::citext[],
		          '2025-01-01'::timestamptz + (g || ' seconds')::interval
		     FROM generate_series(1, 500) g`,
		`ANALYZE ` + extSchema + `.t`,
	}
	for _, s := range stmts {
		if _, err := conn.Exec(ctx, s); err != nil {
			t.Fatalf("seed extensions (%s): %v", s, err)
		}
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+extSchema+` CASCADE`)
	})
}

// TestPullRecordsRequiredExtensions: a citext column names a type that only exists
// once the extension is installed, and a freshly created disposable database has
// none. Without this the whole DDL transaction failed with
//
//	ERROR: type "citext" does not exist (SQLSTATE 42704)
//
// and `validate` could not run AT ALL against any schema using an extension type —
// citext on an email column, hstore, ltree, postgis geometry, pgvector's `vector`.
func TestPullRecordsRequiredExtensions(t *testing.T) {
	conn := adminConn(t)
	seedExtensions(t, conn)

	f, err := Fast(context.Background(), conn, Options{Schemas: []string{extSchema}})
	if err != nil {
		t.Fatalf("Fast: %v", err)
	}

	// citext arrives through a column type; pg_trgm only through an index operator
	// class, which is why opclasses have to feed the resolution too.
	for _, want := range []string{"citext", "pg_trgm"} {
		if _, ok := f.Extensions[want]; !ok {
			t.Errorf("fixture does not require %q; got %v", want, keysOf(f.Extensions))
		}
	}
	// plpgsql is in every database from initdb onward, so requiring it says nothing.
	if _, ok := f.Extensions["plpgsql"]; ok {
		t.Error("plpgsql recorded as a requirement; it is present in every database")
	}
}

// TestPullRecordsOnlyReferencedExtensions: recording every extension INSTALLED on
// the source would make a fixture demand postgis of a disposable server for a
// schema that never touches it.
func TestPullRecordsOnlyReferencedExtensions(t *testing.T) {
	conn := adminConn(t)
	seedExtensions(t, conn)

	// A second schema that uses none of the extensions, read on its own.
	ctx := context.Background()
	const plain = extSchema + "_plain"
	for _, s := range []string{
		`DROP SCHEMA IF EXISTS ` + plain + ` CASCADE`,
		`CREATE SCHEMA ` + plain,
		`CREATE TABLE ` + plain + `.p (id integer PRIMARY KEY, name text)`,
		`INSERT INTO ` + plain + `.p SELECT g, 'n' || g FROM generate_series(1, 100) g`,
		`ANALYZE ` + plain + `.p`,
	} {
		if _, err := conn.Exec(ctx, s); err != nil {
			t.Fatalf("seed plain (%s): %v", s, err)
		}
	}
	t.Cleanup(func() { _, _ = conn.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+plain+` CASCADE`) })

	f, err := Fast(ctx, conn, Options{Schemas: []string{plain}})
	if err != nil {
		t.Fatalf("Fast: %v", err)
	}
	if len(f.Extensions) != 0 {
		t.Errorf("schema uses no extensions but fixture requires %v", keysOf(f.Extensions))
	}
}

// TestPullSplitsIncludeFromIndexKey: an INCLUDE column is payload, and folding it
// into the key recorded `UNIQUE (id, seen_at) INCLUDE (status)` as unique on all
// three — strictly weaker than what production enforces, so the disposable
// database accepted rows the source database rejects and a migration violating the
// real two-column uniqueness could reach PASS.
func TestPullSplitsIncludeFromIndexKey(t *testing.T) {
	conn := adminConn(t)
	seedExtensions(t, conn)

	f, err := Fast(context.Background(), conn, Options{Schemas: []string{extSchema}})
	if err != nil {
		t.Fatalf("Fast: %v", err)
	}
	ix := findIndex(t, f.Tables[extSchema+".t"].Indexes, "t_cover")
	if got, want := strings.Join(ix.Columns, ","), "id,seen_at"; got != want {
		t.Errorf("covering index key = [%s], want [%s]", got, want)
	}
	if got, want := strings.Join(ix.Include, ","), "status"; got != want {
		t.Errorf("covering index include = [%s], want [%s]", got, want)
	}
	if !ix.Unique {
		t.Error("covering index lost its uniqueness")
	}
}

// TestPullKeepsIndexOpclassAndOrdering: pg_get_indexdef's per-column form renders
// the key EXPRESSION only. It was believed to include "any DESC or operator class"
// and does not, so `(seen_at DESC NULLS LAST)` came back as `seen_at` and
// `gin (email gin_trgm_ops)` came back as `email` — which then failed to build
// (`data type text has no default operator class for access method "gin"`),
// leaving the disposable database without an index production has.
func TestPullKeepsIndexOpclassAndOrdering(t *testing.T) {
	conn := adminConn(t)
	seedExtensions(t, conn)

	f, err := Fast(context.Background(), conn, Options{Schemas: []string{extSchema}})
	if err != nil {
		t.Fatalf("Fast: %v", err)
	}
	idxs := f.Tables[extSchema+".t"].Indexes

	trgm := findIndex(t, idxs, "t_email_trgm")
	if len(trgm.Keys) != 1 || !strings.Contains(trgm.Keys[0], "gin_trgm_ops") {
		t.Errorf("trigram index keys = %v, want one key naming gin_trgm_ops", trgm.Keys)
	}

	desc := findIndex(t, idxs, "t_seen_desc")
	if len(desc.Keys) != 1 || !strings.Contains(desc.Keys[0], "DESC") {
		t.Errorf("descending index keys = %v, want one key carrying DESC", desc.Keys)
	}

	// The default-opclass case must stay bare: carrying `keys` that only restate
	// `columns` is redundancy that can later disagree with itself, and it would
	// rewrite every existing fixture for no gain.
	payload := findIndex(t, idxs, "t_payload")
	if len(payload.Keys) != 0 {
		t.Errorf("default-opclass index carries keys %v; it should rely on columns", payload.Keys)
	}
}

func findIndex(t *testing.T, idxs []fixture.Index, name string) fixture.Index {
	t.Helper()
	for _, ix := range idxs {
		if ix.Name == name {
			return ix
		}
	}
	t.Fatalf("index %q not found", name)
	return fixture.Index{}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
