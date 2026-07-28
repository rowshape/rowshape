// Package runner detects a project's migration tool and shells out to it to
// apply the migration set. Rowshape orchestrates; it does NOT reimplement
// migration logic — "rather than being the 40th runner" (PRD §8.1, §13).
//
// A Runner knows only how to build the command that applies a project's
// migrations against a database URL. The caller (`validate`, P2-T7) runs that
// command against the DISPOSABLE target only, then reads what happened. v1
// supports Alembic, Prisma, Drizzle, and a plain directory of raw `.sql` files
// applied with `psql -f` (PRD §13).
package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Kind identifies a migration runner. It is the value a user passes to override
// auto-detection (`--runner`).
type Kind string

const (
	Alembic Kind = "alembic"
	Prisma  Kind = "prisma"
	Drizzle Kind = "drizzle"
	RawSQL  Kind = "rawsql"
)

// Runner applies a project's migration set to a target database by shelling out
// to the project's own tool. Rowshape never reimplements the tool.
type Runner interface {
	// Kind reports which tool this runner drives.
	Kind() Kind
	// ApplyCmd builds the command that applies the full migration set against the
	// database at dsn. The command is executed by the caller against the
	// disposable target only; the DSN is delivered to the tool the way that tool
	// expects it (DATABASE_URL for Alembic/Prisma/Drizzle, a psql connection
	// argument for raw SQL).
	ApplyCmd(ctx context.Context, dsn string) *exec.Cmd
	// Binary names the executable ApplyCmd shells out to, so availability can be
	// checked before the command runs rather than surfacing as a raw exec error.
	Binary() string
}

// EnsureAvailable reports whether the runner's tool is actually invocable,
// turning a missing binary into a named, actionable failure.
//
// internal/target/container.go already does this pre-flight for `docker`; the
// migration runners did not, so a missing tool surfaced as whatever exec
// returned. That matters most off Linux: `psql` is not on PATH on a default
// Windows box even with Postgres installed, and `prisma`/`drizzle-kit` are npm
// shims that resolve to .CMD there.
//
// The returned error names the tool and how to get it; callers wrap it in a
// toolerror with the RunnerNotFound category.
func EnsureAvailable(r Runner) error {
	bin := r.Binary()
	if bin == "" {
		return nil
	}
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("%s runner needs %q on PATH, which was not found: %w", r.Kind(), bin, err)
	}
	return nil
}

// InstallHint suggests how to obtain a runner's tool, for the hint field of a
// toolerror.
func InstallHint(k Kind) string {
	switch k {
	case Alembic:
		return "install Alembic (pip install alembic) or select another runner with --runner"
	case Prisma:
		return "install Prisma (npm i -g prisma) or select another runner with --runner"
	case Drizzle:
		return "install Drizzle Kit (npm i -g drizzle-kit) or select another runner with --runner"
	case RawSQL:
		return "install the PostgreSQL client tools so psql is on PATH, or select another runner with --runner"
	default:
		return "install the migration tool for this project, or select another runner with --runner"
	}
}

// detector pairs a kind with the test that recognizes it in a project directory.
// Order matters: a project using a framework (Alembic/Prisma/Drizzle) is
// detected as that framework before the raw-SQL fallback, even though it also
// contains .sql files.
var detectors = []struct {
	kind  Kind
	build func(dir string) (Runner, bool)
}{
	{Alembic, detectAlembic},
	{Prisma, detectPrisma},
	{Drizzle, detectDrizzle},
	{RawSQL, detectRawSQL}, // fallback: a bare directory of .sql migrations
}

// Detect auto-detects the migration runner rooted at dir (PRD §8.1). It returns
// an error naming the supported runners when none is recognized, so the failure
// is actionable rather than a silent guess.
func Detect(dir string) (Runner, error) {
	for _, d := range detectors {
		if r, ok := d.build(dir); ok {
			return r, nil
		}
	}
	return nil, fmt.Errorf("runner: no supported migration runner detected in %s (looked for Alembic, Prisma, Drizzle, or a raw-SQL migrations directory); select one explicitly with --runner", dir)
}

// ForKind builds the runner of an explicitly selected kind, so detection is
// always overridable (`--runner alembic`). It still binds to dir so the runner
// can locate config and migration files, but it does not require the detection
// signature to be present — the user has asserted the choice.
func ForKind(dir string, kind Kind) (Runner, error) {
	switch kind {
	case Alembic:
		return &alembicRunner{root: dir}, nil
	case Prisma:
		return &prismaRunner{root: dir}, nil
	case Drizzle:
		return &drizzleRunner{root: dir}, nil
	case RawSQL:
		return newRawSQL(dir)
	default:
		return nil, fmt.Errorf("runner: unknown runner %q (supported: alembic, prisma, drizzle, rawsql)", kind)
	}
}

// fileExists reports whether path is an existing regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// dirExists reports whether path is an existing directory.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// firstExisting returns the first path (joined under dir) that exists, or "".
func firstExisting(dir string, rels ...string) string {
	for _, r := range rels {
		p := filepath.Join(dir, r)
		if fileExists(p) || dirExists(p) {
			return p
		}
	}
	return ""
}
