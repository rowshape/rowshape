package runner

import (
	"context"
	"os/exec"
	"path/filepath"
)

// alembicRunner drives Alembic (SQLAlchemy migrations). Signature: an
// `alembic.ini` at the project root (PRD §13).
type alembicRunner struct {
	root string
}

func (a *alembicRunner) Kind() Kind { return Alembic }

// Binary is the executable ApplyCmd shells out to.
func (a *alembicRunner) Binary() string { return "alembic" }

// ApplyCmd runs `alembic upgrade head` from the project root, handing the target
// DSN to Alembic through DATABASE_URL — the convention env.py reads. Rowshape
// invokes Alembic; it does not touch the version table itself.
func (a *alembicRunner) ApplyCmd(ctx context.Context, dsn string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "alembic", "upgrade", "head")
	cmd.Dir = a.root
	cmd.Env = envWithDSN(dsn)
	return cmd
}

// detectAlembic recognizes an Alembic project by its alembic.ini.
func detectAlembic(dir string) (Runner, bool) {
	if fileExists(filepath.Join(dir, "alembic.ini")) {
		return &alembicRunner{root: dir}, true
	}
	return nil, false
}
