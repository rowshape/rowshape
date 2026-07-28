package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rowshape/rowshape/internal/plan"
	"github.com/rowshape/rowshape/internal/toolerror"
)

// plan_against tells an agent what a migration would change on a live target
// (PRD §8.2). It reuses the SAME diff path as the `rowshape plan --against` CLI
// (internal/plan) — no reimplementation — and is strictly read-only: it reads the
// target's schema inside a read-only transaction and applies nothing (PRD §11,
// INV-BLAST-RADIUS-ZERO).

// planOutput is the compact dry-run diff returned to the agent.
type planOutput struct {
	Target  string      `json:"target"`  // credentials redacted
	Applied bool        `json:"applied"` // always false — plan applies nothing
	Items   []plan.Item `json:"items"`
	Note    string      `json:"note"`
}

// handlePlanAgainst implements the plan_against tool.
func handlePlanAgainst(ctx context.Context, _ *sdk.CallToolRequest, in planAgainstInput) (*sdk.CallToolResult, any, error) {
	if in.Target == "" {
		return errorText(toolerror.BadUsage, "no target given", "pass a live database URL to diff against (read-only; nothing is applied)"), nil, nil
	}
	stmts, err := migrationStatements(in.Migration)
	if err != nil {
		return errorText(toolerror.BadUsage, err.Error(), "point `migration` at a .sql file or a directory of them"), nil, nil
	}

	current, err := plan.ReadLiveSchema(ctx, in.Target)
	if err != nil {
		return errorText(toolerror.ConnectFailed, err.Error(), "check the target URL is reachable and the role can read the catalog"), nil, nil
	}

	out := planOutput{
		Target:  plan.RedactURL(in.Target),
		Applied: false,
		Items:   plan.Items(current, stmts),
		Note:    "dry run — nothing was applied; the target was read read-only.",
	}
	return textResult("plan against " + out.Target + " (dry run — nothing applied)"), out, nil
}
