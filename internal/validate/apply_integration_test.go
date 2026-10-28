package validate

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
)

// The capture layer — Apply / applyOne / recordResult / strongestLock — is 0% in
// a default `go test` because it drives a real Postgres: it reads pg_locks for
// the lock mode, captures SQLSTATE from failed statements, and runs a
// CONCURRENTLY build outside a transaction. A fake pgx would test a mock, not the
// behavior these functions exist for, so this covers them directly against the
// live server (DSN-gated; runs in ci.yml). docs/TESTING-GAPS.md item 9.

var capCounter atomic.Int64

// applyConn connects to the test server and returns a connection plus a cleanup.
func applyConn(t *testing.T) (*pgx.Conn, context.Context) {
	t.Helper()
	dsn := os.Getenv("ROWSHAPE_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("set ROWSHAPE_TEST_PG_DSN to run the capture integration tests")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Skipf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	return conn, ctx
}

// freshTable creates a uniquely-named table with the given DDL body and schedules
// its drop. Returns the table name.
func freshTable(t *testing.T, conn *pgx.Conn, ctx context.Context, body string) string {
	t.Helper()
	name := fmt.Sprintf("rowshape_cap_%d_%d", os.Getpid(), capCounter.Add(1))
	if _, err := conn.Exec(ctx, "CREATE TABLE "+name+" ("+body+")"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() { _, _ = conn.Exec(ctx, "DROP TABLE IF EXISTS "+name) })
	return name
}

func located(sql ...string) []Located {
	out := make([]Located, len(sql))
	for i, s := range sql {
		out[i] = Located{SQL: s, Line: i + 1}
	}
	return out
}

// TestApplyCapturesLockMode: a plain ALTER TABLE is captured with the strong lock
// it takes and the table it takes it on (strongestLock reads pg_locks inside the
// still-open transaction).
func TestApplyCapturesLockMode(t *testing.T) {
	conn, ctx := applyConn(t)
	tbl := freshTable(t, conn, ctx, "id int")

	cap := Apply(ctx, conn, located("ALTER TABLE "+tbl+" ADD COLUMN note text"))

	if !cap.Success {
		t.Fatalf("clean ALTER should succeed, got %+v", cap.Statements)
	}
	if len(cap.Statements) != 1 {
		t.Fatalf("want 1 statement, got %d", len(cap.Statements))
	}
	st := cap.Statements[0]
	if st.ErrCode != "" {
		t.Fatalf("unexpected error: %s %s", st.ErrCode, st.ErrMsg)
	}
	if !strings.Contains(st.LockMode, "Exclusive") {
		t.Errorf("lock mode = %q, want an exclusive lock (ALTER TABLE ADD COLUMN)", st.LockMode)
	}
	if !strings.HasSuffix(st.LockTable, tbl) {
		t.Errorf("lock table = %q, want %q", st.LockTable, tbl)
	}
}

// TestApplyCapturesSQLSTATE: a statement that violates a constraint is captured
// with its real SQLSTATE, marks the capture failed, and stops the run (later
// statements are not meaningful once the migration broke).
func TestApplyCapturesSQLSTATE(t *testing.T) {
	conn, ctx := applyConn(t)
	tbl := freshTable(t, conn, ctx, "email text")
	// Two identical emails make a UNIQUE build fail with 23505.
	if _, err := conn.Exec(ctx, "INSERT INTO "+tbl+" VALUES ('a@x'), ('a@x')"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cap := Apply(ctx, conn, located(
		"ALTER TABLE "+tbl+" ADD CONSTRAINT u UNIQUE (email)",
		"ALTER TABLE "+tbl+" ADD COLUMN never_runs int",
	))

	if cap.Success {
		t.Error("a constraint violation must mark the capture failed")
	}
	fs := cap.FailedStatement()
	if fs == nil {
		t.Fatal("FailedStatement should point at the violating statement")
	}
	if !fs.ConstraintViolation() {
		t.Errorf("err code = %q, want a class-23 constraint violation (23505)", fs.ErrCode)
	}
	// Application stops at the first error: the second statement never ran.
	if len(cap.Statements) != 1 {
		t.Errorf("run should stop after the failure, got %d statements", len(cap.Statements))
	}
}

// TestApplyRecordsTxControlWithoutExecuting: BEGIN/COMMIT are recorded (so the
// analyzers see transaction boundaries) but not executed — a following DDL still
// applies in its own transaction and is captured normally.
func TestApplyRecordsTxControlWithoutExecuting(t *testing.T) {
	conn, ctx := applyConn(t)
	tbl := freshTable(t, conn, ctx, "id int")

	cap := Apply(ctx, conn, located(
		"BEGIN",
		"ALTER TABLE "+tbl+" ADD COLUMN c int",
		"COMMIT",
	))

	if !cap.Success {
		t.Fatalf("run should succeed, got %+v", cap.Statements)
	}
	if len(cap.Statements) != 3 {
		t.Fatalf("all three statements should be recorded, got %d", len(cap.Statements))
	}
	// The tx-control statements carry no captured lock/duration signal.
	if cap.Statements[0].LockMode != "" || cap.Statements[2].LockMode != "" {
		t.Error("tx-control statements must not be executed (no lock captured)")
	}
	if !strings.Contains(cap.Statements[1].LockMode, "Exclusive") {
		t.Errorf("the DDL between BEGIN/COMMIT should still capture its lock, got %q", cap.Statements[1].LockMode)
	}
}

// TestApplyConcurrentIndexRunsOutsideTx: CREATE INDEX CONCURRENTLY cannot run in
// a transaction block; Apply must run it directly and succeed (the branch that
// would error 25001 if it were wrapped in BeginTx).
func TestApplyConcurrentIndexRunsOutsideTx(t *testing.T) {
	conn, ctx := applyConn(t)
	tbl := freshTable(t, conn, ctx, "id int")

	cap := Apply(ctx, conn, located("CREATE INDEX CONCURRENTLY idx_"+tbl+" ON "+tbl+" (id)"))

	if !cap.Success {
		t.Fatalf("concurrent build should succeed outside a tx, got %+v", cap.Statements)
	}
	st := cap.Statements[0]
	if !st.Concurrent || !st.IsIndexBuild {
		t.Errorf("statement should be classified as a concurrent index build, got %+v", st)
	}
	if st.ErrCode != "" {
		t.Errorf("concurrent build errored: %s %s", st.ErrCode, st.ErrMsg)
	}
}

// TestApplyOtherConcurrentFormsRunOutsideTx: REINDEX CONCURRENTLY and DROP INDEX
// CONCURRENTLY are refused in a transaction block too. They were wrapped in one,
// so the safe forms RS-INDEX-020 and RS-INDEX-003 recommend failed with 25001 —
// including behind a leading comment, which the splitter keeps.
func TestApplyOtherConcurrentFormsRunOutsideTx(t *testing.T) {
	conn, ctx := applyConn(t)
	tbl := freshTable(t, conn, ctx, "id int")

	cap := Apply(ctx, conn, located(
		"CREATE INDEX idx_"+tbl+" ON "+tbl+" (id)",
		"-- rebuild without blocking writes\nREINDEX INDEX CONCURRENTLY idx_"+tbl,
		"DROP INDEX CONCURRENTLY idx_"+tbl,
	))
	if !cap.Success {
		t.Fatalf("CONCURRENTLY forms should succeed outside a tx, got %+v", cap.Statements)
	}
}
