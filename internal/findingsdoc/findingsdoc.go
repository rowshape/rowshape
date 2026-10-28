// Package findingsdoc renders the finding catalog as Starlight Markdown pages,
// sourced from internal/findings — the SAME catalog `rowshape explain` reads and
// the analyzers cite for remediation (PRD §8.1, §10). Generating the docs from
// that one catalog is what makes the acceptance criterion — every RS-* page's
// remediation matches `rowshape explain <CODE>` — true by construction rather
// than by hand-copying that drifts.
package findingsdoc

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rowshape/rowshape/internal/findings"
)

// PageFile is the Starlight content filename for a finding code, e.g.
// "rs-lock-001.md". Codes are lowercased so URLs are conventional.
func PageFile(code string) string {
	return strings.ToLower(code) + ".md"
}

// Namespace is the RS-<CLASS> prefix of a code (e.g. "RS-LOCK"), used to group
// the index. A code without two dashes returns itself.
func Namespace(code string) string {
	i := strings.LastIndex(code, "-")
	if i <= 0 {
		return code
	}
	return code[:i]
}

// Prose makes catalog text safe to drop into Markdown as a paragraph. The
// catalog writes placeholders the way a terminal reader expects — `<table>`,
// `<name>`, `<predicate>` — and Markdown passes those through as raw HTML: the
// browser drew an empty <table> on RS-DEPLOY-002 and swallowed the others as
// unknown elements, so the page read "tablename = ''". Escaping `<` keeps the
// rendered text identical to `rowshape explain`, which is what readers compare.
func Prose(s string) string { return strings.ReplaceAll(s, "<", "&lt;") }

// Page renders one finding's catalog page. The remediation is copied from the
// Explanation verbatim (through Prose), so it renders identically to
// `rowshape explain <code>`.
func Page(e findings.Explanation) string {
	var b strings.Builder
	// Frontmatter. Values are single-quoted and internal quotes doubled so a
	// title or summary with punctuation cannot break the YAML.
	fmt.Fprintf(&b, "---\n")
	fmt.Fprintf(&b, "title: %s\n", yamlQuote(e.Code+" — "+e.Title))
	fmt.Fprintf(&b, "description: %s\n", yamlQuote(firstSentence(e.Summary)))
	fmt.Fprintf(&b, "---\n\n")

	fmt.Fprintf(&b, "**Namespace:** `%s` · **Code:** `%s`\n\n", Namespace(e.Code), e.Code)
	fmt.Fprintf(&b, "%s\n\n", Prose(e.Summary))

	fmt.Fprintf(&b, "## Remediation\n\n%s\n\n", Prose(e.Remediation))

	if len(e.References) > 0 {
		fmt.Fprintf(&b, "## References\n\n")
		for _, r := range e.References {
			fmt.Fprintf(&b, "- %s\n", r)
		}
		fmt.Fprintf(&b, "\n")
	}

	fmt.Fprintf(&b, "---\n\n")
	fmt.Fprintf(&b, "This page is generated from the same catalog `rowshape explain %s` reads, "+
		"so the remediation here is byte-identical to the one a verdict carries — they cannot drift. "+
		"An agent can read it with the `explain_finding` MCP tool.\n", e.Code)
	return b.String()
}

// namespaceConcern is the one-line description of each finding class, matching
// the intro table.
var namespaceConcern = map[string]string{
	"RS-LOCK":       "Locks a migration takes, and for how long",
	"RS-DATA":       "Existing data that contradicts the change",
	"RS-CONSTRAINT": "Constraints that cannot be added or validated",
	"RS-INDEX":      "Index builds that fail or block",
	"RS-PERF":       "Rewrites and scans that cost more than they look like they do",
	"RS-REVERSE":    "Changes that cannot be safely reversed",
}

// Index renders the finding-catalog landing page: the intro, the namespace
// table, and a generated list of every code grouped by namespace with links to
// its page. Codes are the full set from the catalog (findings.Codes()).
func Index(catalog map[string]findings.Explanation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "---\n")
	fmt.Fprintf(&b, "title: Finding catalog\n")
	fmt.Fprintf(&b, "description: Every finding code rowshape can return, what it means, and how to fix it.\n")
	fmt.Fprintf(&b, "---\n\n")

	fmt.Fprintf(&b, "Every finding rowshape returns carries a permanent, namespaced code, and every\n"+
		"error-severity finding carries remediation. A finding an agent cannot act on is a\n"+
		"bug.\n\n")

	fmt.Fprintf(&b, "You can read any of this from the CLI with `rowshape explain <CODE>`, or from an\n"+
		"agent with the `explain_finding` MCP tool. These pages are generated from that\n"+
		"same catalog, so they never drift from what the tool returns.\n\n")

	// Group codes by namespace, namespaces and codes each sorted.
	byNS := map[string][]string{}
	for code := range catalog {
		ns := Namespace(code)
		byNS[ns] = append(byNS[ns], code)
	}
	nsList := make([]string, 0, len(byNS))
	for ns := range byNS {
		nsList = append(nsList, ns)
	}
	sort.Strings(nsList)

	for _, ns := range nsList {
		concern := namespaceConcern[ns]
		if concern != "" {
			fmt.Fprintf(&b, "## %s — %s\n\n", ns, concern)
		} else {
			fmt.Fprintf(&b, "## %s\n\n", ns)
		}
		codes := byNS[ns]
		sort.Strings(codes)
		for _, code := range codes {
			e := catalog[code]
			slug := strings.TrimSuffix(PageFile(code), ".md")
			fmt.Fprintf(&b, "- [`%s`](./%s/) — %s\n", code, slug, e.Title)
		}
		fmt.Fprintf(&b, "\n")
	}
	return b.String()
}

// Catalog returns the full catalog keyed by code, from the findings package.
func Catalog() map[string]findings.Explanation {
	out := map[string]findings.Explanation{}
	for _, code := range findings.Codes() {
		if e, ok := findings.Explain(code); ok {
			out[code] = e
		}
	}
	return out
}

func firstSentence(s string) string {
	if i := strings.IndexByte(s, '.'); i > 0 {
		return s[:i+1]
	}
	return s
}

// yamlQuote single-quotes a scalar for YAML frontmatter, doubling any embedded
// single quote (YAML's escape for single-quoted scalars).
func yamlQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
