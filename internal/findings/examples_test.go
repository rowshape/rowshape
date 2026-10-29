package findings

import (
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
)

// TestEveryCodeHasExample: each finding page shows a worked example, so each
// code needs one — with a fixture that parses and both migrations present.
// Whether the example actually produces the code is checked against Postgres by
// corpus/harness's TestFindingExamples.
func TestEveryCodeHasExample(t *testing.T) {
	for _, code := range Codes() {
		ex, ok := ExampleFor(code)
		if !ok {
			t.Errorf("%s has no example in examples.go", code)
			continue
		}
		if ex.Shape == "" || strings.TrimSpace(ex.Migration) == "" || strings.TrimSpace(ex.Fixed) == "" {
			t.Errorf("%s: example needs Shape, Migration and Fixed", code)
		}
		for name, fx := range map[string]string{"Fixture": ex.Fixture, "FixedFixture": ex.FixedFixture} {
			if fx == "" && name == "FixedFixture" {
				continue
			}
			f, err := fixture.Parse([]byte(fx))
			if err != nil {
				t.Errorf("%s: %s does not parse: %v", code, name, err)
				continue
			}
			// The pages say the examples are checked on PostgreSQL 16, and the
			// harness runs each example only on its fixture's major.
			if f.Meta.Engine.Version != "16" {
				t.Errorf("%s: %s declares PostgreSQL %s; the published examples are for 16", code, name, f.Meta.Engine.Version)
			}
		}
	}
	for code := range examples {
		if _, ok := Explain(code); !ok {
			t.Errorf("example for %s, which is not in the catalog", code)
		}
	}
}

func TestMigrationFiles(t *testing.T) {
	got := MigrationFiles("-- Migration 1\nA;\n\n-- Migration 2, later\nB;\n")
	if len(got) != 2 || got[0] != "-- Migration 1\nA;" || got[1] != "-- Migration 2, later\nB;" {
		t.Errorf("MigrationFiles = %q", got)
	}
	if got := MigrationFiles("A;\nB;"); len(got) != 1 {
		t.Errorf("SQL with no marker is one file, got %q", got)
	}
}
