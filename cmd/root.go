// Package cmd wires the rowshape CLI subcommand tree.
package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	// Registers the RS-* finding analyzers with the validate pipeline (P2-T8+).
	_ "github.com/rowshape/rowshape/internal/findings"
	"github.com/rowshape/rowshape/internal/rlog"
	"github.com/spf13/cobra"
)

// ExitError carries a process exit code up to main so each command maps its
// outcome onto the stable exit-code contract (INV-VERDICT-STABLE, PRD §10).
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string { return fmt.Sprintf("exit code %d", e.Code) }

// NewRootCmd builds the full command tree. Every subcommand named in PRD §8.1
// is present; in phase 0 each leaf is a stub that returns a tool error.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "rowshape",
		Short: "The type-checker for database migrations",
		Long: "rowshape — execute a proposed schema change against production-shaped\n" +
			"data in a disposable environment and return a machine-readable verdict.\n\n" +
			"A human and an agent get the same answer through the same contract.",
		Version:       fmt.Sprintf("%s (commit %s, built %s)", version, commit, date),
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	// --log-level is persistent so it works on every subcommand. It is applied in
	// PersistentPreRunE, before any command body runs, so a --log-level=debug run
	// gets debug output from the very first step rather than from wherever the
	// flag happened to be read.
	var logLevel string
	root.PersistentFlags().StringVar(&logLevel, "log-level", "info",
		"log verbosity on stderr: debug | info | warn | error")
	root.PersistentPreRunE = func(_ *cobra.Command, _ []string) error {
		if err := rlog.SetLevel(logLevel); err != nil {
			// An unrecognized level is refused rather than silently defaulting:
			// someone asking for more output should not quietly get less.
			return err
		}
		return nil
	}
	root.AddCommand(
		newInitCmd(),
		newPullCmd(),
		newHydrateCmd(),
		newValidateCmd(),
		newExplainCmd(),
		newPlanCmd(),
		newVerifyCmd(),
		newInspectCmd(),
		newMCPCmd(),
		newAnnotateCmd(),
	)
	return root
}

// Execute runs the root command. main maps the returned error onto an exit code.
//
// It uses ExecuteContext with a signal-cancelled context, not plain Execute.
// With Execute, cobra hands every command a context.Background() that is never
// cancelled: Ctrl-C killed the rowshape process while the query it had issued
// kept running server-side to completion, and no CI job timeout could reach the
// database at all. That is the difference between "the operator stopped it" and
// "the operator stopped watching it" — and on a production replica, with the
// unbounded escalation scans pull can issue, it is the difference that matters.
//
// Teardown must NOT ride on this context. Ephemeral.Close opens a new
// connection to issue DROP DATABASE, so a cancelled context would make cleanup
// fail instantly and orphan the disposable database — cancellation has to stop
// the work while still permitting the cleanup. Both deferred teardowns therefore
// go through target.TeardownContext, which detaches from cancellation and
// applies its own deadline. (The Docker path was accidentally safe already:
// container.go derives its timeout from context.Background() rather than
// inheriting.)
//
// A second signal is deliberately left to the runtime's default behavior: the
// first Ctrl-C asks for an orderly stop, and if cleanup itself hangs, another
// one kills the process outright rather than trapping the user.
func Execute() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return NewRootCmd().ExecuteContext(ctx)
}
