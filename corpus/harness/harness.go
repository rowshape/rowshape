// Package harness loads and runs the rowshape corpus: executable
// (migration, fixture, expected verdict) triples covering the documented ways a
// Postgres migration breaks (PRD §12). It is a runnable asset, not prose — each
// case is a directory under corpus/cases/ holding migration.sql, fixture.yaml,
// and expected.json.
//
// The harness consumes a fixture + migration and (once `validate` lands in
// P2-T7, via the Validator hook) asserts the expected verdict. Until then it
// asserts every triple is well-formed and the corpus covers every named
// pathology — the corpus is written and proven BEFORE the findings it tests
// (PRD §14 ordering).
package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rowshape/rowshape/internal/findings"
	"github.com/rowshape/rowshape/internal/fixture"
)

// KnownCodes are the permanent, namespaced finding FAMILIES a corpus case may
// name (INV-VERDICT-STABLE), derived from the finding registry rather than listed
// here.
//
// It used to be a hand-written list of six, which made it the FOURTH place the
// namespaces were enumerated — alongside the invariant in prd.json, the Finding
// doc comment in internal/verdict, and the registry itself. Adding RS-APPLY
// (D-023) had to touch all of them, and the one that was missed failed as
// `unknown finding code "RS-APPLY"` on a case that was perfectly correct. A list
// that must be updated in lockstep with another list is a list that will drift.
//
// The registry is the single source: every documented code belongs to a family,
// and a family exists exactly when a code in it does.
var KnownCodes = knownFamilies()

// knownFamilies reduces the registry's full codes (RS-LOCK-001) to the families a
// corpus case names (RS-LOCK). A code with no numeric suffix contributes itself,
// so an unexpected shape widens the set rather than silently dropping out of it.
func knownFamilies() map[string]bool {
	out := map[string]bool{}
	for _, code := range findings.Codes() {
		family := code
		if i := strings.LastIndex(code, "-"); i > 0 {
			family = code[:i]
		}
		out[family] = true
	}
	return out
}

// KnownVerdicts are the three verdict values (PRD §10).
var KnownVerdicts = map[string]bool{"PASS": true, "WARN": true, "FAIL": true}

// ExpectedFinding is one finding the corpus expects a validator to produce.
type ExpectedFinding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	// ResolveContains, when set, requires the finding's remediation to name a
	// resolving command containing this substring — the "here is how to turn this
	// WARN into a PASS" contract of confidence capping (RFC §7.4).
	ResolveContains string `json:"resolve_contains,omitempty"`
}

// VersionExpectation overrides the expected verdict and findings for a specific
// Postgres major. A version-conditional migration is right on one major and wrong
// on another (RFC §9.1) — e.g. ADD COLUMN ... DEFAULT <const> rewrites the whole
// table on PG 10 (a WARN) but is a catalog-only instant on PG 11+ (a PASS). A
// single expected verdict cannot express that; this does.
type VersionExpectation struct {
	Verdict  string            `json:"verdict"`
	Findings []ExpectedFinding `json:"findings"`
}

// Expected is the expected verdict for a case. Verdict/Findings are the default
// that holds on every major unless overridden by an entry in VersionVerdicts.
type Expected struct {
	Description string            `json:"description"`
	Verdict     string            `json:"verdict"`
	Findings    []ExpectedFinding `json:"findings"`
	// VersionVerdicts overrides the default for specific PG majors (keyed by the
	// major string the matrix drives, e.g. "10"). This is how a case declares that
	// it differs correctly across the version matrix (RFC §9.1, PRD §12).
	VersionVerdicts map[string]VersionExpectation `json:"version_verdicts,omitempty"`
}

// ForMajor resolves the expected verdict and findings for a Postgres major: the
// per-version override when one is declared, else the case default.
func (e Expected) ForMajor(major string) (verdict string, findings []ExpectedFinding) {
	if o, ok := e.VersionVerdicts[major]; ok {
		return o.Verdict, o.Findings
	}
	return e.Verdict, e.Findings
}

// Case is one corpus triple.
type Case struct {
	Name      string
	Dir       string
	Migration string
	Fixture   *fixture.Fixture
	Expected  Expected
}

// PGMajor returns the Postgres major version the harness runs against, from
// ROWSHAPE_PG_VERSION (default 16). This is the knob the CI matrix drives in
// P2-T13 to run the whole corpus against PG 11–17.
func PGMajor() string {
	if v := os.Getenv("ROWSHAPE_PG_VERSION"); v != "" {
		return v
	}
	return "16"
}

// LoadCases reads and parses every triple under <root>/cases. root is the corpus
// directory. A malformed triple is an error — the corpus itself must be valid.
func LoadCases(root string) ([]Case, error) {
	casesDir := filepath.Join(root, "cases")
	entries, err := os.ReadDir(casesDir)
	if err != nil {
		return nil, fmt.Errorf("read cases dir: %w", err)
	}
	var cases []Case
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		c, err := loadCase(filepath.Join(casesDir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("case %s: %w", e.Name(), err)
		}
		cases = append(cases, c)
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].Name < cases[j].Name })
	return cases, nil
}

func loadCase(dir string) (Case, error) {
	c := Case{Name: filepath.Base(dir), Dir: dir}

	mig, err := os.ReadFile(filepath.Join(dir, "migration.sql"))
	if err != nil {
		return c, fmt.Errorf("read migration.sql: %w", err)
	}
	c.Migration = string(mig)

	fx, err := os.ReadFile(filepath.Join(dir, "fixture.yaml"))
	if err != nil {
		return c, fmt.Errorf("read fixture.yaml: %w", err)
	}
	parsed, err := fixture.Parse(fx)
	if err != nil {
		return c, fmt.Errorf("parse fixture.yaml: %w", err)
	}
	c.Fixture = parsed

	exp, err := os.ReadFile(filepath.Join(dir, "expected.json"))
	if err != nil {
		return c, fmt.Errorf("read expected.json: %w", err)
	}
	if err := json.Unmarshal(exp, &c.Expected); err != nil {
		return c, fmt.Errorf("parse expected.json: %w", err)
	}
	return c, nil
}

// Validate checks a triple is well-formed: a non-empty migration, a fixture that
// declares an engine version (required for extrapolation, RFC §9.1), and an
// expected verdict using only known verdicts and finding codes.
func (c Case) Validate() error {
	if strings.TrimSpace(c.Migration) == "" {
		return fmt.Errorf("empty migration.sql")
	}
	if c.Fixture == nil || c.Fixture.Meta.Engine.Version == "" {
		return fmt.Errorf("fixture is missing meta.engine.version")
	}
	if !KnownVerdicts[c.Expected.Verdict] {
		return fmt.Errorf("unknown expected verdict %q", c.Expected.Verdict)
	}
	for _, f := range c.Expected.Findings {
		if !KnownCodes[f.Code] {
			return fmt.Errorf("unknown finding code %q", f.Code)
		}
	}
	for major, ov := range c.Expected.VersionVerdicts {
		if !KnownVerdicts[ov.Verdict] {
			return fmt.Errorf("version_verdicts[%s]: unknown expected verdict %q", major, ov.Verdict)
		}
		if ov.Verdict != "PASS" && len(ov.Findings) == 0 {
			return fmt.Errorf("version_verdicts[%s] expects %s but names no findings", major, ov.Verdict)
		}
		for _, f := range ov.Findings {
			if !KnownCodes[f.Code] {
				return fmt.Errorf("version_verdicts[%s]: unknown finding code %q", major, f.Code)
			}
		}
	}
	return nil
}

// ProducedFinding is a finding a validator actually emitted, enough to check
// both the code and the capping contract (that the remediation names a command).
type ProducedFinding struct {
	// Code is the stable family (e.g. "RS-LOCK"), which is what expected.json
	// names.
	Code string
	// FullCode is the specific code (e.g. "RS-LOCK-001"). Not matched against —
	// expected.json deliberately pins families so a new sibling code is not a
	// corpus break — but carried so a failure message can say WHICH finding
	// arrived, rather than only which family.
	FullCode    string
	Severity    string
	Remediation string
}

// Validator runs a migration against a fixture and returns the verdict and the
// findings it produced. `validate` implements this in P2-T7; until then the
// harness runs only the well-formedness, coverage, and capping-contract checks.
type Validator interface {
	Validate(c Case) (verdict string, findings []ProducedFinding, err error)
}
