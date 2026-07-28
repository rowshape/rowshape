package findings

import (
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/validate"
)

// TestVolatilityOf pins the three-state classification.
//
// A two-state answer was the bug: anything unrecognized fell to "not volatile",
// which on PG 11+ means the catalog fast-path and therefore NO FINDING AT ALL.
// A user-defined volatile function silently certified a full table rewrite as
// safe — fail-open on the single hazard this tool is best known for.
func TestVolatilityOf(t *testing.T) {
	cases := []struct {
		expr string
		want volatility
	}{
		// Known volatile: a rewrite on every version.
		{"gen_random_uuid()", volatileYes},
		{"uuid_generate_v4()", volatileYes},
		{"random()", volatileYes},
		{"clock_timestamp()", volatileYes},
		{"nextval('s')", volatileYes},

		// Known non-volatile: now() and friends are STABLE, fixed for the
		// statement, so the default is one constant and no rewrite is needed.
		{"now()", volatileNo},
		{"current_timestamp", volatileNo},
		{"CURRENT_DATE", volatileNo},

		// Literals: the common, safe case. Must stay silent or the rule becomes
		// noise on every ADD COLUMN ... DEFAULT 0.
		{"0", volatileNo},
		{"-1", volatileNo},
		{"3.14", volatileNo},
		{"'pending'", volatileNo},
		{"'pending'::text", volatileNo},
		{"true", volatileNo},
		{"false", volatileNo},
		{"NULL", volatileNo},

		// THE REGRESSION: unrecognized expressions must be UNKNOWN, not safe.
		{"uuid_generate_v7()", volatileUnknown},
		{"gen_random_bytes(16)", volatileUnknown},
		{"statement_timestamp()", volatileUnknown},
		{"my_app.next_id()", volatileUnknown},
		{"some_user_defined_fn()", volatileUnknown},
	}
	for _, c := range cases {
		t.Run(c.expr, func(t *testing.T) {
			if got := volatilityOf(c.expr); got != c.want {
				t.Errorf("volatilityOf(%q) = %v, want %v", c.expr, got, c.want)
			}
		})
	}
}

func volFixture() *fixture.Fixture {
	return &fixture.Fixture{
		RowshapeFixture: fixture.FormatVersion,
		Meta:            fixture.Meta{Engine: fixture.Engine{Name: "postgres", Version: "16"}},
		Tables: map[string]fixture.Table{
			"public.users": {
				Rows:    fixture.Fact[int64]{Value: 100_000_000, Confidence: fixture.Exact},
				Columns: map[string]fixture.Column{"id": {Type: "bigint"}},
			},
		},
	}
}

func lockFinding(t *testing.T, sql string) *verdictFinding {
	t.Helper()
	res := validate.BuildResult(volFixture(), &validate.Capture{
		Success:    true,
		Statements: []validate.Statement{{SQL: sql}},
	}, validate.Registered(), false)
	for _, fnd := range res.Findings {
		if fnd.Code == "RS-LOCK-001" {
			return &verdictFinding{fnd.Title, fnd.Detail, fnd.Estimate != nil}
		}
	}
	return nil
}

type verdictFinding struct {
	Title, Detail string
	HasEstimate   bool
}

// An unrecognized volatile function on PG 16 produced NOTHING. It must now be
// reported as undecidable rather than certified.
func TestUnknownVolatilityIsReportedNotCertified(t *testing.T) {
	got := lockFinding(t, "ALTER TABLE users ADD COLUMN token uuid DEFAULT uuid_generate_v7();")
	if got == nil {
		t.Fatal("an unrecognized default function produced no finding — a full table rewrite certified as safe")
	}
	if !strings.Contains(got.Detail, "cannot determine") {
		t.Errorf("the finding must say it cannot decide, got %q", got.Detail)
	}
}

// Known-safe defaults must stay silent on PG 11+, or the rule is noise.
func TestKnownSafeDefaultsStaySilent(t *testing.T) {
	for _, sql := range []string{
		"ALTER TABLE users ADD COLUMN status text DEFAULT 'pending';",
		"ALTER TABLE users ADD COLUMN n integer DEFAULT 0;",
		"ALTER TABLE users ADD COLUMN ok boolean DEFAULT false;",
		"ALTER TABLE users ADD COLUMN seen_at timestamptz DEFAULT now();",
		"ALTER TABLE users ADD COLUMN note text;",
	} {
		if got := lockFinding(t, sql); got != nil {
			t.Errorf("%q must not produce RS-LOCK-001 on PG 16, got %q", sql, got.Title)
		}
	}
}

// Known-volatile defaults must still fire.
//
// The estimate is NOT asserted here, and that is the point: lockFinding builds a
// capture with no measured duration, and estimateFor now declines to extrapolate
// from a non-measurement rather than defaulting the basis to 1ms. This test used
// to pass BECAUSE of that fabrication. The estimate path is covered properly in
// TestEstimateForRefusesWithoutAMeasuredBasis, with a real basis.
func TestKnownVolatileStillFires(t *testing.T) {
	got := lockFinding(t, "ALTER TABLE users ADD COLUMN token uuid DEFAULT gen_random_uuid();")
	if got == nil {
		t.Fatal("a known volatile default must fire")
	}
	if got.HasEstimate {
		t.Error("with nothing measured there is no basis, so no estimate should be attached")
	}
}
