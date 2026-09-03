package findings

import (
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/validate"
	"github.com/rowshape/rowshape/internal/verdict"
)

// TestRSConstraintCorpusVerdicts runs the RS-CONSTRAINT analyzer against the
// corpus cases it owns and asserts each verdict matches expected.json.
func TestRSConstraintCorpusVerdicts(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"not_valid_validated_same_tx", verdict.VerdictWarn},
		// CR-T15: renamed and repurposed as the NEGATIVE case — the two-step
		// split RS-CONSTRAINT-001 recommends must not itself be flagged.
		{"rsconstraint-not-valid-separate-tx", verdict.VerdictPass},
		{"rsconstraint-check-conflict", verdict.VerdictFail},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, mig := loadCorpus(t, c.name)
			res := validate.BuildResult(f, plainCapture(mig), []validate.Analyzer{rsConstraint{}}, false)
			if res.Verdict != c.want {
				t.Errorf("verdict = %s, want %s (corpus expected.json)", res.Verdict, c.want)
			}
		})
	}
}

// TestRSConstraintSameTx: NOT VALID + VALIDATE in the same transaction fires
// RS-CONSTRAINT-001 with a bucketed scan estimate, depends_on, and the
// split-across-transactions remediation.
func TestRSConstraintSameTx(t *testing.T) {
	f, mig := loadCorpus(t, "not_valid_validated_same_tx")
	got := rsConstraint{}.Analyze(f, captureOf(mig, "public.orders", 12_000))
	if len(got) != 1 {
		t.Fatalf("expected 1 RS-CONSTRAINT finding, got %d", len(got))
	}
	fnd := got[0]
	if fnd.Code != "RS-CONSTRAINT-001" || fnd.Severity != verdict.SeverityWarn {
		t.Errorf("want RS-CONSTRAINT-001 warn, got %s/%s", fnd.Code, fnd.Severity)
	}
	if fnd.Estimate == nil || fnd.Estimate.Bucket == "" {
		t.Errorf("same-tx finding must report the validation scan as a bucket, got %+v", fnd.Estimate)
	}
	if len(fnd.DependsOn) != 1 || fnd.DependsOn[0] != "public.orders.rows" {
		t.Errorf("depends_on = %v, want [public.orders.rows]", fnd.DependsOn)
	}
	if !strings.Contains(strings.ToLower(fnd.Remediation), "separate transaction") {
		t.Errorf("remediation must prescribe splitting across transactions: %q", fnd.Remediation)
	}
}

// TestRSConstraintSeparateTxNotFlagged: the CORRECT pattern — ADD NOT VALID,
// COMMIT, then VALIDATE in a separate transaction — is not flagged.
func TestRSConstraintSeparateTxNotFlagged(t *testing.T) {
	f, err := fixture.Parse([]byte(`rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.orders:
    rows: {value: 1000000, confidence: exact}
    columns:
      total_cents: {type: integer, nullable: false}
`))
	if err != nil {
		t.Fatal(err)
	}
	mig := "ALTER TABLE public.orders ADD CONSTRAINT c CHECK (total_cents > 0) NOT VALID; COMMIT; ALTER TABLE public.orders VALIDATE CONSTRAINT c;"
	got := rsConstraint{}.Analyze(f, plainCapture(mig))
	if len(got) != 0 {
		t.Errorf("the split-across-transactions pattern must not be flagged, got %+v", got)
	}
}

// TestRSConstraintCheckConflict: a CHECK whose predicate the profiled range
// violates fires RS-CONSTRAINT-010 (error) with the range as evidence.
func TestRSConstraintCheckConflict(t *testing.T) {
	f, mig := loadCorpus(t, "rsconstraint-check-conflict")
	got := rsConstraint{}.Analyze(f, plainCapture(mig))
	if len(got) != 1 {
		t.Fatalf("expected 1 RS-CONSTRAINT finding, got %d", len(got))
	}
	fnd := got[0]
	if fnd.Code != "RS-CONSTRAINT-010" || fnd.Severity != verdict.SeverityError {
		t.Errorf("want RS-CONSTRAINT-010 error, got %s/%s", fnd.Code, fnd.Severity)
	}
	ev, _ := fnd.Evidence.(map[string]any)
	if ev["check"] == nil || ev["range_min"] == nil {
		t.Errorf("evidence must carry the check and the conflicting range, got %v", ev)
	}
	if fnd.Remediation == "" {
		t.Error("remediation is mandatory")
	}
}

// TestRSConstraintCheckNoConflict: a CHECK the range satisfies is not flagged.
func TestRSConstraintCheckNoConflict(t *testing.T) {
	f, err := fixture.Parse([]byte(`rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.accounts:
    rows: {value: 100, confidence: exact}
    columns:
      balance_cents: {type: integer, nullable: false, range: {min: 10, max: 5000}}
`))
	if err != nil {
		t.Fatal(err)
	}
	mig := "ALTER TABLE public.accounts ADD CONSTRAINT c CHECK (balance_cents >= 0);"
	if got := (rsConstraint{}).Analyze(f, plainCapture(mig)); len(got) != 0 {
		t.Errorf("a CHECK the range satisfies must not be flagged, got %+v", got)
	}
}

// TestParseComparison pins the CHECK-predicate parser.
func TestParseComparison(t *testing.T) {
	cases := []struct {
		expr string
		col  string
		op   string
		k    float64
	}{
		{"amount_cents > 0", "amount_cents", ">", 0},
		{"balance_cents >= 0", "balance_cents", ">=", 0},
		{"qty < 100", "qty", "<", 100},
		{"price <= 999", "price", "<=", 999},
	}
	for _, c := range cases {
		col, op, k, ok := parseComparison(c.expr)
		if !ok || col != c.col || op != c.op || k != c.k {
			t.Errorf("parseComparison(%q) = (%q,%q,%v,%v), want (%q,%q,%v,true)", c.expr, col, op, k, ok, c.col, c.op, c.k)
		}
	}
}

// --- CR-T11: depends_on must name the fact the conclusion rests on ----------
//
// checkConflict concludes from the profiled RANGE that a CHECK will fail, but
// declared `<table>.rows` — a fact it never reads. In a DSSE-signed document
// that is a false provenance trail, and it borrowed the row count's confidence
// for a claim the row count does not support.
//
// The honest path is `<table>.<column>.range`. It used to resolve to `absent`
// because fixture.Range carried no confidence at all (D-010); it now resolves to
// the range's own confidence (D-020), and a fixture like this one — written
// without the field — still reads as `absent`, the weakest reading for a fact
// whose provenance is unknown.
func TestCheckConflictDependsOnTheRangeFact(t *testing.T) {
	f, err := fixture.Parse([]byte(`rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.orders:
    rows: {value: 1000000, confidence: exact}
    columns:
      amount: {type: bigint, nullable: false, range: {min: -50, max: 9000}}
`))
	if err != nil {
		t.Fatal(err)
	}

	var got *verdict.Finding
	for _, fnd := range (rsConstraint{}).Analyze(f, plainCapture(
		"ALTER TABLE public.orders ADD CONSTRAINT amount_positive CHECK (amount > 0);")) {
		if fnd.Code == "RS-CONSTRAINT-010" {
			got = &fnd
		}
	}
	if got == nil {
		t.Fatal("expected RS-CONSTRAINT-010: range [-50, 9000] violates CHECK (amount > 0)")
	}

	want := "public.orders.amount.range"
	if len(got.DependsOn) != 1 || got.DependsOn[0] != want {
		t.Errorf("depends_on = %v, want [%s] — the finding reads the range, not the row count",
			got.DependsOn, want)
	}
	for _, d := range got.DependsOn {
		if strings.HasSuffix(d, ".rows") {
			t.Errorf("depends_on still cites %q, a fact this finding never reads", d)
		}
	}
	// Declining to certify must not weaken the detected failure: an error wants
	// FAIL, and capping leaves FAIL untouched.
	if got.Severity != verdict.SeverityError {
		t.Errorf("severity = %q, want error — a proven range conflict is still a definite failure",
			got.Severity)
	}
}
