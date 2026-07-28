// Package target provides the disposable (or user-provided) database that
// hydrate loads into. A single Target interface abstracts the mechanism so the
// disposable backend — an ephemeral database, a Docker container, pg_tmp, or an
// embedded Postgres — can be swapped without touching the synthesis engine
// (OQ-TARGET / docs DECISIONS D-005).
package target

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/rowshape/rowshape/internal/dsn"
)

// Target is a database hydrate materializes rows into.
type Target interface {
	// Connect opens a connection to the target database.
	Connect(ctx context.Context) (*pgx.Conn, error)
	// Disposable reports whether Close destroys the database. A non-disposable
	// target (a user's own database) is a safety signal: hydrate must never treat
	// it as throwaway.
	Disposable() bool
	// Close tears the target down: it drops the throwaway database (or terminates
	// the container) for a disposable target, and only closes resources for a
	// provided one. Close is idempotent.
	Close(ctx context.Context) error
}

// Provided wraps a user-supplied database URL (`hydrate --target <url>`). It is
// not disposable — Close never drops the database.
//
// The field is `url`, not `dsn`, so it cannot shadow the internal/dsn package
// inside these methods.
type Provided struct {
	url string
}

// NewProvided returns a Target for a user-supplied connection string.
func NewProvided(url string) *Provided { return &Provided{url: url} }

// Connect opens a connection to the provided database.
//
// This is the LIVE `--target` path — the one path in the product that writes to
// a database the user nominated rather than one rowshape created — so the lock
// timeout matters more here than anywhere else: rowshape must never sit in a
// lock queue on someone's real database waiting behind a migration or a long
// transaction. Failing fast is strictly better than blocking.
func (p *Provided) Connect(ctx context.Context) (*pgx.Conn, error) {
	cfg, err := pgx.ParseConfig(p.url)
	if err != nil {
		return nil, fmt.Errorf("parse target connection: %w", err)
	}
	dsn.Apply(cfg, dsn.ReadDefaults)
	return pgx.ConnectConfig(ctx, cfg)
}

// Disposable is always false for a user-provided database.
func (p *Provided) Disposable() bool { return false }

// Close is a no-op; a user's database is never dropped.
func (p *Provided) Close(context.Context) error { return nil }

// ephemeralCounter makes disposable database names unique within a process
// without needing a clock or randomness (both of which would hurt determinism).
var ephemeralCounter atomic.Uint64

// Ephemeral is a disposable database created on a reachable Postgres server and
// dropped on Close. It is the default hydrate target: dependency-light (a libpq
// connection, no Docker), and genuinely throwaway.
type Ephemeral struct {
	adminCfg *pgx.ConnConfig
	name     string
}

// NewEphemeral creates a fresh disposable database on the server named by
// adminDSN and returns a Target bound to it. adminDSN must have permission to
// CREATE DATABASE (a role rowshape does not otherwise need — this is the one
// privileged step, isolated here).
func NewEphemeral(ctx context.Context, adminDSN string) (*Ephemeral, error) {
	adminCfg, err := pgx.ParseConfig(adminDSN)
	if err != nil {
		return nil, fmt.Errorf("parse admin connection: %w", err)
	}
	// The admin connection creates and drops databases; a connect that hangs
	// here strands the whole run before it has begun, and an idle transaction on
	// an admin role is worth bounding. The statement limit stays unset: hydrate
	// legitimately issues long COPYs into the disposable database.
	dsn.Apply(adminCfg, dsn.ReadDefaults)
	admin, err := pgx.ConnectConfig(ctx, adminCfg)
	if err != nil {
		return nil, fmt.Errorf("connect to admin database: %w", err)
	}
	defer func() { _ = admin.Close(ctx) }()

	name := ephemeralName(ephemeralCounter.Add(1))
	// Drop any stale database of the same name left by a crashed prior run of
	// this process, then create fresh. DROP/CREATE DATABASE cannot run in a
	// transaction, so these are plain Execs.
	if err := dropDatabase(ctx, admin, name); err != nil {
		return nil, fmt.Errorf("reset disposable database: %w", err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+quoteIdent(name)); err != nil {
		return nil, fmt.Errorf("create disposable database: %w", err)
	}
	return &Ephemeral{adminCfg: adminCfg, name: name}, nil
}

// dropDatabase drops a disposable database, forcing off any lingering sessions.
//
// `DROP DATABASE ... WITH (FORCE)` does that in one statement, but the clause was
// introduced in PostgreSQL 13 and is a SYNTAX ERROR on 11 and 12 — both of which
// rowshape supports (the corpus matrix runs 11-17). So on older servers the
// sessions are terminated explicitly first, which is what FORCE does internally,
// and then the plain drop succeeds.
//
// Without this, `validate --ephemeral` against a PG 11 or 12 server fails before
// it validates anything.
func dropDatabase(ctx context.Context, admin *pgx.Conn, name string) error {
	force, err := supportsDropForce(ctx, admin)
	if err != nil {
		return err
	}
	if force {
		_, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+quoteIdent(name)+" WITH (FORCE)")
		return err
	}

	// Pre-13: terminate every other backend attached to the database, then drop.
	// A session still holding the database open would otherwise make DROP fail
	// with "database is being accessed by other users".
	if _, err := admin.Exec(ctx,
		`SELECT pg_terminate_backend(pid) FROM pg_stat_activity
		 WHERE datname = $1 AND pid <> pg_backend_pid()`, name); err != nil {
		return err
	}
	_, err = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+quoteIdent(name))
	return err
}

// supportsDropForce reports whether the server is PG 13+, where DROP DATABASE
// accepts WITH (FORCE). server_version_num is an integer like 110022 (11.22) or
// 160004 (16.4), which is why it is read instead of parsing the version string.
func supportsDropForce(ctx context.Context, admin *pgx.Conn) (bool, error) {
	var num int
	if err := admin.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&num); err != nil {
		return false, fmt.Errorf("reading server version: %w", err)
	}
	return num >= 130000, nil
}

// Name returns the disposable database name.
func (e *Ephemeral) Name() string { return e.name }

// Connect opens a connection to the disposable database.
func (e *Ephemeral) Connect(ctx context.Context) (*pgx.Conn, error) {
	cfg := e.adminCfg.Copy()
	cfg.Database = e.name
	return pgx.ConnectConfig(ctx, cfg)
}

// Disposable is always true.
func (e *Ephemeral) Disposable() bool { return true }

// Close drops the disposable database, forcing off any lingering connections.
func (e *Ephemeral) Close(ctx context.Context) error {
	if e.name == "" {
		return nil
	}
	admin, err := pgx.ConnectConfig(ctx, e.adminCfg)
	if err != nil {
		return fmt.Errorf("connect to drop disposable database: %w", err)
	}
	defer func() { _ = admin.Close(ctx) }()
	// Forces off lingering sessions so the drop always succeeds — via WITH (FORCE)
	// on PG 13+, and by terminating backends explicitly on 11/12, where that
	// clause does not parse.
	if err := dropDatabase(ctx, admin, e.name); err != nil {
		return fmt.Errorf("drop disposable database: %w", err)
	}
	e.name = ""
	return nil
}

// ephemeralName builds a process-unique, valid database name for a disposable
// target. The PID keeps names distinct across the parallel test processes (and
// concurrent CLI runs); the counter keeps them distinct within a process.
func ephemeralName(n uint64) string {
	return fmt.Sprintf("rowshape_hydrate_%d_%d", os.Getpid(), n)
}

// quoteIdent double-quotes a SQL identifier, escaping embedded quotes.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
