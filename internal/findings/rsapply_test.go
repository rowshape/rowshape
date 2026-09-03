package findings

import (
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/validate"
	"github.com/rowshape/rowshape/internal/verdict"
)

// TestApplyFailureProducesAFinding is the contract regression. `validate` floored
// a failed apply to FAIL and emitted NOTHING, so `--json` returned
// `{"verdict":"FAIL","findings":null}` and the engine's message went to stderr
// only. The MCP tool and the GitHub Action render this struct and nothing else, so
// an agent was told FAIL and handed nothing to act on — and INV-VERDICT-STABLE
// requires remediation on every error.
func TestApplyFailureProducesAFinding(t *testing.T) {
	c := &validate.Capture{
		Success: false,
		Statements: []validate.Statement{{
			SQL: "ALTER TABLE app.ordrs ADD COLUMN note text", File: "migrations/001.sql", Line: 4,
			ErrCode: "42P01", ErrMsg: `relation "app.ordrs" does not exist`,
		}},
	}
	got := (rsApply{}).Analyze(&fixture.Fixture{}, c)
	if len(got) != 1 {
		t.Fatalf("want one finding, got %d", len(got))
	}
	f := got[0]
	if f.Code != "RS-APPLY-001" || f.Severity != verdict.SeverityError {
		t.Errorf("code/severity = %s/%s", f.Code, f.Severity)
	}
	if f.Remediation == "" {
		t.Error("no remediation; INV-VERDICT-STABLE requires it on every error")
	}
	// The location is what lets an editor and the Action's annotation step point at
	// the offending line rather than at the file as a whole.
	if f.Location == nil || f.Location.File != "migrations/001.sql" || f.Location.Line != 4 {
		t.Errorf("location = %+v, want migrations/001.sql:4", f.Location)
	}
	ev, ok := f.Evidence.(map[string]any)
	if !ok || ev["sqlstate"] != "42P01" {
		t.Errorf("evidence does not carry the SQLSTATE: %+v", f.Evidence)
	}
	if !strings.Contains(f.Title, "app.ordrs") {
		t.Errorf("title does not carry the engine's own words: %s", f.Title)
	}
	// No DependsOn: this rests on what the database DID, not on a fixture fact.
	// Declaring one would be false provenance in a DSSE-signed document.
	if len(f.DependsOn) != 0 {
		t.Errorf("depends_on = %v; this finding reads no fixture fact", f.DependsOn)
	}
}

// TestApplySuccessProducesNothing: every other analyzer describes a migration that
// ran. This one must be silent whenever one did.
func TestApplySuccessProducesNothing(t *testing.T) {
	c := &validate.Capture{Success: true, Statements: []validate.Statement{{SQL: "SELECT 1"}}}
	if got := (rsApply{}).Analyze(&fixture.Fixture{}, c); len(got) != 0 {
		t.Errorf("finding emitted for a clean apply: %v", got)
	}
}

// TestApplyFailureWithoutAStatementIsSilent: floored to FAIL with nothing
// identifiable behind it. A bare FAIL is more honest than a finding pointing at a
// statement this cannot name.
func TestApplyFailureWithoutAStatementIsSilent(t *testing.T) {
	if got := (rsApply{}).Analyze(&fixture.Fixture{}, &validate.Capture{Success: false}); len(got) != 0 {
		t.Errorf("finding invented with no failed statement: %v", got)
	}
}

// TestTimedOutStatementIsNotAnApplyFailure: a cancelled statement is a THIRD
// outcome (D-019) — nothing rejected it. Reporting it as an apply failure would
// claim the database refused something it never got to judge.
func TestTimedOutStatementIsNotAnApplyFailure(t *testing.T) {
	c := &validate.Capture{
		Success:    true,
		TimedOut:   true,
		Statements: []validate.Statement{{SQL: "DELETE FROM big", TimedOut: true}},
	}
	if got := (rsApply{}).Analyze(&fixture.Fixture{}, c); len(got) != 0 {
		t.Errorf("cancelled statement reported as an apply failure: %v", got)
	}
}

// TestGenericApplyFailureYieldsToASpecificOne: RS-APPLY-001 is a FLOOR, not an
// addition. When a migration is rejected because the data does not permit it,
// RS-DATA-001 already says so in the column's own terms and its remediation says
// what to do about the NULLs; adding "read the SQLSTATE" alongside it is a second
// entry for one event that dilutes the actionable advice.
//
// Five corpus cases regressed exactly this way the moment the generic finding
// existed, which is what the suppression is for.
func TestGenericApplyFailureYieldsToASpecificOne(t *testing.T) {
	c := &validate.Capture{
		Success: false,
		Statements: []validate.Statement{{
			SQL: "ALTER TABLE app.users ALTER COLUMN email SET NOT NULL", File: "m/001.sql", Line: 1,
			ErrCode: "23502", ErrMsg: `column "email" contains null values`,
		}},
	}
	f := &fixture.Fixture{
		Meta: fixture.Meta{ID: "t", Engine: fixture.Engine{Name: "postgres", Version: "16"}},
		Tables: map[string]fixture.Table{
			"app.users": {
				Rows: fixture.Fact[int64]{Value: 1000, Confidence: fixture.Exact},
				Columns: map[string]fixture.Column{
					"email": {Type: "text", Nullable: true,
						NullFraction: &fixture.Fact[float64]{Value: 0.04, Confidence: fixture.Exact}},
				},
			},
		},
	}

	res := validate.BuildResult(f, c, validate.Registered(), false)
	var codes []string
	for _, fnd := range res.Findings {
		codes = append(codes, fnd.Code)
	}
	saw := func(prefix string) bool {
		for _, c := range codes {
			if strings.HasPrefix(c, prefix) {
				return true
			}
		}
		return false
	}
	if !saw("RS-DATA") {
		t.Fatalf("the specific analyzer did not fire, so this asserts nothing: %v", codes)
	}
	if saw("RS-APPLY") {
		t.Errorf("generic apply-failure finding kept alongside a specific one: %v", codes)
	}
	// And the verdict is still FAIL: suppressing the generic finding must not
	// suppress the failure.
	if res.Verdict != verdict.VerdictFail {
		t.Errorf("verdict = %s, want FAIL", res.Verdict)
	}
}

// TestGenericApplyFailureSurvivesWhenNothingElseExplains: a typo is rejected by
// the schema, not by the data, so no specific analyzer has anything to say — and
// that is precisely the case the generic finding exists for.
func TestGenericApplyFailureSurvivesWhenNothingElseExplains(t *testing.T) {
	c := &validate.Capture{
		Success: false,
		Statements: []validate.Statement{{
			SQL: "ALTER TABLE app.ordrs ADD COLUMN note text", File: "m/001.sql", Line: 1,
			ErrCode: "42P01", ErrMsg: `relation "app.ordrs" does not exist`,
		}},
	}
	f := &fixture.Fixture{
		Meta:   fixture.Meta{ID: "t", Engine: fixture.Engine{Name: "postgres", Version: "16"}},
		Tables: map[string]fixture.Table{},
	}
	res := validate.BuildResult(f, c, validate.Registered(), false)
	found := false
	for _, fnd := range res.Findings {
		if fnd.Code == "RS-APPLY-001" {
			found = true
		}
	}
	if !found {
		t.Errorf("no finding explains the failure: %+v", res.Findings)
	}
}
