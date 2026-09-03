// Package demo_test is the scripted end-to-end run for the agent-loop demo
// (P4-T6, the launch gate / week-12 kill criterion). It drives the real
// `rowshape validate` binary against the committed demo fixture and both
// migration sets on a disposable Postgres, and asserts the loop closes: the
// naive migration is rejected on RS-LOCK-001, and the three-step rewrite reaches
// PASS.
//
// The verdicts asserted here were also confirmed against the analyzers directly
// while building the demo. RS-LOCK-001 is a WARN by design (a rewrite is an
// availability problem, not data corruption); the demo gates on it with
// --warn-fail, so the naive migration exits non-zero. See demo/README.md.
//
// DB-backed: skipped without ROWSHAPE_TEST_PG_DSN (no Postgres in the dev
// sandbox); run in CI by the demo-regression workflow (P4-T7) and
// action-integration.
package demo_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/rowshape/rowshape/internal/verdict"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

// buildBinary compiles rowshape once for the run.
func buildBinary(t *testing.T, root string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "rowshape")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// runValidate runs `rowshape validate` and returns the parsed verdict + exit code.
func runValidate(t *testing.T, bin, root, migrations, admin string, warnFail bool) (verdict.Result, int) {
	t.Helper()
	fixture := filepath.Join(root, "demo", "repo", "rowshape.yaml")
	// --statement-timeout above the default 1m. The rewrite's backfill is a
	// bounded batch LOOP, and the whole loop is one statement to the server, so
	// every batch is charged to the same ceiling — about 90s for the fixture's
	// 5,000,000 rows. At the 1m default the loop is cancelled, which floors the
	// verdict to WARN with no findings (D-019) and reads as the demo breaking
	// when nothing about the migration is wrong. A real backfill of five million
	// rows takes minutes; the ceiling has to admit that.
	args := []string{"validate", fixture, "--migrations", filepath.Join(root, "demo", "repo", migrations),
		"--ephemeral", admin, "--json", "--seed", "1", "--statement-timeout", "5m"}
	if warnFail {
		args = append(args, "--warn-fail")
	}
	cmd := exec.Command(bin, args...)
	out, _ := cmd.Output()
	code := cmd.ProcessState.ExitCode()
	var res verdict.Result
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("validate output is not a JSON verdict (exit %d): %v\n%s", code, err, out)
	}
	return res, code
}

// expected mirrors demo/expected.json — the committed source of truth the
// regression asserts against.
type expected struct {
	Naive   caseExpect `json:"naive"`
	Rewrite caseExpect `json:"rewrite"`
}

type caseExpect struct {
	Verdict          string   `json:"verdict"`
	MustInclude      []string `json:"must_include_findings"`
	GatedExitNonzero bool     `json:"gated_exit_nonzero"`
}

func loadExpected(t *testing.T, root string) expected {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "demo", "expected.json"))
	if err != nil {
		t.Fatalf("read demo/expected.json: %v", err)
	}
	var e expected
	if err := json.Unmarshal(b, &e); err != nil {
		t.Fatalf("parse demo/expected.json: %v", err)
	}
	return e
}

func TestDemoLoopClosesEndToEnd(t *testing.T) {
	admin := os.Getenv("ROWSHAPE_TEST_PG_DSN")
	if admin == "" {
		t.Skip("set ROWSHAPE_TEST_PG_DSN to a Postgres admin connection to run the demo end-to-end")
	}
	ctx := context.Background()
	if conn, err := pgx.Connect(ctx, admin); err != nil {
		t.Skipf("admin connection unusable: %v", err)
	} else {
		_ = conn.Close(ctx)
	}
	root := repoRoot(t)
	bin := buildBinary(t, root)
	exp := loadExpected(t, root)

	// 1) The naive migration is rejected: WARN carrying RS-LOCK-001, and with
	//    --warn-fail (how the demo gates) it exits non-zero. Seeded for
	//    determinism (INV-DETERMINISM), so the assertion is stable across runs.
	naive, naiveCode := runValidate(t, bin, root, "migrations/naive", admin, true)
	if naive.Verdict != exp.Naive.Verdict {
		t.Errorf("naive verdict = %s, want %s", naive.Verdict, exp.Naive.Verdict)
	}
	for _, code := range exp.Naive.MustInclude {
		if !hasFinding(naive, code) {
			t.Errorf("naive migration should carry %s, got findings: %v", code, codes(naive))
		}
	}
	if exp.Naive.GatedExitNonzero && naiveCode == 0 {
		t.Errorf("naive migration should fail the gated job (exit non-zero), got exit 0")
	}

	// 2) The three-step rewrite reaches PASS.
	rewrite, rewriteCode := runValidate(t, bin, root, "migrations/rewrite", admin, false)
	if rewrite.Verdict != exp.Rewrite.Verdict {
		t.Errorf("rewrite verdict = %s, want %s; findings: %v", rewrite.Verdict, exp.Rewrite.Verdict, codes(rewrite))
	}
	if rewrite.Verdict == verdict.VerdictPass && rewriteCode != 0 {
		t.Errorf("rewrite should pass (exit 0), got exit %d", rewriteCode)
	}
}

func hasFinding(r verdict.Result, code string) bool {
	for _, f := range r.Findings {
		if f.Code == code {
			return true
		}
	}
	return false
}

func codes(r verdict.Result) []string {
	var out []string
	for _, f := range r.Findings {
		out = append(out, f.Code)
	}
	return out
}
