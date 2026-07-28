package findings

import (
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/validate"
)

func usersFixture() *fixture.Fixture {
	return &fixture.Fixture{
		RowshapeFixture: fixture.FormatVersion,
		Meta:            fixture.Meta{Engine: fixture.Engine{Name: "postgres", Version: "16"}},
		Tables: map[string]fixture.Table{
			"public.users": {
				Rows: fixture.Fact[int64]{Value: 1000, Confidence: fixture.Exact},
				Columns: map[string]fixture.Column{
					"id":    {Type: "bigint"},
					"email": {Type: "text"},
				},
			},
		},
	}
}

func analyze(t *testing.T, f *fixture.Fixture, sql string) []string {
	t.Helper()
	res := validate.BuildResult(f, &validate.Capture{
		Success:    true,
		Statements: []validate.Statement{{SQL: sql}},
	}, validate.Registered(), false)
	var codes []string
	for _, fnd := range res.Findings {
		codes = append(codes, fnd.Code)
	}
	return codes
}

// TestRenameIsNotCertifiedClean is the regression for what is, in practice, the
// most common migration outage there is.
//
// A rename is instant, takes a brief lock, applies cleanly, and leaves the
// database in a perfectly good state — while every application instance still
// running the previous release starts raising `column "email" does not exist`.
// The catalog was silent: both forms returned PASS with zero findings.
//
// This rule HAS to be static. No amount of executing the migration against a
// hydrated database can surface it, because nothing about the database is wrong.
func TestRenameIsNotCertifiedClean(t *testing.T) {
	f := usersFixture()
	cases := []struct{ name, sql string }{
		{"rename column", "ALTER TABLE users RENAME COLUMN email TO email_address;"},
		{"rename column, no COLUMN keyword variant", "ALTER TABLE users RENAME COLUMN email TO addr;"},
		{"rename table", "ALTER TABLE users RENAME TO people;"},
		{"lowercase", "alter table users rename column email to addr;"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			codes := analyze(t, f, c.sql)
			found := false
			for _, code := range codes {
				if code == "RS-DEPLOY-001" {
					found = true
				}
			}
			if !found {
				t.Fatalf("%q produced %v — a rename must not be certified clean", c.sql, codes)
			}
		})
	}
}

// The finding must rest on NO fixture fact: it is equally true of a rename on an
// empty table and on a billion-row one, and citing a fact that does not support
// it would put a false provenance trail into a signed document.
func TestRenameCitesNoFixtureFact(t *testing.T) {
	res := validate.BuildResult(usersFixture(), &validate.Capture{
		Success:    true,
		Statements: []validate.Statement{{SQL: "ALTER TABLE users RENAME COLUMN email TO addr;"}},
	}, validate.Registered(), false)

	for _, fnd := range res.Findings {
		if fnd.Code != "RS-DEPLOY-001" {
			continue
		}
		if len(fnd.DependsOn) != 0 {
			t.Errorf("DependsOn = %v, want empty: this conclusion rests on no fixture fact", fnd.DependsOn)
		}
		ev, _ := fnd.Evidence.(map[string]any)
		if ev["from"] != "email" || ev["to"] != "addr" {
			t.Errorf("evidence must record both names, got %v", ev)
		}
		if !strings.Contains(fnd.Detail, "ORDERING") {
			t.Errorf("the detail must explain that the hazard is ordering, not locks or data: %q", fnd.Detail)
		}
		return
	}
	t.Fatal("no RS-DEPLOY-001 produced")
}

// Statements that are not renames must not trip the analyzer.
func TestRenameDoesNotOverfire(t *testing.T) {
	f := usersFixture()
	for _, sql := range []string{
		"ALTER TABLE users ADD COLUMN nickname text;",
		"ALTER TABLE users ALTER COLUMN email SET NOT NULL;",
		// A constraint rename breaks nothing an application references by name.
		"ALTER TABLE users RENAME CONSTRAINT users_pkey TO users_pk;",
	} {
		for _, code := range analyze(t, f, sql) {
			if code == "RS-DEPLOY-001" {
				t.Errorf("%q must not produce RS-DEPLOY-001", sql)
			}
		}
	}
}
