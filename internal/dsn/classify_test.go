package dsn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// The whole point of classification is that the operator learns WHICH failure
// happened without the driver's text — which embeds the host, port, user and
// database — ever being printed.
func TestClassifyConnect(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want FailureClass
	}{
		{"cancelled", context.Canceled, FailCancelled},
		{"deadline", context.DeadlineExceeded, FailTimeout},
		{"bad password", &pgconn.PgError{Code: "28P01"}, FailAuth},
		{"bad auth spec", &pgconn.PgError{Code: "28000"}, FailAuth},
		{"no database", &pgconn.PgError{Code: "3D000"}, FailNoDatabase},
		{"insufficient privilege", &pgconn.PgError{Code: "42501"}, FailPermission},
		// Class 42 is "syntax error OR access rule violation": only 42501 is a
		// permission problem. Reporting the rest as permission failures would
		// send an operator to check grants over a typo in their own SQL.
		{"syntax error is not a permission problem", &pgconn.PgError{Code: "42601"}, FailUnknown},
		{"undefined table is not a permission problem", &pgconn.PgError{Code: "42P01"}, FailUnknown},
		{"undefined column is not a permission problem", &pgconn.PgError{Code: "42703"}, FailUnknown},
		{"dns", &net.DNSError{Err: "no such host", Name: "nope.invalid"}, FailDNS},

		// Text fallbacks. The Windows wording differs from the Unix one, and
		// errors.Is(ECONNREFUSED) does NOT match on Windows (WSAECONNREFUSED),
		// so both spellings must be carried by the text check.
		{"refused unix", errors.New("dial tcp 127.0.0.1:5432: connect: connection refused"), FailRefused},
		{"refused windows", errors.New("connectex: No connection could be made because the target machine actively refused it."), FailRefused},
		{"tls", errors.New("tls: failed to verify certificate: x509: certificate has expired"), FailTLS},
		{"auth text", errors.New(`password authentication failed for user "svc"`), FailAuth},
		{"unknown", errors.New("something entirely unexpected"), FailUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, hint := ClassifyConnect(tc.err)
			if got != tc.want {
				t.Errorf("ClassifyConnect = %q, want %q", got, tc.want)
			}
			if hint == "" {
				t.Error("every class must carry an actionable hint; that is the point")
			}
		})
	}
}

// A wrapped error must still classify: callers wrap with %w routinely.
func TestClassifyConnectSeesThroughWrapping(t *testing.T) {
	wrapped := fmt.Errorf("connect to admin database: %w", &pgconn.PgError{Code: "28P01"})
	if got, _ := ClassifyConnect(wrapped); got != FailAuth {
		t.Errorf("wrapped error classified as %q, want %q", got, FailAuth)
	}
}

// THE safety property: nothing the classifier returns may echo the driver's
// text, because that text carries the host, port, user and database — and a
// caller could be handed an error whose message contains a password outright.
func TestClassifyConnectNeverEchoesTheError(t *testing.T) {
	const secret = "hunter2"
	nasty := []error{
		fmt.Errorf("failed to connect to `user=svc database=app password=%s`: dial error", secret),
		&pgconn.PgError{Code: "28P01", Message: `password authentication failed for user "` + secret + `"`},
		errors.New("tls handshake to db.prod.internal:5432 failed for " + secret),
	}
	for _, err := range nasty {
		class, hint := ClassifyConnect(err)
		if strings.Contains(string(class), secret) || strings.Contains(hint, secret) {
			t.Errorf("the classifier leaked the error text: class=%q hint=%q", class, hint)
		}
		// Host and user must not survive either.
		for _, leak := range []string{"db.prod.internal", "user=svc", "database=app"} {
			if strings.Contains(string(class), leak) || strings.Contains(hint, leak) {
				t.Errorf("the classifier leaked %q: class=%q hint=%q", leak, class, hint)
			}
		}
	}
}

func TestClassifyConnectNil(t *testing.T) {
	if c, h := ClassifyConnect(nil); c != "" || h != "" {
		t.Errorf("nil error must classify as empty, got %q/%q", c, h)
	}
}
