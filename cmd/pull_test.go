package cmd

import (
	"context"

	"github.com/jackc/pgx/v5"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/profile"
	"github.com/rowshape/rowshape/internal/verdict"
)

// pull is the one command that touches a production database and writes the
// committed fixture, and it had NO test file at all. Its privacy posture — never
// echo the DSN, record the source host only as a salted hash, refuse to run as a
// superuser — is a load-bearing security contract (PRD §5, RFC §8.4). These
// tests cover the offline failure paths unconditionally and the DB-backed
// invariants under ROWSHAPE_TEST_PG_DSN. docs/TESTING-GAPS.md item 5.

// runPullCapturing runs runPull and returns (exit code, stdout, stderr).
func runPullCapturing(t *testing.T, opts *pullOptions) (int, string, string) {
	t.Helper()
	var runErr error
	stdout, stderr := captureOutput(t, func() error {
		runErr = runPull(context.Background(), opts)
		return runErr
	})
	return exitCodeOf(runErr), stdout, stderr
}

// TestPullRejectsUnknownPrivacy: a bad --privacy value is a tool error (exit 3),
// caught before any connection is attempted.
func TestPullRejectsUnknownPrivacy(t *testing.T) {
	code, _, stderr := runPullCapturing(t, &pullOptions{
		dsn:     "postgres://localhost/db",
		privacy: "paranoid",
		out:     filepath.Join(t.TempDir(), "out.yaml"),
	})
	if code != verdict.ExitToolError {
		t.Errorf("exit code = %d, want %d (tool error)", code, verdict.ExitToolError)
	}
	if !strings.Contains(stderr, "unknown privacy level") {
		t.Errorf("stderr should name the bad privacy level, got:\n%s", stderr)
	}
}

// TestPullMalformedDSNDoesNotLeak: a DSN that fails to parse is a tool error, and
// the credential it carries must never reach the terminal (PRD §5).
func TestPullMalformedDSNDoesNotLeak(t *testing.T) {
	const secret = "leakpw_parse"
	code, stdout, stderr := runPullCapturing(t, &pullOptions{
		// An invalid port makes pgx.ParseConfig fail.
		dsn:     "postgres://u:" + secret + "@host:notaport/db",
		privacy: "standard",
		out:     filepath.Join(t.TempDir(), "out.yaml"),
	})
	if code != verdict.ExitToolError {
		t.Errorf("exit code = %d, want %d (tool error)", code, verdict.ExitToolError)
	}
	if !strings.Contains(stderr, "could not parse the connection settings") {
		t.Errorf("stderr should report a parse failure, got:\n%s", stderr)
	}
	if strings.Contains(stdout+stderr, secret) {
		t.Errorf("the DSN password %q leaked into the output:\nstdout:%s\nstderr:%s", secret, stdout, stderr)
	}
}

// TestPullConnectFailureDoesNotLeak: a well-formed but unreachable DSN is a tool
// error, and pgx's connect error (which embeds user/host/db) must be swallowed —
// only a fixed, credential-free message is shown.
func TestPullConnectFailureDoesNotLeak(t *testing.T) {
	const secret = "leakpw_connect"
	code, stdout, stderr := runPullCapturing(t, &pullOptions{
		// Port 1 is not a Postgres; the connection is refused fast.
		dsn:     "postgres://u:" + secret + "@127.0.0.1:1/db?connect_timeout=1",
		privacy: "standard",
		out:     filepath.Join(t.TempDir(), "out.yaml"),
	})
	if code != verdict.ExitToolError {
		t.Errorf("exit code = %d, want %d (tool error)", code, verdict.ExitToolError)
	}
	if !strings.Contains(stderr, "could not connect to the database") {
		t.Errorf("stderr should report a connect failure, got:\n%s", stderr)
	}
	if strings.Contains(stdout+stderr, secret) {
		t.Errorf("the DSN password %q leaked into the output:\nstdout:%s\nstderr:%s", secret, stdout, stderr)
	}
}

// TestPullRefusesSuperuser: connected as a superuser without --i-know, pull
// refuses and writes no fixture (RFC §8; the read-only posture is the guardrail
// against a pull that could itself be a foothold).
func TestPullRefusesSuperuser(t *testing.T) {
	admin := requireAdminDSN(t) // the test server connects as the superuser `postgres`
	out := filepath.Join(t.TempDir(), "out.yaml")
	code, _, stderr := runPullCapturing(t, &pullOptions{
		dsn:               admin,
		privacy:           "standard",
		out:               out,
		maxEscalationRows: profile.DefaultMaxEscalationRows,
	})
	if code != verdict.ExitToolError {
		t.Errorf("exit code = %d, want %d (tool error)", code, verdict.ExitToolError)
	}
	if !strings.Contains(stderr, "refusing to profile as a superuser") {
		t.Errorf("stderr should carry the superuser refusal, got:\n%s", stderr)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("pull must not write a fixture when it refuses to run")
	}
}

// TestPullHashesSourceHost: a successful pull records the source host ONLY as a
// salted hash (meta.source), never verbatim, and stamps the privacy level. This
// is the RFC §8.4 privacy invariant that makes a committed fixture safe to share.
func TestPullHashesSourceHost(t *testing.T) {
	admin := requireAdminDSN(t)
	// Profile ONE schema this test owns, not the whole server.
	//
	// Unrestricted, this pull enumerates every non-system schema and then
	// profiles each table it found. CI runs the packages in parallel against a
	// SHARED Postgres service, so another package's `DROP SCHEMA ... CASCADE`
	// lands between the enumeration and the profile and pull aborts with
	// `relation "..." does not exist` (42P01) -- on a different schema every run
	// (rowshape_limits_test.big, rowshape_p1b_t3.users, rowshape_p1t11.events),
	// and on whichever matrix leg happened to lose the race. It reads like a
	// version failure and is not one.
	//
	// The subject here is meta.source being a salted hash, which one small table
	// establishes exactly as well as the whole server does.
	schema := seedPullSchema(t, admin)
	out := filepath.Join(t.TempDir(), "out.yaml")
	code, _, stderr := runPullCapturing(t, &pullOptions{
		dsn:               admin,
		privacy:           "standard",
		out:               out,
		schemas:           []string{schema},
		iKnow:             true, // override the superuser refusal for the test server
		maxEscalationRows: profile.DefaultMaxEscalationRows,
	})
	if code != verdict.ExitPass {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("pull wrote no fixture: %v", err)
	}
	f, err := fixture.ParseVerified(data)
	if err != nil {
		t.Fatalf("pull produced an unparseable/tampered fixture: %v", err)
	}

	host := hostOf(admin)
	if host == "" {
		t.Fatal("test DSN has no host to hash")
	}
	wantSource := profile.HashSource(host)
	if f.Meta.Source != wantSource {
		t.Errorf("meta.source = %q, want the salted hash %q", f.Meta.Source, wantSource)
	}
	if !strings.HasPrefix(f.Meta.Source, fixture.DigestPrefix) {
		t.Errorf("meta.source %q is not a digest — a raw host may have leaked", f.Meta.Source)
	}
	// The verbatim host must appear nowhere in the committed bytes.
	if strings.Contains(string(data), host) {
		t.Errorf("the source host %q appears verbatim in the fixture — it must only be hashed", host)
	}
	if f.Meta.Privacy != "standard" {
		t.Errorf("meta.privacy = %q, want standard", f.Meta.Privacy)
	}
}

// seedPullSchema builds a small schema this test alone owns, so a pull restricted
// to it cannot race another package's teardown. It is dropped when the test ends.
func seedPullSchema(t *testing.T, dsn string) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Skipf("admin connection unusable: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	const schema = "rowshape_pull_host_test"
	for _, stmt := range []string{
		`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`,
		`CREATE SCHEMA ` + schema,
		`CREATE TABLE ` + schema + `.t (id bigint PRIMARY KEY, email text)`,
		`INSERT INTO ` + schema + `.t SELECT g, 'u' || g || '@example.invalid' FROM generate_series(1, 50) g`,
		`ANALYZE ` + schema + `.t`,
	} {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("seeding %s (%s): %v", schema, stmt, err)
		}
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})
	return schema
}
