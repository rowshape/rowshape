// Package rlog is rowshape's logging surface: a thin, deliberately small wrapper
// over log/slog that writes to stderr.
//
// There was no logger at all — output was bare fmt.Fprintf to stderr with no
// level and no way to ask for more. Combined with error messages that discarded
// their cause for secret hygiene, that left the tool undebuggable in CI: a red
// job said "could not connect to the database" and nothing else.
//
// Two rules shape this package:
//
//   - STDERR ONLY, never stdout. `validate --json` and `explain --json` put the
//     machine-readable contract on stdout; a stray log line there would corrupt a
//     document an agent is parsing.
//   - Nothing that could carry a credential is ever passed in. The redaction in
//     internal/plan and the classification in internal/dsn exist so callers have
//     something safe to log; this package does not re-derive that and does not
//     scrub. It logs what it is given, so callers must give it safe values.
package rlog

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// Level names accepted by --log-level, loosest to strictest.
const (
	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

var (
	out io.Writer = os.Stderr
	lvl           = new(slog.LevelVar) // defaults to Info
	log           = slog.New(newHandler(out, lvl))
)

// newHandler builds the stderr handler. The format is deliberately plain text
// rather than JSON: the audience is a human reading a CI log, and the
// machine-readable contract is the Verdict on stdout, not this.
func newHandler(w io.Writer, l *slog.LevelVar) slog.Handler {
	return slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: l,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			// Timestamps make CI logs noisy and break golden-output tests for no
			// benefit; the CI system already stamps every line.
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	})
}

// SetLevel parses and applies a level name. An unrecognized name is an error
// rather than a silent fallback: a user asking for --log-level=debgu wants more
// output, and quietly giving them less is the fail-open pattern this codebase
// has been removing.
func SetLevel(name string) error {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", LevelInfo:
		lvl.Set(slog.LevelInfo)
	case LevelDebug:
		lvl.Set(slog.LevelDebug)
	case LevelWarn:
		lvl.Set(slog.LevelWarn)
	case LevelError:
		lvl.Set(slog.LevelError)
	default:
		return &badLevelError{name: name}
	}
	return nil
}

type badLevelError struct{ name string }

func (e *badLevelError) Error() string {
	return "unknown log level " + e.name + " (want debug | info | warn | error)"
}

// SetOutput redirects the log stream. Tests use it to capture; nothing else
// should need it.
func SetOutput(w io.Writer) {
	out = w
	log = slog.New(newHandler(w, lvl))
}

// Enabled reports whether a level would be emitted, so callers can skip
// expensive attribute construction.
func Enabled(l slog.Level) bool { return lvl.Level() <= l }

func Debug(msg string, args ...any) { log.Debug(msg, args...) }
func Info(msg string, args ...any)  { log.Info(msg, args...) }
func Warn(msg string, args ...any)  { log.Warn(msg, args...) }
func Error(msg string, args ...any) { log.Error(msg, args...) }
