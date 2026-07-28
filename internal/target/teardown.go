package target

import (
	"context"
	"time"
)

// teardownGrace bounds cleanup. It has to be generous enough for DROP DATABASE
// to force off lingering sessions on a slow server, and short enough that a
// second Ctrl-C is not the user's only escape.
const teardownGrace = 15 * time.Second

// TeardownContext derives the context cleanup should run under: detached from
// the caller's cancellation, but bounded by its own deadline.
//
// This exists because wiring signal cancellation through the command tree
// introduced a resource leak. Teardown is deferred with the SAME ctx the command
// ran under, and Ephemeral.Close opens a NEW connection to issue DROP DATABASE —
// so on Ctrl-C that connect saw an already-cancelled context, returned
// immediately, and the disposable database was orphaned on the admin server. The
// previous code was accidentally safe: ctx was context.Background(), which is
// never cancelled.
//
// Cancellation must stop the WORK and still allow the CLEANUP; those are
// opposite requirements on the same context, which is exactly what
// context.WithoutCancel is for. The deadline is what keeps "still allow the
// cleanup" from becoming "hang forever after the user asked to stop".
//
// Note this is the reverse of the container path, which was accidentally safe
// for the same reason the database path was not: internal/target/container.go
// builds its own context.Background()-derived timeout rather than inheriting.
func TeardownContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), teardownGrace)
}
