package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rowshape/rowshape/internal/findings"
	"github.com/rowshape/rowshape/internal/toolerror"
)

// explain_finding is remediation without a web search (PRD §8.2): an agent that
// got a compact code back from validate_migration expands exactly the finding it
// hit. Content comes from the same catalog `rowshape explain <CODE>` renders
// (internal/findings), so the CLI and the tool can never drift, and remediation
// is mandatory on every code (PRD §10).
func handleExplainFinding(_ context.Context, _ *sdk.CallToolRequest, in explainFindingInput) (*sdk.CallToolResult, any, error) {
	code := strings.ToUpper(strings.TrimSpace(in.Code))
	e, ok := findings.Explain(code)
	if !ok {
		return errorText(toolerror.BadUsage, fmt.Sprintf("unknown finding code %q", code), "known codes: "+strings.Join(findings.Codes(), ", ")), nil, nil
	}
	summary := fmt.Sprintf("%s — %s\nRemediation: %s", e.Code, e.Title, e.Remediation)
	return textResult(summary), e, nil
}
