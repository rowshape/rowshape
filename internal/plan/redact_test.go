package plan

import "testing"

// TestRedactURL: a connection URL must never carry its credentials into anything
// rowshape shows, echoes, or returns (PRD §5 — the connection URL and any
// credentials are never logged, persisted, or written into a fixture).
//
// This lands here because it was NOT covered anywhere: the only test that looked
// like it checked redaction guarded on the URL containing "@" and ran against a
// localhost URL, so the assertion could never fire. A vacuous check on the buyer's
// stated requirement is worse than no check — it reads as covered in review.
func TestRedactURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "user and password are stripped",
			in:   "postgres://admin:hunter2@db.internal:5432/app",
			want: "postgres://…@db.internal:5432/app",
		},
		{
			name: "user without a password is still stripped",
			in:   "postgres://admin@db.internal:5432/app",
			want: "postgres://…@db.internal:5432/app",
		},
		{
			name: "a password containing @ does not leak the earlier segment",
			in:   "postgres://admin:p@ss@db.internal:5432/app",
			want: "postgres://…@db.internal:5432/app",
		},
		{
			name: "no credentials, unchanged",
			in:   "postgres://localhost:5432/app",
			want: "postgres://localhost:5432/app",
		},
		{
			// An "@" outside the authority is not a credential separator; treating
			// it as one would corrupt the URL this only means to display.
			name: "an @ in the query string is not mistaken for credentials",
			in:   "postgres://localhost:5432/app?options=user@host",
			want: "postgres://localhost:5432/app?options=user@host",
		},
		{
			name: "ipv6 host",
			in:   "postgres://admin:hunter2@[::1]:5432/app",
			want: "postgres://…@[::1]:5432/app",
		},
		{
			name: "no scheme, unchanged",
			in:   "just-a-string@thing",
			want: "just-a-string@thing",
		},
		{
			name: "empty string",
			in:   "",
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RedactURL(c.in)
			if got != c.want {
				t.Errorf("RedactURL(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestRedactURLLeaksNoSecret is the property that actually matters: whatever the
// shape of the URL, the secret must not survive redaction. Table cases assert the
// format; this asserts the guarantee.
func TestRedactURLLeaksNoSecret(t *testing.T) {
	const secret = "hunter2"
	urls := []string{
		"postgres://admin:" + secret + "@db.internal:5432/app",
		"postgresql://u:" + secret + "@10.0.0.1/db?sslmode=require",
		"postgres://admin:" + secret + "@[::1]:5432/app",
	}
	for _, u := range urls {
		if got := RedactURL(u); contains(got, secret) {
			t.Errorf("RedactURL(%q) = %q — the password survived redaction", u, got)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestRedactKeywordValueDSN covers libpq's keyword/value form. pgx.ParseConfig
// accepts it alongside URLs, but RedactURL used to bail out whenever the string
// had no "://" and return it unchanged — so the password reached stdout on
// `plan`/`verify` and, worse, the MCP `plan_against` result, i.e. straight into
// a model's context.
func TestRedactKeywordValueDSN(t *testing.T) {
	const secret = "hunter2"
	cases := []struct {
		name string
		dsn  string
	}{
		{"plain", "host=db.prod user=svc password=" + secret + " sslmode=require"},
		{"password_last", "host=db.prod password=" + secret},
		{"spaces_around_eq", "host=db.prod password = " + secret + " dbname=app"},
		{"quoted_value", "host=db.prod password='" + secret + " with spaces' dbname=app"},
		{"quoted_with_escape", `host=db.prod password='` + secret + `\'x' dbname=app`},
		{"uppercase_keyword", "host=db.prod PASSWORD=" + secret},
		{"tab_separated", "host=db.prod\tpassword=" + secret},
		{"secret_inside_other_value", "host=db.prod options='-c x=1' password=" + secret},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RedactURL(c.dsn)
			if contains(got, secret) {
				t.Errorf("RedactURL(%q) = %q — the password survived redaction", c.dsn, got)
			}
		})
	}
}

// Non-password fields must survive: the redacted string is what the operator
// reads to confirm WHICH database was targeted, so it has to stay legible.
func TestRedactKeywordValuePreservesContext(t *testing.T) {
	got := RedactURL("host=db.prod user=svc password=hunter2 sslmode=require dbname=app")
	for _, want := range []string{"host=db.prod", "user=svc", "sslmode=require", "dbname=app"} {
		if !contains(got, want) {
			t.Errorf("RedactURL dropped %q from the display string: %q", want, got)
		}
	}
}

// TestRedactKeywordValueContainingScheme is the regression for the hole the
// FIRST attempt at keyword/value support left open.
//
// That version dispatched on `strings.Index(url, "://") < 0`, so any
// keyword/value DSN carrying "://" inside one of its VALUES took the URL branch
// instead, found no "@" in what it treated as the authority, and returned the
// input verbatim — i.e. the exact leak the change was written to close, on a
// realistic input. Dispatch is now anchored to the libpq URI scheme prefix.
func TestRedactKeywordValueContainingScheme(t *testing.T) {
	const secret = "hunter2"
	cases := []struct {
		name string
		dsn  string
	}{
		{"scheme inside options", `host=db.prod password=` + secret + ` options='-c foo=bar://baz'`},
		{"scheme inside the password itself", `host=db password=ab://cd` + secret},
		{"scheme-looking dbname", `host=db password=` + secret + ` dbname=x://y`},
		{"application_name with a URL", `host=db password=` + secret + ` application_name=https://example.com`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RedactURL(c.dsn)
			if contains(got, secret) {
				t.Errorf("RedactURL(%q) = %q — the password survived redaction", c.dsn, got)
			}
		})
	}
}

// The URL branch must still be taken for real URLs, including ones whose query
// string or password contains something scheme-like.
func TestRedactURLStillHandlesURLs(t *testing.T) {
	const secret = "hunter2"
	for _, u := range []string{
		"postgres://admin:" + secret + "@db.internal:5432/app",
		"postgresql://admin:" + secret + "@db.internal/app?application_name=x",
	} {
		if got := RedactURL(u); contains(got, secret) {
			t.Errorf("RedactURL(%q) = %q — the password survived", u, got)
		}
	}
}
