package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/verdict"
	"github.com/spf13/cobra"
)

// newInspectCmd audits a committed fixture. `--leaks` enumerates every field
// derived from row values with its source and privacy level (RFC §8.3, PRD §11).
func newInspectCmd() *cobra.Command {
	var leaks bool
	var failOnLeak bool
	var size bool
	cmd := &cobra.Command{
		Use:   "inspect [rowshape.yaml]",
		Short: "Audit a committed fixture",
		Long: "inspect --leaks enumerates every field in a fixture derived from row\n" +
			"values — numeric/temporal ranges, histogram bounds, value sets and\n" +
			"frequencies, verbatim CHECK expressions, and free-text max length — with\n" +
			"its source column and the privacy level at which it appears (RFC §8.3).\n\n" +
			"Pass --fail-on-leak to exit non-zero when any value-derived field is\n" +
			"present. Run it against a strict fixture in CI to assert full redaction.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "rowshape.yaml"
			if len(args) == 1 {
				path = args[0]
			}
			if size {
				return runInspectSize(cmd, path)
			}
			if !leaks {
				return fmt.Errorf("nothing to inspect: pass --leaks or --size")
			}
			return runInspectLeaks(cmd, path, failOnLeak)
		},
	}
	cmd.Flags().BoolVar(&leaks, "leaks", false, "enumerate every value-derived field in the fixture")
	cmd.Flags().BoolVar(&failOnLeak, "fail-on-leak", false, "exit non-zero if any value-derived field is present (for CI)")
	cmd.Flags().BoolVar(&size, "size", false, "report the fixture's size against the committable budget (RFC §3.3)")
	return cmd
}

// runInspectSize reports a fixture's size against the §3.3 committable budget.
//
// The budget is quoted for a fixed table count, so a fixture of any other size is
// compared on its PER-TABLE cost scaled to that count — otherwise a 10-table
// fixture would always look comfortable and a 500-table one always damning,
// regardless of how heavy the emitter had become.
func runInspectSize(cmd *cobra.Command, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rowshape inspect: reading %s failed: %v\n", path, err)
		return toolError()
	}
	f, err := fixture.ParseVerified(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rowshape inspect: %v\n", err)
		return toolError()
	}

	out := cmd.OutOrStdout()
	tables := len(f.Tables)
	if tables == 0 {
		fmt.Fprintf(out, "%s is %d bytes and declares no tables.\n", path, len(data))
		return nil
	}
	perTable := len(data) / tables
	projected := perTable * fixture.BudgetTables

	fmt.Fprintf(out, "%s: %d bytes across %d tables (%d bytes/table)\n", path, len(data), tables, perTable)
	fmt.Fprintf(out, "projected at %d tables: %d bytes against a budget of %d\n",
		fixture.BudgetTables, projected, fixture.BudgetBytes)
	if fixture.OverBudget(len(data), tables) {
		fmt.Fprintln(out, "OVER BUDGET — narrow it with `pull --schema`, or split the schema across fixtures, so it stays reviewable in a diff (RFC §3.3)")
		return nil
	}
	fmt.Fprintln(out, "within budget")
	return nil
}

func runInspectLeaks(cmd *cobra.Command, path string, failOnLeak bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rowshape inspect: reading %s failed: %v\n", path, err)
		return toolError()
	}
	f, err := fixture.ParseVerified(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rowshape inspect: %v\n", err)
		return toolError()
	}

	found := fixture.Leaks(f)
	out := cmd.OutOrStdout()
	if len(found) == 0 {
		fmt.Fprintln(out, "No value-derived fields found: this fixture contains only structure and counts.")
		return nil
	}

	fmt.Fprintf(out, "%d value-derived field(s) in %s:\n\n", len(found), path)
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "LOCATION\tFIELD\tPRIVACY\tREVEALS")
	for _, l := range found {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", l.Location, l.Field, l.Privacy, l.Detail)
	}
	_ = tw.Flush()

	if failOnLeak {
		return &ExitError{Code: verdict.ExitFail}
	}
	return nil
}
