package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/toolerror"
	"github.com/rowshape/rowshape/internal/verdict"
)

// runValidateExpectingToolError runs validate and returns the exit code and the
// parsed JSON tool-error payload (validate is invoked with --json).
func runToolError(t *testing.T, opts *validateOptions) (int, toolerror.ToolError) {
	t.Helper()
	opts.asJSON = true
	var runErr error
	stdout, stderr := captureOutput(t, func() error { runErr = runValidate(context.Background(), opts); return runErr })

	code := 0
	var ee *ExitError
	if errors.As(runErr, &ee) {
		code = ee.Code
	}
	var te toolerror.ToolError
	if err := json.Unmarshal([]byte(stdout), &te); err != nil {
		t.Fatalf("tool-error payload is not JSON: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	return code, te
}

// TestToolErrorExitThreeAndShape: every listed operational failure returns exit 3
// with a structured, machine-readable reason (never a verdict). The payload
// carries "error":"tool_error", never "verdict".
func TestToolErrorExitThreeAndShape(t *testing.T) {
	dir := t.TempDir()
	goodFixture := filepath.Join(dir, "ok.yaml")
	writeFile(t, goodFixture, `rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.t: {rows: {value: 10, confidence: exact}, columns: {c: {type: text, nullable: true}}}
`)
	goodMig := filepath.Join(dir, "m.sql")
	writeFile(t, goodMig, "ALTER TABLE public.t ALTER COLUMN c SET NOT NULL;")

	// Fixture with an unknown major version — MUST refuse (RFC §12).
	badVersion := filepath.Join(dir, "v9.yaml")
	writeFile(t, badVersion, `rowshape_fixture: "9"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables: {}
`)
	// Unparseable fixture.
	badParse := filepath.Join(dir, "junk.yaml")
	writeFile(t, badParse, "this: : : not valid yaml: [")

	cases := []struct {
		name string
		opts *validateOptions
		want toolerror.Category
	}{
		{
			name: "unknown-version",
			opts: &validateOptions{fixturePath: badVersion, migrations: goodMig, ephemeral: "postgres://u@localhost:1/x", scale: 1},
			want: toolerror.UnknownVersion,
		},
		{
			name: "fixture-parse",
			opts: &validateOptions{fixturePath: badParse, migrations: goodMig, ephemeral: "postgres://u@localhost:1/x", scale: 1},
			want: toolerror.FixtureParse,
		},
		{
			name: "no-target",
			opts: &validateOptions{fixturePath: goodFixture, migrations: goodMig, scale: 1},
			want: toolerror.BadUsage,
		},
		{
			name: "target-unavailable",
			opts: &validateOptions{fixturePath: goodFixture, migrations: goodMig, ephemeral: "postgres://u:p@127.0.0.1:1/nope?connect_timeout=1", scale: 1},
			want: toolerror.TargetUnavailable,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, te := runToolError(t, c.opts)
			if code != verdict.ExitToolError {
				t.Errorf("exit code = %d, want 3 (tool error)", code)
			}
			if te.Error_ != toolerror.Kind {
				t.Errorf(`payload error field = %q, want %q (must be distinguishable from a verdict)`, te.Error_, toolerror.Kind)
			}
			if te.Category != c.want {
				t.Errorf("category = %q, want %q", te.Category, c.want)
			}
			if te.Message == "" {
				t.Error("tool error must carry a human-readable message")
			}
		})
	}
}

// TestToolErrorIsNotAVerdict: the tool-error payload is clearly NOT a verdict — it
// has no "verdict" field and its "error" field marks it. An agent can branch on
// this without confusing "the tool couldn't run" with "the migration is unsafe".
func TestToolErrorIsNotAVerdict(t *testing.T) {
	dir := t.TempDir()
	badVersion := filepath.Join(dir, "v9.yaml")
	writeFile(t, badVersion, `rowshape_fixture: "9"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables: {}
`)
	mig := filepath.Join(dir, "m.sql")
	writeFile(t, mig, "SELECT 1;")

	opts := &validateOptions{fixturePath: badVersion, migrations: mig, ephemeral: "postgres://u@localhost:1/x", scale: 1, asJSON: true}
	stdout, _ := captureOutput(t, func() error { return runValidate(context.Background(), opts) })

	var raw map[string]any
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, stdout)
	}
	if _, hasVerdict := raw["verdict"]; hasVerdict {
		t.Error("a tool error must not carry a verdict field")
	}
	if raw["error"] != toolerror.Kind {
		t.Errorf(`error field = %v, want %q`, raw["error"], toolerror.Kind)
	}
}

// TestToolErrorHumanRendering: the human rendering comes from the same struct as
// the JSON (INV-VERDICT-STABLE-style one-struct/two-marshalers).
func TestToolErrorHumanRendering(t *testing.T) {
	te := toolerror.New(toolerror.TargetUnavailable, "no disposable database", "start Postgres")
	human := te.Human()
	for _, want := range []string{string(toolerror.TargetUnavailable), "no disposable database", "start Postgres"} {
		if !strings.Contains(human, want) {
			t.Errorf("human rendering missing %q:\n%s", want, human)
		}
	}
	// It writes to stderr (not stdout) in human mode — no accidental verdict on stdout.
	_ = os.Stdout
}

// TestUnsupportedRunnerFailsBeforeTargetCreation pins the ORDER, which is the
// actual claim.
//
// Detection recognizes Alembic, Prisma and Drizzle projects, but capture only
// supports raw SQL — and that refusal used to happen inside applyAndCapture,
// i.e. AFTER a disposable database had been created and hydrated. The user paid
// for a container and a full hydrate to be told the project is unsupported.
//
// The test works by pointing --ephemeral at an unreachable address. If the
// runner check ran late, the target failure would surface FIRST as
// target_unavailable; getting runner_not_found proves the check happens before
// anything touches a database.
func TestUnsupportedRunnerFailsBeforeTargetCreation(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "prisma"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prisma", "schema.prisma"), []byte("model X {}"), 0o644); err != nil {
		t.Fatal(err)
	}

	fx := filepath.Join(dir, "ok.yaml")
	writeFile(t, fx, `rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.t: {rows: {value: 10, confidence: exact}, columns: {c: {type: text, nullable: true}}}
`)

	opts := &validateOptions{
		fixturePath: fx,
		migrations:  dir,
		// Unreachable on purpose: if the runner check ran after target setup,
		// this would fail first with target_unavailable.
		ephemeral: "postgres://u:p@127.0.0.1:1/nope?connect_timeout=1",
		scale:     1,
	}
	code, te := runToolError(t, opts)
	if code != verdict.ExitToolError {
		t.Errorf("exit code = %d, want 3", code)
	}
	if te.Category != toolerror.RunnerNotFound {
		t.Errorf("category = %q, want %q — an unsupported runner must be refused BEFORE a target is created",
			te.Category, toolerror.RunnerNotFound)
	}
	if !strings.Contains(te.Message, "prisma") {
		t.Errorf("the refusal must name the detected project type, got %q", te.Message)
	}
}

// TestPartitionMigrationIsRefusedNotFailed pins that rowshape declines to
// represent something rather than reporting a verdict about its own limitation.
//
// internal/target/ddl.go emits no PARTITION BY, so an ATTACH PARTITION statement
// errors against the hydrated plain table, Capture.Success goes false, and
// BuildResult floors the whole verdict to FAIL — a MANUFACTURED FAIL for a
// migration that is fine. A tool error says "I cannot decide this", which is
// true; a FAIL asserts something about the migration that is not.
func TestPartitionMigrationIsRefusedNotFailed(t *testing.T) {
	dir := t.TempDir()
	fx := filepath.Join(dir, "f.yaml")
	writeFile(t, fx, `rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.events: {rows: {value: 100, confidence: exact}, columns: {id: {type: bigint, nullable: false}}}
`)
	mig := filepath.Join(dir, "m.sql")
	writeFile(t, mig, "ALTER TABLE events ATTACH PARTITION events_2024 FOR VALUES FROM ('2024-01-01') TO ('2025-01-01');")

	opts := &validateOptions{
		fixturePath: fx,
		migrations:  mig,
		// Unreachable: if the refusal came late, this would fail first as
		// target_unavailable. Getting bad_usage proves it happens before any
		// database work.
		ephemeral: "postgres://u:p@127.0.0.1:1/nope?connect_timeout=1",
		scale:     1,
	}
	code, te := runToolError(t, opts)
	if code != verdict.ExitToolError {
		t.Errorf("exit code = %d, want 3 — a sandbox limitation is a tool error, not a verdict", code)
	}
	if te.Category != toolerror.BadUsage {
		t.Errorf("category = %q, want %q", te.Category, toolerror.BadUsage)
	}
	if !strings.Contains(te.Message, "partition") {
		t.Errorf("the refusal must name the feature it cannot represent, got %q", te.Message)
	}
	if !strings.Contains(te.Hint, "--target") {
		t.Errorf("the hint must name the way forward, got %q", te.Hint)
	}
}

// An ordinary migration against an ordinary table must be untouched: the check
// is narrow on purpose, or it becomes a new false-refusal source.
func TestNonPartitionMigrationIsUnaffected(t *testing.T) {
	dir := t.TempDir()
	fx := filepath.Join(dir, "f.yaml")
	writeFile(t, fx, `rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.events: {rows: {value: 100, confidence: exact}, columns: {id: {type: bigint, nullable: false}}}
`)
	mig := filepath.Join(dir, "m.sql")
	writeFile(t, mig, "ALTER TABLE events ADD COLUMN note text;")

	opts := &validateOptions{
		fixturePath: fx,
		migrations:  mig,
		ephemeral:   "postgres://u:p@127.0.0.1:1/nope?connect_timeout=1",
		scale:       1,
	}
	_, te := runToolError(t, opts)
	// It should get PAST the sandbox check and fail on the unreachable target.
	if te.Category != toolerror.TargetUnavailable {
		t.Errorf("category = %q, want %q — an ordinary migration must not be refused by the partition check",
			te.Category, toolerror.TargetUnavailable)
	}
}

// TestCalibrateWithMaxRowsIsRefused: --calibrate fits a cost curve through two
// runs at DIFFERENT scales. With --max-rows set, both runs clamp to the same row
// count, the second point is identical to the first, and estimateFor's
// `rows2 != rows1` test silently drops back to a single-point estimate — so the
// user pays for a second full hydrate and gets nothing, with no diagnostic.
func TestCalibrateWithMaxRowsIsRefused(t *testing.T) {
	dir := t.TempDir()
	fx := filepath.Join(dir, "f.yaml")
	writeFile(t, fx, `rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.t: {rows: {value: 100, confidence: exact}, columns: {c: {type: text, nullable: true}}}
`)
	mig := filepath.Join(dir, "m.sql")
	writeFile(t, mig, "ALTER TABLE public.t ADD COLUMN x int;")

	opts := &validateOptions{
		fixturePath: fx,
		migrations:  mig,
		ephemeral:   "postgres://u:p@127.0.0.1:1/nope?connect_timeout=1",
		scale:       1,
		calibrate:   true,
		maxRows:     50,
	}
	code, te := runToolError(t, opts)
	if code != verdict.ExitToolError {
		t.Errorf("exit code = %d, want 3", code)
	}
	if !strings.Contains(te.Message, "--calibrate") || !strings.Contains(te.Message, "--max-rows") {
		t.Errorf("the refusal must name both flags, got %q", te.Message)
	}
	if !strings.Contains(te.Hint, "two runs") {
		t.Errorf("the hint must explain why the combination cannot work, got %q", te.Hint)
	}
}
