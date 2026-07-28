package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rowshape/rowshape/cmd"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func committedDir(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", referenceDir)
}

// TestCLIDocsUpToDate regenerates the reference into a temp dir and asserts the
// committed pages are byte-identical. If this fails, the CLI changed and the
// docs were not regenerated: run `go run ./tools/gencli`.
//
// This is the guard that makes the reference worth having. The site previously
// documented four flags out of roughly thirty; hand-written docs fix that number
// once and then rot, because every later flag has to be remembered into a second
// place. Failing the build is what keeps them in step.
func TestCLIDocsUpToDate(t *testing.T) {
	tmp := t.TempDir()
	if err := generate(tmp, filepath.Join("..", "..")); err != nil {
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
			t.Errorf("missing committed page %s — run `go run ./tools/gencli`", e.Name())
			continue
		}
		if string(got) != string(want) {
			t.Errorf("%s is stale — run `go run ./tools/gencli`", e.Name())
		}
	}

	// A command REMOVED from the tree must not leave an orphan page behind.
	committedEntries, err := os.ReadDir(committed)
	if err != nil {
		t.Fatal(err)
	}
	generated := map[string]bool{}
	for _, e := range entries {
		generated[e.Name()] = true
	}
	for _, e := range committedEntries {
		if !generated[e.Name()] {
			t.Errorf("orphan page %s — a command was removed; run `go run ./tools/gencli`", e.Name())
		}
	}
}

// TestEveryFlagIsDocumented is the assertion the story actually asks for: every
// flag the binary accepts appears in the reference. It checks the CONTENT rather
// than only staleness, so a generator bug that silently dropped a flag would be
// caught too.
func TestEveryFlagIsDocumented(t *testing.T) {
	committed := committedDir(t)
	root := cmd.NewRootCmd()

	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(committed, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		return string(b)
	}

	// Persistent flags belong on the index.
	index := read("index.md")
	root.PersistentFlags().VisitAll(func(f *pflag.Flag) {
		if f.Hidden {
			return
		}
		if !strings.Contains(index, "`--"+f.Name+"`") {
			t.Errorf("global flag --%s is not in the CLI reference index", f.Name)
		}
	})

	var total int
	for _, c := range subcommands(root) {
		page := read(pageFile(c))
		c.NonInheritedFlags().VisitAll(func(f *pflag.Flag) {
			if f.Hidden {
				return
			}
			total++
			if !strings.Contains(page, "`--"+f.Name+"`") {
				t.Errorf("rowshape %s: flag --%s is not documented", c.Name(), f.Name)
			}
		})
	}

	// Guard the guard: if the walk stopped finding flags, this test would pass
	// vacuously. The count only ever grows.
	if total < 25 {
		t.Errorf("only %d flags walked; the reference is meant to cover roughly thirty — is the walk broken?", total)
	}
	t.Logf("%d command flags documented", total)
}

// Every subcommand must have a page reachable from the index.
func TestEveryCommandIsLinked(t *testing.T) {
	index, err := os.ReadFile(filepath.Join(committedDir(t), "index.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range subcommands(cmd.NewRootCmd()) {
		if !strings.Contains(string(index), "(./"+c.Name()+"/)") {
			t.Errorf("rowshape %s is not linked from the CLI reference index", c.Name())
		}
	}
}

// The Action page is mirrored from docs/action.md, which lives next to
// action.yml and is reviewed with it. Assert the mirror actually carries the
// guide, so the site cannot go back to linking "GitHub Action" at a page that
// never mentions the Action.
func TestActionPageIsMirrored(t *testing.T) {
	page, err := os.ReadFile(filepath.Join(committedDir(t), "github-action.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"exit-code", "warn-as-fail", "verify-signature", "ephemeral"} {
		if !strings.Contains(string(page), want) {
			t.Errorf("the mirrored Action page is missing %q", want)
		}
	}
}

// subcommands is exercised indirectly above; this pins its exclusions so a
// future cobra version adding a built-in cannot silently land in the docs.
func TestSubcommandsExcludeBuiltins(t *testing.T) {
	for _, c := range subcommands(cmd.NewRootCmd()) {
		switch c.Name() {
		case "help", "completion":
			t.Errorf("%s is a cobra built-in and must not be documented as a rowshape command", c.Name())
		}
		if c.Hidden {
			t.Errorf("%s is hidden and must not be documented", c.Name())
		}
	}
}

var _ = cobra.Command{}
