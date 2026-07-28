package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRawSQLDirIsDiscoveredNotGiven pins the contract that Files() is relative
// to Dir(), NOT to the path the caller passed in. Detection descends into the
// conventional locations, so pointing at a repo root with files in ./migrations
// yields Dir() == <root>/migrations. cmd/validate.go's capture path used to
// join Files() against the user's --migrations value instead, producing
// <root>/001_init.sql — a path that does not exist. Detection succeeded first,
// so the failure surfaced as an unreadable file rather than a missing runner.
func TestRawSQLDirIsDiscoveredNotGiven(t *testing.T) {
	root := t.TempDir()
	mig := filepath.Join(root, "migrations")
	if err := os.MkdirAll(mig, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mig, "001_init.sql"), []byte("SELECT 1;"), 0o644); err != nil {
		t.Fatal(err)
	}

	r, ok := detectRawSQL(root)
	if !ok {
		t.Fatal("detectRawSQL(root) = false, want true — files live in ./migrations")
	}
	raw, ok := r.(interface {
		Files() []string
		Dir() string
	})
	if !ok {
		t.Fatal("raw-SQL runner must expose Files() and Dir()")
	}

	if raw.Dir() != mig {
		t.Errorf("Dir() = %q, want %q (the discovered dir, not the given one)", raw.Dir(), mig)
	}
	files := raw.Files()
	if len(files) != 1 || files[0] != "001_init.sql" {
		t.Fatalf("Files() = %v, want [001_init.sql] (base names)", files)
	}

	// Resolving against Dir() must reach the file; resolving against the path
	// the caller passed must not. That asymmetry is the whole bug.
	if _, err := os.Stat(filepath.Join(raw.Dir(), files[0])); err != nil {
		t.Errorf("Files() must resolve against Dir(): %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, files[0])); err == nil {
		t.Errorf("joining Files() to the caller's path must NOT resolve — the test no longer proves anything")
	}
}

// TestEnsureAvailable: a missing migration tool must be a named, actionable
// failure rather than whatever exec happens to return. internal/target's docker
// probe already did this pre-flight; the migration runners did not. It matters
// most off Linux — psql is routinely absent on Windows even where Postgres is
// installed, and prisma/drizzle-kit are npm shims resolved through PATHEXT.
func TestEnsureAvailable(t *testing.T) {
	t.Run("a missing binary is named in the error", func(t *testing.T) {
		// An empty PATH makes every lookup fail deterministically.
		t.Setenv("PATH", "")
		r := &rawSQLRunner{dir: t.TempDir()}
		err := EnsureAvailable(r)
		if err == nil {
			t.Fatal("EnsureAvailable must fail when psql is not on PATH")
		}
		if !strings.Contains(err.Error(), "psql") {
			t.Errorf("the error must name the missing tool, got: %v", err)
		}
		if !strings.Contains(err.Error(), string(RawSQL)) {
			t.Errorf("the error must name the runner, got: %v", err)
		}
	})

	t.Run("every runner declares its binary", func(t *testing.T) {
		runners := []Runner{
			&rawSQLRunner{},
			&alembicRunner{},
			&prismaRunner{},
			&drizzleRunner{},
		}
		want := map[Kind]string{
			RawSQL:  "psql",
			Alembic: "alembic",
			Prisma:  "prisma",
			Drizzle: "drizzle-kit",
		}
		for _, r := range runners {
			if got := r.Binary(); got != want[r.Kind()] {
				t.Errorf("%s Binary() = %q, want %q", r.Kind(), got, want[r.Kind()])
			}
			if InstallHint(r.Kind()) == "" {
				t.Errorf("%s has no install hint", r.Kind())
			}
		}
	})

	t.Run("an available binary passes", func(t *testing.T) {
		// Use a runner whose binary is whatever this machine certainly has.
		if _, err := exec.LookPath("go"); err != nil {
			t.Skip("no go on PATH to use as a known-present binary")
		}
		if err := EnsureAvailable(stubRunner{bin: "go"}); err != nil {
			t.Errorf("EnsureAvailable must pass for a present binary: %v", err)
		}
	})

	t.Run("an empty binary is not checked", func(t *testing.T) {
		if err := EnsureAvailable(stubRunner{bin: ""}); err != nil {
			t.Errorf("an empty Binary() means nothing to check, got: %v", err)
		}
	})
}

type stubRunner struct{ bin string }

func (s stubRunner) Kind() Kind     { return RawSQL }
func (s stubRunner) Binary() string { return s.bin }
func (s stubRunner) ApplyCmd(ctx context.Context, dsn string) *exec.Cmd {
	return exec.CommandContext(ctx, s.bin)
}
