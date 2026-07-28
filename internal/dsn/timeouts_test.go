package dsn

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func parse(t *testing.T, url string) *pgx.ConnConfig {
	t.Helper()
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatalf("ParseConfig(%q): %v", url, err)
	}
	return cfg
}

// Timeouts must ride in RuntimeParams, which pgx sends as STARTUP parameters —
// so they are in force for the first query rather than after a SET that some
// code path might skip.
func TestApplySetsStartupParameters(t *testing.T) {
	cfg := parse(t, "postgres://u:p@db.example.com:5432/app")
	Apply(cfg, ScanDefaults)

	want := map[string]string{
		"statement_timeout":                   "600000", // 10m
		"lock_timeout":                        "5000",   // 5s
		"idle_in_transaction_session_timeout": "60000",  // 60s
	}
	for k, v := range want {
		if got := cfg.RuntimeParams[k]; got != v {
			t.Errorf("RuntimeParams[%q] = %q, want %q", k, got, v)
		}
	}
	if cfg.ConnectTimeout != 10*time.Second {
		t.Errorf("ConnectTimeout = %v, want 10s", cfg.ConnectTimeout)
	}
}

// A zero duration means "no limit" and must be expressed by ABSENCE, not by
// sending 0 — so a server-side default the DBA configured is respected rather
// than silently overridden.
func TestApplyZeroMeansUnset(t *testing.T) {
	cfg := parse(t, "postgres://u:p@db.example.com:5432/app")
	Apply(cfg, Timeouts{Lock: time.Second}) // everything else zero

	if _, ok := cfg.RuntimeParams["statement_timeout"]; ok {
		t.Error("a zero statement timeout must leave the parameter unset, not send 0")
	}
	if _, ok := cfg.RuntimeParams["idle_in_transaction_session_timeout"]; ok {
		t.Error("a zero idle timeout must leave the parameter unset")
	}
	if cfg.RuntimeParams["lock_timeout"] != "1000" {
		t.Errorf("lock_timeout = %q, want 1000", cfg.RuntimeParams["lock_timeout"])
	}
	if cfg.ConnectTimeout != 0 {
		t.Errorf("ConnectTimeout = %v, want unset", cfg.ConnectTimeout)
	}
}

// Someone who put a timeout in their own DSN meant it. Overriding a user's
// explicit setting with our default would be the tool second-guessing an
// operator about their own database.
func TestApplyDoesNotOverrideUserSettings(t *testing.T) {
	cfg := parse(t, "postgres://u:p@db.example.com:5432/app?statement_timeout=1234")
	if cfg.RuntimeParams["statement_timeout"] != "1234" {
		t.Skipf("pgx did not surface statement_timeout in RuntimeParams (got %v); nothing to assert",
			cfg.RuntimeParams)
	}
	Apply(cfg, ScanDefaults)
	if got := cfg.RuntimeParams["statement_timeout"]; got != "1234" {
		t.Errorf("statement_timeout = %q, want the user's 1234 preserved", got)
	}
	// The ones the user did NOT set still get defaults.
	if cfg.RuntimeParams["lock_timeout"] != "5000" {
		t.Errorf("lock_timeout = %q, want the default applied", cfg.RuntimeParams["lock_timeout"])
	}
}

// ReadDefaults deliberately carries NO statement limit: `pull --exact` is
// documented as a full streaming pass taking minutes to hours, and plan/verify
// read catalogs on schemas that can be very large. Capping those by default
// would turn a slow-but-correct run into a mysterious partial failure.
func TestReadDefaultsDoNotCapStatements(t *testing.T) {
	if ReadDefaults.Statement != 0 {
		t.Errorf("ReadDefaults.Statement = %v, want 0 — --exact is documented as taking hours",
			ReadDefaults.Statement)
	}
	// The limits that are always safe must still be set.
	if ReadDefaults.Lock == 0 {
		t.Error("ReadDefaults must still bound lock waits")
	}
	if ReadDefaults.IdleInTransaction == 0 {
		t.Error("ReadDefaults must still bound idle transactions")
	}
	if ReadDefaults.Connect == 0 {
		t.Error("ReadDefaults must still bound connect")
	}
}

// ScanDefaults is the fast-mode profile, where every query should be a catalog
// read or a sampled aggregate.
func TestScanDefaultsCapStatements(t *testing.T) {
	if ScanDefaults.Statement != 10*time.Minute {
		t.Errorf("ScanDefaults.Statement = %v, want 10m", ScanDefaults.Statement)
	}
}

func TestApplyNilSafe(t *testing.T) {
	Apply(nil, ScanDefaults) // must not panic
}

// TestApplyRespectsOptionsSpelling is the regression for the case the function's
// own comment cited and did not handle.
//
// libpq accepts a timeout two ways, and only one lands in RuntimeParams under
// its own name:
//
//	?statement_timeout=5s             -> RuntimeParams["statement_timeout"]
//	?options=-c statement_timeout=5s  -> RuntimeParams["options"]
//
// Checking only the first meant rowshape sent its own value alongside the user's
// options string in the same startup packet, with undefined precedence.
func TestApplyRespectsOptionsSpelling(t *testing.T) {
	cases := []struct{ name, options string }{
		{"-c with a space", "-c statement_timeout=5s"},
		{"-c without a space", "-cstatement_timeout=5s"},
		{"double dash", "--statement_timeout=5s"},
		{"among other settings", "-c work_mem=64MB -c statement_timeout=5s"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := parse(t, "postgres://u:p@db.example.com:5432/app")
			cfg.RuntimeParams["options"] = c.options
			Apply(cfg, ScanDefaults)
			if _, set := cfg.RuntimeParams["statement_timeout"]; set {
				t.Errorf("the user set statement_timeout via options=%q; rowshape must not also send its own", c.options)
			}
			// The limits the user did NOT set still get defaults.
			if cfg.RuntimeParams["lock_timeout"] != "5000" {
				t.Errorf("lock_timeout = %q, want the default applied", cfg.RuntimeParams["lock_timeout"])
			}
		})
	}
}

// An options string that mentions something else entirely must not suppress our
// defaults, and a key that merely appears as a substring must not match.
func TestOptionsMentionIsPrecise(t *testing.T) {
	if optionsMention("-c work_mem=64MB", "statement_timeout") {
		t.Error("an unrelated option must not suppress the default")
	}
	if optionsMention("", "statement_timeout") {
		t.Error("an empty options string mentions nothing")
	}
	if optionsMention("-c my_statement_timeout=5s", "statement_timeout") {
		t.Error("a key that only contains ours as a suffix must not match")
	}
	if !optionsMention("-c STATEMENT_TIMEOUT=5s", "statement_timeout") {
		t.Error("GUC names are case-insensitive")
	}
}
