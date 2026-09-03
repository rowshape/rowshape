package plan

import (
	"reflect"
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
)

// The plan package's classification logic (Items/classify/planTable/
// addColumnName/addsBareColumn) was covered only end-to-end through
// `rowshape plan --against` — its per-package coverage was 11%, everything but
// RedactURL untested. That is the fragile kind of "covered": the classifier
// moves as a side effect of a live-DB e2e path and would rot silently under a
// refactor. These are direct table tests against the pure functions. See
// docs/TESTING-GAPS.md (backlog item 1).

// schema builds a fixture whose tables/columns exist, so classify's
// exists/conflict branches can be reached without a database.
func schema(tables map[string][]string) *fixture.Fixture {
	f := &fixture.Fixture{Tables: map[string]fixture.Table{}}
	for name, cols := range tables {
		cm := map[string]fixture.Column{}
		for _, c := range cols {
			cm[c] = fixture.Column{}
		}
		f.Tables[name] = fixture.Table{Columns: cm}
	}
	return f
}

func TestItemsClassify(t *testing.T) {
	current := schema(map[string][]string{
		"public.users": {"id", "email"},
	})

	cases := []struct {
		name       string
		stmt       string
		wantChange string
		wantStatus string
		wantNote   string
	}{
		{
			name:       "add new column to existing table",
			stmt:       "ALTER TABLE public.users ADD COLUMN age int",
			wantChange: "add column users.age",
			wantStatus: "ok",
			wantNote:   "column will be added",
		},
		{
			name:       "add column that already exists is a conflict",
			stmt:       "ALTER TABLE public.users ADD COLUMN email text",
			wantChange: "add column users.email",
			wantStatus: "conflict",
			wantNote:   "column already exists",
		},
		{
			name:       "add column to a table not on the live schema",
			stmt:       "ALTER TABLE public.orders ADD COLUMN total int",
			wantChange: "add column orders.total",
			wantStatus: "missing-target",
			wantNote:   "target table not present on the live schema",
		},
		{
			name:       "bare ADD (no COLUMN keyword) still reads as a column add",
			stmt:       "ALTER TABLE public.users ADD age int",
			wantChange: "add column users.age",
			wantStatus: "ok",
			wantNote:   "column will be added",
		},
		{
			name:       "ADD CONSTRAINT is not a column add",
			stmt:       "ALTER TABLE public.users ADD CONSTRAINT users_email_key UNIQUE (email)",
			wantChange: "add constraint on users",
			wantStatus: "ok",
			wantNote:   "target present",
		},
		{
			name:       "SET NOT NULL on an existing table",
			stmt:       "ALTER TABLE public.users ALTER COLUMN email SET NOT NULL",
			wantChange: "set NOT NULL on users",
			wantStatus: "ok",
			wantNote:   "target present",
		},
		{
			name:       "CREATE INDEX targets the ON table",
			stmt:       "CREATE INDEX idx_users_email ON public.users (email)",
			wantChange: "create index on users",
			wantStatus: "ok",
			wantNote:   "target present",
		},
		{
			name:       "CREATE UNIQUE INDEX on a missing table",
			stmt:       "CREATE UNIQUE INDEX idx_orders ON public.orders (id)",
			wantChange: "create index on orders",
			wantStatus: "missing-target",
			wantNote:   "target table not present on the live schema",
		},
		{
			name:       "DROP TABLE that exists",
			stmt:       "DROP TABLE public.users",
			wantChange: "drop table users",
			wantStatus: "ok",
			wantNote:   "table will be dropped",
		},
		{
			name:       "DROP TABLE IF EXISTS on an absent table is a conflict",
			stmt:       "DROP TABLE IF EXISTS public.orders",
			wantChange: "drop table orders",
			wantStatus: "conflict",
			wantNote:   "table is already absent",
		},
		{
			name:       "unrecognized statement falls through to a generic change",
			stmt:       "CREATE TYPE mood AS ENUM ('happy')",
			wantChange: "schema change",
			wantStatus: "ok",
			// planTable finds no table, so the "not present" note is expected.
			wantNote: "target table not present on the live schema",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items := Items(current, []string{tc.stmt})
			if len(items) != 1 {
				t.Fatalf("Items returned %d items, want 1: %+v", len(items), items)
			}
			got := items[0]
			if got.Change != tc.wantChange {
				t.Errorf("Change = %q, want %q", got.Change, tc.wantChange)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tc.wantStatus)
			}
			if got.Note != tc.wantNote {
				t.Errorf("Note = %q, want %q", got.Note, tc.wantNote)
			}
		})
	}
}

// Migrations write unqualified names (`ALTER TABLE users ...`) but the live
// schema — and the fixture it is read into — keys tables by their qualified name
// (`public.users`, RFC §5). classify must resolve the name through
// ResolveTable before the lookup, exactly as the analyzers do; a raw
// current.Tables[table] miss reported a real table as `missing-target` for every
// unqualified statement — the common case. Every existing classify case used a
// qualified name in BOTH the statement and the fixture key, so the miss was never
// exercised. These cases pin the resolved path across all four operation branches.
func TestUnqualifiedStatementResolvesToQualifiedSchema(t *testing.T) {
	current := schema(map[string][]string{
		"public.users":  {"id", "email"},
		"public.orders": {"id"},
	})

	cases := []struct {
		name       string
		stmt       string
		wantStatus string
		wantNote   string
	}{
		{
			name:       "unqualified ADD COLUMN resolves to the qualified table",
			stmt:       "ALTER TABLE users ADD COLUMN age int",
			wantStatus: "ok",
			wantNote:   "column will be added",
		},
		{
			name:       "unqualified CREATE INDEX resolves and is not missing-target",
			stmt:       "CREATE INDEX idx_orders_id ON orders (id)",
			wantStatus: "ok",
			wantNote:   "target present",
		},
		{
			name:       "unqualified ADD CONSTRAINT resolves",
			stmt:       "ALTER TABLE users ADD CONSTRAINT users_email_key UNIQUE (email)",
			wantStatus: "ok",
			wantNote:   "target present",
		},
		{
			name:       "unqualified DROP TABLE resolves to a present table",
			stmt:       "DROP TABLE users",
			wantStatus: "ok",
			wantNote:   "table will be dropped",
		},
		{
			name:       "an unqualified name with no match is still missing-target",
			stmt:       "CREATE INDEX i ON payments (id)",
			wantStatus: "missing-target",
			wantNote:   "target table not present on the live schema",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items := Items(current, []string{tc.stmt})
			if len(items) != 1 {
				t.Fatalf("Items returned %d items, want 1: %+v", len(items), items)
			}
			if items[0].Status != tc.wantStatus {
				t.Errorf("Status = %q, want %q", items[0].Status, tc.wantStatus)
			}
			if items[0].Note != tc.wantNote {
				t.Errorf("Note = %q, want %q", items[0].Note, tc.wantNote)
			}
		})
	}
}

func TestItemsSkipsTxControlAndBlanks(t *testing.T) {
	current := schema(map[string][]string{"public.users": {"id"}})
	stmts := []string{
		"BEGIN",
		"",
		"   ",
		"COMMIT;",
		"START TRANSACTION",
		"ALTER TABLE public.users ADD COLUMN age int",
		"savepoint sp1",
		"ROLLBACK",
	}
	items := Items(current, stmts)
	if len(items) != 1 {
		t.Fatalf("Items kept %d statements, want 1 (only the ALTER): %+v", len(items), items)
	}
	if items[0].Change != "add column users.age" {
		t.Errorf("surviving item = %q, want the column add", items[0].Change)
	}
}

func TestPlanTable(t *testing.T) {
	cases := []struct {
		stmt string
		want string
	}{
		{"ALTER TABLE public.users ADD COLUMN a int", "public.users"},
		{"ALTER TABLE ONLY public.users ADD COLUMN a int", "public.users"},  // ONLY is skipped
		{`ALTER TABLE "public"."Users" ADD COLUMN a int`, `public"."Users`}, // quotes trimmed at the ends
		{"DROP TABLE public.orders", "public.orders"},
		{"DROP TABLE IF EXISTS public.orders", "public.orders"},
		{"DROP TABLE public.orders;", "public.orders"},
		{"CREATE INDEX i ON public.users (email)", "public.users"},
		{"CREATE UNIQUE INDEX i ON public.users(email)", "public.users"}, // no space before ( (regression: mis-parsed as public.users(email))
		{"CREATE TYPE mood AS ENUM ('x')", ""},                           // no table
	}
	for _, tc := range cases {
		got := planTable(tc.stmt, strings.ToUpper(tc.stmt))
		if got != tc.want {
			t.Errorf("planTable(%q) = %q, want %q", tc.stmt, got, tc.want)
		}
	}
}

func TestAddsBareColumn(t *testing.T) {
	yes := []string{
		"ALTER TABLE T ADD C INT",
		"ALTER TABLE T ADD COLUMN C INT",
	}
	no := []string{
		"ALTER TABLE T ADD CONSTRAINT X UNIQUE (C)",
		"ALTER TABLE T ADD PRIMARY KEY (C)",
		"ALTER TABLE T ADD UNIQUE (C)",
		"ALTER TABLE T ADD FOREIGN KEY (C) REFERENCES U",
		"ALTER TABLE T ADD CHECK (C > 0)",
		"ALTER TABLE T ALTER COLUMN C SET NOT NULL",
	}
	for _, s := range yes {
		if !addsBareColumn(strings.ToUpper(s)) {
			t.Errorf("addsBareColumn(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if addsBareColumn(strings.ToUpper(s)) {
			t.Errorf("addsBareColumn(%q) = true, want false", s)
		}
	}
}

func TestAddColumnName(t *testing.T) {
	cases := []struct {
		stmt string
		want string
	}{
		{"ALTER TABLE t ADD COLUMN age int", "age"},
		{"ALTER TABLE t ADD age int", "age"},          // bare ADD fallback
		{`ALTER TABLE t ADD COLUMN "age" int`, "age"}, // quotes trimmed
		{"ALTER TABLE t RENAME TO u", ""},             // no ADD at all
	}
	for _, tc := range cases {
		got := addColumnName(tc.stmt, strings.ToUpper(tc.stmt))
		if got != tc.want {
			t.Errorf("addColumnName(%q) = %q, want %q", tc.stmt, got, tc.want)
		}
	}
}

func TestItemsIsPureAndOrderPreserving(t *testing.T) {
	current := schema(map[string][]string{"t": {"id"}})
	stmts := []string{
		"ALTER TABLE t ADD COLUMN a int",
		"CREATE INDEX i ON t (a)",
		"DROP TABLE t",
	}
	got := Items(current, stmts)
	var order []string
	for _, it := range got {
		order = append(order, it.Change)
	}
	want := []string{"add column t.a", "create index on t", "drop table t"}
	if !reflect.DeepEqual(order, want) {
		t.Errorf("Items order = %v, want %v", order, want)
	}
}
