package fixture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleFixture() *Fixture {
	nf := &Fact[float64]{Value: 0.032, Confidence: Estimated, Via: "pg_stats"}
	uniq := &Fact[bool]{Value: true, Confidence: Exact, Via: "constraint"}
	return &Fixture{
		RowshapeFixture: FormatVersion,
		Meta: Meta{
			ID:          "prod@2026-07-14",
			GeneratedAt: "2026-07-14T09:12:44Z",
			Generator:   "rowshape/1.0.0",
			Engine:      Engine{Name: "postgres", Version: "16.3"},
			Privacy:     "standard",
			Source:      "sha256:41b0",
			Profile:     Profile{Mode: "fast", ScannedAt: "2026-07-14T09:12:44Z"},
		},
		Tables: map[string]Table{
			"public.users": {
				Rows: Fact[int64]{Value: 1200000, Confidence: Exact},
				Columns: map[string]Column{
					"id":    {Type: "bigint", Nullable: false, Unique: uniq},
					"email": {Type: "text", Nullable: true, NullFraction: nf, Format: "email"},
				},
			},
		},
	}
}

// TestEmitRoundTrips: emitted bytes parse back through the P1-T1 data model.
func TestEmitRoundTrips(t *testing.T) {
	f := sampleFixture()
	out, err := Emit(f)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	back, err := Parse(out)
	if err != nil {
		t.Fatalf("re-parse emitted fixture: %v\n%s", err, out)
	}
	if back.Meta.Engine.Version != "16.3" {
		t.Errorf("engine.version lost in round-trip: %+v", back.Meta.Engine)
	}
	if _, ok := back.Tables["public.users"]; !ok {
		t.Errorf("table lost in round-trip")
	}
	// The emitted profile block always carries an explicit escalated list.
	if back.Meta.Profile.Escalated == nil {
		t.Errorf("emitted profile must include an escalated list")
	}
}

// TestEmitRequiresEngineVersion: engine.version is mandatory (RFC §9.1).
func TestEmitRequiresEngineVersion(t *testing.T) {
	f := sampleFixture()
	f.Meta.Engine.Version = ""
	if _, err := Emit(f); err == nil {
		t.Errorf("Emit must refuse a fixture with no engine.version (RFC §9.1)")
	}

	f2 := sampleFixture()
	f2.Meta.Engine.Name = ""
	if _, err := Emit(f2); err == nil {
		t.Errorf("Emit must refuse a fixture with no engine.name")
	}
}

// TestEmitDigestMatchesFile: the stored meta.digest matches a fresh
// recomputation over the emitted file (RFC §11).
func TestEmitDigestMatchesFile(t *testing.T) {
	f := sampleFixture()
	out, err := Emit(f)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	ok, stored, recomputed, err := VerifyDigest(out)
	if err != nil {
		t.Fatalf("VerifyDigest: %v", err)
	}
	if !ok {
		t.Errorf("digest mismatch: stored %q, recomputed %q", stored, recomputed)
	}
	if !strings.HasPrefix(stored, DigestPrefix) {
		t.Errorf("stored digest missing prefix: %q", stored)
	}

	// Tampering with a fact but keeping the old digest is detected.
	tampered := strings.Replace(string(out), "value: 1200000", "value: 999", 1)
	ok, _, _, err = VerifyDigest([]byte(tampered))
	if err != nil {
		t.Fatalf("VerifyDigest(tampered): %v", err)
	}
	if ok {
		t.Errorf("VerifyDigest should reject a fixture whose data was edited without redigest")
	}
}

// TestEmitTwoSpaceIndent: the output is two-space indented for clean diffs.
func TestEmitTwoSpaceIndent(t *testing.T) {
	out, err := Emit(sampleFixture())
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	text := string(out)
	if !strings.Contains(text, "\nmeta:\n  id:") {
		t.Errorf("expected two-space indent under meta:\n%s", text)
	}
	if strings.Contains(text, "\r") {
		t.Errorf("emit must use \\n line endings")
	}
}

// TestEmitSizeAgainstBudget measures the emitter's real per-table cost against
// the RFC §3.3 budget, using a fixture a REAL `rowshape pull` produced.
//
// It replaces a hand-built one, and the replacement is the whole point. The old
// test constructed a 200-table fixture by hand with facts deliberately "at
// estimated (bare)", came in at 101,424 bytes against a 102,400 limit — 976 bytes
// of headroom, under 1% — and passed. A real pull of a 200-table schema is
// 246,129 bytes. The guard was defending a number no user would ever see, on an
// input materially lighter than what the emitter actually writes, which is the
// same trap phase-dd named: a hand-authored fixture is self-consistent in ways a
// real one is not.
//
// testdata/real-pull.yaml is the output of `rowshape pull` against a 20-table
// schema shaped like the RFC's own description — a heavy tail of small lookup
// tables plus a few wide ones. Twenty rather than two hundred so the testdata
// stays small; the assertion is on the PER-TABLE cost, which is what actually
// drifts when the emitted vocabulary grows.
func TestEmitSizeAgainstBudget(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "real-pull.yaml"))
	if err != nil {
		t.Fatalf("read real fixture: %v", err)
	}
	f, err := Parse(data)
	if err != nil {
		t.Fatalf("parse real fixture: %v", err)
	}
	out, err := Emit(f)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	perTable := len(out) / len(f.Tables)
	projected := perTable * BudgetTables

	t.Logf("real pull: %d bytes over %d tables = %d bytes/table; %d-table projection %d bytes",
		len(out), len(f.Tables), perTable, BudgetTables, projected)

	// The budget is the DOCUMENTED one (§3.3). It is set to what the emitter
	// actually costs, not to an aspiration, so a legitimate addition to the
	// vocabulary has room and an accidental blow-up does not.
	if projected > BudgetBytes {
		t.Errorf("a real %d-table fixture projects to %d bytes, over the §3.3 budget of %d; "+
			"either slim what is emitted or move the documented budget deliberately",
			BudgetTables, projected, BudgetBytes)
	}
	// And a floor, so the budget cannot be quietly satisfied by the emitter losing
	// facts: a sudden drop means fields stopped being written, not that anything
	// got more efficient.
	if projected < BudgetBytes/4 {
		t.Errorf("a real %d-table fixture projects to only %d bytes, far under the %d budget — "+
			"check the emitter has not stopped writing facts", BudgetTables, projected, BudgetBytes)
	}
}

// TestParseVerifiedRefusesTamperedFixture: meta.digest is the fixture's identity
// (RFC §11) and the subject of every attestation (INV-DSSE-SHAPE). rowshape
// computed it, stored it, and never checked it.
//
// That is not cosmetic. Demonstrated against a live database before this existed:
// editing a pulled fixture to read null_fraction {value: 0.0, confidence: exact}
// made `validate` return PASS, exit 0, for a SET NOT NULL against a column that
// is 2.9% null in production — while the stale digest sat in the file saying the
// content was untouched. A wrong PASS, which INV-CONFIDENCE-CAPPING calls the one
// thing that must never be wrong, reachable by editing a text file.
func TestParseVerifiedRefusesTamperedFixture(t *testing.T) {
	// Build a fixture and stamp a real digest, the way `pull` emits one.
	f, err := Parse([]byte(`rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.users:
    rows: {value: 100, confidence: exact}
    columns:
      nickname: {type: text, nullable: true, null_fraction: {value: 0.029, confidence: exact}}
`))
	if err != nil {
		t.Fatal(err)
	}
	d, err := Digest(f)
	if err != nil {
		t.Fatal(err)
	}
	good := []byte(`rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}, digest: ` + d + `}
tables:
  public.users:
    rows: {value: 100, confidence: exact}
    columns:
      nickname: {type: text, nullable: true, null_fraction: {value: 0.029, confidence: exact}}
`)

	// Untampered: accepted. Without this the check could just refuse everything.
	if _, err := ParseVerified(good); err != nil {
		t.Fatalf("a fixture whose digest matches its content must be accepted: %v", err)
	}

	// The tamper that produced the wrong PASS: claim the column has no nulls.
	tampered := []byte(strings.Replace(string(good), "value: 0.029", "value: 0.0", 1))
	_, err = ParseVerified(tampered)
	if err == nil {
		t.Fatal("a fixture edited after pull must be refused — its stale digest says the content is " +
			"untouched, and validate would answer from the edited facts")
	}
	for _, want := range []string{"digest mismatch", "rowshape pull"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should mention %q so it is actionable, got: %v", want, err)
		}
	}

	// No digest: accepted. Every fixture in this repo's corpus and test suites is
	// hand-authored and carries none; demanding one would mean rowshape only ever
	// reads its own output.
	none := []byte(`rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.users:
    rows: {value: 100, confidence: exact}
    columns: {nickname: {type: text, nullable: true}}
`)
	if _, err := ParseVerified(none); err != nil {
		t.Errorf("a hand-authored fixture with no digest must be accepted: %v", err)
	}
}
