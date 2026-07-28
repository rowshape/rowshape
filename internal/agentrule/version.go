// Package agentrule holds the agent rule: the text that tells a coding agent to
// reach for rowshape before it writes a migration (PRD §8.3 item 2).
//
// The rule is a PRODUCT ARTIFACT, not a README snippet. It gets iterated against
// real agent sessions the way a prompt does — which is exactly why it ships
// versioned, in the repo, embedded in the binary: an improvement discovered in
// month four has to be able to reach the repos that ran `init --agent` in month
// one. A rule pasted into a doc for users to copy can never do that.
//
// Everything an upgrade needs is here: bump Version, edit rule.md, and every
// re-run of `init --agent` replaces the old managed block in place.
package agentrule

import (
	_ "embed"
	"fmt"
	"regexp"
	"strings"
)

// Version is the rule's version. Bump it whenever rule.md changes so existing
// repos take the new text on their next `rowshape init --agent`.
//
// v2: "a FAIL is a reproduction" overstated it. Most findings are — validate
// hydrates and applies the migration, and it really breaks. But some are static
// facts read from the fixture: the orphan_fraction findings fire off a measured
// production fact without the hydrated data containing an orphan at all. The
// rule now says "not an opinion" and names both grounds, which is just as
// forceful and has the advantage of being true.
//
// v3: the rule told the agent a FAIL meant "the migration was executed against
// production-shaped data and broke". That is true of `rowshape validate` in a
// shell, which hydrates and applies — but the rule directs the agent at the MCP
// tool `validate_migration`, which is STATIC and executes nothing. So the one
// file the agent actually reads described a check the tool it was told to call
// does not perform, and invited it to read a PASS as "this ran". The rule now
// says plainly what the static check covers and names the stronger one. Same
// class of correction as v2, in the place where it matters most.
//
// It is a plain integer, not semver: there is no such thing as a
// backward-incompatible prompt, and the only question a consumer ever asks is
// "is this older than what I ship?".
const Version = 3

// Text is the rule itself, embedded so the binary is self-contained (PRD §7:
// single static binary, no runtime).
//
//go:embed rule.md
var Text string

// Marker delimiters for the managed block. HTML comments render invisibly in
// Markdown, so the rule reads as ordinary prose in AGENTS.md — the agent sees the
// instruction, not the plumbing.
const (
	beginPrefix = "<!-- rowshape:begin v"
	endMarker   = "<!-- rowshape:end -->"
)

// blockRE matches a managed block of ANY version — that is the point. init finds
// the block a previous (older, newer, or equal) rowshape left and replaces it,
// rather than appending a second copy of a rule that contradicts the first.
var blockRE = regexp.MustCompile(`(?s)<!--\s*rowshape:begin.*?<!--\s*rowshape:end\s*-->`)

// versionRE pulls the version out of a begin marker so init can report an upgrade
// and skip a write when nothing changed.
var versionRE = regexp.MustCompile(`<!--\s*rowshape:begin\s+v(\d+)`)

// Block renders the rule wrapped in its versioned managed block.
func Block() string {
	return fmt.Sprintf(
		"%s%d — managed by `rowshape init --agent`. Edits here are overwritten on upgrade; "+
			"put your own guidance outside this block. -->\n\n%s\n\n%s",
		beginPrefix, Version, strings.TrimSpace(Text), endMarker,
	)
}

// FindBlock returns the span of an existing managed block in content, its
// version, and whether one was found. Version is 0 when the marker is malformed
// (hand-edited) — treated as "older than anything", so init repairs it.
func FindBlock(content string) (start, end, version int, found bool) {
	loc := blockRE.FindStringIndex(content)
	if loc == nil {
		return 0, 0, 0, false
	}
	if m := versionRE.FindStringSubmatch(content[loc[0]:loc[1]]); m != nil {
		_, _ = fmt.Sscanf(m[1], "%d", &version)
	}
	return loc[0], loc[1], version, true
}

// Upsert returns content with the rule block inserted or replaced, and whether
// anything actually changed.
//
// The contract that matters: everything outside the managed block is returned
// byte-for-byte. AGENTS.md and CLAUDE.md are the user's files — rowshape is a
// guest in them, and a guest that reformats the house does not get invited back.
func Upsert(content string) (out string, changed bool) {
	block := Block()

	start, end, _, found := FindBlock(content)
	if found {
		if content[start:end] == block {
			return content, false // already current — do not touch the file
		}
		return content[:start] + block + content[end:], true
	}

	if strings.TrimSpace(content) == "" {
		return block + "\n", true
	}
	// Append to an existing file, keeping exactly one blank line as separation.
	return strings.TrimRight(content, "\n") + "\n\n" + block + "\n", true
}
