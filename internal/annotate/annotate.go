// Package annotate renders a verdict.Result into GitHub's PR surface: inline
// file/line annotations (workflow commands) and a Markdown check summary.
//
// It is NOT a separate formatter of the verdict (P4-T2): it reads the SAME
// verdict.Result struct the CLI and MCP server render (INV-VERDICT-SHAPE) and
// only maps its fields onto GitHub's two rendering channels. The mapping lives
// here, in a testable package, rather than in the frozen verdict contract
// package or in shell, so a bespoke bash JSON-parser never diverges from the
// struct.
package annotate

import (
	"fmt"
	"io"
	"strings"

	"github.com/rowshape/rowshape/internal/verdict"
)

// WriteAnnotations emits one GitHub workflow command per finding that carries a
// location, so it appears inline on the PR at the right file and line (PRD §10:
// location is an object {file, line}, parsed — not a string). A finding without
// a location cannot be placed inline and is intentionally omitted here; it still
// appears in the summary (WriteSummary). Severity maps to the command level:
// error -> ::error, warn -> ::warning, info -> ::notice.
func WriteAnnotations(w io.Writer, r verdict.Result) {
	for _, f := range r.Findings {
		if f.Location == nil {
			continue
		}
		level := commandLevel(f.Severity)
		msg := annotationMessage(f)
		// Properties: file, line, title. GitHub renders `title` as the
		// annotation heading; the code belongs there so it is scannable.
		fmt.Fprintf(w, "::%s file=%s,line=%d,title=%s::%s\n",
			level,
			escapeProperty(f.Location.File),
			f.Location.Line,
			escapeProperty(f.Code),
			escapeData(msg),
		)
	}
}

// annotationMessage is the human line an annotation carries: the title, the
// remediation (mandatory on every error — INV-VERDICT-STABLE), and the duration
// bucket when one was extrapolated. Reads only verdict.Result fields.
func annotationMessage(f verdict.Finding) string {
	var b strings.Builder
	b.WriteString(f.Title)
	if f.Estimate != nil && f.Estimate.Bucket != "" {
		fmt.Fprintf(&b, " (duration: %s)", f.Estimate.Bucket)
	}
	if f.Remediation != "" {
		b.WriteString("\n\n")
		b.WriteString(f.Remediation)
	}
	if f.Explain != "" {
		fmt.Fprintf(&b, "\n\n%s", f.Explain)
	}
	return b.String()
}

// WriteSummary writes the check-run summary in Markdown: the overall verdict,
// the fixture it was computed against, and a table of every finding with its
// code, severity, estimate bucket, and remediation. This is what a reviewer
// reads in the checks tab; every field comes from verdict.Result.
func WriteSummary(w io.Writer, r verdict.Result) {
	// The verdict word and the fixture id/digest flow from a verdict JSON that may
	// be untrusted (a committed fixture's meta.id, or a piped verdict). The step
	// summary is rendered as Markdown, so an id like "x`\n\n## rowshape: PASS" would
	// break out of its code span and inject a spoofed heading. Neutralize the
	// structure-breaking characters before embedding — a real verdict/id/digest has
	// none.
	fmt.Fprintf(w, "## rowshape: %s\n\n", inlineText(verdictWord(r.Verdict)))
	if r.Fixture.ID != "" || r.Fixture.Digest != "" {
		fmt.Fprintf(w, "Fixture `%s`", inlineCode(r.Fixture.ID))
		if d := shortDigest(r.Fixture.Digest); d != "" {
			fmt.Fprintf(w, " (`%s`)", inlineCode(d))
		}
		fmt.Fprintf(w, " · %dms\n\n", r.DurationMs)
	}

	if len(r.Findings) == 0 {
		fmt.Fprintf(w, "No findings.\n")
		return
	}

	fmt.Fprintf(w, "| Code | Severity | Estimate | Remediation |\n")
	fmt.Fprintf(w, "| --- | --- | --- | --- |\n")
	for _, f := range r.Findings {
		fmt.Fprintf(w, "| %s | %s | %s | %s |\n",
			cell(f.Code),
			cell(f.Severity),
			cell(estimateCell(f)),
			cell(f.Remediation),
		)
	}
}

func estimateCell(f verdict.Finding) string {
	if f.Estimate == nil || f.Estimate.Bucket == "" {
		return "—"
	}
	return f.Estimate.Bucket
}

func commandLevel(severity string) string {
	switch severity {
	case verdict.SeverityError:
		return "error"
	case verdict.SeverityWarn:
		return "warning"
	default:
		return "notice"
	}
}

func verdictWord(v string) string {
	switch v {
	case verdict.VerdictPass, verdict.VerdictWarn, verdict.VerdictFail:
		return v
	case "":
		return "no verdict (tool error)"
	default:
		return v
	}
}

func shortDigest(d string) string {
	const n = 12
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) > n {
		return d[:n]
	}
	return d
}

// cell renders a value into a Markdown table cell: newlines become spaces and a
// literal pipe is escaped, so a multi-line remediation cannot break the table.
//
// CR-T26 asked for backticks and other inline markdown to be escaped too. That
// was investigated and DELIBERATELY NOT DONE — see D-013. In short: cell() is
// applied to Code, Severity, Estimate and Remediation, none of which is
// free-form user text, and the Remediation strings in the finding catalog
// contain backticks ON PURPOSE so commands render as inline code in the PR
// summary. Escaping them would print literal backslashes to reviewers and
// protect against nothing. Only the two structure-breaking characters are
// escaped, which is what the table actually needs.
func cell(s string) string {
	if s == "" {
		return "—"
	}
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	return s
}

// inlineText neutralizes the characters that would let a value break out of a
// single Markdown line (and thus inject a heading/list/table of its own). Used
// for values interpolated into the summary heading.
func inlineText(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

// inlineCode neutralizes a value embedded inside a Markdown inline-code span: a
// backtick would close the span and a newline would end it, so an id or digest
// could otherwise escape into arbitrary Markdown. A legitimate fixture id/digest
// contains neither.
func inlineCode(s string) string {
	s = strings.ReplaceAll(s, "`", "'")
	return inlineText(s)
}

// escapeData escapes a GitHub workflow-command message per the documented rules.
func escapeData(s string) string {
	s = strings.ReplaceAll(s, "%", "%25")
	s = strings.ReplaceAll(s, "\r", "%0D")
	s = strings.ReplaceAll(s, "\n", "%0A")
	return s
}

// escapeProperty escapes a workflow-command property value (file/title), which
// additionally must escape ':' and ','.
func escapeProperty(s string) string {
	s = strings.ReplaceAll(s, "%", "%25")
	s = strings.ReplaceAll(s, "\r", "%0D")
	s = strings.ReplaceAll(s, "\n", "%0A")
	s = strings.ReplaceAll(s, ":", "%3A")
	s = strings.ReplaceAll(s, ",", "%2C")
	return s
}
