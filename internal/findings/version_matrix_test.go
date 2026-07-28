package findings

import (
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/validate"
	"github.com/rowshape/rowshape/internal/verdict"
)

// TestVersionMatrixAddColumnDefault drives the version-add-column-default corpus
// case across the whole PG 10–17 matrix and asserts the WHOLE verdict — not just
// a finding count — is version-correct on each major (P5-T3 acceptance 2): the
// non-volatile-DEFAULT catalog fast-path makes the same migration a full-table
// rewrite WARN on PG 10 and a catalog-only PASS on PG 11+ (RFC §9.1, D-006/D-007).
//
// This runs offline (no ROWSHAPE_TEST_PG_DSN), so the model's version-conditional
// buckets are proven on every commit; the corpus harness proves the same end to
// end against a real server per major in CI.
func TestVersionMatrixAddColumnDefault(t *testing.T) {
	f, mig := loadCorpus(t, "version-add-column-default")

	want := map[string]string{
		"10": verdict.VerdictWarn, // pre-fast-path: rewrites all rows under ACCESS EXCLUSIVE
		"11": verdict.VerdictPass, // fast-path landed: catalog-only instant
		"12": verdict.VerdictPass,
		"13": verdict.VerdictPass,
		"14": verdict.VerdictPass,
		"15": verdict.VerdictPass,
		"16": verdict.VerdictPass,
		"17": verdict.VerdictPass,
	}

	for major, expect := range want {
		t.Run("pg"+major, func(t *testing.T) {
			fv := *f
			fv.Meta.Engine.Version = major
			res := validate.BuildResult(&fv, plainCapture(mig), validate.Registered(), false)
			if res.Verdict != expect {
				t.Errorf("PG %s: verdict = %s, want %s", major, res.Verdict, expect)
			}
			// The claim under test is the version-conditional REWRITE: RS-LOCK-001
			// must fire on PG 10 and must not on PG 11+. This originally asserted
			// "exactly one finding", which was a proxy for that — and a fragile
			// one: RS-LOCK-010 (no lock_timeout on a lock-holding migration) is
			// also correct on PG 10, additive, and true. Assert the specific code
			// rather than a count, so a genuinely new finding does not read as a
			// regression in the version model.
			hasRewrite := false
			for _, fnd := range res.Findings {
				if fnd.Code == "RS-LOCK-001" {
					hasRewrite = true
				}
			}
			if major == "10" {
				if !hasRewrite {
					t.Errorf("PG 10 must report the full-table rewrite (RS-LOCK-001), got %+v", res.Findings)
				}
			} else {
				if hasRewrite {
					t.Errorf("PG %s must NOT report a rewrite — the catalog fast-path landed in 11, got %+v", major, res.Findings)
				}
				// The fast path is instant, so nothing that depends on the lock
				// being HELD should fire either.
				for _, fnd := range res.Findings {
					if fnd.Code == "RS-LOCK-010" || fnd.Code == "RS-LOCK-011" {
						t.Errorf("PG %s: catalog-only DDL must not raise lock-duration findings, got %s", major, fnd.Code)
					}
				}
			}
		})
	}
}

// TestVersionMatrixCaseParses guards that the corpus case's engine version is
// present so extrapolation never has to guess (RFC §9.1) — the version override
// the harness applies replaces it, but the on-disk fixture must still be valid.
func TestVersionMatrixCaseParses(t *testing.T) {
	f, _ := loadCorpus(t, "version-add-column-default")
	if _, ok := f.Tables["public.customers"]; !ok {
		t.Fatal("fixture must declare public.customers")
	}
	if f.Meta.Engine.Version == "" {
		t.Error("fixture must declare meta.engine.version")
	}
	if f.Tables["public.customers"].Rows.Confidence != fixture.Exact {
		t.Error("rows must be exact so the version-conditional verdict is not confidence-capped")
	}
}
