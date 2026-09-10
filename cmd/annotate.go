package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/rowshape/rowshape/internal/annotate"
	"github.com/rowshape/rowshape/internal/toolerror"
	"github.com/rowshape/rowshape/internal/verdict"
	"github.com/spf13/cobra"
)

// newAnnotateCmd renders a JSON verdict into GitHub's PR surface: inline
// file/line annotations to stdout (workflow commands) and a Markdown check
// summary to $GITHUB_STEP_SUMMARY (or stdout when unset). It is the rendering
// half of the GitHub Action (P4-T2): it consumes the machine-readable Verdict
// that `validate --json` emits and reuses the verdict.Result struct — no bespoke
// formatter (INV-VERDICT-SHAPE). It never re-runs validate and reveals no new
// facts; it is a pure rendering step.
func newAnnotateCmd() *cobra.Command {
	var summaryPath string
	cmd := &cobra.Command{
		Use:   "annotate [verdict.json]",
		Short: "Render a JSON verdict as GitHub PR annotations + a check summary",
		// The meta description for the generated docs page. Deliberately longer
		// than Short: Short is a line of --help output, this is the sentence a
		// search result shows. See docsDescription in tools/gencli.
		Annotations: map[string]string{
			"docs.description": "Render a rowshape JSON verdict as GitHub pull-request annotations and a Markdown check summary, inline on the failing lines.",
		},
		Long: "annotate reads a JSON verdict (from `rowshape validate --json`) on a\n" +
			"file argument or stdin and renders it into GitHub's PR surface:\n" +
			"inline file/line annotations on stdout and a Markdown check summary to\n" +
			"$GITHUB_STEP_SUMMARY. It is used by the rowshape GitHub Action; the\n" +
			"annotations are derived from the Verdict struct, not a separate formatter.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			var path string
			if len(args) == 1 {
				path = args[0]
			}
			if summaryPath == "" {
				summaryPath = os.Getenv("GITHUB_STEP_SUMMARY")
			}
			return runAnnotate(path, summaryPath)
		},
	}
	cmd.Flags().StringVar(&summaryPath, "summary", "", "write the Markdown check summary here (default $GITHUB_STEP_SUMMARY, else stdout)")
	return cmd
}

func runAnnotate(verdictPath, summaryPath string) error {
	data, err := readVerdictInput(verdictPath)
	if err != nil {
		return emitToolError(false, toolerror.New(toolerror.BadUsage, err.Error(), "pass a JSON verdict file or pipe `rowshape validate --json` into annotate"))
	}

	// A tool-error payload unmarshals CLEANLY into verdict.Result — every field
	// it looks for is simply absent — so without this check annotate rendered a
	// check summary headed by an empty verdict word. The payload marks itself
	// with `"error":"tool_error"` for exactly this reason (see internal/toolerror:
	// "a consumer branching on the JSON can tell them apart without heuristics"),
	// so branch on it rather than inferring from a missing field.
	var probe struct {
		Error    string `json:"error"`
		Category string `json:"category"`
		Message  string `json:"message"`
		Hint     string `json:"hint"`
	}
	if err := json.Unmarshal(data, &probe); err == nil && probe.Error == toolerror.Kind {
		msg := probe.Message
		if msg == "" {
			msg = "rowshape could not produce a verdict"
		}
		hint := probe.Hint
		if hint == "" {
			hint = "annotate renders a verdict; this input reports that validate could not produce one"
		}
		cat := probe.Category
		if cat == "" {
			cat = string(toolerror.Internal)
		}
		return emitToolError(false, toolerror.New(toolerror.Category(cat), msg, hint))
	}

	var r verdict.Result
	if err := json.Unmarshal(data, &r); err != nil {
		return emitToolError(false, toolerror.New(toolerror.BadUsage, "input is not a JSON verdict: "+err.Error(), "annotate consumes the output of `rowshape validate --json`"))
	}
	// A document that is neither a verdict nor a tool error should not render as
	// an empty-verdict summary either.
	if r.Verdict == "" {
		return emitToolError(false, toolerror.New(toolerror.BadUsage, "input has no verdict field", "annotate consumes the output of `rowshape validate --json`"))
	}

	annotate.WriteAnnotations(os.Stdout, r)

	if summaryPath != "" {
		f, err := os.OpenFile(summaryPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return emitToolError(false, toolerror.New(toolerror.Internal, "could not open the summary file: "+err.Error(), ""))
		}
		defer func() { _ = f.Close() }()
		annotate.WriteSummary(f, r)
		if err := f.Close(); err != nil {
			return emitToolError(false, toolerror.New(toolerror.Internal, "writing the summary failed: "+err.Error(), ""))
		}
	} else {
		annotate.WriteSummary(os.Stdout, r)
	}
	return nil
}

// readVerdictInput reads the verdict JSON from a file, or from stdin when no
// path is given.
func readVerdictInput(path string) ([]byte, error) {
	if path == "" {
		return io.ReadAll(os.Stdin)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s failed: %w", path, err)
	}
	return b, nil
}
