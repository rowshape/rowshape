// Package profile reads a database's shape from its catalog and turns it into a
// rowshape fixture. It is the emitter half of the format (RFC §13): it reads
// only via catalog views and (in later tasks) sampled/streamed queries, never
// SELECT * on user tables, and it retains no row values (INV-NO-ROWS).
//
// This file implements structure-only reads (P1-T3): tables, columns, types,
// structural nullability, constraints, indexes, and references. Column
// profiling — null fractions, distinct counts, samples — is layered on by the
// fast-mode profiler (P1-T4).
package profile

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rowshape/rowshape/internal/fixture"
)

// Session limits `pull` puts on the database it reads.
//
// They exist because the entire premise is that `pull` runs against PRODUCTION,
// and `--exact` is documented as taking "minutes to hours". Without them a single
// profiling query against a large table runs unbounded; a long-lived read
// transaction holds back vacuum on every table it has touched; and an ACCESS SHARE
// lock queued behind a waiting ALTER TABLE stalls every later query on that table.
// None of that is hypothetical for the customer this is aimed at — it is the
// standard reason a DBA refuses to let a new tool near a primary.
const (
	// DefaultStatementTimeout caps one profiling query. Generous enough for an
	// aggregate over a large table, short enough that a pathological plan degrades a
	// fact instead of occupying the server.
	DefaultStatementTimeout = 5 * time.Minute
	// DefaultLockTimeout caps how long a read waits for its ACCESS SHARE lock. Short
	// on purpose: waiting is precisely the state in which `pull` blocks the queue
	// behind a pending ALTER TABLE, and a lock that is not free in a few seconds is
	// one worth backing off from.
	DefaultLockTimeout = 5 * time.Second
	// DefaultIdleInTransactionTimeout bounds the read transaction when the CLIENT
	// stalls — a suspended process, a dead network path — which is the case where a
	// transaction otherwise sits open indefinitely, holding back vacuum.
	DefaultIdleInTransactionTimeout = 10 * time.Minute
)

// Options controls a structure read.
type Options struct {
	// Schemas restricts the read to these schemas. Empty means every non-system
	// schema (everything but pg_catalog, information_schema, and pg_toast*).
	Schemas []string
	// Privacy is the level the profiler targets. It governs whether candidate
	// value sets are even gathered (only under permissive); the final emit-time
	// gate is ApplyPrivacy. Empty is treated as standard (never permissive).
	Privacy Privacy
	// MaxEscalationRows is the soft cost ceiling for auto-escalation (RFC §14.5).
	// 0 uses DefaultMaxEscalationRows; a negative value disables the ceiling.
	MaxEscalationRows int64
	// Exact runs the full-pass `exact` profile mode (RFC §7.3): every column gets
	// exact null counts and measured (HLL) distinct over the whole table, and
	// uniqueness is probed exactly. Minutes to hours; opt-in via `pull --exact`.
	Exact bool
	// StatementTimeout, LockTimeout and IdleInTransactionTimeout are the session
	// limits the read runs under. Zero uses the Default* above; a NEGATIVE value
	// disables that limit, which has to be asked for explicitly because an unlimited
	// statement against production is the thing these exist to prevent.
	StatementTimeout         time.Duration
	LockTimeout              time.Duration
	IdleInTransactionTimeout time.Duration

	// Warn, if set, receives operational warnings — notably a column whose
	// uniqueness escalation was skipped because the table exceeds the cap. pull
	// wires this to stderr; silent truncation is forbidden.
	Warn func(string)
}

// querier is the subset of pgx used here, satisfied by both *pgx.Conn and
// pgx.Tx, so reads run inside a read-only transaction.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ReadStructure reads engine identity and every in-scope table's structure into
// a fixture — no column profiling. All reads run inside a read-only transaction
// so a conformant emitter can never write (RFC §13, INV-BLAST-RADIUS-ZERO).
func ReadStructure(ctx context.Context, conn *pgx.Conn, opts Options) (*fixture.Fixture, error) {
	return read(ctx, conn, opts, false)
}

// read is the shared body behind ReadStructure and Fast. When profile is set it
// also runs fast-mode column profiling (P1-T4) on each table.
func read(ctx context.Context, conn *pgx.Conn, opts Options, profile bool) (*fixture.Fixture, error) {
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("begin read-only transaction: %w", err)
	}
	// A read-only transaction makes no changes; rolling back is the correct,
	// cheap close whether or not an error occurred.
	defer func() { _ = tx.Rollback(ctx) }()

	// SET LOCAL, so the limits last exactly as long as this transaction and nothing
	// is left behind on a pooled connection. Applied before the first read, because a
	// limit set after the query it was meant to bound protects nothing.
	if err := applySessionLimits(ctx, tx, opts); err != nil {
		return nil, err
	}

	r := &reader{
		tx:                tx,
		attNames:          map[uint32]map[int16]string{},
		opclassUses:       map[uint32]struct{}{},
		privacy:           opts.Privacy,
		maxEscalationRows: effectiveCap(opts.MaxEscalationRows),
		warn:              opts.Warn,
		exact:             opts.Exact,
	}

	f := &fixture.Fixture{
		RowshapeFixture: fixture.FormatVersion,
		Tables:          map[string]fixture.Table{},
	}

	engine, err := r.engine(ctx)
	if err != nil {
		return nil, err
	}
	f.Meta.Engine = engine
	r.serverMajor = majorVersion(engine.Version)

	tables, err := r.tables(ctx, opts.Schemas)
	if err != nil {
		return nil, err
	}
	for _, t := range tables {
		tbl, err := r.readTable(ctx, t)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", t.qualified, err)
		}
		if profile {
			if err := r.profileTable(ctx, t, &tbl); err != nil {
				return nil, fmt.Errorf("profile %s: %w", t.qualified, err)
			}
		}
		f.Tables[t.qualified] = tbl
	}

	// Define the user-defined types the columns just read refer to (§6.7). This
	// runs after the table loop because it is driven by the type OIDs that loop
	// actually saw.
	types, err := r.userTypes(ctx)
	if err != nil {
		return nil, fmt.Errorf("read user-defined types: %w", err)
	}
	f.Types = types

	// Then the extensions those types and the index operator classes come from
	// (§6.8). This runs last for the same reason: it is driven by the type and
	// opclass OIDs the table loop actually saw, so nothing unreferenced is recorded.
	exts, err := r.extensions(ctx)
	if err != nil {
		return nil, fmt.Errorf("read required extensions: %w", err)
	}
	f.Extensions = exts

	// Record the profile mode (RFC §7.3): `exact` for a full pass, `targeted` when
	// auto-escalation fired on a sample-based pass, otherwise `fast`.
	if profile {
		switch {
		case r.exact:
			f.Meta.Profile.Mode = "exact"
		case len(r.escalated) > 0:
			sort.Strings(r.escalated)
			f.Meta.Profile.Mode = "targeted"
			f.Meta.Profile.Escalated = r.escalated
		default:
			f.Meta.Profile.Mode = "fast"
		}
	}
	return f, nil
}

// applySessionLimits puts the read transaction under a statement timeout, a lock
// timeout and an idle-in-transaction timeout (PRD §11).
//
// SET LOCAL rather than SET: the limits belong to this transaction, and leaving
// them on a connection a pooler may hand to someone else would be rowshape
// changing the behaviour of code it knows nothing about.
//
// A server that refuses one of these is not a reason to abandon the read — but it
// IS a reason not to pretend a limit is in force, so the failure is surfaced as a
// warning rather than swallowed.
func applySessionLimits(ctx context.Context, tx querier, opts Options) error {
	limits := []struct {
		setting string
		value   time.Duration
		def     time.Duration
	}{
		{"statement_timeout", opts.StatementTimeout, DefaultStatementTimeout},
		{"lock_timeout", opts.LockTimeout, DefaultLockTimeout},
		{"idle_in_transaction_session_timeout", opts.IdleInTransactionTimeout, DefaultIdleInTransactionTimeout},
	}
	for _, l := range limits {
		d := l.value
		if d == 0 {
			d = l.def
		}
		if d < 0 {
			continue // explicitly disabled by the caller
		}
		// Through Query rather than Exec because `querier` is the read-only subset
		// deliberately shared with the test double; the rows are empty and closed
		// immediately, which is what makes a SET safe to run this way.
		q := fmt.Sprintf("SET LOCAL %s = %d", l.setting, d.Milliseconds())
		rows, err := tx.Query(ctx, q)
		if err != nil {
			return fmt.Errorf("set %s: %w", l.setting, err)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("set %s: %w", l.setting, err)
		}
	}
	return nil
}

// reader holds a read transaction and a per-relation attribute-name cache so
// column numbers in constraints and indexes resolve to names cheaply.
type reader struct {
	tx                querier
	attNames          map[uint32]map[int16]string
	serverMajor       int      // major server version, gating version-specific catalog columns
	privacy           Privacy  // target level; gates whether value sets are gathered
	escalated         []string // qualified columns auto-escalated to a full pass (P1b-T3)
	maxEscalationRows int64    // soft cost ceiling for escalation (P1b-T4)
	warn              func(string)
	exact             bool // full-pass exact mode (P1b-T5)
	// typeUses maps every type OID seen on an in-scope column to the name
	// format_type rendered for it, so only the types actually referenced are
	// defined in the fixture (§6.7) — not every type in the database.
	typeUses map[uint32]string
	// domainBase maps a domain's rendered name to its base type's rendered name, so
	// profiling can categorize a domain-typed column by what it actually holds.
	domainBase map[string]string
	// opclassUses holds every operator class OID an in-scope index key uses, so
	// extensions can resolve the ones an extension owns (§6.8). An index declared
	// `gin (email gin_trgm_ops)` needs pg_trgm and no column type says so.
	opclassUses map[uint32]struct{}
}

// profileType is the type a column should be PROFILED as: a domain reduces to its
// base type, everything else is itself.
//
// A domain is a base type plus constraints, so a domain over integer holds
// integers and deserves a numeric range and histogram. Categorizing it by its own
// name put it in the text category instead, and the fixture came back with neither
// — losing the column's shape at the source, before hydrate ever saw it. Domains
// over domains resolve to the bottom, bounded so a cycle cannot hang the pull.
func (r *reader) profileType(typ string) string {
	name := typ
	for range 16 {
		base, ok := r.domainBase[name]
		if !ok || base == "" {
			return name
		}
		name = base
	}
	return name
}

// degrade turns a statement-timeout error into a DROPPED FACT plus a warning, and
// leaves every other error fatal.
//
// The timeouts exist so `pull` cannot hurt the production database it reads, but a
// ceiling that ABORTS the run makes the tool unusable on exactly the large tables
// it is for — the operator lowers the limit to be safe and gets no fixture at all.
// Degrading is the honest middle: the expensive fact is simply absent, which the
// confidence model already handles (an absent fact cannot license a PASS, RFC
// §7.4), and the warning names the column and the flag that raises the ceiling.
//
// Silent truncation is forbidden, which is why this never drops a fact without
// saying so. Structural reads do NOT route through here: a catalog read that times
// out leaves nothing to describe, and pretending otherwise would emit a fixture
// that claims a table has no columns.
// guarded runs a degradable profiling read inside a savepoint, so a statement the
// server cancels does not poison the whole read transaction.
//
// Without it the first timeout leaves every later statement failing with
// `current transaction is aborted, commands ignored until end of transaction
// block` — so degrading one fact silently destroyed the rest of the pull. The
// savepoint is per READ rather than per table: a timeout on one column's range
// must not cost the next column's.
//
// Establishing and releasing a savepoint costs two round trips per profiling
// query. That is the price of a limit that degrades instead of aborting, and it is
// only paid on the profiling reads — the structural catalog scan runs unguarded.
func (r *reader) guarded(ctx context.Context, name string, fn func() error) error {
	sp := "rowshape_" + name
	if err := r.exec(ctx, "SAVEPOINT "+sp); err != nil {
		return err
	}
	if err := fn(); err != nil {
		if rbErr := r.exec(ctx, "ROLLBACK TO SAVEPOINT "+sp); rbErr != nil {
			return rbErr
		}
		return err
	}
	return r.exec(ctx, "RELEASE SAVEPOINT "+sp)
}

// exec runs a statement that returns no rows through the read-only querier.
func (r *reader) exec(ctx context.Context, sql string) error {
	rows, err := r.tx.Query(ctx, sql)
	if err != nil {
		return err
	}
	rows.Close()
	return rows.Err()
}

func (r *reader) degrade(table, column, fact string, err error) (bool, error) {
	if err == nil {
		return false, nil
	}
	if !isStatementTimeout(err) {
		return false, err
	}
	r.warnf("%s.%s: %s omitted — the profiling query hit the statement timeout; "+
		"raise it with --statement-timeout, or use --exact off-peak, to record this fact",
		table, column, fact)
	return true, nil
}

// isStatementTimeout reports whether an error is Postgres cancelling a statement
// that ran past statement_timeout (SQLSTATE 57014).
func isStatementTimeout(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "57014"
	}
	return false
}

// warnf emits an operational warning if a sink is configured.
func (r *reader) warnf(format string, args ...any) {
	if r.warn != nil {
		r.warn(fmt.Sprintf(format, args...))
	}
}

// majorVersion parses the leading integer of a Postgres server_version string
// ("16.13" -> 16, "11.22" -> 11).
func majorVersion(v string) int {
	n := 0
	seen := false
	for i := 0; i < len(v); i++ {
		if v[i] < '0' || v[i] > '9' {
			break
		}
		n = n*10 + int(v[i]-'0')
		seen = true
	}
	if !seen {
		return 0
	}
	return n
}

// engine reads the engine name and version. The version is mandatory because
// cost models are engine-version-conditional (RFC §9.1).
func (r *reader) engine(ctx context.Context) (fixture.Engine, error) {
	var version string
	if err := r.tx.QueryRow(ctx, "SHOW server_version").Scan(&version); err != nil {
		return fixture.Engine{}, fmt.Errorf("read server_version: %w", err)
	}
	return fixture.Engine{Name: "postgres", Version: version}, nil
}

// tableRef identifies one relation.
type tableRef struct {
	oid       uint32
	schema    string
	name      string
	qualified string // schema.name
	reltuples float64
	bytes     int64
	relkind   string // 'r' ordinary, 'p' partitioned parent
}

// tables lists ordinary and partitioned tables in the requested schemas.
func (r *reader) tables(ctx context.Context, schemas []string) ([]tableRef, error) {
	const q = `
SELECT c.oid, n.nspname, c.relname, c.reltuples, pg_total_relation_size(c.oid), c.relkind::text
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('r', 'p')
  AND NOT c.relispartition
  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
  AND n.nspname NOT LIKE 'pg_toast%'
  AND ($1::text[] IS NULL OR n.nspname = ANY($1))
ORDER BY n.nspname, c.relname`

	var schemaArg any
	if len(schemas) > 0 {
		schemaArg = schemas
	}
	rows, err := r.tx.Query(ctx, q, schemaArg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []tableRef
	for rows.Next() {
		var t tableRef
		if err := rows.Scan(&t.oid, &t.schema, &t.name, &t.reltuples, &t.bytes, &t.relkind); err != nil {
			return nil, err
		}
		t.qualified = t.schema + "." + t.name
		out = append(out, t)
	}
	return out, rows.Err()
}

// readTable assembles one table's columns, constraints, indexes, and references.
func (r *reader) readTable(ctx context.Context, t tableRef) (fixture.Table, error) {
	tbl := fixture.Table{
		Rows:  rowEstimate(t.reltuples),
		Bytes: t.bytes,
	}
	cols, order, err := r.columns(ctx, t.oid)
	if err != nil {
		return tbl, err
	}
	constraints, err := r.constraints(ctx, t.oid)
	if err != nil {
		return tbl, err
	}
	indexes, err := r.indexes(ctx, t.oid)
	if err != nil {
		return tbl, err
	}
	refs, err := r.references(ctx, t.oid)
	if err != nil {
		return tbl, err
	}

	// A single-column unique constraint or unique index is a free proof of
	// uniqueness at exact confidence (RFC §6.4 / §7.2). Uniqueness is never
	// inferred any other way (INV-UNIQUENESS).
	applyUniquenessProofs(cols, constraints, indexes)

	tbl.Columns = cols
	tbl.Constraints = constraints
	tbl.Indexes = indexes
	tbl.References = refs
	_ = order
	return tbl, nil
}

// rowEstimate turns pg_class.reltuples into a rows fact. reltuples is the
// planner's estimate (RFC §7.1 estimated); a never-analyzed table reports -1,
// which we clamp to 0. An exact count is the profiler's job (P1-T4), not here.
func rowEstimate(reltuples float64) fixture.Fact[int64] {
	v := int64(reltuples)
	if v < 0 {
		v = 0
	}
	return fixture.Fact[int64]{Value: v, Confidence: fixture.Estimated}
}

// columns reads column structure. It returns the column map plus the on-disk
// order (attnum ascending) so downstream emitters can be deterministic.
//
// pg_attribute.attgenerated arrived with generated columns in PG12. On 10 and 11
// the column does not exist and selecting it is a hard error — `pull` could not
// read a catalog at all on those servers:
//
//	ERROR: column a.attgenerated does not exist (SQLSTATE 42703)
//
// rowshape claims PG 11-17, so that was `pull` not working on a supported
// version. Below 12 the concept does not exist, so it degrades to a constant
// empty string — the same shape as the indnullsnotdistinct guard below.
func (r *reader) columns(ctx context.Context, oid uint32) (map[string]fixture.Column, []string, error) {
	generated := `''` // PG < 12: no generated columns
	if r.serverMajor >= 12 {
		generated = "a.attgenerated::text"
	}
	// The pg_type join resolves a DOMAIN to its base type. Profiling keys off the
	// type category, and a domain read as an opaque name fell to the text category:
	// a domain over integer got no range and no histogram, so `pull` silently lost
	// the shape of every domain-typed column, and hydrate then had nothing to
	// generate from. Every atttypid has a pg_type row, so the join drops nothing.
	// A STORED generated column's expression lives in pg_attrdef, the same place a
	// DEFAULT does. Without it `generated: stored` says the column is computed but
	// not from what, so a consumer rebuilt it as an ordinary column and the target
	// accepted an UPDATE production rejects.
	genExpr := `''`
	if r.serverMajor >= 12 {
		genExpr = `COALESCE(pg_get_expr(ad.adbin, ad.adrelid), '')`
	}
	q := `
SELECT a.attnum, a.attname, format_type(a.atttypid, a.atttypmod),
       a.attnotnull, a.attidentity::text, ` + generated + `, a.atttypid,
       CASE WHEN t.typtype = 'd' THEN format_type(t.typbasetype, t.typtypmod) ELSE '' END,
       ` + genExpr + `
FROM pg_attribute a
JOIN pg_type t ON t.oid = a.atttypid
LEFT JOIN pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attnum`

	rows, err := r.tx.Query(ctx, q, oid)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	cols := map[string]fixture.Column{}
	var order []string
	names := map[int16]string{}
	for rows.Next() {
		var (
			attnum              int16
			name, typ           string
			notnull             bool
			identity, generated string
			typoid              uint32
			domainBase          string
			genExprText         string
		)
		if err := rows.Scan(&attnum, &name, &typ, &notnull, &identity, &generated, &typoid,
			&domainBase, &genExprText); err != nil {
			return nil, nil, err
		}
		if domainBase != "" {
			if r.domainBase == nil {
				r.domainBase = map[string]string{}
			}
			r.domainBase[typ] = domainBase
		}
		// Remember the type by OID so userTypes can resolve enum/domain definitions
		// exactly, rather than trying to re-parse format_type's rendering. The
		// rendered name is the map key because that is the string a column's `type`
		// carries, so a fixture consumer joins the two by equality.
		if r.typeUses == nil {
			r.typeUses = map[uint32]string{}
		}
		r.typeUses[typoid] = typ
		col := fixture.Column{
			Type:     typ,
			Nullable: !notnull, // structural nullability from the DDL, always exact (§6.1)
		}
		switch {
		case identity == "a" || identity == "d":
			col.Generated = "identity"
			// ALWAYS vs BY DEFAULT is the difference between rejecting an explicit
			// INSERT value and accepting it, so a migration that supplies one behaves
			// differently on each.
			if identity == "a" {
				col.Identity = "always"
			} else {
				col.Identity = "by_default"
			}
		case generated == "v":
			// PostgreSQL 18 added VIRTUAL generated columns, computed on read and never
			// stored. They are recorded, and deliberately NOT reproduced: a consumer
			// meeting `generated: virtual` creates an ordinary column and reports it,
			// exactly as it does for a stored column whose expression is withheld.
			//
			// The branch has to exist even before rowshape claims 18, because without
			// it a virtual column fell through to the DEFAULT case and its generation
			// expression was recorded as a plain DEFAULT — which the target then emits
			// on an ordinary column, so an UPDATE production rejects would succeed
			// there. A wrong verdict from a catalog value this code did not know about.
			col.Generated = "virtual"
			col.GeneratedExpression = genExprText
		case generated == "s":
			col.Generated = "stored"
			// pg_attrdef holds the expression for a generated column exactly as it
			// does for a DEFAULT; only a STORED column's is a generation expression.
			col.GeneratedExpression = genExprText
		default:
			// An ordinary column's pg_attrdef entry IS its DEFAULT. Only reached in the
			// default branch, so an identity column's implicit nextval(...) — which
			// `generated` already records — never leaks in as one, and would name a
			// sequence the target does not have.
			col.Default = genExprText
		}
		cols[name] = col
		order = append(order, name)
		names[attnum] = name
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	r.attNames[oid] = names
	return cols, order, nil
}

// constraints reads primary-key, unique, and check constraints.
//
// NULLS [NOT] DISTINCT is a property of the backing unique index
// (pg_index.indnullsnotdistinct), added in PG15 — it is NOT on pg_constraint.
// On PG < 15 the concept doesn't exist (NULLs were always distinct), so the
// column read degrades to a constant false there, keeping the reader correct
// across the whole PG 11–17 matrix (P2-T13).
func (r *reader) constraints(ctx context.Context, oid uint32) ([]fixture.Constraint, error) {
	nnd := "false"
	from := "FROM pg_constraint con"
	if r.serverMajor >= 15 {
		nnd = "COALESCE(ix.indnullsnotdistinct, false)"
		from = "FROM pg_constraint con LEFT JOIN pg_index ix ON ix.indexrelid = con.conindid"
	}
	q := `
SELECT con.conname, con.contype::text, con.convalidated, ` + nnd + `,
       con.conkey,
       CASE WHEN con.contype = 'c' THEN pg_get_expr(con.conbin, con.conrelid) END
` + from + `
WHERE con.conrelid = $1 AND con.contype IN ('p', 'u', 'c')
ORDER BY con.conname`

	rows, err := r.tx.Query(ctx, q, oid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []fixture.Constraint
	for rows.Next() {
		var (
			name             string
			contype          string
			validated        bool
			nullsNotDistinct bool
			conkey           []int16
			checkExpr        *string
		)
		if err := rows.Scan(&name, &contype, &validated, &nullsNotDistinct, &conkey, &checkExpr); err != nil {
			return nil, err
		}
		c := fixture.Constraint{
			Name:    name,
			Kind:    constraintKind(contype),
			Columns: r.namesFor(oid, conkey),
		}
		// A NOT VALID constraint (validated:false) MUST be preserved (§6.4).
		if !validated {
			v := false
			c.Validated = &v
		}
		if contype == "u" {
			// Default is NULLS DISTINCT; record it explicitly (§6.4).
			nd := !nullsNotDistinct
			c.NullsDistinct = &nd
		}
		if contype == "c" && checkExpr != nil {
			c.Expression = *checkExpr // verbatim (§6.4); opaque under privacy:strict
			c.Columns = nil           // a CHECK's columns aren't meaningful here
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// indexes reads index structure and size.
func (r *reader) indexes(ctx context.Context, oid uint32) ([]fixture.Index, error) {
	// The key-expression array is what makes an EXPRESSION index representable. For
	// a key that is an expression, indkey holds 0 and the expression text lives in
	// indexprs, so `columns` alone described a unique index on lower(email) as having
	// no keys at all — and it was then silently dropped when the disposable database
	// was built, losing a uniqueness constraint production enforces. pg_get_indexdef
	// with a column number renders each key (a plain name, or the expression) exactly
	// as Postgres would, including any DESC / operator class.
	//
	// indnkeyatts, not indnatts: an INCLUDE column is payload, not part of the key,
	// and emitting it as one would change what the index enforces.
	// The rendered key is assembled here rather than taken from pg_get_indexdef's
	// per-column form, because that form returns the key EXPRESSION only. It was
	// believed to include "any DESC or operator class" and it does not: an index
	// declared `(created_at DESC NULLS LAST)` renders as `created_at`, and
	// `gin (email gin_trgm_ops)` renders as `email`. So the ordering was silently
	// dropped, and the trigram index was rebuilt without its operator class and
	// failed to build at all (`data type text has no default operator class for
	// access method "gin"`), leaving the disposable database without an index
	// production has.
	//
	// indclass, indoption and indcollation are 0-based vectors parallel to indkey.
	// Each part is appended only when it is NOT the default, so an ordinary
	// ascending btree key still renders as the bare column name and fixtures that
	// have one do not churn:
	//
	//   - operator class: omitted when opcdefault, schema-qualified otherwise (the
	//     extension providing it may not be on the target's search_path);
	//   - DESC: bit 0 of indoption;
	//   - NULLS FIRST/LAST: bit 1, emitted only when it differs from the default
	//     implied by the direction (ASC defaults to NULLS LAST, DESC to NULLS FIRST).
	//
	// indclass also feeds opclassUses, which is how an extension requirement hiding
	// in an index reaches §6.8: gin_trgm_ops needs pg_trgm and no column type says so.
	const q = `
SELECT ic.relname, am.amname, ix.indisunique,
       pg_get_expr(ix.indpred, ix.indrelid),
       pg_relation_size(ic.oid),
       array_to_string(ix.indkey, ' '),
       (SELECT array_agg(
                 pg_get_indexdef(ix.indexrelid, k.n::int, true)
                 || CASE WHEN oc.opcdefault THEN ''
                         WHEN ocn.nspname = 'pg_catalog' THEN ' ' || quote_ident(oc.opcname)
                         ELSE ' ' || quote_ident(ocn.nspname) || '.' || quote_ident(oc.opcname) END
                 || CASE WHEN (ix.indoption[k.n-1] & 1) = 1 THEN ' DESC' ELSE '' END
                 || CASE WHEN (ix.indoption[k.n-1] & 1) = 1 AND (ix.indoption[k.n-1] & 2) = 0 THEN ' NULLS LAST'
                         WHEN (ix.indoption[k.n-1] & 1) = 0 AND (ix.indoption[k.n-1] & 2) = 2 THEN ' NULLS FIRST'
                         ELSE '' END
                 ORDER BY k.n)
          FROM generate_series(1, ix.indnkeyatts) AS k(n)
          JOIN pg_opclass oc ON oc.oid = ix.indclass[k.n-1]
          JOIN pg_namespace ocn ON ocn.oid = oc.opcnamespace),
       EXISTS (SELECT 1 FROM unnest(ix.indkey) ik WHERE ik = 0),
       ix.indnkeyatts,
       array_to_string(ix.indclass, ' ')
FROM pg_index ix
JOIN pg_class ic ON ic.oid = ix.indexrelid
JOIN pg_am am ON am.oid = ic.relam
WHERE ix.indrelid = $1
ORDER BY ic.relname`

	rows, err := r.tx.Query(ctx, q, oid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []fixture.Index
	for rows.Next() {
		var (
			name, method  string
			unique        bool
			partial       *string
			bytes         int64
			indkey        string
			keys          []string
			hasExpression bool
			nkeyatts      int16
			indclass      string
		)
		if err := rows.Scan(&name, &method, &unique, &partial, &bytes, &indkey, &keys,
			&hasExpression, &nkeyatts, &indclass); err != nil {
			return nil, err
		}
		for _, cls := range parseOIDs(indclass) {
			r.opclassUses[cls] = struct{}{}
		}

		// indkey holds key columns FOLLOWED BY the INCLUDE payload, so it has to be
		// split at indnkeyatts. Emitting the payload as part of the key widened a
		// covering UNIQUE index's key: `UNIQUE (customer_id, created_at) INCLUDE
		// (total)` was recorded as unique on all three, which is strictly weaker than
		// what production enforces — a migration that violates the real two-column
		// uniqueness could reach PASS.
		attnums := parseAttnums(indkey)
		keyAtts, includeAtts := splitKeyAtts(attnums, int(nkeyatts))

		idx := fixture.Index{
			Name:    name,
			Method:  method,
			Unique:  unique,
			Columns: r.namesFor(oid, keyAtts),
			Include: r.namesFor(oid, includeAtts),
			Bytes:   bytes,
		}
		// `keys` is the rendered form of each key, from pg_get_indexdef. It used to be
		// carried only for an EXPRESSION index, on the reasoning that for plain columns
		// it would just restate `columns`. It does not always: `DESC NULLS LAST` and a
		// non-default operator class are part of the rendered key and appear nowhere in
		// a bare column name, so an index recorded from `columns` alone came back with
		// its ordering silently dropped, and a trigram index came back unbuildable
		// (`data type text has no default operator class for access method "gin"`).
		// Carry it whenever the rendering is not reproducible from the names, which
		// keeps an ordinary btree index free of the redundancy.
		if hasExpression || !keysMatchColumns(keys, idx.Columns) {
			idx.Keys = keys
		}
		if partial != nil {
			idx.Partial = *partial
		}
		out = append(out, idx)
	}
	return out, rows.Err()
}

// splitKeyAtts splits an index's attribute numbers into its key columns and its
// INCLUDE payload at nkeyatts. A count outside the slice is clamped, so a catalog
// shape this code did not anticipate degrades to "all of it is key" — the reading
// that over-constrains rather than under-constrains.
func splitKeyAtts(attnums []int16, nkeyatts int) ([]int16, []int16) {
	if nkeyatts <= 0 || nkeyatts >= len(attnums) {
		return attnums, nil
	}
	return attnums[:nkeyatts], attnums[nkeyatts:]
}

// keysMatchColumns reports whether pg_get_indexdef's rendered keys say no more
// than the plain column names do. Postgres renders a plain ascending key on a
// default operator class as just the (quoted where needed) column name, so any
// difference means the rendering carries something `columns` cannot: an ordering,
// a collation, or an operator class.
func keysMatchColumns(keys, cols []string) bool {
	if len(keys) != len(cols) {
		return false
	}
	for i, k := range keys {
		if k != cols[i] && k != `"`+cols[i]+`"` {
			return false
		}
	}
	return true
}

// parseOIDs parses a space-separated oidvector rendering ("3126 10042") into OIDs,
// skipping anything that is not a number.
func parseOIDs(s string) []uint32 {
	var out []uint32
	for _, f := range strings.Fields(s) {
		n, err := strconv.ParseUint(f, 10, 32)
		if err != nil {
			continue
		}
		out = append(out, uint32(n))
	}
	return out
}

// references reads foreign keys, their name, their referential actions and
// whether they are validated. Fan-out and orphan_fraction (the measured §6.6
// fields) are added by P1-T11.
//
// A COMPOSITE key becomes one entry per column pair, which is what fan-out wants
// — but the entries carry the shared constraint NAME so a consumer can group them
// back into the one constraint they came from. Without it, rebuilding produced two
// independent single-column keys, which constrain something else entirely.
func (r *reader) references(ctx context.Context, oid uint32) ([]fixture.Reference, error) {
	const q = `
SELECT con.conkey, con.confkey, con.confrelid, con.confdeltype::text,
       refcl.relname, refns.nspname, con.conname, con.confupdtype::text, con.convalidated
FROM pg_constraint con
JOIN pg_class refcl ON refcl.oid = con.confrelid
JOIN pg_namespace refns ON refns.oid = refcl.relnamespace
WHERE con.conrelid = $1 AND con.contype = 'f'
ORDER BY con.conname`

	rows, err := r.tx.Query(ctx, q, oid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type fkRow struct {
		conkey, confkey     []int16
		confrelid           uint32
		delType             string
		refTable, refSchema string
		name                string
		updType             string
		validated           bool
	}
	var fks []fkRow
	for rows.Next() {
		var fk fkRow
		if err := rows.Scan(&fk.conkey, &fk.confkey, &fk.confrelid, &fk.delType, &fk.refTable,
			&fk.refSchema, &fk.name, &fk.updType, &fk.validated); err != nil {
			return nil, err
		}
		fks = append(fks, fk)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var out []fixture.Reference
	for _, fk := range fks {
		localCols := r.namesFor(oid, fk.conkey)
		refCols, err := r.namesForRel(ctx, fk.confrelid, fk.confkey)
		if err != nil {
			return nil, err
		}
		for i := range localCols {
			refCol := ""
			if i < len(refCols) {
				refCol = refCols[i]
			}
			ref := fixture.Reference{
				Column:   localCols[i],
				To:       fk.refSchema + "." + fk.refTable + "." + refCol,
				Name:     fk.name,
				OnDelete: onDeleteAction(fk.delType),
				OnUpdate: onDeleteAction(fk.updType),
			}
			// Only recorded when false: an absent field means validated, which is the
			// overwhelming majority, and writing `validated: true` on every reference
			// would be noise in a format meant to be read (§3).
			if !fk.validated {
				v := false
				ref.Validated = &v
			}
			out = append(out, ref)
		}
	}
	return out, nil
}

// namesFor maps a relation's attribute numbers to names using the cache built
// by columns(). Zero (an expression column) is skipped.
func (r *reader) namesFor(oid uint32, attnums []int16) []string {
	names := r.attNames[oid]
	var out []string
	for _, n := range attnums {
		if n == 0 {
			continue
		}
		if nm, ok := names[n]; ok {
			out = append(out, nm)
		}
	}
	return out
}

// namesForRel resolves attribute names on a possibly-uncached relation (the
// referenced side of a foreign key), filling the cache on demand.
func (r *reader) namesForRel(ctx context.Context, oid uint32, attnums []int16) ([]string, error) {
	if _, ok := r.attNames[oid]; !ok {
		const q = `SELECT attnum, attname FROM pg_attribute WHERE attrelid = $1 AND attnum > 0 AND NOT attisdropped`
		rows, err := r.tx.Query(ctx, q, oid)
		if err != nil {
			return nil, err
		}
		names := map[int16]string{}
		for rows.Next() {
			var n int16
			var name string
			if err := rows.Scan(&n, &name); err != nil {
				rows.Close()
				return nil, err
			}
			names[n] = name
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		r.attNames[oid] = names
	}
	return r.namesFor(oid, attnums), nil
}

// applyUniquenessProofs sets unique:{true, exact, via:constraint} on any column
// proven unique by a single-column unique constraint or unique index (RFC §6.4).
// Composite uniqueness proves nothing about an individual column, so only
// single-column proofs count.
func applyUniquenessProofs(cols map[string]fixture.Column, constraints []fixture.Constraint, indexes []fixture.Index) {
	proven := map[string]bool{}
	for _, c := range constraints {
		if (c.Kind == "unique" || c.Kind == "primary_key") && len(c.Columns) == 1 {
			proven[c.Columns[0]] = true
		}
	}
	for _, idx := range indexes {
		if idx.Unique && idx.Partial == "" && len(idx.Columns) == 1 {
			proven[idx.Columns[0]] = true
		}
	}
	names := make([]string, 0, len(proven))
	for name := range proven {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		col, ok := cols[name]
		if !ok {
			continue
		}
		t := true
		col.Unique = &fixture.Fact[bool]{Value: t, Confidence: fixture.Exact, Via: "constraint"}
		cols[name] = col
	}
}

// constraintKind maps a pg_constraint contype to the fixture vocabulary (§6.4).
func constraintKind(contype string) string {
	switch contype {
	case "p":
		return "primary_key"
	case "u":
		return "unique"
	case "c":
		return "check"
	case "f":
		return "foreign_key"
	default:
		return contype
	}
}

// onDeleteAction maps a pg_constraint confdeltype to a readable keyword (§6.6).
func onDeleteAction(t string) string {
	switch t {
	case "c":
		return "cascade"
	case "r":
		return "restrict"
	case "n":
		return "set_null"
	case "d":
		return "set_default"
	case "a":
		return "no_action"
	default:
		return t
	}
}

// parseAttnums parses the space-separated attnum list from an int2vector's text
// form (pg_index.indkey) into attribute numbers.
func parseAttnums(s string) []int16 {
	var out []int16
	var cur int16
	inNum := false
	neg := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '-' && !inNum:
			neg = true
			inNum = true
		case ch >= '0' && ch <= '9':
			cur = cur*10 + int16(ch-'0')
			inNum = true
		case ch == ' ':
			if inNum {
				if neg {
					cur = -cur
				}
				out = append(out, cur)
			}
			cur, inNum, neg = 0, false, false
		}
	}
	if inNum {
		if neg {
			cur = -cur
		}
		out = append(out, cur)
	}
	return out
}
