package validate

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestErrMessageCarriesNoRowValues is the INV-NO-ROWS guard on the error path.
//
// Postgres puts data values in the PRIMARY message for a whole class of errors,
// and ErrMsg is serialized into the verdict JSON and printed to stderr. On the
// --target ground-truth path those are production values landing in a CI log and
// a build artifact. The code already avoided pgErr.Detail; Message was missed.
func TestErrMessageCarriesNoRowValues(t *testing.T) {
	const secret = "hunter2-SSN-123-45-6789"
	cases := []struct {
		name string
		err  *pgconn.PgError
	}{
		{"invalid input syntax", &pgconn.PgError{
			Code:    "22P02",
			Message: `invalid input syntax for type integer: "` + secret + `"`,
		}},
		{"out of range", &pgconn.PgError{
			Code:    "22003",
			Message: `value "` + secret + `" is out of range for type integer`,
		}},
		{"datetime out of range", &pgconn.PgError{
			Code:    "22008",
			Message: `date/time field value out of range: "` + secret + `"`,
		}},
		{"single-quoted value", &pgconn.PgError{
			Code:    "22P02",
			Message: `invalid input value for enum mood: '` + secret + `'`,
		}},
		{"unterminated quote runs to end", &pgconn.PgError{
			Code:    "22P02",
			Message: `invalid input syntax: "` + secret,
		}},
		{"value also appears in structured fields", &pgconn.PgError{
			Code:       "23505",
			Message:    `duplicate key value violates unique constraint "users_email_key"`,
			TableName:  "users",
			ColumnName: "email",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := errMessage(tc.err)
			if strings.Contains(got, secret) {
				t.Errorf("a row value survived into ErrMsg: %q", got)
			}
			if got == "" {
				t.Error("the operator must still get something actionable")
			}
		})
	}
}

// Scrubbing must not gut the diagnostic: the message shape and the schema
// identifiers are what make a failure actionable, and neither is sensitive.
func TestErrMessageKeepsDiagnosticValue(t *testing.T) {
	e := &pgconn.PgError{
		Code:       "22P02",
		Message:    `invalid input syntax for type integer: "secret"`,
		TableName:  "users",
		ColumnName: "age",
		SchemaName: "public",
	}
	got := errMessage(e)
	for _, want := range []string{"invalid input syntax for type integer", "relation users", "column age"} {
		if !strings.Contains(got, want) {
			t.Errorf("errMessage dropped %q, got %q", want, got)
		}
	}
	// "public" is noise on every message; it is deliberately omitted.
	if strings.Contains(got, "schema public") {
		t.Errorf("the default schema should not be repeated, got %q", got)
	}
}

func TestScrubQuoted(t *testing.T) {
	cases := []struct{ in, want string }{
		{`no quotes here`, `no quotes here`},
		{`a "b" c`, `a "…" c`},
		{`a 'b' c`, `a '…' c`},
		{`"x" and "y"`, `"…" and "…"`},
		{`unterminated "tail`, `unterminated "…`},
		{``, ``},
		{`""`, `"…"`},
	}
	for _, tc := range cases {
		if got := scrubQuoted(tc.in); got != tc.want {
			t.Errorf("scrubQuoted(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestErrMessageKeepsSyntaxDiagnostics is the regression for over-scrubbing.
//
// The first cut scrubbed every quoted run unconditionally, which cost the
// diagnostic for the two failures an operator hits most — neither of which
// carries row data. The quoted token in class 42 is an identifier or keyword
// from the user's OWN migration file; in the 42703 case below the misspelling
// IS the entire content of the diagnostic.
func TestErrMessageKeepsSyntaxDiagnostics(t *testing.T) {
	cases := []struct {
		code, message, mustContain string
	}{
		{"42601", `syntax error at or near "ALTER"`, "ALTER"},
		{"42P01", `relation "users" does not exist`, "users"},
		{"42703", `column "emial" does not exist`, "emial"},
		{"3D000", `database "nope" does not exist`, "nope"},
		{"3F000", `schema "nope" does not exist`, "nope"},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			got := errMessage(&pgconn.PgError{Code: tc.code, Message: tc.message})
			if !strings.Contains(got, tc.mustContain) {
				t.Errorf("errMessage(%s) = %q; it must keep %q — that identifier is the diagnostic, and it is schema, not row data",
					tc.code, got, tc.mustContain)
			}
		})
	}
}

// The allowlist must fail CLOSED: anything not known to be value-free is still
// scrubbed, including SQLSTATEs that do not exist yet.
func TestScrubsValuesFailsClosed(t *testing.T) {
	mustScrub := []string{"22P02", "22003", "23505", "23502", "XX000", "ZZ999", "", "4"}
	for _, code := range mustScrub {
		if !scrubsValues(code) {
			t.Errorf("scrubsValues(%q) = false; unknown or value-bearing classes must be scrubbed", code)
		}
	}
	mustNotScrub := []string{"42601", "42P01", "3D000", "3F000", "26000", "34000", "08006", "53200", "57014", "58030"}
	for _, code := range mustNotScrub {
		if scrubsValues(code) {
			t.Errorf("scrubsValues(%q) = true; this class carries identifiers, not values", code)
		}
	}
}

// The value-bearing classes must STILL be scrubbed after the gating change —
// this is the property the gate must not have broken.
func TestValueBearingClassesStillScrubbed(t *testing.T) {
	const secret = "hunter2-SSN-123-45-6789"
	for _, code := range []string{"22P02", "22003", "22008", "23505"} {
		got := errMessage(&pgconn.PgError{
			Code:    code,
			Message: `invalid input syntax for type integer: "` + secret + `"`,
		})
		if strings.Contains(got, secret) {
			t.Errorf("class %s leaked a row value: %q", code, got)
		}
	}
}
