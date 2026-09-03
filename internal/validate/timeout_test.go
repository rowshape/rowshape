package validate

import (
	"context"
	"testing"
	"time"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/verdict"
)

// TestTimedOutCaptureCannotPass: a statement that never finished has not been
// shown to be safe, so it must not license a PASS. This is the same rule
// INV-CONFIDENCE-CAPPING states for weak facts, applied to an absent observation.
func TestTimedOutCaptureCannotPass(t *testing.T) {
	f := &fixture.Fixture{
		Meta:   fixture.Meta{ID: "t", Engine: fixture.Engine{Name: "postgres", Version: "16"}},
		Tables: map[string]fixture.Table{},
	}
	c := &Capture{Success: true, TimedOut: true}

	got := BuildResult(f, c, Registered(), false)
	if got.Verdict == verdict.VerdictPass {
		t.Errorf("a cancelled statement produced PASS; it was never observed to complete")
	}
	if got.Verdict != verdict.VerdictWarn {
		t.Errorf("verdict = %s, want WARN — nothing rejected the migration, so FAIL would assert a defect nobody observed",
			got.Verdict)
	}
}

// TestCleanCaptureStillPasses guards the other direction: the floor must not fire
// when no statement was cancelled.
func TestCleanCaptureStillPasses(t *testing.T) {
	f := &fixture.Fixture{
		Meta:   fixture.Meta{ID: "t", Engine: fixture.Engine{Name: "postgres", Version: "16"}},
		Tables: map[string]fixture.Table{},
	}
	if got := BuildResult(f, &Capture{Success: true}, Registered(), false); got.Verdict != verdict.VerdictPass {
		t.Errorf("verdict = %s, want PASS", got.Verdict)
	}
}

// TestRejectedStatementStillFails: a timeout floors to WARN, but a REJECTED
// statement must still be a FAIL. Collapsing the two would downgrade every real
// failure to a warning.
func TestRejectedStatementStillFails(t *testing.T) {
	f := &fixture.Fixture{
		Meta:   fixture.Meta{ID: "t", Engine: fixture.Engine{Name: "postgres", Version: "16"}},
		Tables: map[string]fixture.Table{},
	}
	c := &Capture{Success: false, Statements: []Statement{{SQL: "SELECT 1", ErrCode: "23505", ErrMsg: "dup"}}}
	if got := BuildResult(f, c, Registered(), false); got.Verdict != verdict.VerdictFail {
		t.Errorf("verdict = %s, want FAIL", got.Verdict)
	}
}

// TestApplyCancelsSlowStatement is the behaviour that stops `validate` having to
// SURVIVE an outage in order to REPORT one: once the target enforces the foreign
// keys production has, a cascading DELETE through an unindexed reference runs for
// as long in the disposable database as it would in production.
func TestApplyCancelsSlowStatement(t *testing.T) {
	conn, _ := applyConn(t)

	start := time.Now()
	cap := ApplyWithTimeout(context.Background(), conn,
		[]Located{{SQL: "SELECT pg_sleep(30)"}}, 500*time.Millisecond)
	elapsed := time.Since(start)

	if elapsed > 10*time.Second {
		t.Fatalf("apply took %s; the ceiling did not cancel the statement", elapsed)
	}
	if !cap.TimedOut {
		t.Error("capture does not record the cancellation")
	}
	if len(cap.Statements) != 1 || !cap.Statements[0].TimedOut {
		t.Fatalf("statement not marked as cancelled: %+v", cap.Statements)
	}
	// A cancellation is not a rejection. Leaving the SQLSTATE on it would report an
	// outage as though the data or the schema had said no.
	if got := cap.Statements[0].ErrCode; got != "" {
		t.Errorf("cancelled statement carries error code %q; it was not rejected", got)
	}
	if !cap.Success {
		t.Error("cancellation marked the apply as failed; nothing rejected the migration")
	}
}

// TestApplyWithoutCeilingDoesNotClaimTimeout: with the ceiling disabled, a 57014
// from anywhere else is an ordinary error and must not be relabelled.
func TestApplyWithoutCeilingRunsToCompletion(t *testing.T) {
	conn, _ := applyConn(t)

	cap := ApplyWithTimeout(context.Background(), conn, []Located{{SQL: "SELECT 1"}}, 0)
	if cap.TimedOut {
		t.Error("no ceiling was set, yet the capture claims a timeout")
	}
	if !cap.Success {
		t.Errorf("trivial statement failed: %+v", cap.Statements)
	}
}

// TestApplyCeilingDoesNotAffectFastStatements: the ceiling must be invisible to
// every migration that is not an outage.
func TestApplyCeilingDoesNotAffectFastStatements(t *testing.T) {
	conn, _ := applyConn(t)

	cap := ApplyWithTimeout(context.Background(), conn,
		[]Located{{SQL: "SELECT 1"}, {SQL: "SELECT 2"}}, DefaultStatementTimeout)
	if cap.TimedOut || !cap.Success || len(cap.Statements) != 2 {
		t.Errorf("fast statements disturbed by the ceiling: timedOut=%v success=%v n=%d",
			cap.TimedOut, cap.Success, len(cap.Statements))
	}
}

// TestFindingsMarshalAsEmptyListNotNull: a JSON contract that yields null for
// "none" makes every reader write the same nil guard, and some of them forget.
func TestFindingsMarshalAsEmptyListNotNull(t *testing.T) {
	f := &fixture.Fixture{
		Meta:   fixture.Meta{ID: "t", Engine: fixture.Engine{Name: "postgres", Version: "16"}},
		Tables: map[string]fixture.Table{},
	}
	got := BuildResult(f, &Capture{Success: true}, Registered(), false)
	if got.Findings == nil {
		t.Error("findings is nil; an empty result must marshal as [] so a consumer can iterate it")
	}
}
