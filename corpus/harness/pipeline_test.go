package harness

import (
	"context"
	"os"
	"strings"

	// Registers the RS-* analyzers with the validate pipeline.
	_ "github.com/rowshape/rowshape/internal/findings"
	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/hydrate"
	"github.com/rowshape/rowshape/internal/target"
	"github.com/rowshape/rowshape/internal/validate"
)

// init wires the corpus validator to the real `validate` pipeline when a
// Postgres admin connection is available (ROWSHAPE_TEST_PG_DSN). Without it the
// corpus runs only its offline checks (well-formedness, coverage, capping
// contract) and TestCorpusVerdicts skips — so the PG-version matrix (P2-T13) is
// exactly `go test ./corpus/...` with the DSN set for each major.
func init() {
	if dsn := os.Getenv("ROWSHAPE_TEST_PG_DSN"); dsn != "" {
		validator = pipelineValidator{adminDSN: dsn}
	}
}

// pipelineValidator hydrates a disposable database from a corpus fixture, applies
// the migration through the real capture path, and runs the registered analyzers
// — the same code `rowshape validate` runs. The fixture's engine version is
// overridden to the major under test so version-conditional findings are
// exercised per major (RFC §9.1, PRD §12).
type pipelineValidator struct{ adminDSN string }

func (p pipelineValidator) Validate(c Case) (string, []ProducedFinding, error) {
	return p.validateFiles(c.Fixture, validate.SplitStatementsIn(c.Name+"/migration.sql", c.Migration))
}

// validateFiles is Validate for a migration set that may span several files —
// the statements carry their file, which is what tells the analyzers where one
// migration's transaction ends and the next begins.
func (p pipelineValidator) validateFiles(fx *fixture.Fixture, stmts []validate.Located) (string, []ProducedFinding, error) {
	ctx := context.Background()
	c := Case{Fixture: fx}

	// Exercise the analyzers as if the fixture came from the major under test.
	c.Fixture.Meta.Engine.Version = PGMajor()

	eph, err := target.NewEphemeral(ctx, p.adminDSN)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = eph.Close(ctx) }()

	// Small hydration: findings extrapolate from DECLARED rows, so a cap keeps CI
	// fast without changing any verdict.
	report, err := target.Load(ctx, eph, c.Fixture, hydrate.Options{Scale: 1.0, MaxRows: 500})
	if err != nil {
		return "", nil, err
	}

	conn, err := eph.Connect(ctx)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = conn.Close(ctx) }()

	// gen_random_uuid() is core only on PG 13+; provide it via pgcrypto so the
	// volatile-default corpus case applies on every major.
	_, _ = conn.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS pgcrypto")

	cap := validate.Apply(ctx, conn, stmts)
	cap.TableRows = report.Tables

	res := validate.BuildResult(c.Fixture, cap, validate.Registered(), false)

	produced := make([]ProducedFinding, 0, len(res.Findings))
	for _, f := range res.Findings {
		produced = append(produced, ProducedFinding{
			Code:        codeFamily(f.Code),
			FullCode:    f.Code,
			Severity:    f.Severity,
			Remediation: f.Remediation,
		})
	}
	return res.Verdict, produced, nil
}

// codeFamily reduces a full finding code to its stable family, which is what the
// corpus expected.json names (e.g. "RS-LOCK-001" -> "RS-LOCK").
func codeFamily(code string) string {
	parts := strings.Split(code, "-")
	if len(parts) >= 2 {
		return parts[0] + "-" + parts[1]
	}
	return code
}
