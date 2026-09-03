package findings

import (
	"fmt"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/validate"
	"github.com/rowshape/rowshape/internal/verdict"
)

func init() { validate.Register(rsApply{}) }

// rsApply reports the statement that stopped the migration (RS-APPLY-001).
//
// Every other analyzer reads a migration that RAN and names a hazard in it. This
// one covers the case the verdict contract said nothing about: the migration did
// not run. `validate` floored such a capture to FAIL and emitted no finding at
// all, so `--json` returned `{"verdict":"FAIL","findings":null}` — no code, no
// location, no remediation — with the engine's message going to stderr only.
//
// That breaks the wedge directly. The MCP `validate_migration` tool and the
// GitHub Action both render the Verdict struct and nothing else, so an agent was
// told FAIL and handed nothing to act on — precisely the hand-waving the P4-T8
// agent-rule harness scores against. INV-VERDICT-STABLE also requires remediation
// on every error, and this path had none.
//
// It is the most common real failure there is: a migration with a typo.
type rsApply struct{}

// The fixture is unused: this finding reads only what the database DID. The
// parameter is part of the Analyzer interface every other analyzer shares.
func (rsApply) Analyze(_ *fixture.Fixture, c *validate.Capture) []verdict.Finding {
	if c == nil || c.Success {
		return nil
	}
	st := c.FailedStatement()
	if st == nil {
		// Floored to FAIL with nothing identifiable behind it. A bare FAIL is more
		// honest than a finding pointing at a statement this cannot name.
		return nil
	}

	fnd := verdict.Finding{
		Code:     "RS-APPLY-001",
		Severity: verdict.SeverityError,
		Title:    "Migration did not apply: " + st.ErrMsg,
		Detail: fmt.Sprintf("The database rejected this statement with SQLSTATE %s: %s. Nothing after it was evaluated, so this verdict describes the failure and not the migration's other effects.",
			st.ErrCode, st.ErrMsg),
		Evidence: map[string]any{
			"sqlstate": st.ErrCode,
			"message":  st.ErrMsg,
			"sql":      st.SQL,
		},
		// No DependsOn: this rests on what the database DID, not on a fixture fact.
		// Declaring one would be false provenance in a DSSE-signed document — the
		// mistake RS-CONSTRAINT-010 made when it borrowed the row count's confidence
		// for a claim the row count does not support.
		Remediation: remediation("RS-APPLY-001"),
		Explain:     "rowshape explain RS-APPLY-001",
	}
	// The location the capture already knows, so an editor and the Action's
	// annotation step can both point at the offending line.
	if st.File != "" {
		fnd.Location = &verdict.Location{File: st.File, Line: st.Line}
	}
	return []verdict.Finding{fnd}
}
