package cmd

import (
	"os"
	"path/filepath"
)

// writeFileAtomic writes data to path via a temp file and a rename, so an
// interrupted write cannot leave the file truncated.
//
// This matters because two of the files rowshape writes are NOT rowshape's:
// .mcp.json holds every OTHER MCP server the user has configured, and
// AGENTS.md / CLAUDE.md hold the user's own guidance. os.WriteFile truncates in
// place, so a Ctrl-C, a full disk, or an OOM between truncate and write leaves a
// zero-length .mcp.json — destroying configuration rowshape never owned.
//
// That outcome is exactly what init_agent_mcp already refuses to risk for
// unparseable input: it declines to write at all rather than round-trip a file it
// might mangle. Writing non-atomically undermined that care at the last step.
//
// The temp file is created in the SAME DIRECTORY as the target, because rename is
// only atomic within a filesystem; os.TempDir can easily be on another one.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Best-effort cleanup on every failure path below: a leftover dotfile beside
	// the user's config is untidy, and worse than untidy if it accumulates.
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	// Chmod before the rename so the file never briefly exists with the wrong
	// mode under its final name. CreateTemp makes it 0600.
	if err := tmp.Chmod(perm); err != nil {
		// Not fatal on platforms where it is a no-op (Windows); the rename below
		// still produces a usable file.
		_ = err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
