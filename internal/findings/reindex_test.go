package findings

import (
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
)

func fixtureWithIndex(engineVersion string) *fixture.Fixture {
	return &fixture.Fixture{
		RowshapeFixture: fixture.FormatVersion,
		Meta:            fixture.Meta{Engine: fixture.Engine{Name: "postgres", Version: engineVersion}},
		Tables: map[string]fixture.Table{
			"public.t": {
				Rows:    fixture.Fact[int64]{Value: 1_000_000, Confidence: fixture.Exact},
				Indexes: []fixture.Index{{Name: "idx_big", Bytes: 8 * 1024 * 1024 * 1024}},
			},
		},
	}
}

// TestReindexHonorsTheEngineVersionGate: RFC §9.1 refuses to extrapolate without
// engine.version. CR-T21 consolidated that rule into ONE enforcement point in
// estimateFor "so a fourth analyzer cannot forget it" — and then RS-INDEX-020
// built its Estimate literal directly and forgot it.
//
// The gate cannot simply move into estimateFor for this finding: that function
// extrapolates from a MEASURED basis, while this cost model reads a fixture fact
// (the index's on-disk bytes). So it is enforced at the construction site.
func TestReindexHonorsTheEngineVersionGate(t *testing.T) {
	t.Run("no engine version: no estimate", func(t *testing.T) {
		fnd, ok := reindexFinding(fixtureWithIndex(""), "idx_big", false, false)
		if !ok {
			t.Fatal("the finding itself must still be produced — the lock is real regardless")
		}
		if fnd.Estimate != nil {
			t.Errorf("RFC §9.1 forbids extrapolating without engine.version, got %+v", fnd.Estimate)
		}
		if strings.Contains(fnd.Title, "(") {
			t.Errorf("the title must not carry a duration bucket when there is no estimate: %q", fnd.Title)
		}
	})

	t.Run("engine version present: estimate", func(t *testing.T) {
		fnd, ok := reindexFinding(fixtureWithIndex("16"), "idx_big", false, true)
		if !ok {
			t.Fatal("finding not produced")
		}
		if fnd.Estimate == nil {
			t.Fatal("with an engine version the estimate must be present")
		}
		if fnd.Estimate.Model != "reindex_bytes" {
			t.Errorf("model = %q, want reindex_bytes", fnd.Estimate.Model)
		}
	})
}

// The estimate is derived entirely from the index's bytes, so citing the table's
// row count put a fact the conclusion does not rest on into a signed document —
// the same false-provenance trail indexUniqueFinding explicitly refuses.
func TestReindexProvenanceCitesBytesNotRows(t *testing.T) {
	fnd, ok := reindexFinding(fixtureWithIndex("16"), "idx_big", false, true)
	if !ok {
		t.Fatal("finding not produced")
	}
	for _, dep := range fnd.DependsOn {
		if strings.HasSuffix(dep, ".rows") {
			t.Errorf("DependsOn cites %q, but this estimate rests on the index's bytes, not the row count", dep)
		}
	}
	found := false
	for _, dep := range fnd.DependsOn {
		if strings.Contains(dep, "idx_big") && strings.HasSuffix(dep, ".bytes") {
			found = true
		}
	}
	if !found {
		t.Errorf("DependsOn must cite the index bytes it actually rests on, got %v", fnd.DependsOn)
	}
}

// TestReindexOmitsUnmeasuredBloat: bloat_estimate has NO emitter anywhere —
// internal/profile contains no bloat, n_dead_tup or pgstattuple query — so on
// every fixture a real `rowshape pull` produces the field is nil.
//
// Defaulting it to 0.0 meant every REINDEX finding asserted "bloat estimate 0%",
// the exact opposite of the condition that motivates a REINDEX, and put
// bloat_estimate:0 into the EVIDENCE map. Evidence is part of the Verdict, and
// the Verdict is shaped as a signable in-toto predicate — so this was an
// unmeasured number presented as a measurement inside a document meant to be
// attested.
func TestReindexOmitsUnmeasuredBloat(t *testing.T) {
	fnd, ok := reindexFinding(fixtureWithIndex("16"), "idx_big", false, true)
	if !ok {
		t.Fatal("finding not produced")
	}
	if strings.Contains(fnd.Detail, "bloat") {
		t.Errorf("an absent bloat estimate must not appear in the text, got: %q", fnd.Detail)
	}
	ev, _ := fnd.Evidence.(map[string]any)
	if _, present := ev["bloat_estimate"]; present {
		t.Errorf("an absent bloat estimate must not appear in the evidence of a signed verdict, got: %v", ev)
	}
	if _, present := ev["index_bytes"]; !present {
		t.Errorf("the measured fact must survive, got: %v", ev)
	}
}

// When the fact IS present it must be reported — the fix is about absence, not
// about dropping a real measurement.
func TestReindexReportsMeasuredBloat(t *testing.T) {
	f := fixtureWithIndex("16")
	tbl := f.Tables["public.t"]
	b := 0.42
	tbl.Indexes[0].BloatEstimate = &b
	f.Tables["public.t"] = tbl

	fnd, ok := reindexFinding(f, "idx_big", false, true)
	if !ok {
		t.Fatal("finding not produced")
	}
	if !strings.Contains(fnd.Detail, "42%") {
		t.Errorf("a measured bloat estimate must be reported, got: %q", fnd.Detail)
	}
	ev, _ := fnd.Evidence.(map[string]any)
	if got := ev["bloat_estimate"]; got != 0.42 {
		t.Errorf("evidence bloat_estimate = %v, want 0.42", got)
	}
}
