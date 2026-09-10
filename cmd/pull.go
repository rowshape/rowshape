package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rowshape/rowshape/internal/dsn"
	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/profile"
	"github.com/rowshape/rowshape/internal/rlog"
	"github.com/rowshape/rowshape/internal/verdict"
	"github.com/spf13/cobra"
)

// pullOptions holds the flags for `rowshape pull`.
type pullOptions struct {
	dsn               string
	out               string
	privacy           string
	schemas           []string
	iKnow             bool
	maxEscalationRows int64
	exact             bool
	stmtTimeout       time.Duration
	lockTimeout       time.Duration
	idleTimeout       time.Duration
}

// newPullCmd reads production shape read-only and emits a committable
// rowshape.yaml (RFC §13 emitter; PRD §8.1).
//
// In phase 1 this reads structure via the catalog (P1-T3). Fast-mode column
// profiling (P1-T4), privacy redaction (P1-T5), and the assembled emitter
// (P1-T6) layer on top.
func newPullCmd() *cobra.Command {
	opts := &pullOptions{out: "rowshape.yaml", privacy: "standard", maxEscalationRows: profile.DefaultMaxEscalationRows,
		stmtTimeout: profile.DefaultStatementTimeout,
		lockTimeout: profile.DefaultLockTimeout,
		idleTimeout: profile.DefaultIdleInTransactionTimeout}
	cmd := &cobra.Command{
		Use:   "pull [connection-url]",
		Short: "Read a database's shape (read-only) and emit rowshape.yaml",
		// The meta description for the generated docs page. Deliberately longer
		// than Short: Short is a line of --help output, this is the sentence a
		// search result shows. See docsDescription in tools/gencli.
		Annotations: map[string]string{
			"docs.description": "Read a PostgreSQL database's structure and statistical shape through catalog views only, never your rows, and write a committable fixture.",
		},
		Long: "pull reads a database's structure and statistical shape through catalog\n" +
			"views only — never SELECT * on user tables — and writes a committable,\n" +
			"value-free rowshape.yaml. It requires a read-only role and refuses to run\n" +
			"as a superuser without --i-know.\n\n" +
			"The connection may be given as a URL argument or through the standard\n" +
			"libpq environment variables (PGHOST, PGPORT, PGUSER, PGDATABASE, ...).",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				opts.dsn = args[0]
			}
			// --exact is documented as a full streaming pass taking "minutes to
			// hours", so the fast-mode statement cap would turn a slow-but-correct
			// run into a mysterious partial failure. Disable the ceiling for
			// --exact unless the operator asked for one explicitly.
			if opts.exact && !cmd.Flags().Changed("statement-timeout") {
				opts.stmtTimeout = -1
			}
			return runPull(cmd.Context(), opts)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&opts.out, "out", "o", opts.out, "output path for the fixture")
	f.StringVar(&opts.privacy, "privacy", opts.privacy, "privacy level: strict | standard | permissive")
	f.StringSliceVar(&opts.schemas, "schema", nil, "restrict to these schemas (default: all non-system)")
	f.BoolVar(&opts.iKnow, "i-know", false, "override the refusal to run as a superuser")
	f.Int64Var(&opts.maxEscalationRows, "max-escalation-rows", opts.maxEscalationRows,
		"skip uniqueness escalation on tables larger than this (0 = default, negative = no cap)")
	f.DurationVar(&opts.stmtTimeout, "statement-timeout", opts.stmtTimeout,
		"cap one profiling query against the source database (negative disables it; --exact defaults to disabled)")
	f.DurationVar(&opts.lockTimeout, "lock-timeout", opts.lockTimeout,
		"cap how long a read waits for its lock, so pull never queues behind a pending ALTER TABLE (negative disables it)")
	f.DurationVar(&opts.idleTimeout, "idle-timeout", opts.idleTimeout,
		"cap how long the read transaction may sit idle, so a stalled client cannot hold back vacuum (negative disables it)")
	f.BoolVar(&opts.exact, "exact", false, "full streaming pass: exact null counts and measured (HLL) distinct for every column (minutes to hours)")
	return cmd
}

// runPull connects, enforces the read-only posture, reads structure, and writes
// the fixture. Connection strings and credentials are never logged or written
// into the fixture (PRD §5): errors are sanitized, and meta records no host.
func runPull(ctx context.Context, opts *pullOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}

	level, err := profile.ParsePrivacy(opts.privacy)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rowshape pull: %v\n", err)
		return toolError()
	}

	cfg, err := pgx.ParseConfig(opts.dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rowshape pull: could not parse the connection settings")
		return toolError()
	}
	host := cfg.Host
	if w := dsn.InsecureWarning(cfg); w != "" {
		fmt.Fprintf(os.Stderr, "rowshape pull: warning: %s\n", w)
	}

	// Identify the connection in pg_stat_activity. Without it a DBA watching their
	// primary during a pull sees an anonymous session running unfamiliar aggregates
	// against their largest tables, with nothing to attribute it to — and "what is
	// this connection" is a question that gets tools banned from production.
	//
	// Only when the caller has not set one: an operator who chose their own
	// application_name did so for a reason, most likely to satisfy a monitoring rule.
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	if cfg.RuntimeParams["application_name"] == "" {
		cfg.RuntimeParams["application_name"] = "rowshape/" + version
	}

	rlog.Debug("connecting", "host", host, "database", cfg.Database, "user", cfg.User, "tls", cfg.TLSConfig != nil)

	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		// Never surface the DSN — it may carry a password. But "it failed" alone
		// was not actionable either: an operator could not tell a typo'd host from
		// a wrong password from an expired certificate. Report rowshape's OWN
		// classification of the failure, never the driver's text.
		class, hint := dsn.ClassifyConnect(err)
		fmt.Fprintf(os.Stderr, "rowshape pull: could not connect to the database: %s\n", class)
		if hint != "" {
			fmt.Fprintf(os.Stderr, "rowshape pull: %s\n", hint)
		}
		return toolError()
	}
	defer func() { _ = conn.Close(ctx) }()

	if err := profile.CheckAccess(ctx, conn, opts.iKnow); err != nil {
		fmt.Fprintf(os.Stderr, "rowshape pull: %v\n", err)
		return toolError()
	}

	f, err := profile.Fast(ctx, conn, profile.Options{
		Schemas:                  opts.schemas,
		Privacy:                  level,
		Exact:                    opts.exact,
		MaxEscalationRows:        opts.maxEscalationRows,
		StatementTimeout:         opts.stmtTimeout,
		LockTimeout:              opts.lockTimeout,
		IdleInTransactionTimeout: opts.idleTimeout,
		Warn: func(msg string) {
			fmt.Fprintf(os.Stderr, "rowshape pull: warning: %s\n", msg)
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "rowshape pull: profiling failed: %v\n", err)
		// A profiling read that hits the ceiling DEGRADES to a missing fact; only a
		// STRUCTURAL read reaches here, and a structure read that times out leaves
		// nothing to describe. Say which knob is responsible rather than leaving the
		// operator with a bare SQLSTATE.
		if strings.Contains(err.Error(), "57014") {
			fmt.Fprintln(os.Stderr, "rowshape pull: the CATALOG read hit --statement-timeout; without it there is no schema to describe, so this is fatal rather than a dropped fact — raise --statement-timeout")
		}
		return toolError()
	}

	// Enforce the privacy level before anything is written (RFC §8.2), and record
	// the source host only as a salted hash, never verbatim (RFC §8.4).
	profile.ApplyPrivacy(f, level, 0)
	f.Meta.Source = profile.HashSource(host)

	stampMeta(f, string(level))

	out, err := fixture.Emit(f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rowshape pull: assembling fixture failed: %v\n", err)
		return toolError()
	}
	if err := os.WriteFile(opts.out, out, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "rowshape pull: writing %s failed: %v\n", opts.out, err)
		return toolError()
	}
	fmt.Fprintf(os.Stderr, "rowshape pull: wrote %s (%d tables)\n", opts.out, len(f.Tables))
	// A fixture only works if it gets COMMITTED (RFC §3.3). One that has quietly
	// grown past the budget stops fitting in a diff, and the failure mode is that
	// nobody notices until a reviewer refuses it — so say so at the moment it is
	// written, and name what actually reduces it.
	if fixture.OverBudget(len(out), len(f.Tables)) {
		fmt.Fprintf(os.Stderr, "rowshape pull: warning: %s is %d KiB across %d tables, over the %d KiB budget "+
			"a %d-table fixture should keep to (RFC §3.3) — narrow it with --schema, or split the schema "+
			"across fixtures, so it stays reviewable in a diff\n",
			opts.out, len(out)/1024, len(f.Tables), fixture.BudgetBytes/1024, fixture.BudgetTables)
	}
	return nil
}

// stampMeta fills the meta fields the reader doesn't know: id, timestamps, the
// generator tag, the privacy level, and the fast profile mode. It deliberately
// records no host or connection detail (PRD §5, RFC §8.4).
func stampMeta(f *fixture.Fixture, privacy string) {
	now := time.Now().UTC().Format(time.RFC3339)
	if f.Meta.ID == "" {
		// A neutral, host-free label (PRD §5): the scan date, never the source.
		f.Meta.ID = "fixture@" + now[:10]
	}
	f.Meta.GeneratedAt = now
	f.Meta.Generator = "rowshape/" + version
	f.Meta.Privacy = privacy
	// Profile.Mode is set by the profiler (fast, or targeted when auto-escalation
	// fires — P1b-T3); don't override it here.
	f.Meta.Profile.ScannedAt = now
}

// toolError returns the exit-3 tool-error outcome (PRD §10).
func toolError() error {
	return &ExitError{Code: verdict.ExitToolError}
}
