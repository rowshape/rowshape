package profile

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

// requireMajor skips a test whose SETUP needs a server feature that arrived in a
// later major.
//
// This is deliberately about the FIXTURE, not the reader. rowshape supports
// PostgreSQL 10 through the current release (D-022) and the matrix runs all of
// them, so a test that CREATES a stored generated column cannot run below 12 —
// the column cannot exist there, and the failure is `syntax error at or near "("`
// from the CREATE TABLE, long before any rowshape code is reached.
//
// Skipping is right here and would be wrong for a reader defect. The reader must
// work on every supported major, and a version-gated READ gets a real assertion
// on both sides of the boundary (TestVersionConditionalBoundary). What is being
// skipped is a schema the older server cannot express at all.
func requireMajor(t *testing.T, conn *pgx.Conn, want int) {
	t.Helper()
	var num int
	if err := conn.QueryRow(context.Background(),
		"SELECT current_setting('server_version_num')::int").Scan(&num); err != nil {
		t.Fatalf("reading server_version_num: %v", err)
	}
	if got := num / 10000; got < want {
		t.Skipf("this test builds a schema that needs PostgreSQL %d+; server is %d", want, got)
	}
}
