package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/hydrate"
	"github.com/rowshape/rowshape/internal/target"
	"github.com/rowshape/rowshape/internal/validate"
	"github.com/spf13/cobra"
)

// hydrateOptions holds the flags for `rowshape hydrate`.
type hydrateOptions struct {
	fixturePath string
	out         string
	target      string
	ephemeral   string
	seed        int64
	scale       float64
	maxRows     int64
}

// newHydrateCmd deterministically reconstructs a disposable database from a
// fixture (RFC §10, §13). In phase 1 this is the synthesis engine (P1-T7): it
// reads a fixture and emits deterministic INSERT SQL. Wiring the SQL into a
// disposable Postgres target lands in P1-T9.
func newHydrateCmd() *cobra.Command {
	opts := &hydrateOptions{fixturePath: "rowshape.yaml", out: "-", scale: 1.0}
	cmd := &cobra.Command{
		Use:   "hydrate [rowshape.yaml]",
		Short: "Reconstruct a deterministic disposable database from rowshape.yaml",
		Long: "hydrate synthesizes rows whose SHAPE matches production — row counts,\n" +
			"null fractions, cardinality, fan-out — with obviously-fake content. The\n" +
			"same fixture, seed, and engine version produce identical output on any\n" +
			"platform. In phase 1 it emits INSERT SQL; --seed makes it reproducible.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				opts.fixturePath = args[0]
			}
			return runHydrate(cmd.Context(), opts)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&opts.out, "out", "o", opts.out, "output path for the SQL ('-' for stdout)")
	f.StringVar(&opts.target, "target", "", "hydrate into this database URL instead of emitting SQL")
	f.StringVar(&opts.ephemeral, "ephemeral", "", "admin URL: create a disposable database, hydrate into it, then drop it")
	f.Int64Var(&opts.seed, "seed", 0, "deterministic seed")
	f.Float64Var(&opts.scale, "scale", opts.scale, "fraction of declared rows to synthesize")
	f.Int64Var(&opts.maxRows, "max-rows", 0, "cap synthesized rows per table (0 = no cap)")
	return cmd
}

func runHydrate(ctx context.Context, opts *hydrateOptions) error {
	// Same refusal as validate: --target is a LIVE database this writes into and
	// --ephemeral is a disposable one, so resolving the conflict silently would
	// let the destructive option win an ambiguity the user never saw. validate
	// got this guard first; hydrate writes MORE than validate does, so leaving
	// it out here was the more dangerous half to miss.
	if opts.target != "" && opts.ephemeral != "" {
		fmt.Fprintln(os.Stderr, "rowshape hydrate: --target and --ephemeral are mutually exclusive")
		fmt.Fprintln(os.Stderr, "rowshape hydrate: pass exactly one: --target writes to a live database, --ephemeral uses a disposable one")
		return toolError()
	}
	data, err := os.ReadFile(opts.fixturePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rowshape hydrate: reading %s failed: %v\n", opts.fixturePath, err)
		return toolError()
	}
	f, err := fixture.ParseVerified(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rowshape hydrate: %v\n", err)
		return toolError()
	}

	genOpts := hydrate.Options{Seed: opts.seed, Scale: opts.scale, MaxRows: opts.maxRows}

	// If a live target is requested, load rows into it instead of emitting SQL.
	if opts.target != "" || opts.ephemeral != "" {
		return loadIntoTarget(ctx, f, genOpts, opts)
	}

	res, err := hydrate.Generate(f, genOpts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rowshape hydrate: %v\n", err)
		return toolError()
	}

	if opts.out == "-" {
		if err := hydrate.WriteSQL(os.Stdout, res); err != nil {
			fmt.Fprintf(os.Stderr, "rowshape hydrate: writing SQL failed: %v\n", err)
			return toolError()
		}
		return nil
	}

	file, err := os.Create(opts.out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rowshape hydrate: creating %s failed: %v\n", opts.out, err)
		return toolError()
	}
	if err := hydrate.WriteSQL(file, res); err != nil {
		_ = file.Close()
		fmt.Fprintf(os.Stderr, "rowshape hydrate: writing SQL failed: %v\n", err)
		return toolError()
	}
	// Close is checked, not deferred. This is a write path: a Close error is a
	// failed flush — a full disk, a network filesystem — which means the SQL on
	// disk is truncated. Deferring it would drop that error and report success
	// over a corrupt file, and hydrate's entire promise is that its output is
	// byte-reproducible for a given fixture and seed.
	if err := file.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "rowshape hydrate: closing %s failed (the file may be incomplete): %v\n", opts.out, err)
		return toolError()
	}
	return nil
}

// hydrateTargetHost returns the host hydrate is about to write to: the live
// --target when one is given, otherwise the --ephemeral admin server (the
// disposable database is created ON that server, so it is the host at risk).
func hydrateTargetHost(opts *hydrateOptions) string {
	dsn := opts.target
	if dsn == "" {
		dsn = opts.ephemeral
	}
	return hostOf(dsn)
}

// checkHydrateHost enforces the host-match refusal for hydrate
// (INV-BLAST-RADIUS-ZERO, PRD §11).
//
// `validate` has always refused a target on the fixture's source host. hydrate
// does the SAME CLASS OF WRITE — CREATE SCHEMA, CREATE TABLE, COPY — and had no
// host check at all, so `hydrate --target` walked straight past a refusal that
// `validate --target` enforces on the identical URL. The two commands share flag
// names and habits, which is exactly what makes pointing the wrong one at
// production an easy mistake rather than an exotic one.
//
// The decision is delegated to validate.CheckHost rather than reimplemented: it
// already hashes the target under every spelling that is definitionally the same
// machine (verbatim, DNS-normalized, and all loopback aliases). A second
// implementation here would be a second thing to get wrong, and the single-hash
// compare it replaced was a proven bypass.
func checkHydrateHost(f *fixture.Fixture, opts *hydrateOptions) error {
	host := hydrateTargetHost(opts)
	if host == "" {
		return nil
	}
	return validate.CheckHost(f.Meta.Source, host)
}

// loadIntoTarget hydrates directly into a live database: a user-provided one
// (--target) or a disposable ephemeral database created and dropped for the run
// (--ephemeral). Connection strings and credentials are never logged.
func loadIntoTarget(ctx context.Context, f *fixture.Fixture, genOpts hydrate.Options, opts *hydrateOptions) error {

	// Refuse BEFORE anything is created or connected. This has to precede
	// target.NewEphemeral, which itself issues a CREATE DATABASE on the admin
	// server — by the time that returns, the write has already happened.
	if err := checkHydrateHost(f, opts); err != nil {
		fmt.Fprintln(os.Stderr, "rowshape hydrate: refusing to hydrate into the fixture's source host — hydrate writes to its target (CREATE SCHEMA/TABLE + COPY) and must only ever touch a disposable database, never production")
		fmt.Fprintln(os.Stderr, "point --target/--ephemeral at a disposable or non-production host")
		return toolError()
	}

	var t target.Target
	if opts.target != "" {
		t = target.NewProvided(opts.target)
	} else {
		eph, err := target.NewEphemeral(ctx, opts.ephemeral)
		if err != nil {
			fmt.Fprintln(os.Stderr, "rowshape hydrate: could not create a disposable database (check the admin connection)")
			return toolError()
		}
		// A disposable target is always torn down, even on failure.
		// A disposable target is always torn down, and a failure to do so is
		// reported rather than discarded (CR-T19) — without changing the exit code.
		defer func() {
			// Detached from ctx — see the note in validate.go: cancellation must
			// stop the work and still permit the cleanup.
			tctx, tcancel := target.TeardownContext(ctx)
			defer tcancel()
			warnTeardown("hydrate", eph.Close(tctx))
		}()
		t = eph
	}

	report, err := target.Load(ctx, t, f, genOpts)
	if err != nil {
		// Not %v: the wrapped pgx error carries host, port, username and database
		// name (PRD §5). Proven live — the mutation run for CR-T1 printed
		// "failed to connect to `user=admin database=appdb`" from this very line.
		fmt.Fprintln(os.Stderr, "rowshape hydrate: "+
			redactedTargetError("load failed (check the target is reachable and the fixture hydrates cleanly; set ROWSHAPE_DEBUG=1 for the underlying error)", err))
		return toolError()
	}
	var total int64
	for _, n := range report.Tables {
		total += n
	}
	fmt.Fprintf(os.Stderr, "rowshape hydrate: loaded %d rows across %d tables\n", total, len(report.Tables))
	return nil
}
