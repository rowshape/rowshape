package harness

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// corpusRoot is the corpus directory relative to this test.
const corpusRoot = ".."

// requiredPathologies are the ways a Postgres migration breaks that the corpus
// MUST cover (PRD §12). The corpus is written before the findings that consume it.
var requiredPathologies = []string{
	"volatile_default_rewrite",
	"set_not_null_fullscan",
	"unique_index_cant_build",
	"validate_orphans",
	"cascade_delete_fanout",
	"not_valid_validated_same_tx",
}

// validator is the hook `validate` fills in P2-T7. While nil, the harness asserts
// triple well-formedness and coverage; once set, it runs each migration and
// compares the verdict to expected.json.
var validator Validator

// TestCorpusWellFormed: every triple loads and is internally consistent — a real
// migration, a parseable fixture with an engine version, and an expected verdict
// using only known verdicts and finding codes.
func TestCorpusWellFormed(t *testing.T) {
	cases, err := LoadCases(corpusRoot)
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("corpus has no cases")
	}
	for _, c := range cases {
		if err := c.Validate(); err != nil {
			t.Errorf("case %s is malformed: %v", c.Name, err)
		}
	}
	t.Logf("loaded %d corpus cases (PG %s)", len(cases), PGMajor())
}

// TestCorpusCoversPathologies: at least one triple exists per documented
// pathology (PRD §12).
func TestCorpusCoversPathologies(t *testing.T) {
	cases, err := LoadCases(corpusRoot)
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	have := map[string]bool{}
	for _, c := range cases {
		have[c.Name] = true
	}
	for _, p := range requiredPathologies {
		if !have[p] {
			t.Errorf("corpus is missing a triple for pathology %q", p)
		}
	}
}

// TestCorpusExpectedFindings: each triple names at least one expected finding, so
// a passing verdict is never the silent default.
func TestCorpusExpectedFindings(t *testing.T) {
	cases, _ := LoadCases(corpusRoot)
	for _, c := range cases {
		if c.Expected.Verdict != "PASS" && len(c.Expected.Findings) == 0 {
			t.Errorf("case %s expects verdict %s but names no findings", c.Name, c.Expected.Verdict)
		}
	}
}

// TestCappingContract asserts the wrong-PASS regression suite ENCODES the RFC
// §7.4 contract, before any finding exists to satisfy it (PRD §14 ordering):
//   - a case resting on an estimated/unproven fact must expect WARN (never PASS)
//     and name a resolving command, and
//   - its proven-fact boundary twin must expect PASS.
//
// This is green now (it validates the contract's encoding); the runtime check
// that findings honor it is TestCorpusVerdicts, RED until the capping engine
// (P2-T4) lands.
func TestCappingContract(t *testing.T) {
	cases, err := LoadCases(corpusRoot)
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	seenCapped, seenBoundary, seenCleanPass := false, false, false
	for _, c := range cases {
		if !strings.HasPrefix(c.Name, "capping-") {
			continue
		}
		capped := strings.Contains(c.Name, "estimated") || strings.Contains(c.Name, "unproven")
		if capped {
			seenCapped = true
			if c.Expected.Verdict != "WARN" {
				t.Errorf("%s rests on an estimated/unproven fact: expected WARN, got %s (a PASS here would be a wrong PASS)", c.Name, c.Expected.Verdict)
			}
			if !namesResolveCommand(c.Expected) {
				t.Errorf("%s must name a resolving command (resolve_contains) so the WARN is actionable (§7.4)", c.Name)
			}
		} else {
			seenBoundary = true
			// A proven fact needs no RESOLUTION: the boundary case must not name a
			// resolve command. It MAY still WARN for an ORTHOGONAL hazard — a
			// proven-unique ADD UNIQUE certifies the data (no RS-DATA-014) but its
			// index still builds under ACCESS EXCLUSIVE (RS-INDEX-002) — so the
			// contract is "nothing to resolve," not "overall PASS."
			if namesResolveCommand(c.Expected) {
				t.Errorf("%s rests on a proven fact and must name no resolve command (nothing to resolve): %+v", c.Name, c.Expected.Findings)
			}
			if c.Expected.Verdict == "PASS" {
				seenCleanPass = true
			}
		}
	}
	if !seenCapped || !seenBoundary {
		t.Errorf("capping suite must include both a capped (WARN) case and a proven boundary; capped=%v boundary=%v", seenCapped, seenBoundary)
	}
	// The suite must still contain at least one proven-fact case that is a clean
	// overall PASS (e.g. capping-exact-not-null), so "capping does not touch a
	// proven fact" is demonstrated end to end and not only in the presence of an
	// unrelated finding.
	if !seenCleanPass {
		t.Error("capping suite must include a proven-fact case that is a clean overall PASS")
	}
}

func namesResolveCommand(e Expected) bool {
	for _, f := range e.Findings {
		if f.ResolveContains != "" {
			return true
		}
	}
	return false
}

// TestCorpusVerdicts runs the migrations through `validate` and compares to the
// expected verdict AND the capping contract (a capped WARN's remediation must
// name the resolving command) — once P2-T7 wires the validator. Until then it is
// skipped; for the capping cases it stays RED until P2-T4's capping engine makes
// them pass (PRD §14).
func TestCorpusVerdicts(t *testing.T) {
	if validator == nil {
		t.Skip("validate not yet wired (P2-T7): corpus verdict comparison pending")
	}
	cases, err := LoadCases(corpusRoot)
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			verdict, findings, err := validator.Validate(c)
			if err != nil {
				t.Fatalf("validate %s: %v", filepath.Base(c.Dir), err)
			}
			// Resolve the expectation for the major under test: a version-conditional
			// case (RFC §9.1) declares per-major overrides, so a finding right on one
			// major and wrong on another is asserted correctly on each (PRD §12).
			wantVerdict, wantFindings := c.Expected.ForMajor(PGMajor())
			if verdict != wantVerdict {
				t.Errorf("verdict = %s, want %s (PG %s)", verdict, wantVerdict, PGMajor())
			}
			compareFindings(t, findings, wantFindings)
		})
	}
}

// compareFindings asserts the produced findings match the expectation exactly.
//
// CR-T3. This used to check only that each EXPECTED finding's code family was
// present, which left two holes wide enough to drive the whole corpus through:
//
//  1. Severity was never compared. ExpectedFinding.Severity was declared in
//     every expected.json and read by nothing; ProducedFinding.Severity was
//     collected by the validator and then dropped on the floor. An analyzer
//     could emit `info` where the corpus said `error` and the case stayed green
//     — and severity is what drives the verdict, so that is not a cosmetic
//     mismatch.
//  2. Extra findings were tolerated. Nothing looked at what the analyzer
//     produced beyond the expected list, so a spurious finding on an unrelated
//     table was invisible.
//
// Both matter more than they look, because "the corpus passes" is the evidence
// behind every other correctness claim in this repo. A measuring instrument
// that reports success for two different states is not measuring.
//
// Matching is per code FAMILY, which is what expected.json names: the exact
// count of each family, and the multiset of severities within it. Families are
// matched rather than full codes deliberately (INV-VERDICT-STABLE keeps codes
// permanent, but a new sibling like RS-LOCK-002 should not break every case);
// counts and severities are exact, so an extra finding cannot hide inside a
// family it shares.
func compareFindings(t *testing.T, produced []ProducedFinding, want []ExpectedFinding) {
	t.Helper()

	gotBy := map[string][]ProducedFinding{}
	for _, f := range produced {
		gotBy[f.Code] = append(gotBy[f.Code], f)
	}
	wantBy := map[string][]ExpectedFinding{}
	for _, w := range want {
		wantBy[w.Code] = append(wantBy[w.Code], w)
	}

	for fam, ws := range wantBy {
		gs := gotBy[fam]
		if len(gs) == 0 {
			t.Errorf("missing expected finding %s", fam)
			continue
		}
		if len(gs) != len(ws) {
			t.Errorf("%s: produced %d findings, expected %d (produced: %s)",
				fam, len(gs), len(ws), describeFindings(gs))
		}

		// Severity is compared as a multiset so a case expecting two findings of
		// one family cannot pass by matching the same one twice.
		if gotSev, wantSev := severitiesOf(gs), expectedSeveritiesOf(ws); !equalStrings(gotSev, wantSev) {
			t.Errorf("%s: severities = %v, want %v (produced: %s)",
				fam, gotSev, wantSev, describeFindings(gs))
		}

		// The capping contract: a capped WARN must name the command that
		// resolves it (RFC §7.4). Satisfied by SOME finding in the family.
		for _, w := range ws {
			if w.ResolveContains == "" {
				continue
			}
			found := false
			for _, g := range gs {
				if strings.Contains(g.Remediation, w.ResolveContains) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s: no finding's remediation names the resolving command %q (§7.4); produced: %s",
					fam, w.ResolveContains, describeFindings(gs))
			}
		}
	}

	// Anything produced that nobody expected. This is the half that lets a case
	// "pass" while the analyzer also fires on something unrelated.
	for fam, gs := range gotBy {
		if _, ok := wantBy[fam]; !ok {
			t.Errorf("unexpected finding family %s that expected.json does not account for: %s",
				fam, describeFindings(gs))
		}
	}
}

func severitiesOf(fs []ProducedFinding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Severity)
	}
	sort.Strings(out)
	return out
}

func expectedSeveritiesOf(fs []ExpectedFinding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Severity)
	}
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// describeFindings renders findings as "RS-LOCK-001(error)" so a failure names
// the specific code that arrived, not just its family.
func describeFindings(fs []ProducedFinding) string {
	parts := make([]string, 0, len(fs))
	for _, f := range fs {
		code := f.FullCode
		if code == "" {
			code = f.Code
		}
		parts = append(parts, fmt.Sprintf("%s(%s)", code, f.Severity))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

// TestVersionConditionalBoundary pins the PG 10 vs 11+ boundary that D-006 and
// D-007 turn on, WITHOUT needing seven Postgres servers.
//
// CR-loop audit (D-015): four passing stories claimed "corpus matrix green
// across PG 11-17", but the matrix lives in corpus.yml, which needs the GitHub
// org from P0-T1 — so CI has never run and the claim had never been observed.
// The audit then found the sweep would barely help: exactly ONE case declares
// version_verdicts and it overrides major 10, so 11 through 17 all resolve to
// the same default. Seven runs would assert one thing seven times.
//
// The boundary IS testable against a single server, because a version-
// conditional finding derives from the fixture's DECLARED meta.engine.version
// rather than the server's behavior (pipelineValidator sets it from PGMajor).
// So this drives both branches and asserts they actually DIFFER — which is the
// property the matrix exists to protect, and the one thing a sweep across
// 11..17 cannot show.
func TestVersionConditionalBoundary(t *testing.T) {
	if validator == nil {
		t.Skip("validate not wired (needs ROWSHAPE_TEST_PG_DSN)")
	}
	cases, err := LoadCases(corpusRoot)
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}

	var target *Case
	for i := range cases {
		if len(cases[i].Expected.VersionVerdicts) > 0 {
			target = &cases[i]
			break
		}
	}
	if target == nil {
		t.Fatal("no corpus case declares version_verdicts — the version-conditional " +
			"machinery (RFC §9.1, D-006) has no coverage at all")
	}

	below, _ := target.Expected.ForMajor("10") // rewrites: the old behavior
	above, _ := target.Expected.ForMajor("16") // catalog-only: the fast path
	if below == above {
		t.Fatalf("case %s expects %q on both sides of the PG 11 boundary; it cannot be "+
			"demonstrating version-conditional behavior", target.Name, below)
	}

	for _, major := range []string{"10", "16"} {
		t.Run("PG"+major, func(t *testing.T) {
			t.Setenv("ROWSHAPE_PG_VERSION", major)
			wantVerdict, wantFindings := target.Expected.ForMajor(major)

			gotVerdict, gotFindings, err := validator.Validate(*target)
			if err != nil {
				t.Fatalf("validate: %v", err)
			}
			if gotVerdict != wantVerdict {
				t.Errorf("at PG %s: verdict = %s, want %s — the version-conditional model "+
					"(D-006: ADD COLUMN DEFAULT rewrites on 10, catalog-only on 11+) did not hold",
					major, gotVerdict, wantVerdict)
			}
			compareFindings(t, gotFindings, wantFindings)
		})
	}
}
