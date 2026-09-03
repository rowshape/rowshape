package target

import (
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
)

// allStatements is every statement the loader runs for a fixture, in order: the
// required DDL followed by the best-effort secondary indexes. Tests about
// duplicates or coverage must look at the union, since the split between the two is
// about failure handling, not about what the target ends up with.
func allStatements(f *fixture.Fixture) []string {
	return append(DDL(f), SecondaryIndexes(f)...)
}

// realPullFixture builds a fixture shaped the way a real `rowshape pull` emits
// one — which is the whole point of this file.
//
// Postgres backs a PRIMARY KEY / UNIQUE constraint with an implicit index named
// after the constraint. A conformant pull records BOTH (RFC §6.4 constraints,
// §6.5 indexes) because both genuinely exist in the catalog. Every hand-written
// test fixture in this repo lists constraints WITHOUT their backing indexes, so
// none of them could catch a duplicate-index bug — while every real schema with a
// primary key hits it on the first `validate`.
func realPullFixture() *fixture.Fixture {
	return &fixture.Fixture{
		Tables: map[string]fixture.Table{
			"public.orders": {
				Rows: fixture.Fact[int64]{Value: 20000},
				Columns: map[string]fixture.Column{
					"id":     {Type: "bigint"},
					"email":  {Type: "text"},
					"status": {Type: "text", Nullable: true},
				},
				Constraints: []fixture.Constraint{
					{Name: "orders_pkey", Kind: "primary_key", Columns: []string{"id"}},
					{Name: "orders_email_key", Kind: "unique", Columns: []string{"email"}},
				},
				Indexes: []fixture.Index{
					// The implicit indexes Postgres created for the two constraints
					// above. Same names — they are the same objects.
					{Name: "orders_pkey", Method: "btree", Columns: []string{"id"}, Unique: true},
					{Name: "orders_email_key", Method: "btree", Columns: []string{"email"}, Unique: true},
					// A genuinely secondary index, which MUST still be created.
					{Name: "orders_status_idx", Method: "btree", Columns: []string{"status"}},
				},
			},
		},
	}
}

// TestDDLDoesNotDuplicateConstraintBackedIndexes: CREATE TABLE already emits the
// PRIMARY KEY and UNIQUE constraints, and Postgres creates their indexes with it.
// Emitting those indexes again is a hard failure:
//
//	ERROR: relation "orders_pkey" already exists (SQLSTATE 42P07)
//
// which fails the DDL and takes `rowshape validate` down with it, on any real
// schema.
func TestDDLDoesNotDuplicateConstraintBackedIndexes(t *testing.T) {
	// Both halves, because together they are what the loader actually executes:
	// DDL creates the tables (and their constraint-backed indexes), SecondaryIndexes
	// adds the rest. A duplicate between the two is exactly the bug under test.
	all := strings.Join(allStatements(realPullFixture()), ";\n")

	for _, name := range []string{"orders_pkey", "orders_email_key"} {
		if n := strings.Count(all, `INDEX "`+name+`"`); n != 0 {
			t.Errorf("DDL creates index %q explicitly (%d time(s)), but CREATE TABLE's constraint already builds it — "+
				"Postgres fails with 'relation \"%s\" already exists'.\n%s", name, n, name, all)
		}
	}

	// The constraints themselves must still be declared — dropping them to dodge
	// the duplicate would silently lose the uniqueness the fixture recorded.
	if !strings.Contains(all, "PRIMARY KEY") {
		t.Errorf("the primary key must still be declared:\n%s", all)
	}
	if !strings.Contains(all, "UNIQUE (") {
		t.Errorf("the unique constraint must still be declared:\n%s", all)
	}

	// A genuinely secondary index is still created — that is what the loop is for.
	if !strings.Contains(all, `INDEX "orders_status_idx"`) {
		t.Errorf("secondary indexes must still be created:\n%s", all)
	}
}

// TestDDLKeepsSecondaryIndexWithConstraintLikeName: only names that actually
// belong to a constraint on THIS table are skipped. An index merely named like
// one is still a real index and must be built.
func TestDDLKeepsSecondaryIndexWithConstraintLikeName(t *testing.T) {
	f := &fixture.Fixture{
		Tables: map[string]fixture.Table{
			"public.t": {
				Rows:    fixture.Fact[int64]{Value: 1},
				Columns: map[string]fixture.Column{"a": {Type: "integer"}},
				// No constraints at all.
				Indexes: []fixture.Index{{Name: "t_pkey", Method: "btree", Columns: []string{"a"}}},
			},
		},
	}
	all := strings.Join(allStatements(f), ";\n")
	if !strings.Contains(all, `INDEX "t_pkey"`) {
		t.Errorf("an index with no matching constraint must still be created:\n%s", all)
	}
}

// TestDDLCreatesUserDefinedTypes: a column typed with an enum or a domain needs
// that type to EXIST before its CREATE TABLE runs.
//
// A fixture records a column's type as a name. For `integer` that is enough —
// every server has one. For `z.mood` it is not, and the DDL failed outright:
//
//	ERROR: type "z.mood" does not exist (SQLSTATE 42704)
//
// which meant no schema using a Postgres enum or domain could be hydrated at all.
func TestDDLCreatesUserDefinedTypes(t *testing.T) {
	f := &fixture.Fixture{
		Types: map[string]fixture.UserType{
			"z.mood":         {Kind: "enum", Labels: []string{"sad", "ok", "happy"}, LabelCount: 3},
			"z.positive_int": {Kind: "domain", Base: "integer", Check: "(VALUE > 0)", NotNull: true},
		},
		Tables: map[string]fixture.Table{
			"z.wide": {
				Rows: fixture.Fact[int64]{Value: 10},
				Columns: map[string]fixture.Column{
					"m":   {Type: "z.mood", Nullable: false},
					"pos": {Type: "z.positive_int", Nullable: false},
				},
			},
		},
	}
	stmts := DDL(f)
	joined := strings.Join(stmts, "\n")

	// Enum labels keep declaration order: Postgres orders the type by it, so a
	// migration that compares or sorts the column depends on it.
	wantEnum := `CREATE TYPE "z"."mood" AS ENUM ('sad', 'ok', 'happy')`
	if !strings.Contains(joined, wantEnum) {
		t.Errorf("missing enum definition\nwant: %s\ngot:\n%s", wantEnum, joined)
	}
	wantDomain := `CREATE DOMAIN "z"."positive_int" AS integer NOT NULL CHECK (VALUE > 0)`
	if !strings.Contains(joined, wantDomain) {
		t.Errorf("missing domain definition\nwant: %s\ngot:\n%s", wantDomain, joined)
	}

	// Ordering is the point: schema, then types, then the table that uses them.
	iSchema := strings.Index(joined, "CREATE SCHEMA")
	iType := strings.Index(joined, "CREATE TYPE")
	iDomain := strings.Index(joined, "CREATE DOMAIN")
	iTable := strings.Index(joined, "CREATE TABLE")
	if !(iSchema < iType && iType < iTable) || !(iDomain < iTable) {
		t.Errorf("statement order wrong: schema=%d type=%d domain=%d table=%d\n%s",
			iSchema, iType, iDomain, iTable, joined)
	}
}

// TestDDLTypeSchemaIsCreated: a user-defined type can live in a schema that holds
// no table, so the type names must contribute to the CREATE SCHEMA set — otherwise
// CREATE TYPE targets a schema that does not exist yet.
func TestDDLTypeSchemaIsCreated(t *testing.T) {
	f := &fixture.Fixture{
		Types: map[string]fixture.UserType{
			"enums.mood": {Kind: "enum", Labels: []string{"a", "b"}, LabelCount: 2},
		},
		Tables: map[string]fixture.Table{
			"app.t": {
				Rows:    fixture.Fact[int64]{Value: 1},
				Columns: map[string]fixture.Column{"m": {Type: "enums.mood"}},
			},
		},
	}
	joined := strings.Join(DDL(f), "\n")
	if !strings.Contains(joined, `CREATE SCHEMA IF NOT EXISTS "enums"`) {
		t.Errorf("type's own schema was not created:\n%s", joined)
	}
	if strings.Index(joined, `CREATE SCHEMA IF NOT EXISTS "enums"`) > strings.Index(joined, "CREATE TYPE") {
		t.Errorf("schema must be created before the type in it:\n%s", joined)
	}
}

// TestDDLStrictEnumUsesPlaceholders: under privacy:strict the vocabulary is
// withheld and only label_count survives (RFC §8.2). The type must still be
// created, at the right size — an enum column is otherwise unhydratable, because no
// arbitrary string is a member of the type.
func TestDDLStrictEnumUsesPlaceholders(t *testing.T) {
	f := &fixture.Fixture{
		Types: map[string]fixture.UserType{
			"z.mood": {Kind: "enum", LabelCount: 3}, // labels redacted
		},
		Tables: map[string]fixture.Table{
			"z.t": {Rows: fixture.Fact[int64]{Value: 1}, Columns: map[string]fixture.Column{"m": {Type: "z.mood"}}},
		},
	}
	joined := strings.Join(DDL(f), "\n")
	want := `CREATE TYPE "z"."mood" AS ENUM ('label_0', 'label_1', 'label_2')`
	if !strings.Contains(joined, want) {
		t.Errorf("strict enum not reconstructed at its declared size\nwant: %s\ngot:\n%s", want, joined)
	}
}

// TestDDLEnumLabelQuoting: labels are arbitrary text from the source catalog, so a
// label containing an apostrophe must not be able to break out of the statement.
func TestDDLEnumLabelQuoting(t *testing.T) {
	f := &fixture.Fixture{
		Types: map[string]fixture.UserType{
			"public.st": {Kind: "enum", Labels: []string{"it's", "ok"}, LabelCount: 2},
		},
		Tables: map[string]fixture.Table{
			"public.t": {Rows: fixture.Fact[int64]{Value: 1}, Columns: map[string]fixture.Column{"s": {Type: "public.st"}}},
		},
	}
	joined := strings.Join(DDL(f), "\n")
	if !strings.Contains(joined, `'it''s'`) {
		t.Errorf("apostrophe in an enum label was not escaped:\n%s", joined)
	}
}

// TestDDLOpaqueDomainCheckIsNotInvented: privacy:strict replaces a domain's CHECK
// with "opaque". The domain must still be created, WITHOUT a constraint — emitting
// `CHECK opaque` would be a syntax error, and inventing a real predicate would
// constrain hydrated data in a way production may not.
func TestDDLOpaqueDomainCheckIsNotInvented(t *testing.T) {
	f := &fixture.Fixture{
		Types: map[string]fixture.UserType{
			"z.d": {Kind: "domain", Base: "integer", Check: "opaque"},
		},
		Tables: map[string]fixture.Table{
			"z.t": {Rows: fixture.Fact[int64]{Value: 1}, Columns: map[string]fixture.Column{"c": {Type: "z.d"}}},
		},
	}
	joined := strings.Join(DDL(f), "\n")
	if !strings.Contains(joined, `CREATE DOMAIN "z"."d" AS integer`) {
		t.Errorf("domain not created:\n%s", joined)
	}
	if strings.Contains(joined, "opaque") {
		t.Errorf("the opaque placeholder leaked into SQL:\n%s", joined)
	}
}

// TestExpressionIndexIsRecreated: an index on `lower(email)` has no COLUMN key at
// all, so it was recorded with an empty column list and then silently dropped when
// the disposable database was built. A production UNIQUE index that does not exist
// in the target is a constraint no longer enforced — a migration that violates it
// can come back clean, which is the wrong-PASS the whole format exists to prevent.
func TestExpressionIndexIsRecreated(t *testing.T) {
	f := &fixture.Fixture{
		Tables: map[string]fixture.Table{
			"app.users": {
				Rows:    fixture.Fact[int64]{Value: 10},
				Columns: map[string]fixture.Column{"email": {Type: "text"}},
				Indexes: []fixture.Index{{
					Name:   "users_email_key",
					Method: "btree",
					Unique: true,
					// No Columns — this is what pull records for an expression index.
					Keys: []string{"lower(email)"},
				}},
			},
		},
	}
	stmts := SecondaryIndexes(f)
	all := strings.Join(stmts, ";\n")
	if len(stmts) == 0 {
		t.Fatal("the expression index was dropped; production uniqueness is not enforced in the target")
	}
	want := `CREATE UNIQUE INDEX "users_email_key" ON "app"."users" (lower(email))`
	if !strings.Contains(all, want) {
		t.Errorf("wrong statement\nwant: %s\ngot:  %s", want, all)
	}
}

// TestPartialIndexIsRecreated: a partial UNIQUE index is the standard soft-delete
// uniqueness pattern, and skipping it left the target enforcing less than production.
func TestPartialIndexIsRecreated(t *testing.T) {
	f := &fixture.Fixture{
		Tables: map[string]fixture.Table{
			"app.users": {
				Rows:    fixture.Fact[int64]{Value: 10},
				Columns: map[string]fixture.Column{"email": {Type: "text"}, "deleted_at": {Type: "timestamptz", Nullable: true}},
				Indexes: []fixture.Index{{
					Name:    "users_live_email_key",
					Method:  "btree",
					Unique:  true,
					Columns: []string{"email"},
					Partial: "deleted_at IS NULL",
				}},
			},
		},
	}
	all := strings.Join(SecondaryIndexes(f), ";\n")
	want := `CREATE UNIQUE INDEX "users_live_email_key" ON "app"."users" ("email") WHERE deleted_at IS NULL`
	if !strings.Contains(all, want) {
		t.Errorf("wrong statement\nwant: %s\ngot:  %s", want, all)
	}
}

// TestOpaquePartialPredicateIsNotInvented: privacy:strict leaves "opaque" where the
// predicate was. Emitting `WHERE opaque` is a syntax error, and guessing a predicate
// would change the index's SCOPE — a narrower or wider uniqueness rule than
// production has. The index is skipped instead.
func TestOpaquePartialPredicateIsNotInvented(t *testing.T) {
	f := &fixture.Fixture{
		Tables: map[string]fixture.Table{
			"app.t": {
				Rows:    fixture.Fact[int64]{Value: 1},
				Columns: map[string]fixture.Column{"a": {Type: "integer"}},
				Indexes: []fixture.Index{{
					Name: "t_a_idx", Method: "btree", Columns: []string{"a"}, Partial: "opaque",
				}},
			},
		},
	}
	if stmts := SecondaryIndexes(f); len(stmts) != 0 {
		t.Errorf("an opaque predicate produced SQL: %v", stmts)
	}
}

// TestKeysCarryMethodAndOrder: keys come from pg_get_indexdef and are already valid
// SQL, so a DESC or an operator class in a key must survive verbatim rather than be
// re-quoted into an identifier.
func TestKeysCarryMethodAndOrder(t *testing.T) {
	f := &fixture.Fixture{
		Tables: map[string]fixture.Table{
			"app.t": {
				Rows:    fixture.Fact[int64]{Value: 1},
				Columns: map[string]fixture.Column{"a": {Type: "text"}},
				Indexes: []fixture.Index{{
					Name: "t_multi", Method: "btree",
					Keys: []string{"a DESC", "lower(a) text_pattern_ops"},
				}},
			},
		},
	}
	all := strings.Join(SecondaryIndexes(f), ";\n")
	if !strings.Contains(all, `(a DESC, lower(a) text_pattern_ops)`) {
		t.Errorf("key modifiers were mangled: %s", all)
	}
}
