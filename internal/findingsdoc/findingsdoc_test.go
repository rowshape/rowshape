package findingsdoc

import (
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/findings"
)

func TestPageContainsCatalogFields(t *testing.T) {
	e := findings.Explanation{
		Code:        "RS-LOCK-001",
		Title:       "a title",
		Summary:     "one sentence. two sentence.",
		Remediation: "do the thing",
		References:  []string{"RFC §9.1", "PRD §10"},
	}
	page := Page(e)
	for _, want := range []string{
		"title: 'RS-LOCK-001 — a title'",
		"description: " + yamlQuote(pageMetas["RS-LOCK-001"].Description), // tuned for search
		"seoTitle: " + yamlQuote(pageMetas["RS-LOCK-001"].SEOTitle),
		"`RS-LOCK`",                   // namespace
		"one sentence. two sentence.", // full summary in the body
		"## How to fix",
		"do the thing",
		"[Fixture spec §9.1: Extrapolation is model-based, not linear](/spec/)",
		"rowshape explain RS-LOCK-001",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page missing %q:\n%s", want, page)
		}
	}
	// The internal plan is not published; a reader cannot follow it.
	if strings.Contains(page, "PRD §") {
		t.Errorf("page cites the unpublished plan:\n%s", page)
	}
}

// TestEveryPageHasTheFixedStructure: every finding page carries the same
// sections in the same order, so the fix is in the same place on all of them.
func TestEveryPageHasTheFixedStructure(t *testing.T) {
	sections := []string{
		"## What it means\n", "## Why it matters\n", "## Example\n",
		"```sql\n", "## How to fix\n", "## Related codes\n",
	}
	for code, e := range Catalog() {
		page := Page(e)
		at := 0
		for _, s := range sections {
			i := strings.Index(page[at:], s)
			if i < 0 {
				t.Errorf("%s: missing or out of order: %q", code, strings.TrimSpace(s))
				break
			}
			at += i + len(s)
		}
		if !strings.Contains(page, "sidebar:\n  label: '"+code+"'\n") {
			t.Errorf("%s: the sidebar label must be the bare code", code)
		}
		if !strings.Contains(page, Prose(e.Remediation)) {
			t.Errorf("%s: the catalog remediation must appear verbatim", code)
		}
		for _, rel := range pageGuides[code].Related {
			if rel == code {
				t.Errorf("%s lists itself as related", code)
			}
			if _, ok := findings.Explain(rel); !ok {
				t.Errorf("%s lists unknown related code %s", code, rel)
			}
		}
	}
}

// TestPageDescriptionFallback: a code with no tuned frontmatter still gets a
// description — the summary's first sentence — and no seoTitle.
func TestPageDescriptionFallback(t *testing.T) {
	page := Page(findings.Explanation{Code: "RS-X-001", Title: "t", Summary: "one sentence. two sentence."})
	if !strings.Contains(page, "description: 'one sentence.'\n") || strings.Contains(page, "seoTitle:") {
		t.Errorf("fallback frontmatter wrong:\n%s", page)
	}
}

// TestEveryCodeHasTunedMeta: the search-facing title and description are
// written per code; a new code without them would ship a truncated <title>.
func TestEveryCodeHasTunedMeta(t *testing.T) {
	for _, code := range findings.Codes() {
		m, ok := pageMetas[code]
		if !ok || m.SEOTitle == "" || m.Description == "" {
			t.Errorf("%s has no seoTitle/description in pageMetas (meta.go)", code)
		}
	}
	for code := range pageMetas {
		if _, ok := findings.Explain(code); !ok {
			t.Errorf("pageMetas has an entry for %s, which is not in the catalog", code)
		}
	}
}

func TestPageFileAndNamespace(t *testing.T) {
	if got := PageFile("RS-DATA-014"); got != "rs-data-014.md" {
		t.Errorf("PageFile = %q, want rs-data-014.md", got)
	}
	if got := Namespace("RS-CONSTRAINT-010"); got != "RS-CONSTRAINT" {
		t.Errorf("Namespace = %q, want RS-CONSTRAINT", got)
	}
}

// TestYamlQuoteEscapes: a title/summary with an apostrophe must not break the
// single-quoted YAML frontmatter (RS-LOCK-001's summary has "column's").
func TestYamlQuoteEscapes(t *testing.T) {
	e := findings.Explanation{Code: "RS-X-001", Title: "a column's type", Summary: "it's fine."}
	page := Page(e)
	if !strings.Contains(page, "a column''s type") {
		t.Errorf("apostrophe should be doubled for YAML:\n%s", page)
	}
}

// TestIndexGroupsByNamespace: the index lists every code, grouped, with links.
func TestIndexGroupsByNamespace(t *testing.T) {
	idx := Index(Catalog())
	for _, want := range []string{
		"## RS-LOCK — Locks a migration takes, and for how long",
		"[`RS-LOCK-001`](./rs-lock-001/)",
		"## RS-REVERSE",
		"explain_finding",
	} {
		if !strings.Contains(idx, want) {
			t.Errorf("index missing %q:\n%s", want, idx)
		}
	}
}

// TestCatalogNonEmpty: Catalog() surfaces the real registered codes.
func TestCatalogNonEmpty(t *testing.T) {
	c := Catalog()
	if len(c) == 0 {
		t.Fatal("empty catalog")
	}
	if _, ok := c["RS-LOCK-001"]; !ok {
		t.Error("expected RS-LOCK-001 in the catalog")
	}
}
