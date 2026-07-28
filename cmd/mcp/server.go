// Package mcp runs rowshape as a Model Context Protocol server over stdio, so an
// agent can reach rowshape's tools inside its own turn — the wedge surface of
// PRD §8.2. It is a subcommand of the single rowshape binary, not a separate
// artifact, built on the official Go SDK (modelcontextprotocol/go-sdk, past
// v1.0.0 with a no-breaking-changes guarantee, PRD §7).
//
// This file wires the server: it registers exactly the four tools named in
// PRD §8.2 with brutally thin schemas (fat tool schemas cost tokens in every
// session — the MCPFold discipline) and advertises the fixture format major
// version it understands (RFC §12). Each tool's behavior lives in its own
// tool_*.go file.
package mcp

import (
	"context"
	"fmt"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rowshape/rowshape/internal/fixture"
)

// serverName identifies this server to a client in the handshake.
const serverName = "rowshape"

// Version is the build version reported in the MCP handshake.
//
// It was a hardcoded "0.1.0" while the CLI's version came from ldflags, so an
// MCP client's handshake advertised a version unrelated to the binary actually
// answering it — and an agent that logs or branches on the server version was
// reading a constant. `rowshape mcp` sets this from the same injected value
// `rowshape --version` reports; "dev" matches cmd's own default for a plain
// `go build`.
var Version = "dev"

// ToolNames are exactly the four tools the server exposes (PRD §8.2). The set is
// closed: the server registers these and no others.
var ToolNames = []string{"describe_shape", "validate_migration", "explain_finding", "plan_against"}

// instructions advertise, to a connecting client, the fixture format major this
// build understands — so a peer on a newer major knows to refuse rather than
// best-effort (RFC §12).
var instructions = fmt.Sprintf(
	"rowshape MCP server. Tools: describe_shape (the production shape before you write SQL), "+
		"validate_migration (the loop-closer), explain_finding (remediation, no web search), "+
		"plan_against (what a migration would change on a target). "+
		"This build understands rowshape_fixture format major version %q (RFC-0001 §12).",
	fixture.FormatVersion,
)

// describeShapeInput asks for the shape of a fixture, optionally one table.
// Schemas are kept minimal on purpose (PRD §8.2).
type describeShapeInput struct {
	Fixture string `json:"fixture" jsonschema:"path to the rowshape.yaml fixture"`
	Table   string `json:"table,omitempty" jsonschema:"optional qualified table name to restrict the shape to"`
}

// validateMigrationInput validates a migration against a fixture.
type validateMigrationInput struct {
	Fixture   string `json:"fixture" jsonschema:"path to the rowshape.yaml fixture"`
	Migration string `json:"migration" jsonschema:"path to the migration .sql file or directory"`
}

// explainFindingInput expands a finding code.
type explainFindingInput struct {
	Code string `json:"code" jsonschema:"a finding code, e.g. RS-LOCK-001"`
}

// planAgainstInput dry-runs a migration diff against a live target.
type planAgainstInput struct {
	Migration string `json:"migration" jsonschema:"path to the migration .sql file or directory"`
	Target    string `json:"target" jsonschema:"a live database URL to diff against (read-only)"`
}

// NewServer builds the MCP server with the four PRD §8.2 tools registered.
func NewServer() *sdk.Server {
	s := sdk.NewServer(
		&sdk.Implementation{Name: serverName, Version: Version, Title: "rowshape"},
		&sdk.ServerOptions{Instructions: instructions},
	)

	sdk.AddTool(s, &sdk.Tool{
		Name:        "describe_shape",
		Description: "Return the production shape (row counts, null fractions, cardinality, fan-out) an agent should read BEFORE writing a migration. Never returns a full fixture unless a specific table is asked for.",
	}, handleDescribeShape)

	sdk.AddTool(s, &sdk.Tool{
		Name:        "validate_migration",
		Description: "STATIC check against the committed fixture: same analyzers and Verdict shape as the CLI, but nothing is hydrated or applied, so there are no runtime facts. Run `rowshape validate` for hydrate-and-apply. Findings are codes; expand with explain_finding.",
	}, handleValidateMigration)

	sdk.AddTool(s, &sdk.Tool{
		Name:        "explain_finding",
		Description: "Return the documentation and remediation for a finding code — remediation without a web search.",
	}, handleExplainFinding)

	sdk.AddTool(s, &sdk.Tool{
		Name:        "plan_against",
		Description: "Dry-run diff: what a migration would change on a live target (read-only, applies nothing).",
	}, handlePlanAgainst)

	return s
}

// Serve runs the server over stdio until the context is cancelled or the client
// disconnects. This is what `rowshape mcp` invokes.
func Serve(ctx context.Context) error {
	return NewServer().Run(ctx, &sdk.StdioTransport{})
}
