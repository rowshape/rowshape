// Package dsn holds connection-string posture checks shared by every command
// that opens a database connection.
//
// It exists because nothing in the product constrained or even observed
// `sslmode`. Every path handed the user's string to pgx.ParseConfig and
// connected with whatever came back, and pgx (like libpq) defaults to
// sslmode=prefer — which attempts TLS and SILENTLY FALLS BACK TO PLAINTEXT if
// the server declines. For a tool whose whole pitch is safely touching a
// production database, profiling one in the clear with no indication at all is
// the wrong default.
//
// The check warns rather than refuses. Refusing would break every localhost and
// container-network workflow, including this repo's own CI, and `sslmode=disable`
// is genuinely correct there. What is NOT correct is a plaintext connection to a
// remote host that the user never asked for and is never told about.
package dsn

import (
	"net"
	"strings"

	"github.com/jackc/pgx/v5"
)

// InsecureWarning returns a warning when cfg will connect to a REMOTE host
// without TLS, and "" when the connection is either encrypted or local.
//
// pgx resolves sslmode into cfg.TLSConfig: nil means the connection will be
// plaintext. Reading the resolved config rather than parsing the DSN string
// means every spelling is covered at once — URL query parameter, libpq
// keyword/value, and the PGSSLMODE environment variable, which can turn a DSN
// that looks encrypted into one that is not.
func InsecureWarning(cfg *pgx.ConnConfig) string {
	if cfg == nil || cfg.TLSConfig != nil {
		return ""
	}
	host := cfg.Host
	if isLocal(host) {
		return ""
	}
	return "connecting to " + host + " WITHOUT TLS. " +
		"pgx defaults to sslmode=prefer, which falls back to plaintext when the server declines; " +
		"pass sslmode=require (or stronger) to encrypt this connection"
}

// isLocal reports whether a host needs no TLS to be safe: loopback, or a Unix
// domain socket (pgx reports those as a path, which is a local-only transport).
func isLocal(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	h = strings.TrimSuffix(h, ".")
	h = strings.TrimPrefix(h, "[")
	h = strings.TrimSuffix(h, "]")
	if h == "" {
		return true // no host at all: a local socket
	}
	// A Unix socket directory, e.g. /var/run/postgresql or /tmp.
	if strings.HasPrefix(h, "/") || strings.HasPrefix(h, ".") {
		return true
	}
	if h == "localhost" {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
