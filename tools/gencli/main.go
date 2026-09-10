// Command gencli writes the CLI reference pages from the cobra command tree, so
// the documented flags are BY CONSTRUCTION the flags the binary actually has.
// Run from the repo root:
//
//	go run ./tools/gencli
//
// The generated pages are committed; gen_test.go fails if they are stale, so the
// CLI and the docs cannot drift.
//
// This exists because the docs site documented four flags out of roughly thirty,
// and had no CLI reference and no GitHub Action page at all. Writing those by
// hand would have fixed the count once and then rotted — every flag added since
// P0 would have had to be remembered into a second place. Generating from the
// cobra tree makes the reference a projection of the source rather than a copy
// of it.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rowshape/rowshape/cmd"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// referenceDir is the Starlight content directory for the CLI reference,
// relative to the repo root.
const referenceDir = "docs-site/src/content/docs/reference"

// actionSource is the repo-side Action guide, mirrored onto the site so the
// documentation lives in one place and is published in another.
const actionSource = "docs/action.md"

func main() {
	if err := generate(referenceDir, "."); err != nil {
		fmt.Fprintln(os.Stderr, "gencli:", err)
		os.Exit(1)
	}
}

// generate writes index.md, one page per subcommand, and the mirrored Action
// page into dir. It returns an error rather than exiting so the test can call it
// against a temp dir.
//
// repoRoot is where actionSource is resolved from, because the generator runs
// from the repo root but its test runs from this package's directory.
func generate(dir, repoRoot string) error {
	root := cmd.NewRootCmd()
	subs := subcommands(root)
	if len(subs) == 0 {
		return fmt.Errorf("no subcommands found — the command tree is empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	if err := os.WriteFile(filepath.Join(dir, "index.md"), []byte(indexPage(root, subs)), 0o644); err != nil {
		return err
	}
	for i, c := range subs {
		// Order the sidebar by the order commands are registered, which is the
		// order the workflow runs in, not alphabetical.
		page := commandPage(c, i+2)
		if err := os.WriteFile(filepath.Join(dir, pageFile(c)), []byte(page), 0o644); err != nil {
			return err
		}
	}

	action, err := actionPage(repoRoot)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "github-action.md"), []byte(action), 0o644)
}

// subcommands returns the user-facing subcommands, skipping cobra's built-ins.
func subcommands(root *cobra.Command) []*cobra.Command {
	var out []*cobra.Command
	for _, c := range root.Commands() {
		if c.Hidden || c.Name() == "help" || c.Name() == "completion" {
			continue
		}
		out = append(out, c)
	}
	return out
}

func pageFile(c *cobra.Command) string { return c.Name() + ".md" }

// docsAnnotation is the cobra annotation key carrying a command's meta
// description for the docs site.
const docsAnnotation = "docs.description"

// docsDescription is what the generated page puts in its frontmatter
// `description`, which becomes the page's meta description and the line a
// search result shows under the title.
//
// It is NOT c.Short. Short is one line of `rowshape --help` output, where terse
// is correct and where a sentence long enough to be a useful search snippet
// would be wrong — "Audit a committed fixture" is a good help line and a
// 25-character meta description that Google will discard in favour of guessing
// from the page. So a command may carry a longer description under the
// docs.description annotation, and falls back to Short when it does not.
//
// The alternative was lengthening the Short lines, which would have improved the
// docs by degrading `--help`. This way each surface says what suits it, and
// both still come from the command tree rather than a second hand-written copy.
func docsDescription(c *cobra.Command) string {
	if d, ok := c.Annotations[docsAnnotation]; ok && d != "" {
		return d
	}
	return c.Short
}

// frontmatter is the Starlight page header. sidebarOrder keeps the reference in
// workflow order rather than alphabetical.
func frontmatter(title, description string, sidebarOrder int) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "title: %s\n", yamlString(title))
	if description != "" {
		fmt.Fprintf(&b, "description: %s\n", yamlString(description))
	}
	b.WriteString("sidebar:\n")
	fmt.Fprintf(&b, "  order: %d\n", sidebarOrder)
	b.WriteString("---\n\n")
	return b.String()
}

// yamlString quotes a scalar so a colon or quote in a description cannot break
// the frontmatter.
func yamlString(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

func indexPage(root *cobra.Command, subs []*cobra.Command) string {
	var b strings.Builder
	b.WriteString(frontmatter("CLI reference",
		"Every rowshape command and every flag it accepts, generated from the binary itself so the reference cannot drift from what the CLI does.", 1))

	b.WriteString("This reference is generated from the command tree, so it lists exactly the\n")
	b.WriteString("commands and flags this version of `rowshape` accepts. If a flag is missing\n")
	b.WriteString("here, it does not exist.\n\n")

	b.WriteString("## Commands\n\n")
	b.WriteString("| Command | What it does |\n| --- | --- |\n")
	for _, c := range subs {
		fmt.Fprintf(&b, "| [`rowshape %s`](./%s/) | %s |\n", c.Name(), c.Name(), escapePipes(c.Short))
	}
	b.WriteString("\n")

	if f := globalFlags(root); f != "" {
		b.WriteString("## Global flags\n\nThese work on every command.\n\n")
		b.WriteString(f)
	}

	b.WriteString("## Exit codes\n\n")
	b.WriteString("Every command maps its outcome onto one contract:\n\n")
	b.WriteString("| Code | Meaning |\n| --- | --- |\n")
	b.WriteString("| `0` | PASS — the check ran and found nothing blocking |\n")
	b.WriteString("| `1` | FAIL — the check ran and the migration is unsafe |\n")
	b.WriteString("| `2` | WARN-only — findings worth reading, not blocking |\n")
	b.WriteString("| `3` | Tool error — the check could NOT run |\n\n")
	b.WriteString("`3` is distinct on purpose: \"the tool could not produce a verdict\" must never\n")
	b.WriteString("be mistaken for \"the migration is unsafe\". A tool error carries\n")
	b.WriteString("`\"error\": \"tool_error\"` and a category rather than a verdict field.\n")

	return b.String()
}

func commandPage(c *cobra.Command, order int) string {
	var b strings.Builder
	b.WriteString(frontmatter("rowshape "+c.Name(), docsDescription(c), order))

	if c.Long != "" {
		b.WriteString(c.Long)
		b.WriteString("\n\n")
	} else if c.Short != "" {
		b.WriteString(c.Short)
		b.WriteString("\n\n")
	}

	// UseLine already carries the full path ("rowshape validate [flags]").
	b.WriteString("## Usage\n\n```sh\n" + c.UseLine() + "\n```\n\n")

	if f := localFlags(c); f != "" {
		b.WriteString("## Flags\n\n")
		b.WriteString(f)
	}

	if len(c.Aliases) > 0 {
		fmt.Fprintf(&b, "## Aliases\n\n`%s`\n\n", strings.Join(c.Aliases, "`, `"))
	}
	return b.String()
}

// localFlags renders a command's own flags as a table.
func localFlags(c *cobra.Command) string { return flagTable(c.NonInheritedFlags()) }

// globalFlags renders the persistent flags available everywhere.
func globalFlags(root *cobra.Command) string { return flagTable(root.PersistentFlags()) }

func flagTable(fs *pflag.FlagSet) string {
	type row struct{ name, shorthand, def, usage string }
	var rows []row
	fs.VisitAll(func(f *pflag.Flag) {
		if f.Hidden {
			return
		}
		rows = append(rows, row{f.Name, f.Shorthand, f.DefValue, f.Usage})
	})
	if len(rows) == 0 {
		return ""
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })

	var b strings.Builder
	b.WriteString("| Flag | Default | Description |\n| --- | --- | --- |\n")
	for _, r := range rows {
		name := "`--" + r.name + "`"
		if r.shorthand != "" {
			name += ", `-" + r.shorthand + "`"
		}
		def := "—"
		if r.def != "" && r.def != "false" && r.def != "[]" {
			def = "`" + r.def + "`"
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", name, def, escapePipes(r.usage))
	}
	b.WriteString("\n")
	return b.String()
}

// actionPage mirrors docs/action.md onto the site.
//
// The guide is authored in the repo, where it sits next to action.yml and gets
// reviewed with it, and published here. Mirroring rather than rewriting is what
// stops the two from disagreeing — the previous state had a thorough, accurate
// action.md that the site never linked to, while the site's own "GitHub Action"
// link pointed at an install page that did not mention the Action.
func actionPage(repoRoot string) (string, error) {
	src := filepath.Join(repoRoot, actionSource)
	body, err := os.ReadFile(src)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", src, err)
	}
	text := string(body)

	// Drop the source's own H1: Starlight renders the title from frontmatter, so
	// keeping it would show the heading twice.
	lines := strings.Split(text, "\n")
	title := "GitHub Action"
	for i, ln := range lines {
		if strings.HasPrefix(ln, "# ") {
			title = strings.TrimSpace(strings.TrimPrefix(ln, "# "))
			lines = append(lines[:i], lines[i+1:]...)
			break
		}
	}
	text = strings.TrimLeft(strings.Join(lines, "\n"), "\n")

	var b strings.Builder
	b.WriteString(frontmatter(title, "Run rowshape validate in GitHub Actions and gate a pull request on the verdict, with findings annotated inline on the lines that caused them.", 99))
	b.WriteString("{/* Generated from docs/action.md by `go run ./tools/gencli` — edit that file. */}\n\n")
	b.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		b.WriteString("\n")
	}
	return b.String(), nil
}

// escapePipes keeps a flag description containing "|" from breaking the table.
func escapePipes(s string) string { return strings.ReplaceAll(s, "|", `\|`) }
