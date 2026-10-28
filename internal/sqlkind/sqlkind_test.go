package sqlkind

import "testing"

// This is the single home for the transaction-control test that used to be
// duplicated in internal/plan and internal/validate (docs/TESTING-GAPS.md item
// 3b). Both packages now call IsTxControl, so this one table covers both.
func TestIsTxControl(t *testing.T) {
	yes := []string{
		"BEGIN", "begin", "  COMMIT  ", "COMMIT;", "ROLLBACK",
		"START TRANSACTION", "END", "SAVEPOINT sp1", "RELEASE sp1",
		"begin isolation level serializable",
		"\tROLLBACK\n",
		"-- Migration 2\nBEGIN", // the splitter keeps a leading comment
		"/* step */ COMMIT",
	}
	no := []string{
		"", "SELECT 1",
		"BEGINNING",             // must not prefix-match a real identifier
		"COMMITTED",             // ditto
		"ENDPOINT",              // ditto
		"CREATE TABLE beginner", // the keyword appears but not as a statement verb
		"ALTER TABLE t ADD COLUMN c int",
	}
	for _, s := range yes {
		if !IsTxControl(s) {
			t.Errorf("IsTxControl(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if IsTxControl(s) {
			t.Errorf("IsTxControl(%q) = true, want false", s)
		}
	}
}

// TestRunsOutsideTransaction: every CONCURRENTLY form the sandbox must run
// outside a transaction, including behind a leading comment. DROP INDEX and
// REINDEX CONCURRENTLY were run inside one and reported as RS-APPLY-001 — a
// FAIL on the exact statements RS-INDEX-003 and RS-INDEX-020 recommend.
func TestRunsOutsideTransaction(t *testing.T) {
	yes := []string{
		"CREATE INDEX CONCURRENTLY i ON t (c)",
		"create unique index concurrently i on t (c)",
		"DROP INDEX CONCURRENTLY i",
		"REINDEX INDEX CONCURRENTLY i",
		"REINDEX TABLE CONCURRENTLY t",
		"-- Migration 1: own file\nCREATE UNIQUE INDEX CONCURRENTLY i ON t (c)",
	}
	no := []string{
		"", "CREATE INDEX i ON t (c)", "DROP INDEX i", "REINDEX INDEX i",
		"ALTER TABLE t ADD COLUMN concurrently_seen int",
		"-- CREATE INDEX CONCURRENTLY i ON t (c)\nSELECT 1", // only in the comment
	}
	for _, s := range yes {
		if !RunsOutsideTransaction(s) {
			t.Errorf("RunsOutsideTransaction(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if RunsOutsideTransaction(s) {
			t.Errorf("RunsOutsideTransaction(%q) = true, want false", s)
		}
	}
}
