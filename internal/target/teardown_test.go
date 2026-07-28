package target

import (
	"context"
	"testing"
	"time"
)

// TestTeardownContextSurvivesCancellation is the regression for the leak that
// wiring signal cancellation introduced.
//
// Teardown is deferred with the same context the command ran under, and
// Ephemeral.Close opens a NEW connection to issue DROP DATABASE. Once ctx became
// genuinely cancellable, Ctrl-C made that connect fail instantly and the
// disposable database was orphaned. Cancellation has to stop the WORK while
// still permitting the CLEANUP.
func TestTeardownContextSurvivesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the user pressed Ctrl-C

	if ctx.Err() == nil {
		t.Fatal("precondition: the parent context should be cancelled")
	}

	tctx, tcancel := TeardownContext(ctx)
	defer tcancel()

	if err := tctx.Err(); err != nil {
		t.Fatalf("teardown context must be usable after the parent is cancelled, got %v", err)
	}
	if _, ok := tctx.Deadline(); !ok {
		t.Error("teardown must be bounded, or a hung cleanup traps the user after they asked to stop")
	}
}

// The grace period must actually expire, so a wedged server cannot hold the
// process open indefinitely.
func TestTeardownContextIsBounded(t *testing.T) {
	tctx, tcancel := TeardownContext(context.Background())
	defer tcancel()
	dl, ok := tctx.Deadline()
	if !ok {
		t.Fatal("no deadline set")
	}
	if d := time.Until(dl); d <= 0 || d > teardownGrace+time.Second {
		t.Errorf("deadline %v is not within the %v grace", d, teardownGrace)
	}
}
