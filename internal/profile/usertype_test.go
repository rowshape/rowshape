package profile

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

const typeSchema = "rowshape_types_test"

// seedTypes builds a schema whose columns are typed with an enum and a domain —
// the two user-defined kinds a column commonly carries — plus a money column,
// each of which broke `pull` in a different way.
func seedTypes(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	ctx := context.Background()
	stmts := []string{
		`DROP SCHEMA IF EXISTS ` + typeSchema + ` CASCADE`,
		`CREATE SCHEMA ` + typeSchema,
		`CREATE TYPE ` + typeSchema + `.mood AS ENUM ('sad', 'ok', 'happy')`,
		`CREATE DOMAIN ` + typeSchema + `.positive_int AS integer CHECK (VALUE > 0)`,
		`CREATE TABLE ` + typeSchema + `.t (
			id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			m ` + typeSchema + `.mood NOT NULL,
			pos ` + typeSchema + `.positive_int NOT NULL,
			cost money NOT NULL
		)`,
		`INSERT INTO ` + typeSchema + `.t (m, pos, cost)
		   SELECT (ARRAY['sad','ok','happy'])[1 + (g % 3)]::` + typeSchema + `.mood,
		          1 + (g % 50),
		          ((g % 997)::numeric)::money
		     FROM generate_series(1, 2000) g`,
		`ANALYZE ` + typeSchema + `.t`,
	}
	for _, s := range stmts {
		if _, err := conn.Exec(ctx, s); err != nil {
			t.Fatalf("seed types (%s): %v", s, err)
		}
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+typeSchema+` CASCADE`)
	})
}

// TestPullDefinesUserTypes: a column typed with an enum or a domain is only a NAME
// in the fixture, and a fresh disposable database has never heard of it. Without
// definitions the DDL that receives hydrated rows failed with
//
//	ERROR: type "…mood" does not exist (SQLSTATE 42704)
//
// so no schema using a Postgres enum or domain could be hydrated at all. This
// asserts pull now records both, keyed by the same name the column carries.
func TestPullDefinesUserTypes(t *testing.T) {
	conn := adminConn(t)
	seedTypes(t, conn)

	f, err := Fast(context.Background(), conn, Options{Schemas: []string{typeSchema}})
	if err != nil {
		t.Fatalf("Fast: %v", err)
	}

	tbl, ok := f.Tables[typeSchema+".t"]
	if !ok {
		t.Fatalf("table not pulled; got %v", f.Tables)
	}

	// The definition must be reachable by the exact string the column declares —
	// that is the join a consumer performs.
	enumName := tbl.Columns["m"].Type
	labels, isEnum := f.EnumLabels(enumName)
	if !isEnum {
		t.Fatalf("column m has type %q but no enum definition; types = %v", enumName, f.Types)
	}
	if got := strings.Join(labels, ","); got != "sad,ok,happy" {
		t.Errorf("labels = %q, want sad,ok,happy in declaration order (Postgres orders the type by it)", got)
	}

	domName := tbl.Columns["pos"].Type
	dom, ok := f.Types[domName]
	if !ok {
		t.Fatalf("column pos has type %q but no domain definition; types = %v", domName, f.Types)
	}
	if dom.Kind != "domain" || dom.Base != "integer" {
		t.Errorf("domain recorded as %+v, want kind=domain base=integer", dom)
	}
	if !strings.Contains(dom.Check, "VALUE > 0") {
		t.Errorf("domain check = %q, want the VALUE > 0 predicate", dom.Check)
	}

	// Only REFERENCED types are defined. The database also holds information_schema
	// domains; a fixture that dragged those in would be describing types no column
	// uses.
	for name := range f.Types {
		if !strings.HasPrefix(name, typeSchema+".") {
			t.Errorf("unreferenced type %q leaked into the fixture", name)
		}
	}
}

// TestPullProfilesDomainByItsBaseType: profiling keys off the type category, and a
// domain read as an opaque name fell into the text category — so a domain over
// integer came back with no range and no histogram. The column's shape was lost at
// the source, and hydrate then had nothing to generate from, producing values that
// violated the domain's own CHECK.
//
// The fixture must still RECORD the domain name: that is the column's real type.
// Only the profiling decision follows the base.
func TestPullProfilesDomainByItsBaseType(t *testing.T) {
	conn := adminConn(t)
	seedTypes(t, conn)

	f, err := Fast(context.Background(), conn, Options{Schemas: []string{typeSchema}})
	if err != nil {
		t.Fatalf("Fast: %v", err)
	}
	pos := f.Tables[typeSchema+".t"].Columns["pos"]

	if !strings.HasSuffix(pos.Type, ".positive_int") {
		t.Errorf("column type = %q; the domain name must be preserved, not replaced by its base", pos.Type)
	}
	if pos.Range == nil {
		t.Fatal("domain over integer got no range — it was profiled as text, losing the column's shape")
	}
	lo, hi := pos.Range.Min, pos.Range.Max
	if !numericNear(lo, 1) || !numericNear(hi, 50) {
		t.Errorf("range = [%v, %v], want about [1, 50] (the seeded extent)", lo, hi)
	}
	// A range on a text column is forbidden by §6.1, so the presence of one is also
	// proof the column was categorized as numeric.
	if pos.Format != "" {
		t.Errorf("format = %q; a numeric column carries no text format class", pos.Format)
	}
}

// TestPullHandlesMoneyColumns: Postgres refuses to cast money to a float
// directly, so the range aggregate aborted the ENTIRE pull with a tool error —
//
//	ERROR: cannot cast type money to double precision (SQLSTATE 42846)
//
// for every schema that prices anything in `money`. Routing through numeric, which
// money does cast to, is the documented path.
func TestPullHandlesMoneyColumns(t *testing.T) {
	conn := adminConn(t)
	seedTypes(t, conn)

	f, err := Fast(context.Background(), conn, Options{Schemas: []string{typeSchema}})
	if err != nil {
		t.Fatalf("Fast failed on a money column: %v", err)
	}
	cost := f.Tables[typeSchema+".t"].Columns["cost"]
	if cost.Range == nil {
		t.Fatal("money column got no range")
	}
	if !numericNear(cost.Range.Min, 0) || !numericNear(cost.Range.Max, 996) {
		t.Errorf("money range = [%v, %v], want about [0, 996]", cost.Range.Min, cost.Range.Max)
	}
}

// numericNear reports whether an untyped range bound is within 1 of want. Bounds
// come back as a float or an integer depending on the column, and the exact
// representation is not what these tests are about.
func numericNear(v any, want float64) bool {
	var got float64
	switch x := v.(type) {
	case float64:
		got = x
	case int64:
		got = float64(x)
	case int:
		got = float64(x)
	default:
		return false
	}
	d := got - want
	return d < 1 && d > -1
}
