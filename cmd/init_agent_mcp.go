package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
)

// The first of the three things `init --agent` writes (PRD §8.3): MCP config for
// the detected client, registering `rowshape mcp` as a server.
//
// An MCP server the agent never calls is decoration. Nothing about serving the
// four tools makes Claude Code reach for them — the client has to be told the
// server exists, and that means writing its config file. This is the step that
// actually wires the wedge in.
//
// The formats have quietly diverged. Most clients use the root key `mcpServers`;
// VS Code uses `servers` and wants an explicit transport type. Writing "the MCP
// config" as if there were one format produces a file the client silently
// ignores, which is worse than writing nothing — so the differences live in the
// table below rather than in a comment.

// serverName is the key rowshape registers itself under, in every client.
const mcpServerName = "rowshape"

// mcpClient is one client's config contract: where the file lives and what shape
// it expects.
type mcpClient struct {
	// Name is what we call the client in output.
	Name string
	// Path is the config file, relative to the repo root. Every supported client
	// has a repo-local, committable config — `init --agent` deliberately does not
	// write to machine-global paths, which are a different consent surface and are
	// not committable anyway.
	Path string
	// Key is the root object the servers live under. This is where the formats
	// diverge: `mcpServers` for most, `servers` for VS Code.
	Key string
	// Markers are repo paths that indicate this client is in use.
	Markers []string
	// StdioType records whether the entry needs an explicit `"type": "stdio"`
	// (VS Code wants it; the others infer stdio from `command`).
	StdioType bool
}

// supportedMCPClients is the closed set of clients `init --agent` writes for.
//
// Zed is deliberately absent: it keys servers under `context_servers` with a
// different entry shape, its settings file is JSONC (comments would not survive
// the round-trip below), and the shape has moved across versions. A half-right
// entry in a user's settings.json is worse than an honest omission, so rowshape
// does not write one.
//
// NOTE: this comment used to claim "the summary prints the entry to paste
// instead". Nothing printed one — grep for "zed" matches only this comment. The
// reasoning for excluding Zed is sound; the mitigation was never built. Printing
// a paste-able snippet would make the omission actionable and is worth doing;
// claiming it while not doing it was worse than saying nothing.
var supportedMCPClients = []mcpClient{
	{
		Name:    "Claude Code",
		Path:    ".mcp.json",
		Key:     "mcpServers",
		Markers: []string{".mcp.json", ".claude", "CLAUDE.md"},
	},
	{
		Name:    "Cursor",
		Path:    filepath.Join(".cursor", "mcp.json"),
		Key:     "mcpServers",
		Markers: []string{".cursor"},
	},
	{
		Name:      "VS Code",
		Path:      filepath.Join(".vscode", "mcp.json"),
		Key:       "servers",
		Markers:   []string{".vscode"},
		StdioType: true,
	},
}

// detectMCPClients returns the clients in use in dir, detected from repo layout
// and environment.
//
// A repo can be worked in by more than one client — a Cursor user and a Claude
// Code user in the same codebase is ordinary — so this returns every client it
// finds rather than guessing a winner. When it finds none, it falls back to
// `.mcp.json`: that is the de-facto standard location, the file is committable,
// and a config the agent reads once it arrives beats no config at all.
func detectMCPClients(dir string) []mcpClient {
	var found []mcpClient
	for _, c := range supportedMCPClients {
		if clientPresent(dir, c) {
			found = append(found, c)
		}
	}
	if len(found) == 0 {
		// Fall back to the de-facto standard rather than writing nothing.
		return []mcpClient{supportedMCPClients[0]}
	}
	return found
}

// clientPresent reports whether a client's markers exist in dir, or whether we
// are running inside that client right now.
func clientPresent(dir string, c mcpClient) bool {
	for _, m := range c.Markers {
		if _, err := os.Stat(filepath.Join(dir, m)); err == nil {
			return true
		}
	}
	// Claude Code sets CLAUDECODE in the environment of the shells it spawns, so a
	// fresh repo with no markers yet is still correctly detected when the agent
	// itself is the one running `rowshape init --agent`.
	return c.Name == "Claude Code" && os.Getenv("CLAUDECODE") != ""
}

// serverEntry is the registration rowshape writes into a client's config.
//
// The command is the bare name `rowshape`, resolved from PATH — not the absolute
// path of the binary running init. These files get committed; an absolute path
// from the machine that ran init is broken for everyone else on the team.
func serverEntry(c mcpClient) map[string]any {
	e := map[string]any{
		"command": "rowshape",
		"args":    []any{"mcp"},
	}
	if c.StdioType {
		e["type"] = "stdio"
	}
	return e
}

// writeStatus is what happened to one client's config.
type writeStatus int

const (
	statusCreated   writeStatus = iota // the file did not exist
	statusUpdated                      // merged our entry into an existing file
	statusUnchanged                    // already registered, byte-for-byte
)

// writeMCPConfig registers `rowshape mcp` in one client's config, merging into
// whatever is already there.
//
// Idempotency is the whole game: this runs on a repo that already has three other
// MCP servers configured, and again next month after an upgrade. So it reads the
// existing file, replaces only the `rowshape` key, and leaves every other server
// and every unrelated top-level field exactly as it found them. When our entry is
// already correct it does not touch the file at all.
func writeMCPConfig(dir string, c mcpClient) (writeStatus, error) {
	path := filepath.Join(dir, c.Path)

	root := map[string]any{}
	existed := false
	if data, err := os.ReadFile(path); err == nil {
		existed = true
		if err := json.Unmarshal(data, &root); err != nil {
			// Refuse rather than clobber. A config we cannot parse is a config we
			// cannot safely rewrite — it may be JSONC, or hand-edited and broken,
			// and either way overwriting it destroys the user's other servers.
			return 0, fmt.Errorf("%s exists but is not valid JSON (%v); add the rowshape entry by hand:\n%s", c.Path, err, entrySnippet(c))
		}
		if root == nil {
			root = map[string]any{}
		}
	} else if !os.IsNotExist(err) {
		return 0, fmt.Errorf("reading %s: %w", c.Path, err)
	}

	// CR-T25: the type assertion's failure used to be discarded, so a `servers`
	// key holding anything other than an object (an array, a string, null from a
	// half-finished edit) silently became an empty map and was then written back
	// — destroying whatever the user had there. That contradicted this function's
	// own rule three lines up, where an unparseable file is refused precisely
	// because "overwriting it destroys the user's other servers". A wrong-shaped
	// value is the same situation with a narrower blast radius, so it gets the
	// same answer: refuse, and print the entry to add by hand.
	var servers map[string]any
	switch existing := root[c.Key]; v := existing.(type) {
	case nil:
		servers = map[string]any{}
	case map[string]any:
		servers = v
	default:
		return 0, fmt.Errorf("%s has a %q key that is not an object (found %T); rowshape will not "+
			"overwrite it. Fix or remove it, or add the rowshape entry by hand:\n%s",
			c.Path, c.Key, existing, entrySnippet(c))
	}

	want := serverEntry(c)
	if existed && reflect.DeepEqual(servers[mcpServerName], normalize(want)) {
		return statusUnchanged, nil
	}

	servers[mcpServerName] = want
	root[c.Key] = servers

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("encoding %s: %w", c.Path, err)
	}
	out = append(out, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, fmt.Errorf("creating %s: %w", filepath.Dir(c.Path), err)
	}
	// Atomic: this file holds every OTHER MCP server the user has configured,
	// and a truncating write that is interrupted destroys all of them.
	if err := writeFileAtomic(path, out, 0o644); err != nil {
		return 0, fmt.Errorf("writing %s: %w", c.Path, err)
	}
	if existed {
		return statusUpdated, nil
	}
	return statusCreated, nil
}

// normalize round-trips a value through JSON so it compares equal to what was
// parsed from disk (where every number is a float64 and every array an []any).
// Without this, an unchanged entry read back from a file would never compare
// equal to the one we just built, and init would rewrite the file every run.
func normalize(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return v
	}
	return out
}

// entrySnippet renders the config the user should paste when we refuse to write.
// A refusal that does not say what to do instead is just a dead end.
func entrySnippet(c mcpClient) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("  ", "  ")
	_ = enc.Encode(map[string]any{c.Key: map[string]any{mcpServerName: serverEntry(c)}})
	return buf.String()
}
