// Package validate orchestrates `rowshape validate`: it applies a proposed
// migration against a hydrated disposable (or user-provided) database and
// captures what actually happened, feeding those captures to the finding
// analyzers, the extrapolation model, and the confidence-capping engine to
// produce a Verdict.
//
// validate is free forever and never calls the cloud (INV-NEVER-GATE-VALIDATE):
// this package imports no network client. Its blast radius is zero
// (INV-BLAST-RADIUS-ZERO): there is no `apply`, and the CLI hard-refuses a target
// whose host matches the fixture's source host.
package validate

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rowshape/rowshape/internal/sqlkind"
)

// Capture is the record of applying a migration: the six signal classes a
// verdict is built from (PRD §8.1) — success/failure, per-statement wall time,
// lock mode + duration, rows affected, constraint violations, and index build
// behavior.
type Capture struct {
	// Success is true when every statement applied without error.
	Success bool
	// DurationMs is the wall time of the whole migration.
	DurationMs int64
	// Statements is the per-statement record, in application order.
	Statements []Statement
	// TableRows is the hydrated row count per qualified table — the basis an
	// analyzer extrapolates from (declared rows drive the projection, RFC §9).
	// Populated by the pipeline from the hydration report; empty for a provided
	// (ground-truth) target, where the fixture's declared rows are already real.
	TableRows map[string]int64
	// Calibration, when set, carries a SECOND measured run at a different scale so
	// analyzers can fit the cost curve to two points and mark the estimate
	// `measured` (RFC §9.2, `validate --calibrate`). Nil for a normal single-run
	// validate, where estimates stay `estimated`.
	Calibration *Calibration
}

// Calibration is a second measured run of the same migration at a different
// hydration scale: the per-table row counts and the per-statement wall times,
// indexed parallel to Capture.Statements.
type Calibration struct {
	TableRows    map[string]int64
	StatementMs2 []int64
}

// Statement is the capture of one applied SQL statement.
type Statement struct {
	SQL          string // the statement text (trimmed)
	File         string // migration file it came from ("" for inline SQL)
	Line         int    // 1-based line where the statement begins
	DurationMs   int64  // wall time — also the lock hold time for a blocking DDL
	RowsAffected int64  // command-tag row count (0 for pure DDL)
	LockMode     string // strongest lock this statement held on a relation, "" if none
	LockTable    string // the relation the strongest lock was held on
	// ErrCode is the SQLSTATE when the statement failed, "" on success. Class 23
	// (e.g. 23505 unique_violation, 23502 not_null_violation) is a constraint
	// violation surfaced by applying the migration against production-shaped data.
	ErrCode string
	ErrMsg  string
	// IsIndexBuild / Concurrent describe index build behavior: whether the
	// statement built an index and whether it used CREATE INDEX CONCURRENTLY
	// (which holds no exclusive lock but cannot run in a transaction block).
	IsIndexBuild bool
	Concurrent   bool
}

// ConstraintViolation reports whether the statement failed on an integrity
// constraint (SQLSTATE class 23) — a real problem the migration hit against the
// data, not a tool error.
func (s Statement) ConstraintViolation() bool {
	return strings.HasPrefix(s.ErrCode, "23")
}

// FailedStatement returns the first statement that errored, or nil.
func (c *Capture) FailedStatement() *Statement {
	for i := range c.Statements {
		if c.Statements[i].ErrCode != "" {
			return &c.Statements[i]
		}
	}
	return nil
}

// Apply runs each statement against conn in application order, capturing the six
// signal classes. A statement that can run inside a transaction is executed in
// its own transaction so its held locks can be inspected before commit; a
// CONCURRENTLY index build (which cannot run in a transaction block) is executed
// directly. Application stops at the first error — a broken migration's later
// statements are not meaningful.
func Apply(ctx context.Context, conn *pgx.Conn, statements []Located) *Capture {
	cap := &Capture{Success: true}
	start := time.Now()
	for _, loc := range statements {
		sql := strings.TrimSpace(loc.SQL)
		if sql == "" {
			continue
		}
		// Transaction-control statements (BEGIN/COMMIT/…) are recorded so the
		// analyzers can see the transaction boundaries (RS-CONSTRAINT tracks
		// NOT VALID + VALIDATE in one transaction), but they are not executed:
		// each DDL statement is applied in its own transaction for lock
		// inspection, so replaying the migration's own BEGIN/COMMIT would clash.
		if sqlkind.IsTxControl(sql) {
			cap.Statements = append(cap.Statements, Statement{SQL: sql, File: loc.File, Line: loc.Line})
			continue
		}
		st := applyOne(ctx, conn, sql)
		st.File, st.Line = loc.File, loc.Line
		cap.Statements = append(cap.Statements, st)
		if st.ErrCode != "" {
			cap.Success = false
			break
		}
	}
	cap.DurationMs = time.Since(start).Milliseconds()
	return cap
}

// applyOne executes and captures a single statement.
func applyOne(ctx context.Context, conn *pgx.Conn, sql string) Statement {
	st := Statement{SQL: sql}
	st.IsIndexBuild, st.Concurrent = classifyIndexBuild(sql)

	if st.Concurrent {
		// CREATE INDEX CONCURRENTLY cannot run inside a transaction block; run it
		// directly. It holds no exclusive lock, so there is nothing to inspect.
		start := time.Now()
		tag, err := conn.Exec(ctx, sql)
		st.DurationMs = time.Since(start).Milliseconds()
		recordResult(&st, tag, err)
		return st
	}

	tx, err := conn.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		st.ErrCode = "TXBEGIN"
		st.ErrMsg = scrubQuoted(err.Error())
		return st
	}
	start := time.Now()
	tag, execErr := tx.Exec(ctx, sql)
	st.DurationMs = time.Since(start).Milliseconds()
	recordResult(&st, tag, execErr)

	if execErr == nil {
		// Locks are still held inside the open transaction — read the strongest.
		st.LockMode, st.LockTable = strongestLock(ctx, tx)
		if err := tx.Commit(ctx); err != nil {
			// A commit-time failure (e.g. a deferred constraint) is still a
			// migration failure worth capturing.
			recordResult(&st, pgconn.CommandTag{}, err)
		}
	} else {
		_ = tx.Rollback(ctx)
	}
	return st
}

// recordResult folds a command tag and error into the statement capture.
func recordResult(st *Statement, tag pgconn.CommandTag, err error) {
	if err != nil {
		var pgErr *pgconn.PgError
		if asPgError(err, &pgErr) {
			st.ErrCode = pgErr.Code
			st.ErrMsg = errMessage(pgErr)
		} else {
			st.ErrCode = "EXEC"
			st.ErrMsg = scrubQuoted(err.Error())
		}
		return
	}
	st.RowsAffected = tag.RowsAffected()
}

// errMessage renders a Postgres error for the capture WITHOUT carrying row
// values out of the database.
//
// Postgres puts data values in the PRIMARY message for a whole class of errors —
//
//	invalid input syntax for type integer: "hunter2"
//	value "..." is out of range for type integer
//	date/time field value out of range: "..."
//
// and ErrMsg is serialized into the verdict JSON and printed to stderr by
// cmd/validate.go. On the --target ground-truth path those are PRODUCTION values
// landing in a CI log and a build artifact, which falsifies INV-NO-ROWS. (The
// code already avoided pgErr.Detail, so the risk was clearly considered; the
// Message case was missed. Where and InternalQuery can carry values too and are
// likewise not used.)
//
// Quoted runs are replaced wholesale rather than guessed at, because a value and
// an identifier are quoted identically and telling them apart from the text is
// not reliable. That would also throw away the identifiers, which are genuinely
// useful and are NOT sensitive — a column or table name is schema, which
// rowshape already publishes in fixtures — so they are added back from the
// structured fields Postgres provides separately. The diagnostic shape survives:
//
//	invalid input syntax for type integer: "…" [relation users, column age]
func errMessage(e *pgconn.PgError) string {
	msg := e.Message
	if scrubsValues(e.Code) {
		msg = scrubQuoted(msg)
	}
	var ctx []string
	if e.SchemaName != "" && e.SchemaName != "public" {
		ctx = append(ctx, "schema "+e.SchemaName)
	}
	if e.TableName != "" {
		ctx = append(ctx, "relation "+e.TableName)
	}
	if e.ColumnName != "" {
		ctx = append(ctx, "column "+e.ColumnName)
	}
	if e.ConstraintName != "" {
		ctx = append(ctx, "constraint "+e.ConstraintName)
	}
	if e.DataTypeName != "" {
		ctx = append(ctx, "type "+e.DataTypeName)
	}
	if len(ctx) == 0 {
		return msg
	}
	return msg + " [" + strings.Join(ctx, ", ") + "]"
}

// scrubsValues reports whether a SQLSTATE's primary message can embed a ROW
// VALUE, and therefore has to be scrubbed.
//
// Scrubbing unconditionally was too blunt. It cost the diagnostic for the two
// failures an operator hits most, neither of which carries row data:
//
//	42601  syntax error at or near "ALTER"   ->  ... at or near "…"
//	42703  column "emial" does not exist     ->  column "…" does not exist
//
// The quoted token in class 42 is a SQL identifier or a keyword from the user's
// OWN migration file — the misspelling in that third example is the entire
// content of the diagnostic — and a relation or column name is schema, which
// rowshape already publishes in fixtures. Blanking it protected nothing and hid
// the answer.
//
// The list is an ALLOWLIST of classes known to carry no values, so an
// unrecognized or future SQLSTATE is scrubbed by default: this fails closed,
// which is the right direction for INV-NO-ROWS.
func scrubsValues(code string) bool {
	if len(code) < 2 {
		return true // unknown shape: scrub
	}
	switch code[:2] {
	case "42", // syntax error or access rule violation - identifiers and SQL tokens
		"3D", // invalid catalog name
		"3F", // invalid schema name
		"26", // invalid SQL statement name
		"34", // invalid cursor name
		"08", // connection exception
		"53", // insufficient resources
		"57", // operator intervention
		"58": // system error
		return false
	}
	// Everything else is scrubbed. The classes that matter most here are 22
	// (data exception: "invalid input syntax for type integer: \"...\"") and 23
	// (integrity constraint violation), which embed values directly.
	return true
}

// scrubQuoted replaces the contents of every quoted run with an ellipsis, so a
// message keeps its shape while carrying no literal out of the database.
//
// Both quote styles are handled: Postgres quotes identifiers and values with
// double quotes and some messages use single quotes. An unterminated quote is
// treated as running to the end of the string — the conservative reading, since
// the alternative emits the tail verbatim.
func scrubQuoted(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '"' && c != '\'' {
			b.WriteByte(c)
			continue
		}
		// Copy the opening quote, skip to the matching close, emit a placeholder.
		b.WriteByte(c)
		j := i + 1
		for j < len(s) && s[j] != c {
			j++
		}
		b.WriteString("…")
		if j < len(s) {
			b.WriteByte(c) // closing quote
		}
		i = j
	}
	return b.String()
}

// strongestLock reads the strongest relation lock the current transaction holds,
// resolving the relation name. Returns ("", "") when no relation lock is held.
func strongestLock(ctx context.Context, tx pgx.Tx) (mode, table string) {
	const q = `
		SELECT l.mode, c.relname
		FROM pg_locks l
		JOIN pg_class c ON c.oid = l.relation
		WHERE l.locktype = 'relation'
		  AND l.pid = pg_backend_pid()
		  AND c.relkind IN ('r','p')
		ORDER BY array_position(ARRAY[
			'AccessShareLock','RowShareLock','RowExclusiveLock',
			'ShareUpdateExclusiveLock','ShareLock','ShareRowExclusiveLock',
			'ExclusiveLock','AccessExclusiveLock'], l.mode) DESC
		LIMIT 1`
	row := tx.QueryRow(ctx, q)
	if err := row.Scan(&mode, &table); err != nil {
		return "", ""
	}
	return mode, table
}

// classifyIndexBuild reports whether sql builds an index and whether it does so
// CONCURRENTLY.
func classifyIndexBuild(sql string) (isIndex, concurrent bool) {
	upper := strings.ToUpper(strings.TrimSpace(sql))
	if !strings.HasPrefix(upper, "CREATE") || !strings.Contains(upper, "INDEX") {
		return false, false
	}
	// Guard against "CREATE TABLE ... " that merely mentions INDEX in a name.
	if !strings.Contains(upper, "CREATE INDEX") && !strings.Contains(upper, "CREATE UNIQUE INDEX") {
		return false, false
	}
	return true, strings.Contains(upper, "CONCURRENTLY")
}

// asPgError unwraps err into a *pgconn.PgError, reporting whether it matched.
func asPgError(err error, target **pgconn.PgError) bool {
	for e := err; e != nil; {
		if pe, ok := e.(*pgconn.PgError); ok {
			*target = pe
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := e.(unwrapper)
		if !ok {
			break
		}
		e = u.Unwrap()
	}
	return false
}
