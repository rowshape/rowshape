package findings

import (
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/validate"
	"github.com/rowshape/rowshape/internal/verdict"
)

// plainCapture builds a capture of applying a migration with no runtime signals
// — RS-DATA reasons from the fixture facts plus the statement text, not from
// lock/timing, so a bare statement list is enough.
func plainCapture(migration string) *validate.Capture {
	var stmts []validate.Statement
	for _, s := range validate.SplitStatements(migration) {
		stmts = append(stmts, validate.Statement{SQL: s})
	}
	return &validate.Capture{Success: true, Statements: stmts}
}

// TestRSDataCorpusVerdicts runs the RS-DATA analyzer against the corpus cases it
// owns — including the four capping-* cases (the wrong-PASS regression suite,
// P2-T2) that RS-DATA-014 and RS-DATA-001 must satisfy — and asserts each verdict
// matches expected.json, with a resolving command on every capped WARN (§7.4).
func TestRSDataCorpusVerdicts(t *testing.T) {
	cases := []struct {
		name        string
		want        string
		wantResolve bool
	}{
		{"capping-proven-unique-ok", verdict.VerdictPass, false},
		{"capping-unproven-unique", verdict.VerdictWarn, true},
		{"capping-exact-not-null", verdict.VerdictPass, false},
		{"capping-estimated-not-null", verdict.VerdictWarn, true},
		{"rsdata-unique-unproven", verdict.VerdictWarn, true},
		{"rsdata-notnull-has-nulls", verdict.VerdictFail, false},
		{"validate_orphans", verdict.VerdictFail, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, mig := loadCorpus(t, c.name)
			res := validate.BuildResult(f, plainCapture(mig), []validate.Analyzer{rsData{}}, false)
			if res.Verdict != c.want {
				t.Errorf("verdict = %s, want %s (corpus expected.json)", res.Verdict, c.want)
			}
			if c.wantResolve {
				if len(res.Findings) == 0 {
					t.Fatal("expected a capped WARN finding, got none")
				}
				if !strings.Contains(res.Findings[0].Remediation, "pull --exact") {
					t.Errorf("capped WARN must name the resolving command, got %q", res.Findings[0].Remediation)
				}
			}
		})
	}
}

// TestRSDataNonExactUniqueIsNotProof: `unique` MUST be exact or absent (RFC §7.2,
// INV-UNIQUENESS). A fact carrying a weaker confidence — which a hand-authored or
// non-conformant fixture can slip past ParseVerified (it checks only the digest) —
// is not a proof in either direction. A non-exact unique=false must NOT drive a
// confident FAIL; it must be treated as absent and cap to WARN.
func TestRSDataNonExactUniqueIsNotProof(t *testing.T) {
	// unique=false at ESTIMATED confidence: an emitter guessing non-uniqueness from
	// n_distinct < rows. Reading Value produced a FAIL asserting an exactness the
	// fact never had.
	f, err := fixture.Parse([]byte(`rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.users:
    rows: {value: 2000000, confidence: exact}
    columns:
      email: {type: text, nullable: false, unique: {value: false, confidence: estimated, via: pg_stats}}
`))
	if err != nil {
		t.Fatal(err)
	}
	mig := "ALTER TABLE public.users ADD CONSTRAINT u UNIQUE (email);"
	res := validate.BuildResult(f, plainCapture(mig), []validate.Analyzer{rsData{}}, false)

	if res.Verdict != verdict.VerdictWarn {
		t.Fatalf("verdict = %s, want WARN — a non-exact unique fact is not a proof and must not FAIL", res.Verdict)
	}
	if len(res.Findings) != 1 || res.Findings[0].Code != "RS-DATA-014" {
		t.Fatalf("want one RS-DATA-014 finding, got %+v", res.Findings)
	}
	if sev := res.Findings[0].Severity; sev == verdict.SeverityError {
		t.Errorf("severity = %q, must not be a confident FAIL on non-exact evidence", sev)
	}
	if d := res.Findings[0].Detail; strings.Contains(d, "proven non-unique") {
		t.Errorf("detail must not claim a proven violation from non-exact evidence: %q", d)
	}
}

// TestRSDataUnprovenUnique: RS-DATA-014 warns (never PASS) when uniqueness is
// unproven, declares depends_on, carries capped confidence, and names
// `rowshape pull --exact <col>` (RFC §7.4, INV-UNIQUENESS).
func TestRSDataUnprovenUnique(t *testing.T) {
	f, mig := loadCorpus(t, "rsdata-unique-unproven")
	res := validate.BuildResult(f, plainCapture(mig), []validate.Analyzer{rsData{}}, false)

	if res.Verdict != verdict.VerdictWarn {
		t.Fatalf("verdict = %s, want WARN (never PASS on unproven uniqueness)", res.Verdict)
	}
	fnd := res.Findings[0]
	if fnd.Code != "RS-DATA-014" {
		t.Errorf("code = %s, want RS-DATA-014", fnd.Code)
	}
	if len(fnd.DependsOn) != 1 || fnd.DependsOn[0] != "public.users.email.unique" {
		t.Errorf("depends_on = %v, want [public.users.email.unique]", fnd.DependsOn)
	}
	// email.unique is absent → the capped confidence is the weakest reading.
	if fnd.Confidence == string(fixture.Exact) || fnd.Confidence == string(fixture.Measured) {
		t.Errorf("confidence = %q, must be capped below measured for unproven uniqueness", fnd.Confidence)
	}
	// The resolve clause must name `pull --exact` AND the fact to raise. It is not
	// a bare `pull --exact <column>` — that fed a column selector where a DSN
	// belongs and errored when run verbatim.
	if !strings.Contains(fnd.Remediation, "pull --exact") || !strings.Contains(fnd.Remediation, "public.users.email") {
		t.Errorf("remediation must name the resolving command and the target fact, got %q", fnd.Remediation)
	}
}

// TestRSDataProvenUniquePasses: proven-exact uniqueness certifies PASS — capping
// does not touch a proven fact (§7.4 boundary).
func TestRSDataProvenUniquePasses(t *testing.T) {
	f, mig := loadCorpus(t, "capping-proven-unique-ok")
	res := validate.BuildResult(f, plainCapture(mig), []validate.Analyzer{rsData{}}, false)
	if res.Verdict != verdict.VerdictPass {
		t.Errorf("verdict = %s, want PASS (uniqueness proven exact)", res.Verdict)
	}
	if len(res.Findings) != 0 {
		t.Errorf("a clean PASS should surface no findings, got %+v", res.Findings)
	}
}

// TestRSDataNotNullWithNulls: SET NOT NULL against a proven-nonzero null_fraction
// is an error/FAIL with the null_fraction as evidence.
func TestRSDataNotNullWithNulls(t *testing.T) {
	f, mig := loadCorpus(t, "rsdata-notnull-has-nulls")
	got := rsData{}.Analyze(f, plainCapture(mig))
	if len(got) != 1 {
		t.Fatalf("expected 1 RS-DATA finding, got %d", len(got))
	}
	if got[0].Code != "RS-DATA-001" || got[0].Severity != verdict.SeverityError {
		t.Errorf("want RS-DATA-001 error, got %s/%s", got[0].Code, got[0].Severity)
	}
	ev, _ := got[0].Evidence.(map[string]any)
	if ev["null_fraction"] != 0.02 {
		t.Errorf("null_fraction evidence = %v, want 0.02", ev["null_fraction"])
	}
}

// TestRSDataOrphanValidate: validating a FK whose orphan_fraction is nonzero is
// flagged as will-fail (RFC §6.6). The VALIDATE resolves its column from the
// earlier ADD CONSTRAINT ... NOT VALID in the same migration.
func TestRSDataOrphanValidate(t *testing.T) {
	f, mig := loadCorpus(t, "validate_orphans")
	got := rsData{}.Analyze(f, plainCapture(mig))
	if len(got) != 1 {
		t.Fatalf("expected 1 RS-DATA orphan finding, got %d: %+v", len(got), got)
	}
	if got[0].Code != "RS-DATA-020" || got[0].Severity != verdict.SeverityError {
		t.Errorf("want RS-DATA-020 error (will-fail), got %s/%s", got[0].Code, got[0].Severity)
	}
	if len(got[0].DependsOn) != 1 || got[0].DependsOn[0] != "public.orders.user_id.orphan_fraction" {
		t.Errorf("depends_on = %v, want [public.orders.user_id.orphan_fraction]", got[0].DependsOn)
	}
}
