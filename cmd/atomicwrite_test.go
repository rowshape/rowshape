package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteFileAtomicReplacesContent is the basic contract.
func TestWriteFileAtomicReplacesContent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte("old contents"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(p, []byte("new contents"), 0o644); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new contents" {
		t.Errorf("content = %q, want %q", got, "new contents")
	}
}

// The temp file must live in the TARGET's directory. rename is only atomic
// within a filesystem, and os.TempDir is frequently on a different one — a
// cross-device rename fails, or degrades to a copy that is not atomic at all.
func TestWriteFileAtomicUsesTheTargetDirectory(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := writeFileAtomic(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Nothing should be left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file %q was left behind", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Errorf("want exactly the target file, got %d entries", len(entries))
	}
}

// A failure must leave the ORIGINAL intact rather than a truncated file. This is
// the whole point: .mcp.json holds every other MCP server the user configured,
// and AGENTS.md holds their own guidance.
func TestWriteFileAtomicLeavesOriginalOnFailure(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "config.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	original := `{"mcpServers":{"other-tool":{"command":"other"}}}`
	if err := os.WriteFile(p, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	// Point at a path whose parent directory does not exist, so CreateTemp fails
	// before anything touches the real file.
	bad := filepath.Join(dir, "nope", "config.json")
	if err := writeFileAtomic(bad, []byte("irrelevant"), 0o644); err == nil {
		t.Fatal("expected a failure when the parent directory is missing")
	}

	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("a failed write elsewhere must not disturb this file:\n got: %s\nwant: %s", got, original)
	}
}

// The neighbouring-servers guarantee, end to end: rewriting an MCP config must
// preserve every other server byte-for-byte.
func TestAtomicWritePreservesNeighbouringServers(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".mcp.json")
	original := `{"mcpServers":{"other-tool":{"command":"other","args":["x"]}}}`
	if err := os.WriteFile(p, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	updated := `{"mcpServers":{"other-tool":{"command":"other","args":["x"]},"rowshape":{"command":"rowshape"}}}`
	if err := writeFileAtomic(p, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"other-tool"`) {
		t.Errorf("the neighbouring server was lost: %s", got)
	}
}
