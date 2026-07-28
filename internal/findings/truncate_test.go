package findings

import (
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/validate"
)

func bigUsersFixture() *fixture.Fixture {
	return &fixture.Fixture{
		RowshapeFixture: fixture.FormatVersion,
		Meta:            fixture.Meta{Engine: fixture.Engine{Name: "postgres", Version: "16"}},
		Tables: map[string]fixture.Table{
			"public.users": {
				Rows:    fixture.Fact[int64]{Value: 200_000_000, Confidence: fixture.Exact},
				Columns: map[string]fixture.Column{"id": {Type: "bigint"}},
			},
		},
	}
}

// TestTruncateIsNotCertifiedClean is the regression for the worst false negative
// in the catalog.
//
// TRUNCATE reached only rsperf's deleteTarget, which exists solely to look for
// long-tailed CASCADING children — so a TRUNCATE with no cascading child
// produced ZERO findings and the verdict was PASS. Total irreversible data loss
// under ACCESS EXCLUSIVE, certified clean. It also succeeds against the hydrated
// table, so the apply-failure floor never fired either.
func TestTruncateIsNotCertifiedClean(t *testing.T) {
	f := bigUsersFixture()
	for _, sql := range []string{
		"TRUNCATE users;",
		"TRUNCATE TABLE users;",
		"TRUNCATE ONLY users;",
		"TRUNCATE users CASCADE;",
		"truncate users;",
	} {
		t.Run(sql, func(t *testing.T) {
			res := validate.BuildResult(f, &validate.Capture{
				Success:    true,
				Statements: []validate.Statement{{SQL: sql}},
			}, validate.Registered(), false)

			if res.Verdict == "PASS" {
				t.Fatalf("%q returned PASS — irreversible loss of 200M rows must not be certified clean", sql)
			}
			var found *string
			for i := range res.Findings {
				if res.Findings[i].Code == "RS-REVERSE-004" {
					found = &res.Findings[i].Title
				}
			}
			if found == nil {
				t.Fatalf("no RS-REVERSE-004 for %q; findings: %v", sql, len(res.Findings))
			}
			// The size must come from the DECLARED rows: running TRUNCATE against
			// hydrated data says nothing about what production holds.
			if !strings.Contains(*found, "200.0M") {
				t.Errorf("the finding must be sized from declared rows, got title %q", *found)
			}
		})
	}
}

// CASCADE reaches every table with a foreign key into this one, which is a
// materially larger blast radius and must be called out.
func TestTruncateCascadeIsCalledOut(t *testing.T) {
	f := bigUsersFixture()
	res := validate.BuildResult(f, &validate.Capture{
		Success:    true,
		Statements: []validate.Statement{{SQL: "TRUNCATE users CASCADE;"}},
	}, validate.Registered(), false)

	for _, fnd := range res.Findings {
		if fnd.Code != "RS-REVERSE-004" {
			continue
		}
		if !strings.Contains(fnd.Detail, "CASCADE") {
			t.Errorf("CASCADE must be called out in the detail, got %q", fnd.Detail)
		}
		ev, _ := fnd.Evidence.(map[string]any)
		if ev["cascade"] != true {
			t.Errorf("evidence must record cascade=true, got %v", ev)
		}
		return
	}
	t.Fatal("no RS-REVERSE-004 produced")
}

// TestIrreversibleFindingsStateTheGateBehaviour: these are SeverityWarn, and the
// Action's default is warn-as-fail:false — so the default configuration of the
// default surface lets irreversible data loss merge without blocking.
//
// Whether the severity should change is a product decision. Saying plainly what
// the current default DOES is not, and a reviewer should not have to know the
// Action's defaults to understand that a DROP TABLE finding will not stop the
// merge.
func TestIrreversibleFindingsStateTheGateBehaviour(t *testing.T) {
	f := bigUsersFixture()
	cases := []struct{ name, sql, code string }{
		{"TRUNCATE", "TRUNCATE users;", "RS-REVERSE-004"},
		{"DROP TABLE", "DROP TABLE users;", "RS-REVERSE-002"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := validate.BuildResult(f, &validate.Capture{
				Success:    true,
				Statements: []validate.Statement{{SQL: c.sql}},
			}, validate.Registered(), false)

			for _, fnd := range res.Findings {
				if fnd.Code != c.code {
					continue
				}
				if !strings.Contains(fnd.Detail, "warn-as-fail") {
					t.Errorf("%s must say plainly that the default gate will not block it, got %q",
						c.code, fnd.Detail)
				}
				return
			}
			t.Fatalf("no %s produced for %q", c.code, c.sql)
		})
	}
}
