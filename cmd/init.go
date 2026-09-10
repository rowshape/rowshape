package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// configFile is the scaffolded config filename.
const configFile = "rowshape.toml"

// newInitCmd detects the migration stack and scaffolds a committable rowshape
// config file (PRD §8.1). Detection is offline — init never connects to a
// database and never runs a migration. `init --agent` (P3-T8+) extends this base.
func newInitCmd() *cobra.Command {
	var force bool
	var agent bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Scaffold rowshape config in the current repo (offline detection only)",
		// The meta description for the generated docs page. Deliberately longer
		// than Short: Short is a line of --help output, this is the sentence a
		// search result shows. See docsDescription in tools/gencli.
		Annotations: map[string]string{
			"docs.description": "Detect your database engine and migration runner from the repo layout and write a starter rowshape.toml. No network, no database connection.",
		},
		Long: "init detects your database engine and migration runner from the repo\n" +
			"layout and writes a starter " + configFile + ". It makes no network or\n" +
			"database connection. Re-running it leaves an existing config untouched\n" +
			"unless you pass --force.\n\n" +
			"NOTE: " + configFile + " is currently a RECORD OF WHAT WAS DETECTED, not a\n" +
			"settings file. No rowshape command reads it yet — every option is passed as\n" +
			"a flag. Editing it will not change any behaviour.\n\n" +
			"--agent additionally wires this repo for coding agents: it registers\n" +
			"`rowshape mcp` in the MCP config of every detected client (.mcp.json for\n" +
			"Claude Code, .cursor/mcp.json, .vscode/mcp.json). It merges into existing\n" +
			"config and is safe to re-run.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return newInitAgentRunner(&agent, &force)()
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "regenerate an existing "+configFile)
	cmd.Flags().BoolVar(&agent, "agent", false, "also wire this repo for coding agents (MCP client config)")
	return cmd
}

// runInit scaffolds the config in dir. It is idempotent: an existing config is
// left untouched (preserving the user's edits) unless force is set.
func runInit(dir string, force bool) error {
	path := filepath.Join(dir, configFile)
	if _, err := os.Stat(path); err == nil && !force {
		// Idempotent re-run: never clobber the user's edits.
		fmt.Fprintf(os.Stderr, "rowshape init: %s already exists; leaving it untouched (use --force to regenerate)\n", configFile)
		return nil
	}

	stack := detectStack(dir)
	if err := os.WriteFile(path, []byte(scaffoldConfig(stack)), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "rowshape init: writing %s failed: %v\n", configFile, err)
		return toolError()
	}
	fmt.Fprintf(os.Stderr, "rowshape init: wrote %s (engine: %s, runner: %s)\n", configFile, stack.Engine, runnerLabel(stack.Runner))
	return nil
}

// scaffoldConfig renders the starter config with the detected engine and runner
// filled in. It carries no secrets — the connection comes from libpq env vars or
// a URL passed to `rowshape pull`.
func scaffoldConfig(stack detectedStack) string {
	runner := stack.Runner
	runnerComment := ""
	if runner == "" {
		runner = "unknown"
		runnerComment = "  # not detected — set to alembic | prisma | drizzle | rawsql"
	}
	return `# rowshape configuration — https://rowshape.com
#
# NOT YET READ BY ANY COMMAND. This file records what ` + "`rowshape init`" + ` detected
# from your repo layout, so it is useful to commit and to read — but no rowshape
# command loads it today, and editing a value here changes nothing. Every option
# below is passed as a flag instead (see ` + "`rowshape <command> --help`" + `).
# The values are shown so you can see what was detected and what the defaults are.
#
# Commit this file; it carries no secrets. The database connection comes from the
# standard libpq environment variables (PGHOST, PGUSER, ...) or a URL passed to
# ` + "`rowshape pull`" + `. init detected this offline, from the repo layout only.

# Database engine (v1 targets Postgres).
engine = "` + stack.Engine + `"

# Privacy level for pull: strict | standard | permissive.
# strict emits no numeric/temporal ranges, histograms, values, or verbatim CHECK
# expressions (RFC-0001 §8). Never commit a fixture you have not reviewed.
privacy = "standard"

# Where pull writes the fixture.
out = "rowshape.yaml"

[pull]
# Restrict profiling to specific schemas (default: all non-system schemas).
# schemas = ["public"]

[migrations]
# Your migration runner. rowshape orchestrates it; it does not reimplement it.
runner = "` + runner + `"` + runnerComment + `
`
}
