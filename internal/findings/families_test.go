package findings

import (
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/validate"
)

func famFixture() *fixture.Fixture {
	return &fixture.Fixture{
		RowshapeFixture: fixture.FormatVersion,
		Meta:            fixture.Meta{Engine: fixture.Engine{Name: "postgres", Version: "16"}},
		Tables: map[string]fixture.Table{
			"public.events": {
				Rows: fixture.Fact[int64]{Value: 100_000_000, Confidence: fixture.Exact},
				Columns: map[string]fixture.Column{
					"id":    {Type: "bigint", Nullable: false},
					"email": {Type: "text", Nullable: false},
					"cents": {Type: "integer", Nullable: false},
				},
			},
		},
	}
}

func famCodes(t *testing.T, sql string) []string {
	t.Helper()
	var sc []validate.Statement
	for _, s := range validate.SplitStatements(sql) {
		sc = append(sc, validate.Statement{SQL: s})
	}
	res := validate.BuildResult(famFixture(), &validate.Capture{Success: true, Statements: sc}, validate.Registered(), false)
	var out []string
	for _, fnd := range res.Findings {
		out = append(out, fnd.Code)
	}
	return out
}

// Whole operation families produced nothing. Each of these is a real hazard the
// catalog was silent on.
func TestPreviouslySilentFamilies(t *testing.T) {
	cases := []struct{ name, sql, want string }{
		{
			"STORED generated column forces a rewrite",
			"ALTER TABLE events ADD COLUMN total numeric GENERATED ALWAYS AS (cents / 100.0) STORED;",
			"RS-LOCK-001",
		},
		{
			"REPLICA IDENTITY NOTHING breaks CDC silently",
			"ALTER TABLE events REPLICA IDENTITY NOTHING;",
			"RS-DEPLOY-002",
		},
		{
			"DROP NOT NULL withdraws a contract",
			"ALTER TABLE events ALTER COLUMN email DROP NOT NULL;",
			"RS-DEPLOY-003",
		},
		{
			"ATTACH PARTITION locks the parent",
			"ALTER TABLE events ATTACH PARTITION events_2024 FOR VALUES FROM ('2024-01-01') TO ('2025-01-01');",
			"RS-LOCK-003",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			codes := famCodes(t, c.sql)
			found := false
			for _, code := range codes {
				if code == c.want {
					found = true
				}
			}
			if !found {
				t.Errorf("%q produced %v, want %s", c.sql, codes, c.want)
			}
		})
	}
}

// A VIRTUAL generated column is computed on read and does NOT rewrite, so it
// must not be flagged as one.
func TestVirtualGeneratedColumnIsNotARewrite(t *testing.T) {
	for _, code := range famCodes(t, "ALTER TABLE events ADD COLUMN total numeric GENERATED ALWAYS AS (cents / 100.0);") {
		if code == "RS-LOCK-001" {
			t.Error("a non-STORED generated column does not rewrite the table and must not be flagged as one")
		}
	}
}

// The ATTACH finding must say that the sandbox cannot represent partitioning —
// otherwise a clean apply reads as evidence.
func TestAttachPartitionAdmitsTheSandboxLimit(t *testing.T) {
	var sc []validate.Statement
	sql := "ALTER TABLE events ATTACH PARTITION events_2024 FOR VALUES FROM ('2024-01-01') TO ('2025-01-01');"
	for _, s := range validate.SplitStatements(sql) {
		sc = append(sc, validate.Statement{SQL: s})
	}
	res := validate.BuildResult(famFixture(), &validate.Capture{Success: true, Statements: sc}, validate.Registered(), false)
	for _, fnd := range res.Findings {
		if fnd.Code != "RS-LOCK-003" {
			continue
		}
		if !strings.Contains(fnd.Detail, "does not reproduce partitioning") {
			t.Errorf("the finding must admit the hydrated schema is not partitioned, got %q", fnd.Detail)
		}
		if !strings.Contains(fnd.Detail, "PARENT") {
			t.Errorf("the finding must say the lock is on the parent, got %q", fnd.Detail)
		}
		return
	}
	t.Fatal("no RS-LOCK-003 produced")
}
