package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/rowshape/rowshape/internal/verdict"
)

// The exit-code contract (0 PASS · 1 FAIL · 2 WARN-only · 3 tool error) is a
// public API an agent and a CI gate both branch on. It was asserted only as a
// pure function (internal/exitcode) and for tool errors (exit 3); no test drove
// a FAIL or WARN verdict THROUGH the command and checked that runValidate maps
// it to the right process code. A regression collapsing WARN into PASS(0), or
// mis-wiring result.ExitCode, would ship green. docs/TESTING-GAPS.md item 4.
//
// These are DSN-gated (they hydrate a disposable database from a corpus case and
// apply a real migration) and run in ci.yml, which sets ROWSHAPE_TEST_PG_DSN.

// exitCodeOf extracts the process exit code a runE returned: an *ExitError
// carries it; nil means the command succeeded (PASS, exit 0).
func exitCodeOf(err error) int {
	if err == nil {
		return verdict.ExitPass
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return -1 // an unexpected non-exit error; never a valid contract code
}

func requireAdminDSN(t *testing.T) string {
	t.Helper()
	admin := os.Getenv(testAdminEnv)
	if admin == "" {
		t.Skipf("set %s to a Postgres admin connection to run the end-to-end exit-code tests", testAdminEnv)
	}
	conn, err := pgx.Connect(context.Background(), admin)
	if err != nil {
		t.Skipf("admin connection unusable: %v", err)
	}
	_ = conn.Close(context.Background())
	return admin
}

func corpusCase(name string) (fixture, migration string) {
	return filepath.Join("..", "corpus", "cases", name, "fixture.yaml"),
		filepath.Join("..", "corpus", "cases", name, "migration.sql")
}

// runValidateCase hydrates the named corpus case into a disposable database,
// applies its migration, and returns the exit code runValidate produced.
func runValidateCase(t *testing.T, admin, caseName string, warnFail bool) int {
	t.Helper()
	fx, mig := corpusCase(caseName)
	opts := &validateOptions{
		fixturePath: fx,
		migrations:  mig,
		ephemeral:   admin,
		scale:       1.0,
		asJSON:      true,
		warnFail:    warnFail,
	}
	var runErr error
	_, stderr := captureOutput(t, func() error { runErr = runValidate(context.Background(), opts); return runErr })
	code := exitCodeOf(runErr)
	if code == -1 {
		t.Fatalf("runValidate(%s) returned a non-exit error: %v\nstderr:\n%s", caseName, runErr, stderr)
	}
	return code
}

// TestValidateExitCodeFail: a FAIL verdict must exit 1 through the command.
// validate_orphans adds a FK whose child rows are orphaned — a hard FAIL.
func TestValidateExitCodeFail(t *testing.T) {
	admin := requireAdminDSN(t)
	if code := runValidateCase(t, admin, "validate_orphans", false); code != verdict.ExitFail {
		t.Errorf("FAIL verdict exit code = %d, want %d", code, verdict.ExitFail)
	}
}

// warnCase is a corpus case whose migration APPLIES CLEANLY and yields a stable
// WARN (a type change: safe to run, worth flagging for the lock it takes). Cases
// like rsdata-unique-unproven are unsuitable here — hydrated at scale 1.0 they
// produce real duplicate rows, so the unique build genuinely FAILs on a live
// apply. That surprise is exactly why exit-code wiring needs an end-to-end test.
const warnCase = "rslock-type-change"

// TestValidateExitCodeWarnOnly: a WARN-only verdict must exit 2 by default — the
// distinct code that lets an agent tell "advisory" from "blocked". This is the
// single most important untested exit path in the CLI (docs/TESTING-GAPS.md).
func TestValidateExitCodeWarnOnly(t *testing.T) {
	admin := requireAdminDSN(t)
	if code := runValidateCase(t, admin, warnCase, false); code != verdict.ExitWarnOnly {
		t.Errorf("WARN-only verdict exit code = %d, want %d", code, verdict.ExitWarnOnly)
	}
}

// TestValidateExitCodeWarnFail: the same WARN case with --warn-fail must exit 1,
// the "configurable to fail" knob CI pipelines gate on.
func TestValidateExitCodeWarnFail(t *testing.T) {
	admin := requireAdminDSN(t)
	if code := runValidateCase(t, admin, warnCase, true); code != verdict.ExitFail {
		t.Errorf("WARN + --warn-fail exit code = %d, want %d", code, verdict.ExitFail)
	}
}

// TestValidateExitCodePass: a clean, safe migration exits 0.
func TestValidateExitCodePass(t *testing.T) {
	admin := requireAdminDSN(t)
	if code := runValidateCase(t, admin, "capping-exact-not-null", false); code != verdict.ExitPass {
		t.Errorf("PASS verdict exit code = %d, want %d", code, verdict.ExitPass)
	}
}
