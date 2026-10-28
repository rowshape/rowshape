package harness

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/findings"
	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/validate"
)

// TestFindingExamples runs the worked example on every finding page through the
// real validate pipeline: the example migration must produce its code against
// its fixture, and the fixed migration must not (or, where rowshape cannot see
// the fix, must at least not report it as an error).
//
// The examples are published as "this is what triggers RS-XXX-000"; this is the
// test that makes that sentence true. It needs a Postgres (ROWSHAPE_TEST_PG_DSN)
// and skips without one, like TestCorpusVerdicts.
func TestFindingExamples(t *testing.T) {
	if validator == nil {
		t.Skip("no validator wired (set ROWSHAPE_TEST_PG_DSN to run the examples against Postgres)")
	}
	for _, code := range findings.Codes() {
		ex, ok := findings.ExampleFor(code)
		if !ok {
			continue // TestEveryCodeHasExample (internal/findings) owns this
		}
		t.Run(code, func(t *testing.T) {
			// An example documents one Postgres major — the one its fixture
			// declares — and some fixes exist only from a given major (REINDEX
			// CONCURRENTLY is 12+). The corpus matrix runs this package on every
			// major; the examples are only a claim about theirs.
			if f, err := fixture.Parse([]byte(ex.Fixture)); err == nil && f.Meta.Engine.Version != PGMajor() {
				t.Skipf("example is written for PostgreSQL %s; this run is %s", f.Meta.Engine.Version, PGMajor())
			}
			got := runExample(t, code+"-example", ex.Fixture, ex.Migration)
			if sev := got[code]; sev != "warn" && sev != "error" {
				t.Errorf("example migration does not report %s (got %v)", code, got)
			}

			fx := ex.Fixture
			if ex.FixedFixture != "" {
				fx = ex.FixedFixture
			}
			fixed := runExample(t, code+"-fixed", fx, ex.Fixed)
			if sev := fixed[code]; ex.StillReported == "" && sev == "warn" {
				t.Errorf("fixed migration still reports %s (got %v); if rowshape cannot see this fix, say why in StillReported", code, fixed)
			}
			// The fix is published as the thing to do instead, so it must not FAIL
			// for any reason — a fixed migration that does not apply is not a fix.
			for c, sev := range fixed {
				if sev == "error" {
					t.Errorf("fixed migration reports %s as an error (got %v)", c, fixed)
				}
			}
		})
	}
}

// runExample validates one migration against one fixture and returns the
// highest severity reported per full code.
func runExample(t *testing.T, name, fx, sql string) map[string]string {
	t.Helper()
	f, err := fixture.Parse([]byte(fx))
	if err != nil {
		t.Fatalf("%s: fixture does not parse: %v", name, err)
	}
	// One migration file per "-- Migration N" section, as a runner would apply
	// them: the file boundary is a transaction boundary.
	pv, ok := validator.(pipelineValidator)
	if !ok {
		t.Skip("the examples need the real pipeline validator")
	}
	var stmts []validate.Located
	for i, file := range findings.MigrationFiles(sql) {
		stmts = append(stmts, validate.SplitStatementsIn(fmt.Sprintf("%s/%03d.sql", name, i+1), file)...)
	}
	_, produced, err := pv.validateFiles(f, stmts)
	if err != nil {
		t.Fatalf("%s: validate: %v", name, err)
	}
	rank := map[string]int{"info": 1, "warn": 2, "error": 3}
	out := map[string]string{}
	var all []string
	for _, p := range produced {
		if rank[p.Severity] > rank[out[p.FullCode]] {
			out[p.FullCode] = p.Severity
		}
		all = append(all, fmt.Sprintf("%s/%s", p.FullCode, p.Severity))
	}
	t.Logf("%s: %s", name, strings.Join(all, " "))
	return out
}
