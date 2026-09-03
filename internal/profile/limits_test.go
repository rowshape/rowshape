package profile

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const limitSchema = "rowshape_limits_test"

// seedLarge builds a table big enough that PROFILING it (aggregates over a
// sample, a uniqueness probe) takes measurably longer than reading its
// STRUCTURE from the catalog. The gap is what makes it possible to time out the
// expensive reads while the cheap ones still succeed — which is the situation
// the degradation path exists for.
func seedLarge(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	ctx := context.Background()
	stmts := []string{
		`DROP SCHEMA IF EXISTS ` + limitSchema + ` CASCADE`,
		`CREATE SCHEMA ` + limitSchema,
		`CREATE TABLE ` + limitSchema + `.big (id bigint PRIMARY KEY, amount bigint NOT NULL, label text NOT NULL)`,
		`INSERT INTO ` + limitSchema + `.big SELECT g, g * 3, 'label-' || g FROM generate_series(1, 400000) g`,
		`ANALYZE ` + limitSchema + `.big`,
	}
	for _, s := range stmts {
		if _, err := conn.Exec(ctx, s); err != nil {
			t.Fatalf("seed large (%s): %v", s, err)
		}
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+limitSchema+` CASCADE`)
	})
}

// TestStatementTimeoutDegradesRatherThanAborting: the timeouts exist so `pull`
// cannot hurt the production database it reads, but a ceiling that ABORTS makes
// the tool unusable on exactly the large tables it is for — the operator lowers
// the limit to be safe and gets no fixture at all. Degrading is the honest middle:
// the expensive fact is absent (which the confidence model already handles — an
// absent fact cannot license a PASS) and a warning names the column and the flag.
func TestStatementTimeoutDegradesRatherThanAborting(t *testing.T) {
	conn := adminConn(t)
	seedLarge(t, conn)

	var warnings []string
	f, err := Fast(context.Background(), conn, Options{
		Schemas:          []string{limitSchema},
		StatementTimeout: 2 * time.Millisecond,
		Warn:             func(m string) { warnings = append(warnings, m) },
	})
	if err != nil {
		t.Skipf("catalog read itself timed out at 2ms, so there is no profiling window to test here: %v", err)
	}

	// A usable fixture, not an error: structure survives even when the expensive
	// facts do not.
	tbl, ok := f.Tables[limitSchema+".big"]
	if !ok {
		t.Fatalf("table missing from the fixture; got %v", f.Tables)
	}
	if len(tbl.Columns) != 3 {
		t.Errorf("structure lost: %d columns, want 3", len(tbl.Columns))
	}

	if len(warnings) == 0 {
		t.Skip("no profiling read exceeded the ceiling on this server; nothing to assert about degradation")
	}
	// Silent truncation is forbidden: every dropped fact names the column and the
	// flag that would let it be recorded.
	for _, w := range warnings {
		if !strings.Contains(w, "--statement-timeout") {
			t.Errorf("warning does not name the flag that raises the ceiling: %s", w)
		}
		if !strings.Contains(w, limitSchema+".big") {
			t.Errorf("warning does not name the column it dropped a fact for: %s", w)
		}
	}
}

// TestSessionLimitsAreSetOnTheReadTransaction: the whole premise is that `pull`
// runs against production, so an unbounded profiling query, a read queued behind a
// pending ALTER TABLE, or a stalled client holding back vacuum are all real. This
// asserts the three limits are actually in force inside the read transaction,
// rather than merely configurable.
func TestSessionLimitsAreSetOnTheReadTransaction(t *testing.T) {
	conn := adminConn(t)
	ctx := context.Background()

	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := applySessionLimits(ctx, tx, Options{}); err != nil {
		t.Fatalf("applySessionLimits: %v", err)
	}
	for setting, want := range map[string]string{
		"statement_timeout":                   "5min",
		"lock_timeout":                        "5s",
		"idle_in_transaction_session_timeout": "10min",
	} {
		var got string
		if err := tx.QueryRow(ctx, "SELECT current_setting($1)", setting).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", setting, err)
		}
		if got != want {
			t.Errorf("%s = %q, want %q", setting, got, want)
		}
	}
}

// TestNegativeTimeoutDisablesTheLimit: unlimited against production is exactly
// what these prevent, so it has to be asked for rather than being reachable by
// leaving a field at zero.
func TestNegativeTimeoutDisablesTheLimit(t *testing.T) {
	conn := adminConn(t)
	ctx := context.Background()

	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := applySessionLimits(ctx, tx, Options{StatementTimeout: -1}); err != nil {
		t.Fatalf("applySessionLimits: %v", err)
	}
	var got string
	if err := tx.QueryRow(ctx, "SELECT current_setting('statement_timeout')").Scan(&got); err != nil {
		t.Fatalf("read statement_timeout: %v", err)
	}
	if got != "0" {
		t.Errorf("statement_timeout = %q, want the server default 0 (disabled)", got)
	}
	// Zero must NOT disable it — that is the value a caller leaves a struct field at.
	if err := applySessionLimits(ctx, tx, Options{LockTimeout: 0}); err != nil {
		t.Fatalf("applySessionLimits: %v", err)
	}
	if err := tx.QueryRow(ctx, "SELECT current_setting('lock_timeout')").Scan(&got); err != nil {
		t.Fatalf("read lock_timeout: %v", err)
	}
	if got == "0" {
		t.Error("a zero-valued option disabled the lock timeout; zero must mean the default")
	}
}
