package findings

import (
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/validate"
)

func txFixture(version string) *fixture.Fixture {
	return &fixture.Fixture{
		RowshapeFixture: fixture.FormatVersion,
		Meta:            fixture.Meta{Engine: fixture.Engine{Name: "postgres", Version: version}},
		Tables: map[string]fixture.Table{
			"public.orders": {
				Rows:    fixture.Fact[int64]{Value: 1000, Confidence: fixture.Exact},
				Columns: map[string]fixture.Column{"user_id": {Type: "bigint"}},
			},
		},
	}
}

func run(t *testing.T, f *fixture.Fixture, sql string) validateResult {
	t.Helper()
	var sc []validate.Statement
	for _, s := range validate.SplitStatements(sql) {
		sc = append(sc, validate.Statement{SQL: s})
	}
	res := validate.BuildResult(f, &validate.Capture{Success: true, Statements: sc}, validate.Registered(), false)
	out := validateResult{verdict: res.Verdict}
	for _, fnd := range res.Findings {
		if fnd.Code == "RS-TX-001" {
			out.tx = append(out.tx, fnd.Severity)
			out.detail = fnd.Detail
		}
	}
	return out
}

type validateResult struct {
	verdict string
	tx      []string
	detail  string
}

// TestConcurrentIndexInsideExplicitTransactionIsAnError is the regression for
// the sharpest false negative in the catalog.
//
// validate does not execute the migration's own BEGIN/COMMIT — it records them
// and runs each statement in its own transaction so locks can be inspected — and
// it hoists CREATE INDEX CONCURRENTLY out of any transaction entirely. So this
// migration applied cleanly and returned PASS, while in production it raises
// 25001. The sandbox's own convenience erased a guaranteed production failure.
func TestConcurrentIndexInsideExplicitTransactionIsAnError(t *testing.T) {
	got := run(t, txFixture("16"), "BEGIN;\nCREATE INDEX CONCURRENTLY idx ON orders (user_id);\nCOMMIT;")
	if len(got.tx) == 0 {
		t.Fatal("no RS-TX-001: a statement that cannot succeed in production was certified")
	}
	if got.tx[0] != "error" {
		t.Errorf("severity = %q, want error: inside an explicit BEGIN the failure is CERTAIN", got.tx[0])
	}
	if got.verdict != "FAIL" {
		t.Errorf("verdict = %q, want FAIL", got.verdict)
	}
	// The detail must warn that a clean apply here proves nothing.
	if !strings.Contains(got.detail, "sandbox") {
		t.Errorf("the detail must say the sandbox does not execute BEGIN/COMMIT, got %q", got.detail)
	}
}

// Without an explicit transaction the outcome depends on the RUNNER, which
// rowshape cannot see. Guessing "safe" is what produced the original bug;
// guessing "unsafe" would cry wolf on every raw-SQL project using CONCURRENTLY
// correctly. So it warns and says why.
func TestConcurrentIndexWithoutExplicitTransactionWarns(t *testing.T) {
	got := run(t, txFixture("16"), "CREATE INDEX CONCURRENTLY idx ON orders (user_id);")
	if len(got.tx) == 0 {
		t.Fatal("no RS-TX-001 produced")
	}
	if got.tx[0] != "warn" {
		t.Errorf("severity = %q, want warn: rowshape cannot see the runner's transaction setting", got.tx[0])
	}
	for _, runner := range []string{"Alembic", "Django", "Rails", "Flyway", "psql"} {
		if !strings.Contains(got.detail, runner) {
			t.Errorf("the detail must name the runners whose default decides this; missing %q", runner)
		}
	}
}

func TestNonTransactionalStatements(t *testing.T) {
	f := txFixture("16")
	for _, sql := range []string{
		"VACUUM orders;",
		"VACUUM FULL orders;",
		"CLUSTER orders USING idx;",
		"DROP INDEX CONCURRENTLY idx;",
		"REINDEX INDEX CONCURRENTLY idx;",
		"CREATE UNIQUE INDEX CONCURRENTLY u ON orders (user_id);",
		"ALTER SYSTEM SET work_mem = '64MB';",
	} {
		t.Run(sql, func(t *testing.T) {
			if len(run(t, f, sql).tx) == 0 {
				t.Errorf("%q must produce RS-TX-001", sql)
			}
		})
	}
}

// ALTER TYPE ... ADD VALUE became transactional in PG 12, so the finding is
// version-conditional — and with no declared version rowshape must not guess
// (RFC §9.1).
func TestAlterTypeAddValueIsVersionConditional(t *testing.T) {
	if len(run(t, txFixture("11"), "ALTER TYPE mood ADD VALUE 'sad';").tx) == 0 {
		t.Error("PG 11: ALTER TYPE ADD VALUE is non-transactional and must be flagged")
	}
	if got := run(t, txFixture("16"), "ALTER TYPE mood ADD VALUE 'sad';"); len(got.tx) != 0 {
		t.Error("PG 16: ALTER TYPE ADD VALUE is transactional and must NOT be flagged")
	}
	got := run(t, txFixture(""), "ALTER TYPE mood ADD VALUE 'sad';")
	if len(got.tx) == 0 {
		t.Error("no engine version: rowshape must not assume the safe reading")
	}
	if !strings.Contains(got.detail, "no engine version") {
		t.Errorf("the detail must say why it cannot decide, got %q", got.detail)
	}
}

// Ordinary DDL must not trip the analyzer.
func TestTxAnalyzerDoesNotOverfire(t *testing.T) {
	f := txFixture("16")
	for _, sql := range []string{
		"CREATE INDEX idx ON orders (user_id);",
		"ALTER TABLE orders ADD COLUMN note text;",
		"BEGIN;\nALTER TABLE orders ADD COLUMN note text;\nCOMMIT;",
	} {
		if len(run(t, f, sql).tx) != 0 {
			t.Errorf("%q must not produce RS-TX-001", sql)
		}
	}
}
