package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/rowshape/rowshape/internal/profile"
	"github.com/rowshape/rowshape/internal/validate"
	"github.com/rowshape/rowshape/internal/verdict"
)

const testAdminEnv = "ROWSHAPE_TEST_PG_DSN"

// TestValidateRefusesSourceHost: validate hard-refuses a target whose host
// matches the fixture's source host, BEFORE it connects (INV-BLAST-RADIUS-ZERO,
// PRD §11). It is exercised through runValidate, which computes the refusal from
// the fixture's meta.source and the target URL's host.
func TestValidateRefusesSourceHost(t *testing.T) {
	const prodHost = "prod-db.internal.example.com"
	dir := t.TempDir()
	fx := filepath.Join(dir, "rowshape.yaml")
	// meta.source is the salted hash of the production host (RFC §8.4).
	writeFile(t, fx, `rowshape_fixture: "1"
meta:
  id: t
  engine: {name: postgres, version: "16"}
  source: `+profile.HashSource(prodHost)+`
tables:
  public.users:
    rows: {value: 10, confidence: exact}
    columns:
      email: {type: text, nullable: true}
`)
	mig := filepath.Join(dir, "m.sql")
	writeFile(t, mig, "ALTER TABLE public.users ALTER COLUMN email SET NOT NULL;")

	// The ephemeral admin URL points at the SAME host the fixture was pulled from.
	opts := &validateOptions{
		fixturePath: fx,
		migrations:  mig,
		ephemeral:   "postgres://admin:secret@" + prodHost + ":5432/postgres",
		scale:       1.0,
	}
	_, stderr := captureOutput(t, func() error { return runValidate(context.Background(), opts) })

	if !strings.Contains(stderr, "refusing to run against the fixture's source host") {
		t.Errorf("expected a host-match refusal on stderr, got:\n%s", stderr)
	}
}

// TestCheckHostRefusal pins the refusal predicate directly (RFC §8.4, PRD §11):
// the target host is hashed with the same salt as meta.source and compared.
func TestCheckHostRefusal(t *testing.T) {
	source := profile.HashSource("prod.example.com")
	if err := validate.CheckHost(source, "prod.example.com"); err == nil {
		t.Error("must refuse when the target host matches the fixture source")
	}
	if err := validate.CheckHost(source, "localhost"); err != nil {
		t.Errorf("must allow a different host, got %v", err)
	}
	if err := validate.CheckHost("", "localhost"); err != nil {
		t.Errorf("no source host means nothing to collide with, got %v", err)
	}
}

// forbiddenEgress are import paths that would let validate talk to something
// other than its Postgres target: HTTP and RPC clients, and the cloud SDKs.
//
// Note what is NOT here. `net` itself is REQUIRED — validate's entire job is to
// connect to a Postgres target, and pgx opens a TCP socket and resolves DNS to
// do it. INV-NEVER-GATE-VALIDATE says validate never calls the CLOUD, not that
// it performs no I/O, so a blanket "no networking" rule would be both wrong and
// unsatisfiable. The line is drawn at the protocols an egress would use.
var forbiddenEgress = []string{
	"net/http", "net/rpc", "net/smtp",
	"google.golang.org", "cloud.google.com", "github.com/aws/", "github.com/Azure/",
	"grpc", "opentelemetry", "datadog", "sentry",
}

// allowedNetwork are the network packages the Postgres connection legitimately
// pulls in, each allowed for a stated reason rather than by silent omission.
var allowedNetwork = map[string]string{
	"net":                                    "pgx opens a TCP connection to the target; this is the connection validate exists to make",
	"net/netip":                              "address parsing on the pgx connection path",
	"net/url":                                "parsing the target DSN",
	"internal/nettrace":                      "pulled in by net; runtime tracing hooks, not a client",
	"vendor/golang.org/x/net/dns/dnsmessage": "DNS resolution for the target hostname, via net",
}

// TestValidateNoCloudEgress asserts structurally that nothing on the validate
// code path can call the cloud (INV-NEVER-GATE-VALIDATE). Scanning imports is a
// deterministic proof that no egress can happen, stronger than observing a run.
//
// CR-T13: this used to scan cmd/validate.go and internal/validate/ ONLY. But
// runValidate reaches internal/target, internal/hydrate, internal/runner,
// internal/toolerror, internal/findings and more — none of which were scanned. A
// network import in any of them passed the guard silently. Since this test is
// the mechanical proof behind the privacy claim the docs make verbatim (P4-T4),
// a guard that names a guarantee it does not actually check is worse than no
// guard: it converts an unverified claim into a verified-looking one.
//
// It now walks the REAL transitive closure via `go list -deps`, seeded from
// cmd/validate.go's own imports so a newly added dependency is picked up without
// anyone remembering to extend a list.
func TestValidateNoCloudEgress(t *testing.T) {
	// Seed: whatever cmd/validate.go imports today.
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, "validate.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse validate.go: %v", err)
	}
	var seeds []string
	for _, imp := range af.Imports {
		// Stdlib, third-party and internal paths alike; `go list` resolves all.
		seeds = append(seeds, strings.Trim(imp.Path.Value, `"`))
	}
	if len(seeds) == 0 {
		t.Fatal("no imports found in validate.go; the closure would be vacuously clean")
	}

	// Transitive closure. `go list` is the same resolver the compiler uses, so
	// this cannot drift from what actually gets linked in.
	out, err := osexec.Command("go", append([]string{"list", "-deps"}, seeds...)...).Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	deps := strings.Fields(string(out))
	if len(deps) < len(seeds) {
		t.Fatalf("closure (%d) smaller than the seed set (%d); the walk did not run", len(deps), len(seeds))
	}

	// Guard the guard: the closure must actually contain the packages the review
	// found unscanned, or this test is passing over an empty set again.
	mustCover := []string{
		"github.com/rowshape/rowshape/internal/target",
		"github.com/rowshape/rowshape/internal/hydrate",
		"github.com/rowshape/rowshape/internal/runner",
		"github.com/rowshape/rowshape/internal/toolerror",
	}
	inClosure := make(map[string]bool, len(deps))
	for _, d := range deps {
		inClosure[d] = true
	}
	for _, m := range mustCover {
		if !inClosure[m] {
			t.Errorf("closure is missing %s — this is one of the packages CR-T13 exists to cover", m)
		}
	}

	for _, dep := range deps {
		if reason, ok := allowedNetwork[dep]; ok {
			t.Logf("allowed network package %s: %s", dep, reason)
			continue
		}
		for _, bad := range forbiddenEgress {
			if strings.Contains(dep, bad) {
				t.Errorf("validate's import closure contains %q (matched %q) — validate must never "+
					"call the cloud (INV-NEVER-GATE-VALIDATE, PRD §8.4). If this is legitimate, add it "+
					"to allowedNetwork WITH A REASON.", dep, bad)
			}
		}
	}
	t.Logf("scanned %d packages in validate's transitive import closure", len(deps))
}

// TestEmitResultTwoRenderings: --json and the default human output are two
// renderings of the SAME struct (INV-VERDICT-SHAPE). The JSON round-trips to an
// equal Result; the human text surfaces the verdict.
func TestEmitResultTwoRenderings(t *testing.T) {
	r := verdict.Result{
		Rowshape: verdict.Rowshape,
		Verdict:  verdict.VerdictWarn,
		Fixture:  verdict.FixtureRef{ID: "prod@2026-07-14", Digest: "sha256:abc"},
		Findings: []verdict.Finding{{Code: "RS-DATA-014", Severity: verdict.SeverityWarn, Title: "cannot confirm uniqueness", Remediation: "rowshape pull --exact public.users.email"}},
	}

	var jsonBuf bytes.Buffer
	if err := emitResult(&jsonBuf, r, true); err != nil {
		t.Fatalf("emit json: %v", err)
	}
	var back verdict.Result
	if err := decodeJSON(jsonBuf.Bytes(), &back); err != nil {
		t.Fatalf("json did not round-trip: %v", err)
	}
	if back.Verdict != r.Verdict || len(back.Findings) != 1 || back.Findings[0].Code != "RS-DATA-014" {
		t.Errorf("json rendering diverged from the struct: %+v", back)
	}

	var humanBuf bytes.Buffer
	if err := emitResult(&humanBuf, r, false); err != nil {
		t.Fatalf("emit human: %v", err)
	}
	human := humanBuf.String()
	for _, want := range []string{"WARN", "RS-DATA-014", "rowshape pull --exact public.users.email"} {
		if !strings.Contains(human, want) {
			t.Errorf("human rendering missing %q:\n%s", want, human)
		}
	}
}

// TestValidateEndToEnd runs the full pipeline against a real Postgres when
// ROWSHAPE_TEST_PG_DSN is set: hydrate a disposable database from a corpus
// fixture, apply the migration, capture, and emit a well-formed verdict. With no
// analyzers registered yet (P2-T8+), a cleanly-applying safe migration is PASS.
func TestValidateEndToEnd(t *testing.T) {
	admin := os.Getenv(testAdminEnv)
	if admin == "" {
		t.Skipf("set %s to a Postgres admin connection to run the end-to-end validate", testAdminEnv)
	}
	// Sanity: the admin connection actually works.
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Skipf("admin connection unusable: %v", err)
	}
	_ = conn.Close(ctx)

	opts := &validateOptions{
		fixturePath: filepath.Join("..", "corpus", "cases", "capping-exact-not-null", "fixture.yaml"),
		migrations:  filepath.Join("..", "corpus", "cases", "capping-exact-not-null", "migration.sql"),
		ephemeral:   admin,
		scale:       1.0,
		asJSON:      true,
	}
	stdout, stderr := captureOutput(t, func() error { return runValidate(context.Background(), opts) })

	if !strings.Contains(stdout, `"verdict"`) || !strings.Contains(stdout, `"rowshape"`) {
		t.Errorf("expected a JSON verdict on stdout, got:\n%s\nstderr:\n%s", stdout, stderr)
	}
	var res verdict.Result
	if err := decodeJSON([]byte(stdout), &res); err != nil {
		t.Fatalf("verdict is not valid JSON: %v\n%s", err, stdout)
	}
	// A SET NOT NULL against exact-0%% nulls applies cleanly; no analyzers yet → PASS.
	if res.Verdict != verdict.VerdictPass {
		t.Errorf("verdict = %s, want PASS (clean apply, no analyzers). stderr:\n%s", res.Verdict, stderr)
	}
}

// --- test helpers ---

func decodeJSON(b []byte, v any) error { return json.Unmarshal(b, v) }

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// captureOutput redirects os.Stdout/os.Stderr around fn and returns what it
// wrote to each. runValidate writes directly to the process streams.
func captureOutput(t *testing.T, fn func() error) (stdout, stderr string) {
	t.Helper()
	origOut, origErr := os.Stdout, os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout, os.Stderr = wOut, wErr

	done := make(chan struct{})
	var outBuf, errBuf bytes.Buffer
	go func() {
		_, _ = outBuf.ReadFrom(rOut)
		_, _ = errBuf.ReadFrom(rErr)
		close(done)
	}()

	err := fn()
	var ee *ExitError
	if err != nil && !errors.As(err, &ee) {
		// Non-exit errors are unexpected from runValidate (it maps to ExitError).
		t.Logf("runValidate returned non-exit error: %v", err)
	}

	_ = wOut.Close()
	_ = wErr.Close()
	<-done
	os.Stdout, os.Stderr = origOut, origErr
	return outBuf.String(), errBuf.String()
}

// TestIsSQLFileCaseInsensitive: Windows and macOS default to case-insensitive
// filesystems, where `001_init.SQL` is an ordinary file. isSQLFile used to
// compare the extension exactly while plan.go, mcp/tool_validate_migration.go
// and runner/rawsql.go all folded case — so the same file validated through one
// command and failed with RunnerNotFound through another.
func TestIsSQLFileCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.sql", "b.SQL", "c.Sql"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("SELECT 1;"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !isSQLFile(p) {
			t.Errorf("isSQLFile(%q) = false, want true — .sql matching must fold case", name)
		}
	}
	notSQL := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notSQL, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if isSQLFile(notSQL) {
		t.Errorf("isSQLFile(notes.txt) = true, want false")
	}
	if isSQLFile(dir) {
		t.Errorf("isSQLFile(<dir>) = true, want false")
	}
}
