package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rowshape/rowshape/internal/dsn"
	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/hydrate"
	"github.com/rowshape/rowshape/internal/runner"
	"github.com/rowshape/rowshape/internal/target"
	"github.com/rowshape/rowshape/internal/toolerror"
	"github.com/rowshape/rowshape/internal/validate"
	"github.com/rowshape/rowshape/internal/verdict"
	"github.com/spf13/cobra"
)

// validateOptions holds the flags for `rowshape validate`.
type validateOptions struct {
	fixturePath string
	migrations  string // a .sql file or a project/migrations directory
	target      string // a provided live target (e.g. a Neon branch URL)
	ephemeral   string // admin URL: create a disposable database for the run
	runnerKind  string // override runner auto-detection
	asJSON      bool
	warnFail    bool // make a WARN-only verdict exit non-zero
	calibrate   bool // fit the cost curve at two scales, upgrading estimates to measured
	seed        int64
	scale       float64
	maxRows     int64
	iKnowTarget bool // acknowledge that --target is written to and committed
	stmtTimeout time.Duration
}

// newValidateCmd applies a proposed migration against a hydrated disposable
// target (or a provided live branch) and returns a verdict. It is free forever
// and never calls the cloud (INV-NEVER-GATE-VALIDATE): this command imports no
// network client. Its blast radius is zero (INV-BLAST-RADIUS-ZERO): there is no
// `apply`, and it hard-refuses a target whose host matches the fixture's source.
func newValidateCmd() *cobra.Command {
	opts := &validateOptions{fixturePath: "rowshape.yaml", migrations: "migrations", scale: 1.0,
		stmtTimeout: validate.DefaultStatementTimeout}
	cmd := &cobra.Command{
		Use:   "validate [rowshape.yaml]",
		Short: "Validate a migration against production-shaped data; return a verdict",
		// The meta description for the generated docs page. Deliberately longer
		// than Short: Short is a line of --help output, this is the sentence a
		// search result shows. See docsDescription in tools/gencli.
		Annotations: map[string]string{
			"docs.description": "Validate a migration against production-shaped data in a disposable PostgreSQL and return a verdict: which lock, how long, which rows break it.",
		},
		Long: "validate hydrates a disposable Postgres from the fixture, applies the\n" +
			"migration set through your own runner, captures what happened (locks,\n" +
			"durations, rows, constraint violations, index builds), and returns a\n" +
			"verdict. Against a provided live branch (--target) the facts are ground\n" +
			"truth. validate never touches the fixture's source database.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				opts.fixturePath = args[0]
			}
			return runValidate(cmd.Context(), opts)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&opts.migrations, "migrations", "m", opts.migrations, "migration .sql file or directory")
	f.StringVar(&opts.target, "target", "", "validate against this live database URL (its data is ground truth)")
	f.StringVar(&opts.ephemeral, "ephemeral", "", "admin URL: create a disposable database, hydrate into it, then drop it")
	f.StringVar(&opts.runnerKind, "runner", "", "override runner detection (rawsql; alembic|prisma|drizzle are DETECTED but cannot be validated yet)")
	f.BoolVar(&opts.asJSON, "json", false, "emit the machine-readable verdict as JSON")
	f.BoolVar(&opts.warnFail, "warn-fail", false, "exit non-zero on a WARN-only verdict")
	f.BoolVar(&opts.calibrate, "calibrate", false, "hydrate at two scales and fit the cost curve, upgrading duration estimates to measured (slower)")
	f.Int64Var(&opts.seed, "seed", 0, "deterministic hydration seed")
	f.Float64Var(&opts.scale, "scale", opts.scale, "fraction of declared rows to hydrate")
	f.Int64Var(&opts.maxRows, "max-rows", 0, "cap hydrated rows per table (0 = no cap)")
	f.BoolVar(&opts.iKnowTarget, "i-know-target-is-writable", false,
		"proceed with --target when the fixture records no source host to check it against")
	f.DurationVar(&opts.stmtTimeout, "statement-timeout", opts.stmtTimeout,
		"cancel a migration statement that runs longer than this (0 = no ceiling); a cancelled statement is reported, never certified")
	return cmd
}

func runValidate(ctx context.Context, opts *validateOptions) error {
	data, err := os.ReadFile(opts.fixturePath)
	if err != nil {
		return emitToolError(opts.asJSON, toolerror.New(toolerror.FixtureParse, fmt.Sprintf("reading %s failed: %v", opts.fixturePath, err), "check the fixture path"))
	}
	f, err := fixture.ParseVerified(data)
	if err != nil {
		return emitToolError(opts.asJSON, fixtureParseError(err))
	}

	// --target and --ephemeral name different databases with very different
	// consequences: --target is a LIVE database this command will write to,
	// --ephemeral is a disposable one. Giving both used to silently prefer
	// --target, so an ambiguous request resolved toward the destructive option
	// with no diagnostic. Refuse instead of guessing.
	if opts.target != "" && opts.ephemeral != "" {
		return emitToolError(opts.asJSON, toolerror.New(
			toolerror.BadUsage,
			"--target and --ephemeral are mutually exclusive",
			"pass exactly one: --target writes to a live database, --ephemeral uses a disposable one",
		))
	}

	// Refuse an unsupported migration set BEFORE standing up a target.
	//
	// Detection recognizes Alembic, Prisma and Drizzle projects and --runner
	// accepts all four by name, but capture only supports raw SQL. That refusal
	// used to happen inside applyAndCapture — i.e. AFTER a disposable database
	// had been created, hydrated and (on the ephemeral path) was about to be
	// dropped again. The user paid for a container and a full hydrate to be told
	// the project is unsupported. Nothing about the check needs a database.
	if err := checkMigrationsSupported(opts); err != nil {
		return emitToolError(opts.asJSON, asToolError(err))
	}

	// Refuse a migration whose outcome the SANDBOX cannot represent, rather than
	// running it and reporting a verdict about rowshape's own limitations.
	if err := checkSandboxCanRepresent(f, opts); err != nil {
		return emitToolError(opts.asJSON, asToolError(err))
	}

	// --calibrate fits a cost curve through two runs at DIFFERENT scales. With
	// --max-rows set, both clamp to the same row count, the second point is
	// identical to the first, and estimateFor's `rows2 != rows1` test silently
	// drops back to a single-point estimate — so the user pays for a second full
	// hydrate and gets nothing, with no diagnostic. Refused HERE, before any
	// hydration: a flag combination that cannot work should not cost two runs to
	// discover.
	if opts.calibrate && opts.maxRows > 0 {
		return emitToolError(opts.asJSON, toolerror.New(
			toolerror.BadUsage,
			"--calibrate cannot be combined with --max-rows",
			"calibration fits a cost curve through two runs at DIFFERENT scales, but --max-rows clamps "+
				"both to the same row count, so the second run adds no information. Drop --max-rows for "+
				"the calibrated run, or drop --calibrate.",
		))
	}

	// Resolve the target and enforce the host-match refusal BEFORE touching it.
	groundTruth := opts.target != ""
	adminOrTarget := opts.target
	if adminOrTarget == "" {
		adminOrTarget = opts.ephemeral
	}
	if adminOrTarget == "" {
		return emitToolError(opts.asJSON, toolerror.New(toolerror.BadUsage, "no target given", "provide a disposable target (--ephemeral <admin-url>) or a live target (--target <url>)"))
	}
	host := hostOf(adminOrTarget)
	if host != "" {
		if err := validate.CheckHost(f.Meta.Source, host); err != nil {
			return emitToolError(opts.asJSON, toolerror.New(toolerror.BadUsage, err.Error(), "point --target/--ephemeral at a disposable or non-production host"))
		}
	}
	// --target is the one path that WRITES: it opens a transaction, executes the
	// migration DDL and COMMITS. Its guard must not switch itself off just because
	// the fixture happens to carry no source to compare against.
	if groundTruth {
		if err := validate.CheckWriteTarget(f.Meta.Source, host, opts.iKnowTarget); err != nil {
			return emitToolError(opts.asJSON, toolerror.New(
				toolerror.BadUsage,
				err.Error(),
				"--target APPLIES AND COMMITS the migration to that database. Re-run `rowshape pull` so the "+
					"fixture records a source host to check against, use --ephemeral for a disposable target, "+
					"or pass --i-know-target-is-writable if you are certain this is not production",
			))
		}
	}
	// Warn on a plaintext connection to a remote host. Never to stdout: --json
	// puts the machine-readable Verdict there and a stray line would corrupt it.
	if cfg, err := pgx.ParseConfig(adminOrTarget); err == nil {
		if w := dsn.InsecureWarning(cfg); w != "" {
			fmt.Fprintf(os.Stderr, "rowshape validate: warning: %s\n", w)
		}
	}

	// Prepare the target and capture. A provided live branch is used as-is
	// (ground truth); a disposable database is hydrated from the fixture.
	var cap *validate.Capture
	if groundTruth {
		if opts.calibrate {
			return emitToolError(opts.asJSON, toolerror.New(toolerror.BadUsage, "--calibrate applies only to a disposable target", "a provided branch's data is already ground truth"))
		}
		cap, err = applyAndCapture(ctx, target.NewProvided(opts.target), opts)
		if err != nil {
			return emitToolError(opts.asJSON, asToolError(err))
		}
	} else {
		cap, err = hydrateApplyEphemeral(ctx, f, opts, opts.scale)
		if err != nil {
			return emitToolError(opts.asJSON, asToolError(err))
		}
		// --calibrate fits the cost curve to a SECOND run at half scale, upgrading
		// duration estimates from `estimated` to `measured` (RFC §9.2). Slower and
		// honest — the option for the one migration you're genuinely nervous about.
		if opts.calibrate {
			cap2, err := hydrateApplyEphemeral(ctx, f, opts, opts.scale/2)
			if err != nil {
				return emitToolError(opts.asJSON, asToolError(err))
			}
			cap.Calibration = &validate.Calibration{
				TableRows:    cap2.TableRows,
				StatementMs2: statementDurations(cap2),
			}
		}
	}

	result := validate.BuildResult(f, cap, validate.Registered(), groundTruth)
	if err := result.Validate(); err != nil {
		return emitToolError(opts.asJSON, toolerror.New(toolerror.Internal, "produced verdict is malformed: "+err.Error(), ""))
	}
	if fs := cap.FailedStatement(); fs != nil {
		fmt.Fprintf(os.Stderr, "rowshape validate: migration did not apply cleanly: %s (%s)\n", fs.ErrMsg, fs.ErrCode)
	}

	if err := emitResult(os.Stdout, result, opts.asJSON); err != nil {
		return emitToolError(opts.asJSON, toolerror.New(toolerror.Internal, "emitting the verdict failed: "+err.Error(), ""))
	}
	if code := result.ExitCode(opts.warnFail); code != verdict.ExitPass {
		return &ExitError{Code: code}
	}
	return nil
}

// hydrateApplyEphemeral creates a disposable database, hydrates the fixture at
// the given scale, applies the migration, and returns the capture (with hydrated
// row counts). The database is torn down before returning.
func hydrateApplyEphemeral(ctx context.Context, f *fixture.Fixture, opts *validateOptions, scale float64) (*validate.Capture, error) {
	eph, err := target.NewEphemeral(ctx, opts.ephemeral)
	if err != nil {
		return nil, toolerror.New(toolerror.TargetUnavailable, "could not create a disposable database", "check the admin connection (--ephemeral); a disposable Postgres must be reachable (PRD §17.2)")
	}
	defer func() {
		// Detached from ctx: on Ctrl-C the run's context is already cancelled,
		// and Close opens a NEW connection to issue DROP DATABASE. Reusing ctx
		// there orphaned the disposable database on the admin server.
		tctx, tcancel := target.TeardownContext(ctx)
		defer tcancel()
		warnTeardown("validate", eph.Close(tctx))
	}()

	report, err := target.Load(ctx, eph, f, hydrate.Options{Seed: opts.seed, Scale: scale, MaxRows: opts.maxRows})
	if err != nil {
		return nil, toolerror.New(toolerror.TargetUnavailable,
			redactedTargetError("hydration into the disposable database failed", err),
			"check the admin connection (--ephemeral) and that the fixture hydrates cleanly; set ROWSHAPE_DEBUG=1 for the underlying error")
	}
	// Surfaced before the verdict: a missing UNIQUE index means the disposable
	// database enforces less than production does, which is exactly the direction that
	// turns a real FAIL into a PASS.
	warnSkippedIndexes("validate", report.SkippedIndexes)
	warnSkippedConstraints("validate", report.SkippedConstraints)
	warnUnreproducibleGenerated("validate", report.UnreproducibleGenerated)
	warnUnreproducedPartitions("validate", report.UnreproducedPartitionCount)
	warnUnreproducibleDefaults("validate", report.UnreproducibleDefaults)

	cap, err := applyAndCapture(ctx, eph, opts)
	if err != nil {
		return nil, err
	}
	cap.TableRows = report.Tables
	return cap, nil
}

// statementDurations extracts the per-statement wall times of a capture, aligned
// by index with the primary run's statements (both apply the same migration).
func statementDurations(c *validate.Capture) []int64 {
	ms := make([]int64, len(c.Statements))
	for i, st := range c.Statements {
		ms[i] = st.DurationMs
	}
	return ms
}

// checkSandboxCanRepresent refuses a migration that exercises a schema feature
// hydrate does not reproduce.
//
// internal/target/ddl.go emits column types and NOT NULL only: no CHECK
// constraints, no foreign keys, and no PARTITION BY. For partitioning that is
// not merely lossy, it is verdict-CORRUPTING in both directions. An
// ATTACH/DETACH PARTITION statement errors against the hydrated plain table,
// Capture.Success goes false, and BuildResult floors the whole verdict to FAIL —
// a manufactured FAIL for a migration that is fine. (The opposite also happens:
// ADD COLUMN on a real 400-partition parent recurses across every partition
// under ACCESS EXCLUSIVE, where hydrated it is one instant catalog write.)
//
// Refusing is the honest answer while the larger fidelity question is open: a
// tool error says "I cannot decide this", which is true, where a FAIL asserts
// something about the migration that is not.
//
// Deliberately narrow. It fires only when the migration actually references
// partitioning AND the fixture declares the table partitioned — so an ordinary
// migration against an ordinary table is untouched, and a partitioned table that
// the migration does not touch structurally still validates normally.
func checkSandboxCanRepresent(f *fixture.Fixture, opts *validateOptions) error {
	// Ground truth runs against a real database that HAS the real schema, so the
	// sandbox's limitations do not apply.
	if opts.target != "" {
		return nil
	}
	stmts, err := migrationStatementsFor(opts)
	if err != nil {
		return nil // unreadable migrations surface elsewhere, with a better message
	}
	for _, sql := range stmts {
		up := strings.ToUpper(collapseWS(sql))
		if !strings.Contains(up, "ATTACH PARTITION") && !strings.Contains(up, "DETACH PARTITION") {
			continue
		}
		return toolerror.New(
			toolerror.BadUsage,
			"this migration attaches or detaches a partition, and rowshape's disposable target does not "+
				"reproduce partitioning",
			"hydrate creates plain tables (no PARTITION BY), so the statement would fail against the "+
				"sandbox and rowshape would report a FAIL about its own limitation rather than about your "+
				"migration. Use --target against a database that has the real schema. The lock behaviour is "+
				"still reported statically as RS-LOCK-003.",
		)
	}
	return nil
}

// collapseWS reduces whitespace runs to single spaces for keyword matching.
func collapseWS(s string) string { return strings.Join(strings.Fields(s), " ") }

// migrationStatementsFor reads the migration set as raw SQL statements, or
// returns an error if it cannot.
func migrationStatementsFor(opts *validateOptions) ([]string, error) {
	if isSQLFile(opts.migrations) {
		located, err := readSQLFile(opts.migrations)
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, len(located))
		for _, l := range located {
			out = append(out, l.SQL)
		}
		return out, nil
	}
	r, err := detectValidateRunner(opts)
	if err != nil {
		return nil, err
	}
	raw, ok := r.(interface {
		Files() []string
		Dir() string
	})
	if !ok {
		return nil, fmt.Errorf("not a raw-SQL runner")
	}
	var out []string
	for _, name := range raw.Files() {
		located, err := readSQLFile(filepath.Join(raw.Dir(), name))
		if err != nil {
			return nil, err
		}
		for _, l := range located {
			out = append(out, l.SQL)
		}
	}
	return out, nil
}

// checkMigrationsSupported reports whether the migration set can be captured at
// all, without touching a database.
//
// It is deliberately separate from applyAndCapture rather than being called by
// it: the point is that this answer is available from the filesystem alone, so
// it belongs before the expensive part of the run.
func checkMigrationsSupported(opts *validateOptions) error {
	if isSQLFile(opts.migrations) {
		return nil
	}
	r, err := detectValidateRunner(opts)
	if err != nil {
		return toolerror.New(toolerror.RunnerNotFound, err.Error(),
			"select a runner with --runner, or point --migrations at a raw-SQL file/directory")
	}
	if _, ok := r.(interface {
		Files() []string
		Dir() string
	}); ok && r.Kind() == runner.RawSQL {
		return nil
	}
	return toolerror.New(
		toolerror.RunnerNotFound,
		fmt.Sprintf("this looks like a %s project, and capturing %s migrations is not yet supported", r.Kind(), r.Kind()),
		"point --migrations at a raw-SQL file or directory, or use --runner rawsql if the project also has plain .sql migrations",
	)
}

// applyAndCapture applies the migration set to the target and captures the six
// signal classes. A raw-SQL migration (a .sql file, or a directory the raw-SQL
// runner recognizes) is executed statement-by-statement over a connection for
// full per-statement capture — the primary path and the corpus format. A
// detected framework runner is reported as not-yet-captured: its per-statement
// introspection lands with the framework finding rules.
func applyAndCapture(ctx context.Context, t target.Target, opts *validateOptions) (*validate.Capture, error) {
	if isSQLFile(opts.migrations) {
		stmts, err := readSQLFile(opts.migrations)
		if err != nil {
			return nil, toolerror.New(toolerror.BadUsage, err.Error(), "check the --migrations path")
		}
		return applyStatements(ctx, t, stmts, opts.stmtTimeout)
	}

	r, err := detectValidateRunner(opts)
	if err != nil {
		return nil, toolerror.New(toolerror.RunnerNotFound, err.Error(), "select a runner with --runner, or point --migrations at a raw-SQL file/directory")
	}
	raw, ok := r.(interface {
		Files() []string
		Dir() string
	})
	if r.Kind() != runner.RawSQL || !ok {
		// Detection recognizes Alembic/Prisma/Drizzle projects, but capture only
		// supports raw SQL — so those projects get here after a target has
		// already been stood up. Say plainly that the project was recognized and
		// which part is unsupported, rather than implying no runner was found.
		return nil, toolerror.New(
			toolerror.RunnerNotFound,
			fmt.Sprintf("this looks like a %s project, and capturing %s migrations is not yet supported", r.Kind(), r.Kind()),
			"point --migrations at a raw-SQL file or directory, or use --runner rawsql if the project also has plain .sql migrations",
		)
	}
	// NOTE: deliberately NO runner.EnsureAvailable here. This path does not shell
	// out — it reads the .sql files and applies them over the existing pgx
	// connection (see applyStatements) — so requiring psql on PATH would break
	// raw-SQL validation on machines that legitimately do not have it. The
	// pre-flight belongs at the ApplyCmd call site, whenever one exists.
	var stmts []validate.Located
	// Resolve against the runner's own directory, not opts.migrations: detection
	// descends into migrations/, db/migrations/, … so `--migrations .` with files
	// in ./migrations/ has Files() relative to ./migrations, and joining them to
	// "." yields ./001.sql — a path that does not exist. Detection succeeded, so
	// the failure surfaced as an unreadable file rather than a missing runner.
	for _, name := range raw.Files() {
		s, err := readSQLFile(filepath.Join(raw.Dir(), name))
		if err != nil {
			return nil, toolerror.New(toolerror.BadUsage, err.Error(), "check the --migrations path")
		}
		stmts = append(stmts, s...)
	}
	return applyStatements(ctx, t, stmts, opts.stmtTimeout)
}

// applyStatements connects to the target and captures each statement.
func applyStatements(ctx context.Context, t target.Target, stmts []validate.Located, stmtTimeout time.Duration) (*validate.Capture, error) {
	conn, err := t.Connect(ctx)
	if err != nil {
		return nil, toolerror.New(toolerror.ConnectFailed, "could not connect to the target", "check the target is reachable and the credentials are valid")
	}
	defer func() { _ = conn.Close(ctx) }()
	return validate.ApplyWithTimeout(ctx, conn, stmts, stmtTimeout), nil
}

func detectValidateRunner(opts *validateOptions) (runner.Runner, error) {
	if opts.runnerKind != "" {
		return runner.ForKind(opts.migrations, runner.Kind(opts.runnerKind))
	}
	return runner.Detect(opts.migrations)
}

// emitToolError renders an operational failure as exit code 3 — clearly distinct
// from a verdict (PRD §10, INV-VERDICT-STABLE). --json writes the machine-readable
// payload to stdout (so an agent branches on `"error":"tool_error"` where a
// verdict has `"verdict"`); otherwise the same struct is rendered for a human on
// stderr. It never emits a PASS/FAIL/WARN.
func emitToolError(asJSON bool, te *toolerror.ToolError) error {
	if asJSON {
		_ = te.WriteJSON(os.Stdout)
	} else {
		te.WriteHuman(os.Stderr)
	}
	return &ExitError{Code: te.ExitCode()}
}

// warnTeardown reports a disposable-database teardown failure without changing
// the outcome of the command.
//
// CR-T19: these two sites discarded the error entirely (`_ = eph.Close(ctx)`),
// so a failed cleanup produced no diagnostic anywhere and orphan databases could
// accumulate across CI runs with nothing pointing at the cause. This is
// deliberately DIFFERENT from the ~14 reviewed `_ =` sites recorded under P0-T6:
// those are safe-by-inspection deferred closes on read paths, where there is
// genuinely nothing to report. Here the discarded value is the outcome of
// releasing a resource.
//
// It warns and does not fail. A teardown failure is not a verdict
// (INV-VERDICT-STABLE), and turning a clean PASS into an error because cleanup
// stumbled would make the tool wrong about the migration, which is the one thing
// it must not be. The message carries no connection details (PRD §5, and
// consistent with CR-T7).
func warnTeardown(cmdName string, err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "rowshape %s: warning: could not drop the disposable database; "+
		"it may need removing by hand (set ROWSHAPE_DEBUG=1 for the underlying error)\n", cmdName)
	if os.Getenv("ROWSHAPE_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "rowshape %s: teardown error: %v\n", cmdName, err)
	}
}

// redactedTargetError describes a failure that touched the target WITHOUT
// echoing the error, because pgx embeds the connection in its message: a real
// one reads "failed to connect to `user=admin database=appdb`: hostname
// resolving error: lookup prod-db.internal.example.com". Host, port, username
// and database name all reach the user's terminal, CI log, and --json output.
// PRD §5 says connection details are never logged or persisted, and every other
// connect-failure path in this tree already substitutes a fixed string — these
// were the outliers.
//
// The detail is gated, not discarded: ROWSHAPE_DEBUG=1 prints it. That is an env
// var rather than a flag on purpose. The CLI surface is part of the public
// contract (INV-VERDICT-STABLE) and a debugging aid does not belong in it, while
// an env var is available exactly when someone is debugging and never appears in
// a verdict.
func redactedTargetError(what string, err error) string {
	if os.Getenv("ROWSHAPE_DEBUG") != "" {
		return what + ": " + err.Error()
	}
	return what
}

// asToolError coerces an error to a *toolerror.ToolError: those returned by the
// capture helpers pass through with their category; anything else is an Internal
// tool error (still exit 3, never a verdict).
func asToolError(err error) *toolerror.ToolError {
	var te *toolerror.ToolError
	if errors.As(err, &te) {
		return te
	}
	return toolerror.New(toolerror.Internal, err.Error(), "")
}

// fixtureParseError maps a fixture parse failure to a category: an unknown format
// major is a distinct refusal (RFC §12), never a partial-understanding verdict.
func fixtureParseError(err error) *toolerror.ToolError {
	var ve *fixture.VersionError
	if errors.As(err, &ve) {
		return toolerror.New(toolerror.UnknownVersion, ve.Error(), "this build understands fixture format version \""+fixture.FormatVersion+"\"")
	}
	return toolerror.New(toolerror.FixtureParse, err.Error(), "the fixture is not valid rowshape.yaml")
}

// emitResult writes the verdict: JSON (the machine contract, PRD §10) or the
// human rendering of the SAME struct (INV-VERDICT-SHAPE).
func emitResult(w io.Writer, r verdict.Result, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	r.WriteHuman(w)
	return nil
}

// hostOf extracts the host from a Postgres connection string, or "" if it cannot
// be parsed (in which case there is no host to compare and the caller proceeds).
func hostOf(dsn string) string {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return ""
	}
	return cfg.Host
}

// isSQLFile matches the extension case-insensitively, as every other .sql check
// does (plan.go, mcp/tool_validate_migration.go, runner/rawsql.go). On the
// case-insensitive filesystems that Windows and macOS default to, `001_init.SQL`
// is an ordinary file: matching it exactly made `validate -m 001_init.SQL` skip
// the raw-SQL path and fail with RunnerNotFound, while `plan` on the same file
// worked and a DIRECTORY containing it validated fine — since rawsql.go's own
// scan already folds case.
func isSQLFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && strings.EqualFold(filepath.Ext(path), ".sql")
}

// readSQLFile reads a .sql file and splits it into statements.
func readSQLFile(path string) ([]validate.Located, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return validate.SplitStatementsIn(path, string(b)), nil
}
