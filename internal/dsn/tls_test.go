package dsn

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestInsecureWarning: nothing in the product constrained or observed sslmode.
// pgx defaults to sslmode=prefer, which attempts TLS and silently falls back to
// PLAINTEXT when the server declines — so a production database could be
// profiled in the clear with no indication whatsoever.
//
// The check reads the RESOLVED pgx config rather than the DSN text, so every
// spelling is covered at once: URL query parameter, libpq keyword/value, and
// PGSSLMODE (which can turn a DSN that looks encrypted into one that is not).
func TestInsecureWarning(t *testing.T) {
	cases := []struct {
		name     string
		url      string
		wantWarn bool
	}{
		// Remote + plaintext: the case worth warning about.
		{"remote sslmode=disable", "postgres://u:p@db.prod.example.com:5432/app?sslmode=disable", true},
		{"remote keyword/value disable", "host=db.prod.example.com user=u password=p sslmode=disable", true},

		// Remote + TLS: fine.
		{"remote sslmode=require", "postgres://u:p@db.prod.example.com:5432/app?sslmode=require", false},
		{"remote sslmode=verify-full", "postgres://u:p@db.prod.example.com/app?sslmode=verify-full", false},

		// Local: plaintext is correct and extremely common (CI services, compose).
		{"localhost disable", "postgres://u:p@localhost:5432/app?sslmode=disable", false},
		{"127.0.0.1 disable", "postgres://u:p@127.0.0.1:5432/app?sslmode=disable", false},
		{"ipv6 loopback disable", "postgres://u:p@[::1]:5432/app?sslmode=disable", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := pgx.ParseConfig(tc.url)
			if err != nil {
				t.Fatalf("ParseConfig(%q): %v", tc.url, err)
			}
			got := InsecureWarning(cfg)
			if (got != "") != tc.wantWarn {
				t.Errorf("InsecureWarning = %q, wantWarn=%v (TLSConfig nil: %v, host %q)",
					got, tc.wantWarn, cfg.TLSConfig == nil, cfg.Host)
			}
			// The warning must never echo the DSN, which carries the password.
			if strings.Contains(got, "password") || strings.Contains(got, ":p@") {
				t.Errorf("the warning must not echo credentials: %q", got)
			}
		})
	}
}

// PGSSLMODE can silently downgrade a DSN that carries no sslmode of its own.
// Reading the resolved config is what makes that visible.
func TestInsecureWarningHonorsEnvironment(t *testing.T) {
	t.Setenv("PGSSLMODE", "disable")
	cfg, err := pgx.ParseConfig("postgres://u:p@db.prod.example.com:5432/app")
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if InsecureWarning(cfg) == "" {
		t.Error("PGSSLMODE=disable on a remote host must warn, even though the DSN text looks clean")
	}
}

func TestIsLocal(t *testing.T) {
	local := []string{"localhost", "LocalHost", "127.0.0.1", "::1", "[::1]", "127.0.0.53", "/var/run/postgresql", ""}
	for _, h := range local {
		if !isLocal(h) {
			t.Errorf("isLocal(%q) = false, want true", h)
		}
	}
	remote := []string{"db.prod.example.com", "10.0.0.5", "192.168.1.10", "example.com"}
	for _, h := range remote {
		if isLocal(h) {
			t.Errorf("isLocal(%q) = true, want false", h)
		}
	}
}

func TestInsecureWarningNilSafe(t *testing.T) {
	if InsecureWarning(nil) != "" {
		t.Error("a nil config must not warn")
	}
}
