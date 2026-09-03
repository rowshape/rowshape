package annotate

import (
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/verdict"
)

func sampleResult() verdict.Result {
	return verdict.Result{
		Rowshape:   "1",
		Verdict:    verdict.VerdictFail,
		Fixture:    verdict.FixtureRef{ID: "orders-2026", Digest: "sha256:abcdef0123456789"},
		DurationMs: 421,
		Findings: []verdict.Finding{
			{
				Code:        "RS-LOCK-001",
				Severity:    verdict.SeverityError,
				Title:       "SET NOT NULL takes ACCESS EXCLUSIVE and rewrites the table",
				Location:    &verdict.Location{File: "db/migrations/003_email.sql", Line: 12},
				Estimate:    &verdict.Estimate{Bucket: verdict.BucketOutage},
				Remediation: "Add the column nullable, backfill in batches, then SET NOT NULL via a validated CHECK.",
				Explain:     "rowshape explain RS-LOCK-001",
			},
			{
				// No location: cannot be placed inline, but must appear in the summary.
				Code:        "RS-DATA-002",
				Severity:    verdict.SeverityWarn,
				Title:       "column is not provably unique",
				Remediation: "Run `rowshape pull --exact` to prove uniqueness.",
			},
		},
	}
}

// TestAnnotationsMapLocation: each finding with a location produces an
// annotation at the right file and line, and only those with a location.
func TestAnnotationsMapLocation(t *testing.T) {
	var b strings.Builder
	WriteAnnotations(&b, sampleResult())
	out := b.String()

	lines := nonEmptyLines(out)
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 annotation (only one finding has a location), got %d:\n%s", len(lines), out)
	}
	got := lines[0]
	for _, want := range []string{
		"::error ",
		"file=db/migrations/003_email.sql",
		"line=12",
		"title=RS-LOCK-001",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("annotation missing %q:\n%s", want, got)
		}
	}
	// The message carries the remediation (mandatory on errors) and the bucket.
	if !strings.Contains(got, "duration: outage") {
		t.Errorf("annotation should include the duration bucket:\n%s", got)
	}
	if !strings.Contains(got, "backfill in batches") {
		t.Errorf("annotation should include the remediation:\n%s", got)
	}
}

// TestAnnotationSeverityLevels maps severity onto the workflow command level.
func TestAnnotationSeverityLevels(t *testing.T) {
	cases := map[string]string{
		verdict.SeverityError: "::error ",
		verdict.SeverityWarn:  "::warning ",
		verdict.SeverityInfo:  "::notice ",
	}
	for sev, want := range cases {
		r := verdict.Result{Findings: []verdict.Finding{{
			Code: "RS-X-001", Severity: sev, Title: "t",
			Location: &verdict.Location{File: "a.sql", Line: 1}, Remediation: "fix",
		}}}
		var b strings.Builder
		WriteAnnotations(&b, r)
		if !strings.Contains(b.String(), want) {
			t.Errorf("severity %s -> want %q, got:\n%s", sev, want, b.String())
		}
	}
}

// TestAnnotationEscaping: newlines and command-significant characters in the
// message and properties are escaped so a multi-line remediation or a path with
// a colon cannot break the workflow command.
func TestAnnotationEscaping(t *testing.T) {
	r := verdict.Result{Findings: []verdict.Finding{{
		Code:        "RS-X-001",
		Severity:    verdict.SeverityError,
		Title:       "line one\nline two",
		Location:    &verdict.Location{File: "weird:name,1.sql", Line: 3},
		Remediation: "do a, then b",
	}}}
	var b strings.Builder
	WriteAnnotations(&b, r)
	out := strings.TrimSpace(b.String())

	// The raw newline from the title must not survive into the output as a real
	// newline (it would split the single workflow command into two lines).
	if len(nonEmptyLines(out)) != 1 {
		t.Fatalf("escaped annotation must be a single line, got:\n%q", out)
	}
	if !strings.Contains(out, "%0A") {
		t.Errorf("title newline should be escaped to %%0A:\n%s", out)
	}
	if !strings.Contains(out, "weird%3Aname%2C1.sql") {
		t.Errorf("file property should escape ':' and ',':\n%s", out)
	}
}

// TestSummaryListsEveryFinding: the check summary contains the verdict and one
// row per finding — including findings without a location — with code,
// severity, estimate bucket, and remediation.
func TestSummaryListsEveryFinding(t *testing.T) {
	var b strings.Builder
	WriteSummary(&b, sampleResult())
	out := b.String()

	for _, want := range []string{
		"rowshape: FAIL",
		"orders-2026",
		"abcdef012345", // short digest, sha256: prefix stripped
		"RS-LOCK-001", "error", "outage",
		"RS-DATA-002", "warn", // the location-less finding still appears
		"Run `rowshape pull --exact`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
	// The location-less finding has no estimate -> em dash, not a crash.
	if !strings.Contains(out, "| — |") {
		t.Errorf("expected an em-dash cell for the missing estimate:\n%s", out)
	}
}

// TestSummaryNoFindings renders a clean PASS without a table.
func TestSummaryNoFindings(t *testing.T) {
	var b strings.Builder
	WriteSummary(&b, verdict.Result{Verdict: verdict.VerdictPass, Fixture: verdict.FixtureRef{ID: "x"}})
	out := b.String()
	if !strings.Contains(out, "rowshape: PASS") || !strings.Contains(out, "No findings.") {
		t.Errorf("expected a PASS summary with no table, got:\n%s", out)
	}
	if strings.Contains(out, "| Code |") {
		t.Errorf("no-findings summary should not render a table:\n%s", out)
	}
}

// TestSummaryNeutralizesFixtureAndVerdictInjection: a crafted fixture id, digest,
// or verdict string must not break out of its Markdown context and inject headings
// into the rendered PR step summary. The id/digest sit in inline-code spans and
// the verdict in a heading; a backtick or newline previously escaped both.
func TestSummaryNeutralizesFixtureAndVerdictInjection(t *testing.T) {
	r := verdict.Result{
		Verdict: "FAIL\n\n## rowshape: PASS",
		Fixture: verdict.FixtureRef{
			ID:     "x`\n\n## rowshape: PASS\n\nAll checks passed",
			Digest: "sha256:d`e`f0123456789",
		},
	}
	var b strings.Builder
	WriteSummary(&b, r)
	out := b.String()

	// The only heading may be the one WriteSummary emits itself (at the very start
	// of the output). No value may inject a heading at a line start.
	if n := strings.Count(out, "\n## "); n != 0 {
		t.Errorf("injected heading rendered (%d '## ' at a line start):\n%s", n, out)
	}
	if !strings.HasPrefix(out, "## rowshape:") {
		t.Errorf("summary should open with the rowshape heading:\n%s", out)
	}
	// The "All checks passed" text smuggled in the id must stay inline on the
	// Fixture line, not become its own line/structure.
	if strings.Contains(out, "\nAll checks passed") {
		t.Errorf("id content escaped onto its own line:\n%s", out)
	}
	// The backtick in the id must have been neutralized (no stray span open).
	if strings.Contains(out, "x`") {
		t.Errorf("backtick in fixture id was not neutralized:\n%s", out)
	}
}

// TestSummaryCellEscapesPipe: a remediation containing a pipe must not break the
// Markdown table.
func TestSummaryCellEscapesPipe(t *testing.T) {
	r := verdict.Result{Verdict: verdict.VerdictWarn, Findings: []verdict.Finding{{
		Code: "RS-X-001", Severity: verdict.SeverityWarn, Title: "t",
		Remediation: "use a | b\nor c",
	}}}
	var b strings.Builder
	WriteSummary(&b, r)
	out := b.String()
	if !strings.Contains(out, `a \| b`) {
		t.Errorf("pipe in a cell should be escaped:\n%s", out)
	}
	if strings.Contains(out, "or c\n") && strings.Contains(out, "use a") {
		// newline collapsed to a space keeps it on one table row
		if strings.Contains(out, "b\nor c") {
			t.Errorf("newline in a cell should be collapsed to a space:\n%s", out)
		}
	}
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// --- CR-T26: what cell() escapes, and what it deliberately does not --------
//
// The story asked for backticks and inline markdown to be escaped. Investigated
// and declined (D-013): cell() is applied to Code, Severity, Estimate and
// Remediation — none free-form user text — and the catalog's Remediation strings
// contain backticks ON PURPOSE so commands render as inline code in the PR
// summary. Escaping them would show reviewers literal backslashes and protect
// against nothing.
//
// This test pins BOTH halves of that decision, so neither can be changed by
// accident: structure-breaking characters are escaped, authored markdown is not.
func TestCellEscapesStructureButPreservesAuthoredMarkdown(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		// Structure: these would break the table row itself.
		{"pipe is escaped", "a|b", `a\|b`},
		{"newline becomes a space", "a\nb", "a b"},
		{"crlf becomes a space", "a\r\nb", "a b"},
		{"empty renders as a dash", "", "—"},

		// Rendering: authored markdown must survive verbatim. The catalog writes
		// these deliberately; see internal/findings/registry.go.
		{"authored backticks survive", "Run `rowshape pull --exact` first.", "Run `rowshape pull --exact` first."},
		{"snake_case is left alone", "public.users.user_id", "public.users.user_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cell(tc.in); got != tc.want {
				t.Errorf("cell(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSummaryRendersRemediationCodeSpans is the regression this nearly caused:
// escaping backticks made the summary print `\`rowshape pull --exact\“ to
// reviewers instead of an inline code span.
func TestSummaryRendersRemediationCodeSpans(t *testing.T) {
	got := cell("Run `rowshape pull --exact` to prove uniqueness.")
	if strings.Contains(got, `\`) {
		t.Errorf("remediation must not gain backslashes; a reviewer would see them literally: %q", got)
	}
}
