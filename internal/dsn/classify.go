package dsn

import (
	"context"
	"errors"
	"net"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5/pgconn"
)

// FailureClass is why a connection attempt failed, in terms an operator can act
// on. It is rowshape's OWN vocabulary, deliberately not the driver's text.
type FailureClass string

const (
	FailRefused    FailureClass = "connection refused"
	FailDNS        FailureClass = "host not found"
	FailTimeout    FailureClass = "timed out"
	FailCancelled  FailureClass = "cancelled"
	FailAuth       FailureClass = "authentication failed"
	FailNoDatabase FailureClass = "database does not exist"
	FailTLS        FailureClass = "TLS handshake failed"
	FailPermission FailureClass = "permission denied"
	FailUnknown    FailureClass = "unknown"
)

// ClassifyConnect turns a connection error into a class and an actionable hint.
//
// This exists because the secret-hygiene instinct had been taken one step too
// far. `pull` reported exactly "could not connect to the database (check your
// connection settings)" and DISCARDED the underlying error — so an operator
// staring at a red CI job could not tell a typo'd hostname from a wrong password
// from an expired certificate from a database that was simply down. That is not
// a small inconvenience: PRD §10 is explicit that a failure an agent cannot act
// on is a bug, and a human is no better off.
//
// The safety property is preserved by construction: this returns rowshape's own
// strings and NEVER the driver's message. A Postgres auth error reads
// `password authentication failed for user "svc"`, and pgx's dial errors embed
// the full host and port — none of which is echoed here. The class is derived
// from the error, then discarded.
func ClassifyConnect(err error) (FailureClass, string) {
	if err == nil {
		return "", ""
	}

	// Context first: a cancelled or expired context outranks whatever the driver
	// reported on the way out, because the caller already knows why.
	if errors.Is(err, context.Canceled) {
		return FailCancelled, "the run was cancelled before the connection completed"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return FailTimeout, "the connection did not complete before the deadline; check the host is reachable or raise the timeout"
	}

	// A server-side error means the connection itself worked, which is a very
	// different situation from not reaching the server at all.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "28P01", "28000":
			return FailAuth, "the server rejected the credentials; check the user and password"
		case "3D000":
			return FailNoDatabase, "the server is reachable but has no such database; check the database name"
		// Only 42501 is insufficient_privilege. Class 42 is "syntax error OR
		// access rule violation", so 42601 (syntax), 42703 (undefined column)
		// and 42P01 (undefined table) are NOT permission problems, and telling
		// an operator to check their grants would send them the wrong way.
		case "42501":
			return FailPermission, "the role lacks permission; rowshape needs a role that can read the catalog"
		}
		return FailUnknown, "the server refused the connection with SQLSTATE " + pgErr.Code
	}

	// syscall.ECONNREFUSED matches on Unix. Windows reports WSAECONNREFUSED, which
	// does NOT satisfy errors.Is here, so the text check below carries that case —
	// verified against the real driver error, which on Windows reads
	// "connectex: No connection could be made because the target machine actively
	// refused it."
	if errors.Is(err, syscall.ECONNREFUSED) {
		return FailRefused, "nothing is listening on that host and port; check the port and that the server is running"
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return FailDNS, "the hostname did not resolve; check it for typos and check DNS"
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return FailTimeout, "the connection timed out; check the host is reachable and not firewalled"
	}

	// Fall back to substring matching on the driver text WITHOUT retaining it.
	// The text is inspected and dropped; only the class escapes.
	low := strings.ToLower(err.Error())
	switch {
	case strings.Contains(low, "refused"): // "connection refused" (unix) / "actively refused it" (windows)
		return FailRefused, "nothing is listening on that host and port; check the port and that the server is running"
	case strings.Contains(low, "no such host"), strings.Contains(low, "name resolution"):
		return FailDNS, "the hostname did not resolve; check it for typos and check DNS"
	case strings.Contains(low, "timeout"), strings.Contains(low, "timed out"), strings.Contains(low, "deadline"):
		return FailTimeout, "the connection timed out; check the host is reachable and not firewalled"
	case strings.Contains(low, "tls"), strings.Contains(low, "certificate"), strings.Contains(low, "x509"):
		return FailTLS, "the TLS handshake failed; check the server's certificate and your sslmode/sslrootcert"
	case strings.Contains(low, "password"), strings.Contains(low, "authentication"):
		return FailAuth, "the server rejected the credentials; check the user and password"
	case strings.Contains(low, "does not exist"):
		return FailNoDatabase, "the server is reachable but has no such database; check the database name"
	}
	return FailUnknown, "check the host, port, database, credentials, and sslmode"
}
