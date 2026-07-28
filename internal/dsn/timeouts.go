package dsn

import (
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Timeouts are the server-side limits rowshape asks for on every connection.
//
// Nothing set any of these: a repo-wide search for statement_timeout,
// lock_timeout, idle_in_transaction_session_timeout and connect_timeout found no
// hits outside tests. Meanwhile the uniqueness probes, exact null counts,
// count(*) and HLL streaming run FULL SCANS against the pulled database, fired
// automatically by auto-escalation on any column whose estimated distinct/rows
// exceeds 0.95 — every email, uuid, slug and external_ref column in the schema.
//
// Client-side cancellation alone does not solve this. Killing the client closes
// the socket; Postgres notices at the next network write, which for a long
// sequential scan can be minutes away. A server-side timeout is what actually
// bounds the work.
type Timeouts struct {
	// Statement bounds a single query. Zero means no limit.
	Statement time.Duration
	// Lock bounds how long a statement waits to ACQUIRE a lock. This is the one
	// worth being aggressive about: rowshape should never sit in a lock queue on
	// someone's production database, and failing fast is strictly better than
	// blocking behind a migration or a long transaction.
	Lock time.Duration
	// IdleInTransaction bounds an open-but-idle transaction, so a wedged client
	// cannot pin an old snapshot and block VACUUM indefinitely.
	IdleInTransaction time.Duration
	// Connect bounds the TCP/TLS handshake. Without it a black-holed host hangs
	// until the OS gives up, which can be minutes.
	Connect time.Duration
}

// ReadDefaults are the limits for read-only work: pull, plan, verify.
//
// Statement is deliberately ZERO. `pull --exact` is documented as a full
// streaming pass taking "minutes to hours", and auto-escalation issues genuinely
// long scans by design — so a default statement_timeout would break the
// product's own documented behavior, turning a slow-but-correct run into a
// mysterious failure partway through. Callers that know their workload is
// bounded (CI, in particular) should set one explicitly; ScanDefaults below is
// the ready-made choice for fast-mode profiling.
//
// Lock, IdleInTransaction and Connect are safe to default because no legitimate
// rowshape workload waits on a lock, holds an idle transaction, or wants an
// unbounded connect.
var ReadDefaults = Timeouts{
	Statement:         0,
	Lock:              5 * time.Second,
	IdleInTransaction: 60 * time.Second,
	Connect:           10 * time.Second,
}

// ScanDefaults bound fast-mode profiling, where every query is expected to be a
// catalog read or a sampled aggregate rather than a full scan. Ten minutes is
// far above any healthy fast-mode query and far below "took down the replica".
var ScanDefaults = Timeouts{
	Statement:         10 * time.Minute,
	Lock:              5 * time.Second,
	IdleInTransaction: 60 * time.Second,
	Connect:           10 * time.Second,
}

// Apply sets the timeouts on a parsed config, in place.
//
// The three server-side limits go in RuntimeParams, which pgx sends as startup
// parameters on the connection — so they are in force for the FIRST query, not
// merely after a SET that something might skip. A zero duration leaves that
// limit unset rather than sending 0, so "no limit" is expressed by absence and a
// server-side default (if the DBA configured one) is respected rather than
// overridden.
//
// Any value the user already supplied in the DSN wins: someone who wrote
// `?options=-c statement_timeout%3D5s` or set the parameter explicitly meant it.
func Apply(cfg *pgx.ConnConfig, t Timeouts) {
	if cfg == nil {
		return
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	// A user can spell a timeout two ways, and only one lands in RuntimeParams
	// under its own name:
	//
	//	?statement_timeout=5s              -> RuntimeParams["statement_timeout"]
	//	?options=-c statement_timeout=5s   -> RuntimeParams["options"]
	//
	// The first cut only checked the first form — which meant the exact spelling
	// this function's comment cited as the case it respected was the one it did
	// not detect, and rowshape would send its own value alongside the user's
	// options string in the same startup packet, with undefined precedence.
	opts := cfg.RuntimeParams["options"]
	set := func(key string, d time.Duration) {
		if d <= 0 {
			return
		}
		if _, given := cfg.RuntimeParams[key]; given {
			return // the user asked for something specific; do not override it
		}
		if optionsMention(opts, key) {
			return // set via `options=-c key=...`; still the user's choice
		}
		cfg.RuntimeParams[key] = strconv.FormatInt(d.Milliseconds(), 10)
	}
	set("statement_timeout", t.Statement)
	set("lock_timeout", t.Lock)
	set("idle_in_transaction_session_timeout", t.IdleInTransaction)

	if t.Connect > 0 && cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = t.Connect
	}
}

// optionsMention reports whether a libpq `options` string sets key, in any of
// the spellings libpq accepts: "-c key=v", "-ckey=v", or "--key=v".
//
// It matches on a delimiter boundary rather than with a bare substring test, so
// a key that is a SUFFIX of another (lock_timeout inside
// idle_in_transaction_session_timeout would be the trap if the names ever
// overlapped) cannot produce a false positive.
func optionsMention(options, key string) bool {
	if options == "" {
		return false
	}
	for _, field := range strings.Fields(options) {
		f := strings.TrimPrefix(strings.TrimPrefix(field, "--"), "-c")
		f = strings.TrimSpace(f)
		if name, _, ok := strings.Cut(f, "="); ok && strings.EqualFold(strings.TrimSpace(name), key) {
			return true
		}
	}
	return false
}
