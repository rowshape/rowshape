package findings

import (
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/validate"
)

func seqFixture() *fixture.Fixture {
	return &fixture.Fixture{
		RowshapeFixture: fixture.FormatVersion,
		Meta:            fixture.Meta{Engine: fixture.Engine{Name: "postgres", Version: "16"}},
		Tables: map[string]fixture.Table{
			"public.users": {
				Rows: fixture.Fact[int64]{Value: 50_000_000, Confidence: fixture.Exact},
				Columns: map[string]fixture.Column{
					"id":    {Type: "bigint", Nullable: false},
					"email": {Type: "text", Nullable: false},
				},
			},
		},
	}
}

func seqCodes(t *testing.T, sql string) []string {
	t.Helper()
	var sc []validate.Statement
	for _, s := range validate.SplitStatements(sql) {
		sc = append(sc, validate.Statement{SQL: s})
	}
	res := validate.BuildResult(seqFixture(), &validate.Capture{Success: true, Statements: sc}, validate.Registered(), false)
	var out []string
	for _, fnd := range res.Findings {
		out = append(out, fnd.Code)
	}
	return out
}

func hasCode(codes []string, want string) bool {
	for _, c := range codes {
		if c == want {
			return true
		}
	}
	return false
}

// TestMissingLockTimeoutIsReported: the mechanism behind most "one quick
// migration took the site down" incidents. With no lock_timeout, DDL that cannot
// acquire its lock WAITS — and a pending ACCESS EXCLUSIVE request blocks incoming
// readers, so the whole table queues behind it. No single statement looks wrong,
// which is why only a whole-migration rule can see it.
func TestMissingLockTimeoutIsReported(t *testing.T) {
	codes := seqCodes(t, "ALTER TABLE users ALTER COLUMN email TYPE varchar(500);")
	if !hasCode(codes, "RS-LOCK-010") {
		t.Errorf("a lock-holding migration with no lock_timeout must be reported, got %v", codes)
	}
}

// Setting lock_timeout is the remediation, so it must silence the finding.
func TestLockTimeoutSilencesTheFinding(t *testing.T) {
	for _, prefix := range []string{
		"SET lock_timeout = '3s';",
		"SET LOCAL lock_timeout = '3s';",
		"SET SESSION lock_timeout = '3s';",
	} {
		codes := seqCodes(t, prefix+"\nALTER TABLE users ALTER COLUMN email TYPE varchar(500);")
		if hasCode(codes, "RS-LOCK-010") {
			t.Errorf("%q must silence RS-LOCK-010, got %v", prefix, codes)
		}
	}
}

// The rule fires only where the lock is actually HELD. Catalog-only DDL is
// silent, or the tool warns on essentially every migration ever written — noise
// that trains people to ignore findings.
func TestInstantDDLDoesNotRaiseLockDurationFindings(t *testing.T) {
	for _, sql := range []string{
		"ALTER TABLE users ADD COLUMN nickname text;",
		"ALTER TABLE users ADD COLUMN status text DEFAULT 'active';", // PG 16 fast-path
		"ALTER TABLE users RENAME COLUMN email TO email_address;",
	} {
		codes := seqCodes(t, sql)
		if hasCode(codes, "RS-LOCK-010") {
			t.Errorf("%q is instant on PG 16 and must not raise a lock-duration finding, got %v", sql, codes)
		}
	}
}

// Several exclusive locks on one table queue independently, so the table is
// unavailable across the whole sequence rather than for the longest statement.
func TestRepeatedExclusiveLocksAreReported(t *testing.T) {
	sql := "SET lock_timeout = '3s';\n" +
		"ALTER TABLE users ALTER COLUMN email TYPE varchar(500);\n" +
		"ALTER TABLE users ADD CONSTRAINT c CHECK (id > 0);\n"
	codes := seqCodes(t, sql)
	if !hasCode(codes, "RS-LOCK-011") {
		t.Errorf("two exclusive-lock statements on one table must be reported, got %v", codes)
	}
}

// One locking statement is not a sequence problem.
func TestSingleExclusiveLockIsNotReported(t *testing.T) {
	sql := "SET lock_timeout = '3s';\nALTER TABLE users ALTER COLUMN email TYPE varchar(500);"
	if hasCode(seqCodes(t, sql), "RS-LOCK-011") {
		t.Error("a single locking statement must not raise the repeated-lock finding")
	}
}

// The finding must explain that the hazard is the migration as a whole.
func TestLockTimeoutFindingExplainsTheMechanism(t *testing.T) {
	var sc []validate.Statement
	for _, s := range validate.SplitStatements("ALTER TABLE users ALTER COLUMN email TYPE varchar(500);") {
		sc = append(sc, validate.Statement{SQL: s})
	}
	res := validate.BuildResult(seqFixture(), &validate.Capture{Success: true, Statements: sc}, validate.Registered(), false)
	for _, fnd := range res.Findings {
		if fnd.Code != "RS-LOCK-010" {
			continue
		}
		if !strings.Contains(fnd.Detail, "queues behind") {
			t.Errorf("the detail must explain that readers queue behind a pending lock, got %q", fnd.Detail)
		}
		if len(fnd.DependsOn) != 0 {
			t.Errorf("this rests on no fixture fact, got DependsOn %v", fnd.DependsOn)
		}
		return
	}
	t.Fatal("no RS-LOCK-010 produced")
}
