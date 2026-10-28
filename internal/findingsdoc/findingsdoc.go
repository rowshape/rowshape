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

// Page renders one finding's catalog page. The remediation is copied from the
// Explanation verbatim, so it is byte-identical to `rowshape explain <code>`.
func Page(e findings.Explanation) string {
	var b strings.Builder
	// Frontmatter. Values are single-quoted and internal quotes doubled so a
	// title or summary with punctuation cannot break the YAML.
	fmt.Fprintf(&b, "---\n")
	meta, tuned := pageMetas[e.Code]
	desc := firstSentence(e.Summary)
	if tuned && meta.Description != "" {
		desc = meta.Description
	}
	fmt.Fprintf(&b, "title: %s\n", yamlQuote(e.Code+" — "+e.Title))
	fmt.Fprintf(&b, "description: %s\n", yamlQuote(desc))
	if tuned && meta.SEOTitle != "" {
		fmt.Fprintf(&b, "seoTitle: %s\n", yamlQuote(meta.SEOTitle))
	}
	// The sidebar shows the code alone: it is what a reader arriving from a CI
	// log is looking for, and the full title wrapped to three lines per item.
	// The page heading keeps the descriptive title.
	fmt.Fprintf(&b, "sidebar:\n  label: %s\n", yamlQuote(e.Code))
	fmt.Fprintf(&b, "---\n\n")

	fmt.Fprintf(&b, "**Namespace:** `%s` · **Code:** `%s`\n\n", Namespace(e.Code), e.Code)

	// A fixed order on every page, so a reader who has seen one finding page can
	// find the fix on any other without reading: what it is, what it costs, a
	// migration that triggers it, what to write instead, and its neighbours.
	fmt.Fprintf(&b, "## What it means\n\n%s\n\n", e.Summary)

	guide := pageGuides[e.Code]
	if guide.Why != "" {
		fmt.Fprintf(&b, "## Why it matters\n\n%s\n\n", guide.Why)
	}

	ex, hasExample := findings.ExampleFor(e.Code)
	if hasExample {
		fmt.Fprintf(&b, "## Example\n\n")
		fmt.Fprintf(&b, "Production shape: %s This migration produces `%s` against it:\n\n", ex.Shape, e.Code)
		writeSQL(&b, ex.Migration)
		if ex.NeedsTarget != "" {
			fmt.Fprintf(&b, ":::note\n%s\n:::\n\n", ex.NeedsTarget)
		}
		writeFixture(&b, "The fixture this example is checked against", ex.Fixture)
	}

	// The remediation is the catalog's, verbatim — it is what a verdict carries
	// and what `rowshape explain` prints. The worked fix follows it.
	fmt.Fprintf(&b, "## How to fix\n\n%s\n\n", e.Remediation)
	if hasExample {
		if ex.FixedNote != "" {
			fmt.Fprintf(&b, "**For the example above.** %s\n\n", ex.FixedNote)
		}
		writeSQL(&b, ex.Fixed)
		if ex.FixedFixture != "" {
			writeFixture(&b, "The re-pulled fixture the fix is checked against", ex.FixedFixture)
		}
		if ex.StillReported != "" {
			fmt.Fprintf(&b, ":::note[rowshape still reports %s here]\n%s\n:::\n\n", e.Code, ex.StillReported)
		}
	}

	if len(guide.Related) > 0 {
		fmt.Fprintf(&b, "## Related codes\n\n")
		for _, code := range guide.Related {
			if rel, ok := findings.Explain(code); ok {
				// Bare code, so the site's finding-code linker turns it into a
				// link; the title says why it is related without a click.
				fmt.Fprintf(&b, "- %s — %s\n", code, rel.Title)
			}
		}
		fmt.Fprintf(&b, "\n")
	}

	if refs := publicReferences(e.References); len(refs) > 0 {
		fmt.Fprintf(&b, "## References\n\n")
		for _, r := range refs {
			fmt.Fprintf(&b, "- %s\n", r)
		}
		fmt.Fprintf(&b, "\n")
	}

	fmt.Fprintf(&b, "---\n\n")
	fmt.Fprintf(&b, "This page is generated from the same catalog `rowshape explain %s` reads, "+
		"so the remediation here is byte-identical to the one a verdict carries — they cannot drift. "+
		"An agent can read it with the `explain_finding` MCP tool.", e.Code)
	if hasExample {
		fmt.Fprintf(&b, " The example is run against PostgreSQL 16 by rowshape's test suite: "+
			"the first migration must produce %s and the fix must not fail.", e.Code)
	}
	fmt.Fprintf(&b, "\n")
	return b.String()
}

// writeSQL renders example SQL as one code block per migration file. A fix that
// spans files ("-- Migration 1", "-- Migration 2") is shown as separate, titled
// blocks, because the file boundary is a transaction boundary and copying both
// halves into one file would reintroduce the hazard.
func writeSQL(b *strings.Builder, sql string) {
	files := findings.MigrationFiles(sql)
	for _, f := range files {
		if len(files) == 1 {
			fmt.Fprintf(b, "```sql\n%s\n```\n\n", f)
			continue
		}
		title, body, _ := strings.Cut(f, "\n")
		title = strings.TrimSpace(strings.TrimPrefix(title, "--"))
		fmt.Fprintf(b, "```sql title=%q\n%s\n```\n\n", title, body)
	}
}

// writeFixture renders a fixture collapsed: it is the evidence the example is
// true, not something every reader needs to read.
func writeFixture(b *strings.Builder, summary, fx string) {
	fmt.Fprintf(b, "<details>\n<summary>%s</summary>\n\n```yaml\n%s\n```\n\n</details>\n\n",
		summary, strings.TrimRight(fx, "\n"))
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
