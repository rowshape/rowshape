package findings

import (
	"sort"

	"github.com/rowshape/rowshape/internal/fixture"
)

// sortedTableNames returns a fixture's table names in a stable order.
//
// Analyzers must never range a map directly. Analyzer REGISTRATION order is
// already deterministic (package init order), so the only thing that made a
// verdict's Findings slice vary run to run was map iteration inside individual
// analyzers. The verdict is shaped as a signable in-toto predicate
// (internal/verdict/dsse.go), so "same inputs, same bytes" is a correctness
// requirement here and not a cosmetic one.
//
// internal/fixture/leaks.go and internal/hydrate/engine.go already applied this
// discipline; two analyzers were missed.
func sortedTableNames(f *fixture.Fixture) []string {
	out := make([]string, 0, len(f.Tables))
	for name := range f.Tables {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
