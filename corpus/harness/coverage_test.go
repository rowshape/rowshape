package harness

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/findings"
	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/validate"
)

// knownUncovered records codes that deliberately have no corpus case, with the
// reason. An entry here is a debt with a name, not an exemption — the test fails
// if a code listed here starts being covered, so the list cannot rot silently in
// either direction.
var knownUncovered = map[string]string{
	"RS-LOCK-003": "ATTACH PARTITION errors against the hydrated table because hydrate does not " +
		"reproduce partitioning (CR4-T11), so a corpus case would fail for a reason unrelated to the " +
		"finding. Adding one would paper over the fidelity gap rather than expose it.",
}

// TestEveryFindingCodeHasACorpusCase is the coverage guard.
//
// docs/TESTING-GAPS.md asserted that all finding codes were exercised by at
// least one corpus case. That was true when there were 14 codes; by the time
// there were 26, six were exercised only by unit tests. The corpus is the ONLY
// thing that runs findings against a real Postgres in CI, so a code with no case
// has never been proven to fire end to end — and a prose claim in a markdown file
// cannot notice when it stops being true. This can.
func TestEveryFindingCodeHasACorpusCase(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join("..", "cases", "*"))
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no corpus cases found (err=%v) — this test would pass vacuously", err)
	}

	fired := map[string]bool{}
	for _, d := range dirs {
		fb, err := os.ReadFile(filepath.Join(d, "fixture.yaml"))
		if err != nil {
			continue
		}
		f, err := fixture.Parse(fb)
		if err != nil {
			t.Errorf("%s: fixture does not parse: %v", filepath.Base(d), err)
			continue
		}
		mb, err := os.ReadFile(filepath.Join(d, "migration.sql"))
		if err != nil {
			continue
		}
		var sc []validate.Statement
		for _, s := range validate.SplitStatements(string(mb)) {
			sc = append(sc, validate.Statement{SQL: s})
		}
		// Statically, with a success-capture: enough to prove the analyzer fires
		// on the case's SQL. The DB-backed run in CI proves the rest.
		res := validate.BuildResult(f, &validate.Capture{Success: true, Statements: sc}, validate.Registered(), false)
		for _, fnd := range res.Findings {
			fired[fnd.Code] = true
		}
	}

	codes := findings.Codes()
	if len(codes) == 0 {
		t.Fatal("the registry reported no codes — the walk is broken and this test would pass vacuously")
	}

	var missing []string
	for _, code := range codes {
		if fired[code] {
			if why, listed := knownUncovered[code]; listed {
				t.Errorf("%s IS covered by a corpus case but is still listed in knownUncovered (%q) — "+
					"remove the entry", code, why)
			}
			continue
		}
		if _, allowed := knownUncovered[code]; allowed {
			continue
		}
		missing = append(missing, code)
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d finding code(s) have no corpus case: %s\n"+
			"The corpus is the only thing that exercises findings against a real Postgres in CI, so an "+
			"uncovered code has never been proven to fire end to end. Add a case under corpus/cases/, or "+
			"record it in knownUncovered with the reason.",
			len(missing), strings.Join(missing, ", "))
	}
	t.Logf("%d registry codes, %d fired across %d corpus cases, %d deliberately uncovered",
		len(codes), len(fired), len(dirs), len(knownUncovered))
}
