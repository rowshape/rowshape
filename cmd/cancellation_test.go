package cmd

import (
	"context"
	"testing"
	"time"
)

// TestPullHonorsContextCancellation pins that cancellation actually reaches the
// database work.
//
// cmd.Execute used to call cobra's Execute rather than ExecuteContext, so every
// command received a context.Background() that was never cancelled. Ctrl-C killed
// the rowshape process while the query it had issued kept running server-side to
// completion, and no CI job timeout could reach the database at all — which,
// combined with the unbounded escalation scans pull can issue against a replica,
// is the "took down production and could not stop it" case.
//
// The assertion is about SPEED, not just outcome: without cancellation this path
// blocks for the full 10s ConnectTimeout against a black-holed address, so a fast
// return is only possible if the context is genuinely plumbed through.
//
// Signals themselves are not asserted here. Windows has no POSIX signal delivery,
// so a `kill -INT` test would be testing the harness rather than the code; what
// signal.NotifyContext does is cancel this context, and this is that context.
func TestPullHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before we start

	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		// 10.255.255.1 black-holes: without cancellation this blocks for the
		// full ConnectTimeout (10s).
		_ = runPull(ctx, &pullOptions{
			dsn:     "postgres://u:p@10.255.255.1:5432/app?sslmode=disable",
			out:     t.TempDir() + "/out.yaml",
			privacy: "standard",
		})
	}()

	select {
	case <-done:
		if el := time.Since(start); el > 3*time.Second {
			t.Errorf("cancelled context took %v to abort; it must not wait out the connect timeout", el)
		} else {
			t.Logf("aborted in %v", el)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled context did not abort the connect — cancellation is not plumbed through")
	}
}
