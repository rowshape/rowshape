package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/rowshape/rowshape/internal/agentrule"
)

// The second of the three things `init --agent` writes (PRD §8.3): the agent rule.
//
// Registering the MCP server (P3-T8) tells the client the tools EXIST. Nothing in
// that makes an agent reach for them: agents don't invoke tools they haven't been
// told about, and an MCP server the agent never calls is decoration. The rule is
// what turns four available tools into four called tools, which makes it closer to
// the wedge than the server itself.

// ruleTarget is one file the rule can live in.
type ruleTarget struct {
	// Name is what we call it in output.
	Name string
	// Path is repo-relative.
	Path string
	// Marker, when set, is the repo path whose presence means this target is in
	// use. Empty means the target is matched by its own Path existing.
	Marker string
	// Seed is written above the managed block when we create the file from
	// scratch. It carries whatever frontmatter the client requires.
	Seed string
}

// cursorFrontmatter is what a Cursor rule file needs to apply at all: an .mdc
// without `alwaysApply` is a rule Cursor loads only when it feels like it, which
// is indistinguishable from not writing one.
const cursorFrontmatter = `---
description: Check database migrations against production shape with rowshape
alwaysApply: true
---

`

// ruleTargets is the closed set of agent-rule files rowshape writes.
//
// AGENTS.md leads deliberately: it is the cross-tool convention, so one file
// reaches the agents this repo hasn't met yet. CLAUDE.md and Cursor's rules
// directory are client-specific and only written when the repo shows they're used.
var ruleTargets = []ruleTarget{
	{Name: "AGENTS.md", Path: "AGENTS.md"},
	{Name: "CLAUDE.md", Path: "CLAUDE.md"},
	{Name: "Cursor rules", Path: filepath.Join(".cursor", "rules", "rowshape.mdc"), Marker: ".cursor", Seed: cursorFrontmatter},
}

// detectRuleTargets returns the rule files to write in dir.
//
// It writes to the conventions the repo already keeps, rather than littering it
// with every file some agent might read: a repo with a CLAUDE.md gets the rule in
// CLAUDE.md. When a repo keeps none of them, it gets AGENTS.md — the rule has to
// land somewhere or `--agent` has done nothing, and AGENTS.md is the choice that
// isn't tied to one vendor.
func detectRuleTargets(dir string) []ruleTarget {
	var found []ruleTarget
	for _, t := range ruleTargets {
		probe := t.Marker
		if probe == "" {
			probe = t.Path
		}
		if _, err := os.Stat(filepath.Join(dir, probe)); err == nil {
			found = append(found, t)
		}
	}
	if len(found) == 0 {
		return []ruleTarget{ruleTargets[0]} // AGENTS.md
	}
	return found
}

// writeRule inserts or updates the managed rule block in one target.
//
// Idempotency here is not a nicety. This file is the user's — they have their own
// conventions above and below our block — and `init --agent` re-runs on every
// upgrade. So: everything outside the block survives byte-for-byte, an outdated
// block is replaced in place rather than appended to, and a current block means
// the file is not touched at all.
func writeRule(dir string, t ruleTarget) (writeStatus, error) {
	path := filepath.Join(dir, t.Path)

	content := ""
	existed := false
	if data, err := os.ReadFile(path); err == nil {
		existed = true
		content = string(data)
	} else if !os.IsNotExist(err) {
		return 0, fmt.Errorf("reading %s: %w", t.Path, err)
	}

	if !existed && t.Seed != "" {
		content = t.Seed
	}

	out, changed := agentrule.Upsert(content)
	if !changed {
		return statusUnchanged, nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, fmt.Errorf("creating %s: %w", filepath.Dir(t.Path), err)
	}
	// Atomic: AGENTS.md / CLAUDE.md are the user's files and may hold a great
	// deal that rowshape did not write.
	if err := writeFileAtomic(path, []byte(out), 0o644); err != nil {
		return 0, fmt.Errorf("writing %s: %w", t.Path, err)
	}
	if existed {
		return statusUpdated, nil
	}
	return statusCreated, nil
}
