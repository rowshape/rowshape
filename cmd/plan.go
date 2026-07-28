package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/rowshape/rowshape/internal/plan"
	"github.com/rowshape/rowshape/internal/validate"
	"github.com/spf13/cobra"
)

// planOptions holds the flags for `rowshape plan`.
type planOptions struct {
	against    string
	migrations string
}

// newPlanCmd implements `rowshape plan --against <url>`: a dry-run diff of a
// migration against a LIVE target's current schema. It reads the target's
// structure read-only (INV-BLAST-RADIUS-ZERO) and reports what each statement
// would change — it never applies anything (PRD §8.1). The diff logic lives in
// internal/plan, shared with the plan_against MCP tool.
func newPlanCmd() *cobra.Command {
	opts := &planOptions{migrations: "migrations"}
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Dry-run diff of a migration against a live target (read-only, applies nothing)",
		Long: "plan reads a live target's current schema (read-only) and reports what\n" +
			"each migration statement would change against it — a dry run that applies\n" +
			"nothing. Point --against at the target and -m at a .sql file or directory.",
		RunE: func(c *cobra.Command, _ []string) error {
			return runPlan(c.Context(), opts)
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.against, "against", "", "live database URL to plan against (read-only)")
	f.StringVarP(&opts.migrations, "migrations", "m", opts.migrations, "migration .sql file or directory")
	_ = cmd.MarkFlagRequired("against")
	return cmd
}

func runPlan(ctx context.Context, opts *planOptions) error {

	stmts, err := migrationStatements(opts.migrations)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rowshape plan: %v\n", err)
		return toolError()
	}

	current, err := plan.ReadLiveSchema(ctx, opts.against)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rowshape plan: %v\n", err)
		return toolError()
	}

	writePlan(os.Stdout, opts.against, plan.Items(current, stmts))
	return nil
}

// writePlan renders the plan diff. It is purely descriptive — nothing is applied.
func writePlan(w io.Writer, against string, items []plan.Item) {
	fmt.Fprintf(w, "plan against %s (dry run — nothing is applied)\n\n", plan.RedactURL(against))
	if len(items) == 0 {
		fmt.Fprintln(w, "  no schema-changing statements")
		return
	}
	for _, it := range items {
		mark := "+"
		switch it.Status {
		case "conflict":
			mark = "!"
		case "missing-target":
			mark = "?"
		}
		fmt.Fprintf(w, "  %s %s\n", mark, it.Change)
		if it.Note != "" {
			fmt.Fprintf(w, "      %s\n", it.Note)
		}
	}
}

// migrationStatements reads a .sql file (or a directory's .sql files) and splits
// it into statements.
func migrationStatements(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if !info.IsDir() {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return validate.SplitStatements(string(b)), nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filePathExt(e.Name()), ".sql") {
			b, err := os.ReadFile(path + string(os.PathSeparator) + e.Name())
			if err != nil {
				return nil, err
			}
			out = append(out, validate.SplitStatements(string(b))...)
		}
	}
	return out, nil
}

func filePathExt(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i:]
	}
	return ""
}
