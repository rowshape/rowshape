package cmd

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/profile"
	"github.com/rowshape/rowshape/internal/verdict"
)

// CR-T1. cmd/hydrate.go had no test file of any kind, and no host check:
// `validate` refused a target on the fixture's source host while `hydrate`,
// which issues the same class of write (CREATE SCHEMA/TABLE + COPY), wrote to it
// without a word. These tests pin the refusal (INV-BLAST-RADIUS-ZERO, PRD §11).

const hydrateProdHost = "prod-db.internal.example.com"

// writeHydrateFixture writes a minimal fixture whose meta.source is the salted
// hash of host, i.e. a fixture "pulled from" that machine (RFC §8.4).
func writeHydrateFixture(t *testing.T, dir, host string) string {
	t.Helper()
	path := filepath.Join(dir, "rowshape.yaml")
	writeFile(t, path, `rowshape_fixture: "1"
meta:
  id: t
  engine: {name: postgres, version: "16"}
  source: `+profile.HashSource(host)+`
tables:
  public.users:
    rows: {value: 10, confidence: exact}
    columns:
      email: {type: text, nullable: true}
`)
	return path
}

// TestHydrateRefusesSourceHostTarget: `hydrate --target` pointed at the host the
// fixture came from must refuse. This is the blocker CR-T1 was filed for — the
// refusal did not exist, so this ran a COPY into production instead.
func TestHydrateRefusesSourceHostTarget(t *testing.T) {
	fx := writeHydrateFixture(t, t.TempDir(), hydrateProdHost)

	opts := &hydrateOptions{
		fixturePath: fx,
		target:      "postgres://admin:secret@" + hydrateProdHost + ":5432/appdb",
		scale:       1.0,
	}
	var runErr error
	_, stderr := captureOutput(t, func() error { runErr = runHydrate(context.Background(), opts); return runErr })

	if !strings.Contains(stderr, "refusing to hydrate into the fixture's source host") {
		t.Errorf("expected a host-match refusal on stderr, got:\n%s", stderr)
	}
	assertExitCode(t, runErr, verdict.ExitToolError)
	// The refusal must be the ONLY thing that happened: if it had connected and
	// failed, stderr would carry a load error instead.
	if strings.Contains(stderr, "load failed") {
		t.Errorf("hydrate reached the target before refusing:\n%s", stderr)
	}
	// PRD §5: the refusal must not echo connection details.
	assertNoConnectionDetails(t, stderr)
}

// TestHydrateRefusesSourceHostEphemeral: the --ephemeral path is the more
// dangerous of the two, because target.NewEphemeral issues a CREATE DATABASE on
// the admin server — once it returns, the write to production has happened. The
// refusal therefore has to precede it, not merely precede the COPY.
func TestHydrateRefusesSourceHostEphemeral(t *testing.T) {
	fx := writeHydrateFixture(t, t.TempDir(), hydrateProdHost)

	opts := &hydrateOptions{
		fixturePath: fx,
		ephemeral:   "postgres://admin:secret@" + hydrateProdHost + ":5432/postgres",
		scale:       1.0,
	}
	var runErr error
	_, stderr := captureOutput(t, func() error { runErr = runHydrate(context.Background(), opts); return runErr })

	if !strings.Contains(stderr, "refusing to hydrate into the fixture's source host") {
		t.Errorf("expected a host-match refusal on stderr, got:\n%s", stderr)
	}
	assertExitCode(t, runErr, verdict.ExitToolError)
	if strings.Contains(stderr, "could not create a disposable database") {
		t.Errorf("hydrate tried to CREATE DATABASE before refusing:\n%s", stderr)
	}
	assertNoConnectionDetails(t, stderr)
}

// TestHydrateAllowsNonSourceHost: the refusal must not be so broad that hydrate
// cannot do its job. A different host is allowed through — it then fails at
// CONNECT (127.0.0.1:1 refuses instantly), which is the proof it got past the
// check rather than being stopped by it.
func TestHydrateAllowsNonSourceHost(t *testing.T) {
	fx := writeHydrateFixture(t, t.TempDir(), hydrateProdHost)

	opts := &hydrateOptions{
		fixturePath: fx,
		target:      "postgres://u:p@127.0.0.1:1/db",
		scale:       1.0,
	}
	var runErr error
	_, stderr := captureOutput(t, func() error { runErr = runHydrate(context.Background(), opts); return runErr })

	if strings.Contains(stderr, "refusing to hydrate") {
		t.Errorf("must NOT refuse a host the fixture did not come from:\n%s", stderr)
	}
	if runErr == nil {
		t.Error("expected a connect failure against a closed port")
	}
}

// TestHydrateHostRefusalEquivalence pins the decision predicate directly, across
// the spellings that are definitionally the same machine. A host is not a
// string: the single-hash compare this replaced was a proven bypass (a fixture
// pulled from `localhost`, hydrated against `127.0.0.1`, sailed past it).
//
// checkHydrateHost delegates to validate.CheckHost, so this asserts the wiring —
// that hydrate consults it with the right host — rather than re-testing the
// equivalence matrix that internal/validate already owns.
func TestHydrateHostRefusalEquivalence(t *testing.T) {
	cases := []struct {
		name       string
		sourceHost string // host the fixture was pulled from ("" = no source)
		opts       *hydrateOptions
		wantRefuse bool
	}{
		{"exact match via --target", hydrateProdHost,
			&hydrateOptions{target: "postgres://h@" + hydrateProdHost + "/d"}, true},
		{"exact match via --ephemeral", hydrateProdHost,
			&hydrateOptions{ephemeral: "postgres://h@" + hydrateProdHost + "/d"}, true},
		{"case differs (DNS is case-insensitive)", "DB.Internal",
			&hydrateOptions{target: "postgres://h@db.internal/d"}, true},
		{"trailing FQDN dot", "db.internal",
			&hydrateOptions{target: "postgres://h@db.internal./d"}, true},
		{"loopback alias: localhost source, IPv4 target", "localhost",
			&hydrateOptions{target: "postgres://h@127.0.0.1/d"}, true},
		{"loopback alias: IPv4 source, localhost target", "127.0.0.1",
			&hydrateOptions{target: "postgres://h@localhost/d"}, true},

		// Negative cases matter as much: a check that refuses everything stops
		// hydrate from doing its job at all.
		{"a genuinely different host is allowed", hydrateProdHost,
			&hydrateOptions{target: "postgres://h@staging.example.com/d"}, false},
		{"128.0.0.1 is not loopback and is allowed", "127.0.0.1",
			&hydrateOptions{target: "postgres://h@128.0.0.1/d"}, false},
		{"no source host means nothing to collide with", "",
			&hydrateOptions{target: "postgres://h@" + hydrateProdHost + "/d"}, false},
		{"no target at all (SQL emit path)", hydrateProdHost,
			&hydrateOptions{}, false},

		// --target wins over --ephemeral in loadIntoTarget, so the check must
		// look at the same one that will actually be written to.
		{"--target takes precedence over --ephemeral", hydrateProdHost,
			&hydrateOptions{
				target:    "postgres://h@" + hydrateProdHost + "/d",
				ephemeral: "postgres://h@staging.example.com/d",
			}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fixture.Fixture{Meta: fixture.Meta{Source: profile.HashSource(tc.sourceHost)}}
			err := checkHydrateHost(f, tc.opts)
			if tc.wantRefuse && err == nil {
				t.Error("expected a refusal, got none")
			}
			if !tc.wantRefuse && err != nil {
				t.Errorf("expected no refusal, got %v", err)
			}
		})
	}
}

// assertExitCode asserts the command exited with the given code. The exit code
// is part of the public contract (INV-VERDICT-STABLE), so a refusal that printed
// the right words while exiting 0 would still be a broken refusal.
func assertExitCode(t *testing.T, err error, want int) {
	t.Helper()
	var ee *ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("expected an ExitError, got %v", err)
	}
	if ee.Code != want {
		t.Errorf("exit code = %d, want %d", ee.Code, want)
	}
}

// assertNoConnectionDetails guards PRD §5: host, port and username never reach
// user-facing output.
func assertNoConnectionDetails(t *testing.T, out string) {
	t.Helper()
	for _, secret := range []string{"secret", "admin:", ":5432"} {
		if strings.Contains(out, secret) {
			t.Errorf("output leaked a connection detail (%q):\n%s", secret, out)
		}
	}
}

// --- CR-T7: connection details must never reach user-facing output ----------
//
// pgx embeds the connection in its error text, so a raw `%v` on a target failure
// printed host, port, username and database name to the terminal, the CI log and
// --json. This was seen live rather than inferred: the CR-T1 mutation run
// printed "failed to connect to `user=admin database=appdb`: hostname resolving
// error: lookup prod-db.internal.example.com" straight from hydrate's load path.
//
// PRD §5 — connection details are never logged or persisted. Every other
// connect-failure path already substituted a fixed string; these were the
// outliers.

// leakDSN carries every detail that must not escape: username, password, a
// non-default port, and a database name. Port 15432 is closed, so the connect
// fails fast without needing a live server.
const leakDSN = "postgres://leakuser:leakpassword@127.0.0.1:15432/leakdb"

var leakTokens = []string{"leakuser", "leakpassword", "15432", "leakdb"}

func assertNoLeak(t *testing.T, label, out string) {
	t.Helper()
	for _, tok := range leakTokens {
		if strings.Contains(out, tok) {
			t.Errorf("%s leaked %q (PRD §5):\n%s", label, tok, out)
		}
	}
}

// TestHydrateTargetFailureHidesConnectionDetails drives the exact path that was
// proven to leak.
func TestHydrateTargetFailureHidesConnectionDetails(t *testing.T) {
	t.Setenv("ROWSHAPE_DEBUG", "")
	fx := writeHydrateFixture(t, t.TempDir(), hydrateProdHost)
	opts := &hydrateOptions{fixturePath: fx, target: leakDSN, scale: 1.0}

	var runErr error
	stdout, stderr := captureOutput(t, func() error { runErr = runHydrate(context.Background(), opts); return runErr })

	if runErr == nil {
		t.Fatal("expected a failure against a closed port")
	}
	assertNoLeak(t, "hydrate stderr", stderr)
	assertNoLeak(t, "hydrate stdout", stdout)
	if !strings.Contains(stderr, "load failed") {
		t.Errorf("the failure must still be reported, just without details:\n%s", stderr)
	}
}

// TestValidateTargetFailureHidesConnectionDetails covers the same class on the
// validate side, including --json (the machine contract is just as public as the
// terminal).
func TestValidateTargetFailureHidesConnectionDetails(t *testing.T) {
	t.Setenv("ROWSHAPE_DEBUG", "")
	dir := t.TempDir()
	fx := writeHydrateFixture(t, dir, hydrateProdHost)
	mig := filepath.Join(dir, "m.sql")
	writeFile(t, mig, "ALTER TABLE public.users ALTER COLUMN email SET NOT NULL;")

	for _, asJSON := range []bool{false, true} {
		opts := &validateOptions{fixturePath: fx, migrations: mig, ephemeral: leakDSN, scale: 1.0, asJSON: asJSON}
		stdout, stderr := captureOutput(t, func() error { return runValidate(context.Background(), opts) })
		assertNoLeak(t, fmt.Sprintf("validate stderr (json=%v)", asJSON), stderr)
		assertNoLeak(t, fmt.Sprintf("validate stdout (json=%v)", asJSON), stdout)
	}
}

// TestRedactedTargetErrorDebugOptIn: the detail is gated, not destroyed —
// otherwise the next person debugging a real connection problem is stuck.
func TestRedactedTargetErrorDebugOptIn(t *testing.T) {
	err := errors.New("failed to connect to `user=leakuser database=leakdb`")

	t.Run("hidden by default", func(t *testing.T) {
		t.Setenv("ROWSHAPE_DEBUG", "")
		got := redactedTargetError("load failed", err)
		if got != "load failed" {
			t.Errorf("got %q, want the bare message with no error detail", got)
		}
		assertNoLeak(t, "redacted", got)
	})

	t.Run("revealed under ROWSHAPE_DEBUG", func(t *testing.T) {
		t.Setenv("ROWSHAPE_DEBUG", "1")
		got := redactedTargetError("load failed", err)
		if !strings.Contains(got, "user=leakuser") {
			t.Errorf("ROWSHAPE_DEBUG must reveal the underlying error, got %q", got)
		}
	})
}

// --- CR-T19: teardown failures are reported, never silent -------------------
//
// Both commands discarded the teardown error (`_ = eph.Close(ctx)`), so a failed
// cleanup produced no diagnostic anywhere and orphan databases could accumulate
// across CI runs with nothing pointing at the cause. Deliberately different from
// the ~14 reviewed `_ =` sites under P0-T6: those are safe-by-inspection
// deferred closes on read paths; this one discards the outcome of releasing a
// resource.

func TestWarnTeardownReportsWithoutLeakingDetails(t *testing.T) {
	t.Setenv("ROWSHAPE_DEBUG", "")
	err := errors.New("failed to connect to `user=leakuser database=leakdb` on 127.0.0.1:15432")

	_, stderr := captureOutput(t, func() error {
		warnTeardown("validate", err)
		return nil
	})

	if !strings.Contains(stderr, "could not drop the disposable database") {
		t.Errorf("a teardown failure must be reported, got %q", stderr)
	}
	// PRD §5, consistent with CR-T7: the warning must not echo the connection.
	assertNoLeak(t, "teardown warning", stderr)
}

func TestWarnTeardownSilentOnSuccess(t *testing.T) {
	_, stderr := captureOutput(t, func() error {
		warnTeardown("validate", nil)
		return nil
	})
	if stderr != "" {
		t.Errorf("a successful teardown must say nothing, got %q", stderr)
	}
}

// TestWarnTeardownDebugOptIn: the underlying error is gated, not destroyed.
func TestWarnTeardownDebugOptIn(t *testing.T) {
	t.Setenv("ROWSHAPE_DEBUG", "1")
	_, stderr := captureOutput(t, func() error {
		warnTeardown("hydrate", errors.New("drop database failed: still in use"))
		return nil
	})
	if !strings.Contains(stderr, "still in use") {
		t.Errorf("ROWSHAPE_DEBUG must reveal the teardown error, got %q", stderr)
	}
}
