package verdict

import (
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
)

// mustFixture parses a fixture from YAML or fails the test.
func mustFixture(t *testing.T, yaml string) *fixture.Fixture {
	t.Helper()
	f, err := fixture.Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return f
}

// The four RFC §7.4 capping scenarios, mirroring the corpus capping-* cases:
// a PASS a finding wants to assert is allowed only when the fixture fact it
// rests on is proven (exact/measured); an estimated or absent fact caps it to a
// resolving WARN.
const (
	fxEstimatedNotNull = `rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.users:
    rows: {value: 400000, confidence: exact}
    columns:
      email: {type: text, nullable: true, null_fraction: {value: 0.0, confidence: estimated}}
`
	fxExactNotNull = `rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.users:
    rows: {value: 400000, confidence: exact}
    columns:
      email: {type: text, nullable: true, null_fraction: {value: 0.0, confidence: exact}}
`
	fxUnprovenUnique = `rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.users:
    rows: {value: 1200000, confidence: exact}
    columns:
      email: {type: text, nullable: false, distinct: {value: 1199000, confidence: estimated}}
`
	fxProvenUnique = `rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.users:
    rows: {value: 1200000, confidence: exact}
    columns:
      email: {type: text, nullable: false, unique: {value: true, confidence: exact, via: probe}}
`
)

// TestCapMatchesCorpusScenarios: a finding wanting PASS produces PASS only when
// its dependency is proven, and WARN (naming the resolving command) when the
// dependency is estimated or absent — the four corpus capping cases (RFC §7.4).
func TestCapMatchesCorpusScenarios(t *testing.T) {
	cases := []struct {
		name        string
		fx          string
		dep         string
		wantVerdict string
		wantResolve bool
	}{
		{"estimated-not-null", fxEstimatedNotNull, "public.users.email.null_fraction", VerdictWarn, true},
		{"exact-not-null", fxExactNotNull, "public.users.email.null_fraction", VerdictPass, false},
		{"unproven-unique", fxUnprovenUnique, "public.users.email.unique", VerdictWarn, true},
		{"proven-unique", fxProvenUnique, "public.users.email.unique", VerdictPass, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := NewEngine(mustFixture(t, c.fx))
			f := Finding{Code: "RS-DATA-014", Severity: SeverityInfo, Title: "ADD constraint", DependsOn: []string{c.dep}}
			got, out := e.Cap(VerdictPass, f)
			if got != c.wantVerdict {
				t.Errorf("verdict = %s, want %s", got, c.wantVerdict)
			}
			if c.wantResolve {
				if out.Severity != SeverityWarn {
					t.Errorf("capped finding severity = %s, want warn", out.Severity)
				}
				if !strings.Contains(out.Remediation, "pull --exact") {
					t.Errorf("capped WARN remediation %q must name the resolving command (pull --exact)", out.Remediation)
				}
				if !strings.Contains(out.Remediation, "public.users.email") {
					t.Errorf("resolve command %q should target the weak column public.users.email", out.Remediation)
				}
				// It must NOT be the bare `pull --exact <column>` form: that fed the
				// column selector where pull expects a connection URL, so running it
				// verbatim errored. The honest form names the source connection.
				if strings.Contains(out.Remediation, "pull --exact public.users.email") {
					t.Errorf("resolve command uses the broken `pull --exact <column>` form that errors when run: %q", out.Remediation)
				}
			} else {
				if got != VerdictPass {
					t.Errorf("proven dependency must allow PASS, got %s", got)
				}
			}
		})
	}
}

// TestCapCannotBeRaisedByFinding is the structural guarantee (acceptance
// criterion 3): a finding that fills in a bogus high Confidence for itself cannot
// use it to escape capping. The engine reads the dependency's confidence from the
// FIXTURE and overwrites the finding's field — there is no API path by which the
// finding's asserted confidence reaches the ceiling computation.
func TestCapCannotBeRaisedByFinding(t *testing.T) {
	e := NewEngine(mustFixture(t, fxUnprovenUnique)) // email.unique is absent → weakest
	f := Finding{
		Code:       "RS-DATA-014",
		Severity:   SeverityInfo,
		DependsOn:  []string{"public.users.email.unique"},
		Confidence: string(fixture.Exact), // a finding lying about its own confidence
	}
	got, out := e.Cap(VerdictPass, f)
	if got != VerdictWarn {
		t.Fatalf("a finding must not raise its verdict by asserting confidence: got %s, want WARN", got)
	}
	// The engine replaced the fabricated confidence with the fixture's reading
	// (absent → empty string), never trusting the finding's own claim.
	if out.Confidence == string(fixture.Exact) {
		t.Errorf("engine trusted the finding's self-asserted confidence %q instead of reading the fixture", out.Confidence)
	}
}

// TestCeilingTable pins the RFC §7.4 confidence→verdict table.
func TestCeilingTable(t *testing.T) {
	cases := []struct {
		c    fixture.Confidence
		want string
	}{
		{fixture.Exact, VerdictPass},
		{fixture.Measured, VerdictPass},
		{fixture.Estimated, VerdictWarn},
		{fixture.Declared, VerdictWarn},
		{absent, VerdictWarn},
	}
	for _, c := range cases {
		if got := Ceiling(c.c); got != c.want {
			t.Errorf("Ceiling(%q) = %s, want %s", c.c, got, c.want)
		}
	}
}

// TestDependencyConfidenceMin: the min across several deps drives the ceiling.
func TestDependencyConfidenceMin(t *testing.T) {
	e := NewEngine(mustFixture(t, fxEstimatedNotNull))
	// rows is exact, null_fraction is estimated → min is estimated.
	min := e.DependencyConfidence([]string{"public.users.rows", "public.users.email.null_fraction"})
	if min != fixture.Estimated {
		t.Errorf("min confidence = %q, want estimated", min)
	}
	// No declared dependencies → not capped (exact), so a structural finding PASSes.
	if got := e.DependencyConfidence(nil); got != fixture.Exact {
		t.Errorf("no-dependency confidence = %q, want exact (uncapped)", got)
	}
}

// TestCapDoesNotWeakenFailures: capping never turns a detected problem (FAIL/WARN)
// into a weaker verdict — it only prevents a wrong PASS.
func TestCapDoesNotWeakenFailures(t *testing.T) {
	e := NewEngine(mustFixture(t, fxUnprovenUnique)) // weakest possible deps
	fail, _ := e.Cap(VerdictFail, Finding{Code: "RS-LOCK-001", Severity: SeverityError, Remediation: "x", DependsOn: []string{"public.users.email.unique"}})
	if fail != VerdictFail {
		t.Errorf("FAIL must pass through capping unchanged, got %s", fail)
	}
	warn, _ := e.Cap(VerdictWarn, Finding{Code: "RS-PERF-002", Severity: SeverityWarn, DependsOn: []string{"public.users.email.unique"}})
	if warn != VerdictWarn {
		t.Errorf("WARN must pass through capping unchanged, got %s", warn)
	}
}

// TestCombine: overall verdict is the worst finding verdict.
func TestCombine(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, VerdictPass},
		{[]string{VerdictPass, VerdictPass}, VerdictPass},
		{[]string{VerdictPass, VerdictWarn}, VerdictWarn},
		{[]string{VerdictWarn, VerdictFail}, VerdictFail},
		{[]string{VerdictFail, VerdictPass}, VerdictFail},
	}
	for _, c := range cases {
		if got := Combine(c.in...); got != c.want {
			t.Errorf("Combine(%v) = %s, want %s", c.in, got, c.want)
		}
	}
}

// TestExactRowsUnlockPassEstimatedDoesNot is the CONSEQUENCE of CR-T28, pinned
// in the capping engine rather than in the profiler.
//
// `profile --exact` now records rows at `exact` instead of the planner's
// `estimated`. That upgrade is only worth paying a full table scan for because
// of what it unlocks here: exact/measured facts may certify PASS, while
// estimated/declared/absent are capped to WARN (RFC §7.4). This asserts the two
// row-count confidences produce DIFFERENT verdicts for the same finding — if
// they ever produce the same one, either capping has stopped discriminating or
// --exact has stopped being worth its cost.
func TestExactRowsUnlockPassEstimatedDoesNot(t *testing.T) {
	finding := Finding{
		Code:      "RS-LOCK-001",
		Severity:  SeverityInfo,
		Title:     "rewrite is small enough to be safe",
		DependsOn: []string{"public.users.rows"},
	}

	verdictFor := func(t *testing.T, conf fixture.Confidence) string {
		t.Helper()
		f := &fixture.Fixture{Tables: map[string]fixture.Table{
			"public.users": {Rows: fixture.Fact[int64]{Value: 1000, Confidence: conf}},
		}}
		got, _ := NewEngine(f).Cap(VerdictPass, finding)
		return got
	}

	exact := verdictFor(t, fixture.Exact)
	estimated := verdictFor(t, fixture.Estimated)

	if exact != VerdictPass {
		t.Errorf("an exact row count must be able to certify PASS, got %s — this is what "+
			"`profile --exact` buys (CR-T28)", exact)
	}
	if estimated != VerdictWarn {
		t.Errorf("an estimated row count must be capped to WARN, got %s", estimated)
	}
	if exact == estimated {
		t.Error("exact and estimated row counts produced the same verdict; either capping has " +
			"stopped discriminating or --exact no longer earns anything")
	}
}

// --- CR-T8: an unrecognized severity must never reach the PASS branch -------
//
// Severity is a plain string on the wire (INV-VERDICT-STABLE fixes the JSON
// shape, so it cannot become a typed enum without a contract break). wantFor's
// old `default` sent anything it did not recognize to PASS — the single most
// permissive outcome was the fallback for "I do not know what this is" — and
// nothing downstream would catch it: capping can only weaken a PASS resting on
// weak FACTS, and a typo'd severity says nothing about facts.
//
// Not live when found: every in-repo analyzer emits one of the three constants.
// This is the structural guard around that.

func TestValidSeverityAcceptsOnlyTheContract(t *testing.T) {
	for _, ok := range []string{SeverityError, SeverityWarn, SeverityInfo, ""} {
		if !ValidSeverity(ok) {
			t.Errorf("ValidSeverity(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"warnn", "eror", "ERROR", "critical", "notice", "0"} {
		if ValidSeverity(bad) {
			t.Errorf("ValidSeverity(%q) = true — an unrecognized severity must not be accepted", bad)
		}
	}
}

// TestUnknownSeverityIsRejectedAtEmitTime: the loud half. A malformed severity
// becomes a contract error (exit 3, explicitly not a verdict) rather than
// silently steering the decision.
func TestUnknownSeverityIsRejectedAtEmitTime(t *testing.T) {
	r := Result{
		Rowshape: Rowshape,
		Verdict:  VerdictPass,
		Findings: []Finding{{Code: "RS-X-001", Severity: "warnn", Title: "typo'd severity"}},
	}
	err := r.Validate()
	if err == nil {
		t.Fatal("a finding with an unknown severity must fail contract validation")
	}
	if !strings.Contains(err.Error(), "warnn") {
		t.Errorf("the error should name the offending value, got %v", err)
	}

	// The three real severities still validate, or this guard would break every
	// legitimate verdict.
	for _, sev := range []string{SeverityWarn, SeverityInfo} {
		ok := Result{Rowshape: Rowshape, Verdict: VerdictPass,
			Findings: []Finding{{Code: "RS-X-001", Severity: sev, Title: "t"}}}
		if err := ok.Validate(); err != nil {
			t.Errorf("severity %q must validate, got %v", sev, err)
		}
	}
}

// --- CR-T24: two FKs on one column resolve to the WEAKEST -------------------
//
// factConfidence's orphan_fraction branch returned the FIRST reference matching
// the column. First-match is the wrong tie-break for a capping engine on
// principle: the design caps a verdict by the MINIMUM confidence of the facts it
// rests on (RFC §7.4), so resolving a genuine ambiguity by declaration order
// could let a strong fact mask a weak sibling and license a PASS the weaker one
// would have capped. Declaration order is not evidence.
//
// No corpus fixture has two references on one column, so this case is
// unreachable there — which is exactly why it needs a test built by hand. These
// assert the property in BOTH declaration orders, because a first-match bug
// passes whenever the weak fact happens to be declared first.
func twoFKFixture(first, second fixture.Confidence) *fixture.Fixture {
	return &fixture.Fixture{Tables: map[string]fixture.Table{
		"public.orders": {
			Rows: fixture.Fact[int64]{Value: 1000, Confidence: fixture.Exact},
			References: []fixture.Reference{
				{Column: "user_id", To: "public.users.id",
					OrphanFraction: &fixture.Fact[float64]{Value: 0, Confidence: first}},
				{Column: "user_id", To: "public.accounts.owner_id",
					OrphanFraction: &fixture.Fact[float64]{Value: 0, Confidence: second}},
			},
		},
	}}
}

func TestTwoReferencesOnOneColumnResolveToWeakest(t *testing.T) {
	cases := []struct {
		name          string
		first, second fixture.Confidence
		want          fixture.Confidence
	}{
		{"weak declared first", fixture.Estimated, fixture.Exact, fixture.Estimated},
		// The order that a first-match implementation gets WRONG.
		{"strong declared first", fixture.Exact, fixture.Estimated, fixture.Estimated},
		{"both exact", fixture.Exact, fixture.Exact, fixture.Exact},
		{"declared beats nothing", fixture.Exact, fixture.Declared, fixture.Declared},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEngine(twoFKFixture(tc.first, tc.second))
			got := e.DependencyConfidence([]string{"public.orders.user_id.orphan_fraction"})
			if got != tc.want {
				t.Errorf("two FKs on one column (%s, %s) resolved to %q, want the WEAKEST %q — "+
					"declaration order must not decide which fact caps the verdict",
					tc.first, tc.second, got, tc.want)
			}
		})
	}
}

// TestTwoReferencesCannotLicenseAPass is the consequence: with one strong and one
// weak FK fact, a finding resting on that column must be capped to WARN whichever
// order they were declared in. A first-match engine certifies PASS for one of the
// two orderings, which is a wrong PASS decided by nothing but file layout.
func TestTwoReferencesCannotLicenseAPass(t *testing.T) {
	finding := Finding{
		Code:      "RS-DATA-020",
		Severity:  SeverityInfo,
		Title:     "no orphans block this FK",
		DependsOn: []string{"public.orders.user_id.orphan_fraction"},
	}
	for _, order := range []struct {
		name          string
		first, second fixture.Confidence
	}{
		{"strong first", fixture.Exact, fixture.Estimated},
		{"weak first", fixture.Estimated, fixture.Exact},
	} {
		t.Run(order.name, func(t *testing.T) {
			got, _ := NewEngine(twoFKFixture(order.first, order.second)).Cap(VerdictPass, finding)
			if got != VerdictWarn {
				t.Errorf("verdict = %s, want WARN — one of the two FK facts is only `estimated`, "+
					"so this must not certify regardless of declaration order", got)
			}
		})
	}
}

// TestSingleReferenceUnchanged guards the common path: one FK on a column must
// still resolve to exactly its own confidence.
func TestSingleReferenceUnchanged(t *testing.T) {
	f := &fixture.Fixture{Tables: map[string]fixture.Table{
		"public.orders": {References: []fixture.Reference{
			{Column: "user_id", To: "public.users.id",
				OrphanFraction: &fixture.Fact[float64]{Value: 0, Confidence: fixture.Exact}},
		}},
	}}
	if got := NewEngine(f).DependencyConfidence([]string{"public.orders.user_id.orphan_fraction"}); got != fixture.Exact {
		t.Errorf("single reference resolved to %q, want exact", got)
	}
}
