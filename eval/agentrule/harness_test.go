package main

import "testing"

// TestScoreStrongSession: a rule-adherent session passes every behaviour and the
// loop closes.
func TestScoreStrongSession(t *testing.T) {
	s := Session{RuleLabel: "v2", Events: []Event{
		{Type: "describe_shape", Tables: []string{"public.users"}},
		{Type: "write_sql", Path: "naive.sql"},
		{Type: "validate", Verdict: "WARN", Findings: []string{"RS-LOCK-001"}},
		{Type: "explain", Code: "RS-LOCK-001"},
		{Type: "write_sql", Path: "rewrite.sql"},
		{Type: "validate", Verdict: "PASS"},
		{Type: "open_pr"},
	}}
	c := Score(s)
	if !c.DescribeBeforeSQL || !c.ValidateBeforePR || !c.NoHandWavedFail || !c.LoopClosed {
		t.Errorf("strong session should pass everything, got %+v", c)
	}
	if c.Adherence() != 1 {
		t.Errorf("adherence = %.2f, want 1", c.Adherence())
	}
}

// TestScoreDescribeAfterSQL: writing SQL before ever calling describe_shape fails
// the first behaviour.
func TestScoreDescribeAfterSQL(t *testing.T) {
	s := Session{Events: []Event{
		{Type: "write_sql", Path: "m.sql"},
		{Type: "describe_shape"},
		{Type: "validate", Verdict: "PASS"},
		{Type: "open_pr"},
	}}
	if c := Score(s); c.DescribeBeforeSQL {
		t.Errorf("describe_shape after write_sql should fail DescribeBeforeSQL")
	}
}

// TestScoreHandWavedVerdict: opening a PR while the last verdict is not PASS is a
// hand-wave and must fail NoHandWavedFail — for both WARN and FAIL.
func TestScoreHandWavedVerdict(t *testing.T) {
	for _, v := range []string{"WARN", "FAIL"} {
		s := Session{Events: []Event{
			{Type: "describe_shape"},
			{Type: "write_sql", Path: "m.sql"},
			{Type: "validate", Verdict: v, Findings: []string{"RS-LOCK-001"}},
			{Type: "open_pr"},
		}}
		c := Score(s)
		if c.NoHandWavedFail {
			t.Errorf("opening a PR on %s should fail NoHandWavedFail", v)
		}
		if c.LoopClosed {
			t.Errorf("a %s that is never fixed did not close the loop", v)
		}
	}
}

// TestScoreNoValidateBeforePR: opening a PR with no validate at all fails both
// ValidateBeforePR and NoHandWavedFail.
func TestScoreNoValidateBeforePR(t *testing.T) {
	s := Session{Events: []Event{
		{Type: "write_sql", Path: "m.sql"},
		{Type: "open_pr"},
	}}
	c := Score(s)
	if c.ValidateBeforePR || c.NoHandWavedFail {
		t.Errorf("PR with no validate should fail ValidateBeforePR and NoHandWavedFail, got %+v", c)
	}
}

// TestWeakenedRuleScoresLower checks that the SCORER ranks an adherent session
// above a non-adherent one.
//
// It is NOT an A/B of the rule, despite the name and despite P4-T8 criterion 3
// describing it that way. This package never loads internal/agentrule, and the
// `rule_version` field is parsed into Session and then read by nothing — so the
// two arms are simply four hand-authored fixtures, two written to score well and
// two written to score badly. This test would pass byte-for-byte if rule.md were
// emptied, deleted, or replaced with its own negation.
//
// What it genuinely pins is Score(): that skipping describe_shape, opening a PR
// on a WARN, and never reaching PASS all cost points, in the direction they
// should. That is worth having. Measuring the RULE needs the live trace runner
// described in README.md, which does not exist in this repo yet.
//
// Note also that the assertions below pin exact outcomes (adherence 1.0 / 0-ish,
// closure 1 / 0). Real traces carry variance, so this test would have to relax
// before it could consume any.
func TestWeakenedRuleScoresLower(t *testing.T) {
	sessions, err := LoadSessions("traces")
	if err != nil {
		t.Fatalf("load traces: %v", err)
	}
	aggs := Summarize(sessions)
	byLabel := map[string]Aggregate{}
	for _, a := range aggs {
		byLabel[a.RuleLabel] = a
		t.Logf("%s: sessions=%d adherence=%.0f%% closure=%.0f%%", a.RuleLabel, a.Sessions, a.AdherenceMean*100, a.ClosureRate*100)
	}
	v2, ok1 := byLabel["v2"]
	weak, ok2 := byLabel["weakened"]
	if !ok1 || !ok2 {
		t.Fatalf("expected both v2 and weakened traces, got %v", aggs)
	}
	if !(v2.AdherenceMean > weak.AdherenceMean) {
		t.Errorf("v2 adherence %.2f should exceed weakened %.2f", v2.AdherenceMean, weak.AdherenceMean)
	}
	if !(v2.ClosureRate > weak.ClosureRate) {
		t.Errorf("v2 closure %.2f should exceed weakened %.2f", v2.ClosureRate, weak.ClosureRate)
	}
	// The v2 fixtures are fully adherent and close the loop; the weakened ones do
	// not close at all.
	if v2.ClosureRate != 1 || weak.ClosureRate != 0 {
		t.Errorf("expected v2 closure 1.0 and weakened 0.0, got %.2f / %.2f", v2.ClosureRate, weak.ClosureRate)
	}
}
