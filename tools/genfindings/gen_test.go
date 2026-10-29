package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/findings"
	"github.com/rowshape/rowshape/internal/findingsdoc"
)

// committedDir is the checked-in catalog directory, relative to this package.
func committedDir(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", findingsDir)
}

// TestFindingsDocsUpToDate regenerates the catalog into a temp dir and asserts
// the committed pages are byte-identical. If this fails, the catalog changed and
// the docs weren't regenerated: run `go run ./tools/genfindings`.
func TestFindingsDocsUpToDate(t *testing.T) {
	tmp := t.TempDir()
	if err := generate(tmp); err != nil {
		t.Fatalf("generate: %v", err)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	committed := committedDir(t)
	for _, e := range entries {
		want, err := os.ReadFile(filepath.Join(tmp, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(committed, e.Name()))
		if err != nil {
			t.Errorf("missing committed page %s — run `go run ./tools/genfindings`", e.Name())
			continue
		}
		if string(got) != string(want) {
			t.Errorf("%s is stale — run `go run ./tools/genfindings`", e.Name())
		}
	}
}

// TestEveryCodeHasMatchingPage is the P4-T4 acceptance criterion, checked
// directly: every shipped RS-* code has a catalog page whose remediation is
// exactly what `rowshape explain <CODE>` returns.
func TestEveryCodeHasMatchingPage(t *testing.T) {
	codes := findings.Codes()
	if len(codes) == 0 {
		t.Fatal("no finding codes registered")
	}
	dir := committedDir(t)
	for _, code := range codes {
		e, ok := findings.Explain(code)
		if !ok {
			t.Errorf("code %s has no explanation", code)
			continue
		}
		page := filepath.Join(dir, findingsdoc.PageFile(code))
		body, err := os.ReadFile(page)
		if err != nil {
			t.Errorf("code %s has no catalog page at %s", code, page)
			continue
		}
		if !strings.Contains(string(body), findingsdoc.Prose(e.Remediation)) {
			t.Errorf("catalog page for %s does not contain the explain remediation verbatim", code)
		}
	}
}

// TestNoOrphanPages guards the other direction: a committed page whose code is
// no longer in the catalog would be dead docs. index.md is exempt.
func TestNoOrphanPages(t *testing.T) {
	dir := committedDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	catalog := findingsdoc.Catalog()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || name == "index.md" || !strings.HasSuffix(name, ".md") {
			continue
		}
		code := strings.ToUpper(strings.TrimSuffix(name, ".md"))
		if _, ok := catalog[code]; !ok {
			t.Errorf("orphan catalog page %s — no such code in the catalog", name)
		}
	}
}
