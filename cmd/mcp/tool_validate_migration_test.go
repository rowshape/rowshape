package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rowshape/rowshape/internal/verdict"
)

// callValidate calls validate_migration and returns the compact structured output.
func callValidate(t *testing.T, cs *sdk.ClientSession, fixture, migration string) (*sdk.CallToolResult, map[string]any) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{
		Name:      "validate_migration",
		Arguments: map[string]any{"fixture": fixture, "migration": migration},
	})
	if err != nil {
		t.Fatalf("call validate_migration: %v", err)
	}
	var out map[string]any
	if res.StructuredContent != nil {
		b, _ := json.Marshal(res.StructuredContent)
		_ = json.Unmarshal(b, &out)
	}
	return res, out
}

func writeFile2(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestValidateMigrationCappingPreserved: a migration whose safety rests on an
// unproven fact never reports PASS — the tool honors confidence capping end to
// end (RFC §7.4). ADD UNIQUE against a column with no proven uniqueness → WARN.
func TestValidateMigrationCappingPreserved(t *testing.T) {
	cs := connectClient(t)
	dir := t.TempDir()
	fx := writeFile2(t, dir, "rowshape.yaml", `rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.users:
    rows: {value: 800000, confidence: exact}
    columns:
      email: {type: text, nullable: false, distinct: {value: 799000, confidence: estimated}}
`)
	// Inline SQL — the agent passes the migration it just wrote, unsaved.
	_, out := callValidate(t, cs, fx, "ALTER TABLE public.users ADD CONSTRAINT u UNIQUE (email);")

	if out["verdict"] != verdict.VerdictWarn {
		t.Fatalf("verdict = %v, want WARN (capping: uniqueness unproven, never PASS)", out["verdict"])
	}
	if ec, _ := out["exit_code"].(float64); int(ec) != verdict.ExitWarnOnly {
		t.Errorf("exit_code = %v, want %d (WARN-only)", out["exit_code"], verdict.ExitWarnOnly)
	}
	// Assert the SPECIFIC code rather than a count. The claim under test is that
	// capping keeps an unproven fact from reporting PASS, and a count is only a
	// proxy for that — a fragile one, since a correct additional finding (the
	// ADD CONSTRAINT here also builds an index under ACCESS EXCLUSIVE) reads as
	// a regression. The compact-shape check below now runs over EVERY finding
	// rather than just the first, which makes this test stricter than the count
	// version it replaces.
	findings, _ := out["findings"].([]any)
	if len(findings) == 0 {
		t.Fatal("expected at least one finding")
	}
	var f0 map[string]any
	for _, raw := range findings {
		m, _ := raw.(map[string]any)
		if m["code"] == "RS-DATA-014" {
			f0 = m
		}
	}
	if f0 == nil {
		t.Fatalf("RS-DATA-014 not among the findings: %+v", findings)
	}
	for _, raw := range findings {
		m, _ := raw.(map[string]any)
		if _, has := m["remediation"]; has {
			t.Errorf("compact finding %v must not inline remediation prose", m["code"])
		}
	}
	// Compact: the finding carries a code and an explain path, NOT remediation prose.
	if _, hasRemediation := f0["remediation"]; hasRemediation {
		t.Error("compact finding must not inline remediation prose")
	}
	if !strings.Contains(f0["explain"].(string), "RS-DATA-014") {
		t.Errorf("finding should carry the explain_finding expansion path, got %v", f0["explain"])
	}
}

// TestValidateMigrationPass: a safe migration reports PASS with no findings.
func TestValidateMigrationPass(t *testing.T) {
	cs := connectClient(t)
	dir := t.TempDir()
	fx := writeFile2(t, dir, "rowshape.yaml", `rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.users:
    rows: {value: 100, confidence: exact}
    columns:
      email: {type: text, nullable: true, null_fraction: {value: 0.0, confidence: exact}}
`)
	// SET NOT NULL against a proven-zero null_fraction is safe.
	_, out := callValidate(t, cs, fx, "ALTER TABLE public.users ALTER COLUMN email SET NOT NULL;")
	if out["verdict"] != verdict.VerdictPass {
		t.Errorf("verdict = %v, want PASS", out["verdict"])
	}
}

// TestValidateMigrationLockFinding: a rewrite fires RS-LOCK with a duration
// bucket, returned compactly.
func TestValidateMigrationLockFinding(t *testing.T) {
	cs := connectClient(t)
	dir := t.TempDir()
	fx := writeFile2(t, dir, "rowshape.yaml", `rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.orders:
    rows: {value: 5000000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false}
`)
	_, out := callValidate(t, cs, fx, "ALTER TABLE public.orders ADD COLUMN token uuid NOT NULL DEFAULT gen_random_uuid();")
	if out["verdict"] != verdict.VerdictWarn {
		t.Fatalf("verdict = %v, want WARN (RS-LOCK)", out["verdict"])
	}
	findings := out["findings"].([]any)
	f0 := findings[0].(map[string]any)
	if f0["code"] != "RS-LOCK-001" {
		t.Errorf("code = %v, want RS-LOCK-001", f0["code"])
	}
	// NO duration bucket, and that is the correct answer.
	//
	// validate_migration is STATIC: it parses the SQL and runs the analyzers
	// against the fixture, executing nothing. There is therefore no measured
	// basis, and estimateFor now declines rather than defaulting basisMs to 1ms
	// and scaling that non-measurement by the row ratio.
	//
	// This assertion used to require a bucket, and it passed only because of that
	// fabrication — which means the AGENT surface was handing a model a confident
	// duration prediction derived from a measurement that never happened. Of all
	// the places to invent a number, the one built for agents is the worst.
	if b, present := f0["bucket"]; present && b != nil && b != "" {
		t.Errorf("static analysis has no measured basis, so it must carry no duration bucket, got %v", b)
	}
}

// TestValidateMigrationFromFile: the migration argument also accepts a path.
func TestValidateMigrationFromFile(t *testing.T) {
	cs := connectClient(t)
	dir := t.TempDir()
	fx := writeFile2(t, dir, "rowshape.yaml", `rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.t: {rows: {value: 10, confidence: exact}, columns: {c: {type: text, nullable: true}}}
`)
	mig := writeFile2(t, dir, "m.sql", "ALTER TABLE public.t ADD COLUMN d int;")
	res, out := callValidate(t, cs, fx, mig)
	if res.IsError {
		t.Fatalf("unexpected error validating a migration file")
	}
	if out["verdict"] == nil {
		t.Error("expected a verdict from a migration file path")
	}
}

// TestCappedWarnCarriesItsResolveCommand closes a dead end in the wedge loop.
//
// The agent rule tells the agent: "A WARN is not a pass ... The finding names
// the command that resolves it. Run that command." Over MCP that was
// unfollowable. verdict.Engine.ResolveCommand produces a command parameterized
// by THIS run's weakest fact — `rowshape pull --exact public.users.email` — and
// compactFinding had no field for it, while explain_finding returns a static
// catalog entry that cannot know the run. So on every confidence-capped WARN the
// agent was instructed to run a command it could not see, from either tool.
func TestCappedWarnCarriesItsResolveCommand(t *testing.T) {
	cs := connectClient(t)
	dir := t.TempDir()
	// Uniqueness only ESTIMATED, so ADD UNIQUE caps to WARN rather than certifying.
	fx := writeFile2(t, dir, "rowshape.yaml", `rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.users:
    rows: {value: 800000, confidence: exact}
    columns:
      email: {type: text, nullable: false, distinct: {value: 799000, confidence: estimated}}
`)
	_, out := callValidate(t, cs, fx, "ALTER TABLE public.users ADD CONSTRAINT u UNIQUE (email);")

	findings, _ := out["findings"].([]any)
	if len(findings) == 0 {
		t.Fatal("expected at least one finding")
	}
	var resolve string
	for _, raw := range findings {
		m, _ := raw.(map[string]any)
		if m["code"] == "RS-DATA-014" {
			resolve, _ = m["resolve"].(string)
		}
	}
	if resolve == "" {
		t.Fatal("a confidence-capped WARN must carry the command that resolves it, or the rule's " +
			"instruction to run that command is unfollowable over MCP")
	}
	// It must be the PARAMETERIZED command, not generic advice — that is exactly
	// what explain_finding could never supply.
	if !strings.Contains(resolve, "--exact") || !strings.Contains(resolve, "email") {
		t.Errorf("resolve = %q, want the run-specific `rowshape pull --exact <weakest fact>`", resolve)
	}
}

// A finding that rests on nothing weak must not carry a resolve command, or the
// field becomes noise on every finding.
func TestUncappedFindingHasNoResolveCommand(t *testing.T) {
	cs := connectClient(t)
	dir := t.TempDir()
	fx := writeFile2(t, dir, "rowshape.yaml", `rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.users:
    rows: {value: 800000, confidence: exact}
    columns:
      email: {type: text, nullable: false, unique: {value: true, confidence: exact, via: constraint}}
`)
	_, out := callValidate(t, cs, fx, "ALTER TABLE public.users ADD CONSTRAINT u UNIQUE (email);")
	findings, _ := out["findings"].([]any)
	for _, raw := range findings {
		m, _ := raw.(map[string]any)
		if r, _ := m["resolve"].(string); r != "" {
			t.Errorf("finding %v rests on exact facts and must carry no resolve command, got %q", m["code"], r)
		}
	}
}
