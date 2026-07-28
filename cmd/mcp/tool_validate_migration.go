package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rowshape/rowshape/internal/toolerror"

	// Registers the RS-* analyzers so validate.Registered() is populated.
	_ "github.com/rowshape/rowshape/internal/findings"
	"github.com/rowshape/rowshape/internal/validate"
	"github.com/rowshape/rowshape/internal/verdict"
)

// validate_migration is the loop-closer (PRD §8.2): an agent writes a migration,
// calls this, reads the verdict, fixes, and re-validates — all in its own turn
// (PRD §2). It runs the SAME analyzer + confidence-capping path as the CLI
// (validate.BuildResult over the registered analyzers) and returns the verdict as
// compact finding CODES, not remediation prose — the agent expands a code with
// explain_finding when it needs the fix (PRD §8.2 token discipline).
//
// This is the fast, target-free path: it analyzes the migration SQL against the
// committed fixture (findings, extrapolation, capping) without hydrating a
// database, so an agent can call it every turn. The CLI's `rowshape validate`
// additionally hydrates a disposable Postgres and applies the migration to catch
// runtime failures; both return the same Verdict struct (PRD §10).

// compactFinding is a finding stripped to what an agent branches on. Remediation,
// detail, and evidence are intentionally omitted — explain_finding is the
// expansion path.
type compactFinding struct {
	Code       string `json:"code"`
	Severity   string `json:"severity"`
	Title      string `json:"title"`
	Confidence string `json:"confidence,omitempty"`
	Bucket     string `json:"bucket,omitempty"` // duration bucket, if the finding has one
	Explain    string `json:"explain"`          // e.g. "rowshape explain RS-LOCK-001"
	// Resolve is the command that would raise this finding's weakest dependency
	// to a certifying confidence — "rowshape pull --exact public.users.email".
	//
	// It is here, and not behind explain_finding, because it CANNOT be there:
	// it is parameterized by THIS run's weakest fact, while explain_finding
	// returns a static catalog entry that knows nothing about the run. Without
	// it the agent rule's instruction ("the finding names the command that
	// resolves it — run that command") was unfollowable over MCP: the loop
	// dead-ended on every confidence-capped WARN.
	//
	// It is a single short command, not the remediation prose the budget test
	// keeps out of this payload.
	Resolve string `json:"resolve,omitempty"`
}

// validateOutput is the compact verdict returned to the agent.
type validateOutput struct {
	Verdict  string           `json:"verdict"`   // PASS | WARN | FAIL
	ExitCode int              `json:"exit_code"` // 0 PASS / 1 FAIL / 2 WARN-only
	Findings []compactFinding `json:"findings"`
	Note     string           `json:"note,omitempty"`
}

// handleValidateMigration implements the validate_migration tool.
func handleValidateMigration(_ context.Context, _ *sdk.CallToolRequest, in validateMigrationInput) (*sdk.CallToolResult, any, error) {
	f, err := loadFixture(in.Fixture)
	if err != nil {
		return errorText(toolerror.FixtureParse, err.Error(), "check the fixture path, or run `rowshape pull` to produce one"), nil, nil
	}
	stmts, err := migrationStatements(in.Migration)
	if err != nil {
		return errorText(toolerror.BadUsage, err.Error(), "point `migration` at a .sql file or a directory of them"), nil, nil
	}
	if len(stmts) == 0 {
		return errorText(toolerror.BadUsage, "no SQL statements found in the migration", "check the file is not empty and contains statements, not only comments"), nil, nil
	}

	// Build a capture from the statements (no runtime apply) and run the SAME
	// analyzers + capping the CLI runs.
	var sc []validate.Statement
	for _, s := range stmts {
		sc = append(sc, validate.Statement{SQL: s})
	}
	cap := &validate.Capture{Success: true, Statements: sc}
	result := validate.BuildResult(f, cap, validate.Registered(), false)

	out := validateOutput{
		Verdict:  result.Verdict,
		ExitCode: result.ExitCode(false),
		Note:     "static analysis against the committed fixture; run `rowshape validate` for a full hydrate-and-apply. Expand a code with explain_finding.",
	}
	// Same engine the pipeline used, so the resolve command names the same
	// weakest dependency the capping decision was made on.
	eng := verdict.NewEngine(f)
	for _, fnd := range result.Findings {
		cf := compactFinding{
			Code:       fnd.Code,
			Severity:   fnd.Severity,
			Title:      fnd.Title,
			Confidence: fnd.Confidence,
			Explain:    fnd.Explain,
			Resolve:    eng.ResolveCommand(fnd.DependsOn),
		}
		if fnd.Estimate != nil {
			cf.Bucket = fnd.Estimate.Bucket
		}
		out.Findings = append(out.Findings, cf)
	}

	summary := fmt.Sprintf("%s (exit %d), %d finding(s).", out.Verdict, out.ExitCode, len(out.Findings))
	return textResult(summary), out, nil
}

// maxMigrationFiles bounds how many .sql files validate_migration will read from
// a directory in one call.
//
// This tool is the loop-closer: an agent calls it on the migration it just wrote.
// Handed a mature migrations/ directory it previously ingested the whole project
// history and reported findings across all of it — expensive, and answering a
// question nobody asked.
const maxMigrationFiles = 25

// migrationStatements resolves the `migration` argument to SQL statements: an
// existing .sql file or a directory of them is read from disk; anything else is
// treated as inline SQL, so an agent can pass the migration it just wrote without
// saving it first.
func migrationStatements(migration string) ([]string, error) {
	if migration == "" {
		return nil, fmt.Errorf("no migration given")
	}
	info, err := os.Stat(migration)
	switch {
	case err == nil && info.IsDir():
		entries, err := os.ReadDir(migration)
		if err != nil {
			return nil, err
		}
		// Bounded on purpose. Pointed at a mature migrations/ directory this read
		// and analyzed the project's ENTIRE history — every statement ever
		// written — and returned findings for all of it, in a tool designed to be
		// called mid-turn. An agent validating the migration it just wrote does
		// not want the last three years of them.
		//
		// Refusing beats truncating here: silently analyzing "some" of a
		// directory would produce a verdict about an arbitrary subset, and a
		// verdict over the wrong statements is worse than no verdict.
		var files []string
		for _, e := range entries {
			if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".sql") {
				files = append(files, e.Name())
			}
		}
		if len(files) > maxMigrationFiles {
			return nil, fmt.Errorf(
				"%s holds %d .sql files and validate_migration reads a directory whole; pass the single "+
					"migration file you are working on, or the SQL itself",
				migration, len(files))
		}
		var out []string
		for _, name := range files {
			b, err := os.ReadFile(filepath.Join(migration, name))
			if err != nil {
				return nil, err
			}
			out = append(out, validate.SplitStatements(string(b))...)
		}
		return out, nil
	case err == nil:
		b, err := os.ReadFile(migration)
		if err != nil {
			return nil, err
		}
		return validate.SplitStatements(string(b)), nil
	default:
		// Not a path — treat the argument as inline SQL.
		return validate.SplitStatements(migration), nil
	}
}
