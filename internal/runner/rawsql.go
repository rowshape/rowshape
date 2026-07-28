package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// rawSQLRunner applies a plain directory of `.sql` migration files with
// `psql -f`, in filename order — the convention hand-rolled migration setups
// follow (PRD §13). This is the fallback when no framework is detected.
type rawSQLRunner struct {
	dir   string   // directory holding the .sql files
	files []string // base names, sorted in apply order
}

func (r *rawSQLRunner) Kind() Kind { return RawSQL }

// Binary is the executable ApplyCmd shells out to. psql is frequently absent on
// Windows even where Postgres itself is installed.
func (r *rawSQLRunner) Binary() string { return "psql" }

// Files returns the migration file names in apply order (exposed for tests and
// for the caller to report what will run).
func (r *rawSQLRunner) Files() []string { return r.files }

// Dir returns the directory the names from Files() are relative to. Detection
// descends into the conventional locations (migrations/, db/migrations/, …), so
// this is NOT necessarily the path the user passed to --migrations: pointing at
// a repo root with files in ./migrations/ yields dir=./migrations. Callers that
// resolve Files() against the user's path instead of this one build paths that
// do not exist. ApplyCmd has always used it as cmd.Dir; Dir exposes the same
// base to callers that read the files themselves.
func (r *rawSQLRunner) Dir() string { return r.dir }

// ApplyCmd runs `psql` against the target, applying each file in order under a
// single connection with ON_ERROR_STOP so a failure halts immediately rather
// than continuing past a broken migration. Rowshape shells out to psql; it does
// not parse or execute the SQL itself.
func (r *rawSQLRunner) ApplyCmd(ctx context.Context, dsn string) *exec.Cmd {
	args := []string{"-v", "ON_ERROR_STOP=1", "-d", dsn}
	for _, f := range r.files {
		args = append(args, "-f", f)
	}
	cmd := exec.CommandContext(ctx, "psql", args...)
	cmd.Dir = r.dir
	cmd.Env = os.Environ()
	return cmd
}

// rawSQLDirs are the conventional locations a raw-SQL project keeps its
// migrations, most specific first; "." lets the caller point directly at a
// migrations directory.
var rawSQLDirs = []string{"migrations", filepath.Join("db", "migrations"), filepath.Join("database", "migrations"), "sql", "."}

// detectRawSQL recognizes a raw-SQL project: a conventional migrations directory
// containing at least one .sql file.
func detectRawSQL(dir string) (Runner, bool) {
	for _, sub := range rawSQLDirs {
		cand := filepath.Join(dir, sub)
		if files := sqlFiles(cand); len(files) > 0 {
			return &rawSQLRunner{dir: cand, files: files}, true
		}
	}
	return nil, false
}

// newRawSQL builds a raw-SQL runner for an explicitly selected project, erroring
// when no .sql migrations can be found so the override fails loudly.
func newRawSQL(dir string) (Runner, error) {
	if r, ok := detectRawSQL(dir); ok {
		return r, nil
	}
	return nil, fmt.Errorf("runner: no .sql migration files found under %s (looked in %s)", dir, strings.Join(rawSQLDirs, ", "))
}

// sqlFiles returns the sorted base names of .sql files directly in dir.
func sqlFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	return files
}
