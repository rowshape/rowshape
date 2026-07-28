package cmd

import (
	"fmt"
	"os"

	rsmcp "github.com/rowshape/rowshape/cmd/mcp"
	"github.com/spf13/cobra"
)

// newMCPCmd serves rowshape as a Model Context Protocol server over stdio, so an
// agent can reach rowshape's tools in its own turn — the wedge of PRD §8.2. It is
// a subcommand of the single binary, built on the official Go SDK (PRD §7).
func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run rowshape as an MCP server (stdio) for agents",
		Long: "mcp starts a Model Context Protocol server over stdio, exposing rowshape's\n" +
			"four tools (describe_shape, validate_migration, explain_finding,\n" +
			"plan_against) to an agent. Point your MCP client at `rowshape mcp`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Report the real build version in the handshake, not a constant.
			rsmcp.Version = version
			if err := rsmcp.Serve(cmd.Context()); err != nil {
				fmt.Fprintf(os.Stderr, "rowshape mcp: %v\n", err)
				return toolError()
			}
			return nil
		},
	}
}
