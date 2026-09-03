package verdict

import (
	"strings"

	"github.com/rowshape/rowshape/internal/fixture"
)

// Engine caps the verdict a finding can produce by the confidence of the fixture
// facts it rests on (RFC §7.4, INV-CONFIDENCE-CAPPING — the one thing that must
// never be wrong, because a wrong PASS is unrecoverable).
//
// The load-bearing design choice: the confidence of every dependency is read
// FROM THE FIXTURE, keyed by the fact path a finding declares in depends_on. A
// finding names a fact; it never asserts the fact's confidence. There is no API
// that accepts a caller-supplied confidence for a dependency, so a finding
// structurally cannot downgrade (or upgrade) a dependency's confidence to reach
// a stronger verdict — the RFC §7.4 rule enforced in code, not by convention.
type Engine struct {
	fx *fixture.Fixture
}

// NewEngine builds a capping engine reading confidences from fx.
func NewEngine(fx *fixture.Fixture) *Engine { return &Engine{fx: fx} }

// absent is the confidence of a dependency the fixture does not carry. It ranks
// below every named level (fixture.Confidence.rank), so an unresolvable or
// missing dependency is the weakest possible reading and can never license PASS
// (RFC §7.4: "declared / absent → WARN").
const absent = fixture.Confidence("")

// Ceiling returns the strongest verdict a finding resting on confidence c may
// produce (RFC §7.4 table): PASS at exact/measured, WARN at estimated, declared,
// or absent.
func Ceiling(c fixture.Confidence) string {
	if c.AtLeast(fixture.Measured) {
		return VerdictPass
	}
	return VerdictWarn
}

// DependencyConfidence returns the minimum confidence across the fixture facts a
// finding declares in depends_on (RFC §7.4). Each path is resolved from the
// fixture; a declared-but-unresolvable path (the fact is absent) drives the
// minimum to absent — the weakest reading. A finding with NO declared
// dependencies rests on no uncertain fact and is not capped (exact).
//
// This is the ONLY source of a dependency's confidence. There is no parameter by
// which a caller asserts one — that is the structural guarantee.
func (e *Engine) DependencyConfidence(deps []string) fixture.Confidence {
	if len(deps) == 0 {
		return fixture.Exact
	}
	min := fixture.Exact
	for _, d := range deps {
		min = fixture.Min(min, e.factConfidence(d))
	}
	return min
}

// Cap applies confidence capping to one finding (RFC §7.4). want is the verdict
// the finding argues for: PASS for a clean certification, WARN, or FAIL for a
// detected problem. Cap:
//
//   - reads the minimum confidence across the finding's declared DependsOn from
//     the fixture and records it on the finding's Confidence field;
//   - downgrades a PASS resting on facts weaker than measured to WARN, marking
//     the finding warn and ensuring its Remediation names the resolving command;
//   - leaves WARN and FAIL untouched.
//
// It returns the verdict the finding actually produces and the possibly-updated
// finding. Capping only ever prevents over-certification: it turns a wrong PASS
// into a loud, actionable WARN and never weakens a detected failure.
func (e *Engine) Cap(want string, f Finding) (string, Finding) {
	minConf := e.DependencyConfidence(f.DependsOn)
	f.Confidence = string(minConf)

	got := capToCeiling(want, Ceiling(minConf))
	if got == VerdictWarn && want == VerdictPass {
		if f.Severity == "" || f.Severity == SeverityInfo {
			f.Severity = SeverityWarn
		}
		if resolve := e.ResolveCommand(f.DependsOn); resolve != "" && !strings.Contains(f.Remediation, resolve) {
			f.Remediation = joinResolve(f.Remediation, resolve)
		}
	}
	return got, f
}

// ResolveCommand returns the instruction that would raise the weakest declared
// dependency to a certifying confidence — the "here is how to turn this WARN into
// a PASS" string (RFC §7.4).
//
// It names `pull --exact` and the fact that needs raising, but is deliberately NOT
// a bare `rowshape pull --exact <target>`: `--exact` is a boolean that re-profiles
// the WHOLE source, and `pull` takes a connection URL, not a column selector. The
// old form `rowshape pull --exact public.users.email` fed the selector in where a
// DSN belongs, so an agent that ran it verbatim — as init --agent's rule tells it
// to — got a connection-parse tool error, not a re-pull. This phrasing is honest
// about what to run (an exact re-pull of the source) and why (to raise this fact).
func (e *Engine) ResolveCommand(deps []string) string {
	// Only for a finding whose facts do NOT already certify. This is the "here is
	// how to turn this WARN into a PASS" string, so on a finding resting on exact
	// facts it is not merely redundant, it is false: it tells the reader a fact
	// needs raising when nothing does.
	//
	// The capping path only reaches here after a downgrade, so the guard is a
	// no-op there. The MCP surface asks for one on EVERY finding, and without it
	// an RS-INDEX-002 resting on an exact row count told the agent to re-profile
	// the source to raise a fact that was already exact — advice that costs a full
	// production re-pull and changes nothing, delivered to the surface the agent
	// loop is built on.
	if Ceiling(e.DependencyConfidence(deps)) != VerdictWarn {
		return ""
	}
	target := e.weakestTarget(deps)
	if target == "" {
		return ""
	}
	return "rowshape pull --exact <source-url> (re-profiles the source, raising " + target + " to exact)"
}

// capToCeiling downgrades a PASS that exceeds the ceiling to WARN; it never
// touches WARN or FAIL. Capping only prevents over-certification.
func capToCeiling(want, ceiling string) string {
	if want == VerdictPass && ceiling == VerdictWarn {
		return VerdictWarn
	}
	return want
}

// factConfidence resolves one fixture fact path to its confidence, reading it
// from the fixture. Supported paths: `<schema>.<table>.rows`,
// `<schema>.<table>.<column>.{unique,null_fraction,distinct,range}`. An unresolvable
// path yields absent — the weakest reading — so an unknown dependency can never
// license PASS.
func (e *Engine) factConfidence(path string) fixture.Confidence {
	if e.fx == nil {
		return absent
	}
	table, rest, ok := e.splitTable(path)
	if !ok {
		return absent
	}
	tbl := e.fx.Tables[table]
	if rest == "rows" {
		return tbl.Rows.Confidence
	}
	col, fact, ok := cut(rest)
	if !ok {
		return absent
	}
	// orphan_fraction lives on a reference (keyed by local column), not on the
	// column profile, so it resolves even when the FK column has no column entry
	// (RFC §6.6).
	if fact == "orphan_fraction" {
		// CR-T24: when a column carries MORE THAN ONE reference — legal, and what a
		// column participating in two foreign keys looks like — take the WEAKEST
		// confidence, not the first one found.
		//
		// First-match is the wrong tie-break for a capping engine on principle: the
		// whole design caps a verdict by the MINIMUM confidence of the facts it
		// rests on (RFC §7.4), so resolving a genuine ambiguity by declaration
		// order could let a strong fact mask a weak sibling and license a PASS the
		// weaker one would have capped. Declaration order is not evidence.
		//
		// Not reachable today: no fixture in the corpus has two references on one
		// column, which is why this needed its own test rather than corpus
		// coverage. The underlying ambiguity is a FORMAT-level question — the
		// fixture does not distinguish which FK a `<table>.<col>.orphan_fraction`
		// path means — and this only makes the engine's answer the safe one.
		found := false
		weakest := absent
		for _, ref := range tbl.References {
			if ref.Column == col && ref.OrphanFraction != nil {
				if !found {
					found, weakest = true, ref.OrphanFraction.Confidence
					continue
				}
				weakest = fixture.Min(weakest, ref.OrphanFraction.Confidence)
			}
		}
		if !found {
			return absent
		}
		return weakest
	}
	c, ok := tbl.Columns[col]
	if !ok {
		return absent
	}
	switch fact {
	case "unique":
		if c.Unique == nil {
			return absent
		}
		return c.Unique.Confidence
	case "null_fraction":
		if c.NullFraction == nil {
			return absent
		}
		return c.NullFraction.Confidence
	case "distinct":
		if c.Distinct == nil {
			return absent
		}
		return c.Distinct.Confidence
	case "range":
		// `range` used to have no case here, so a finding resting on the profiled
		// extremes resolved to `absent` — recorded as D-010, on the reasoning that
		// fixture.Range carried no confidence at all. It does now (§6.1), because a
		// SAMPLED range understates the extremes and a finding that keys off them
		// then fails to fire at all.
		if c.Range == nil {
			return absent
		}
		if c.Range.Confidence == "" {
			// A fixture written before the field. Absent, not estimated: the weakest
			// reading is the only safe one for a fact whose provenance is unknown.
			return absent
		}
		return c.Range.Confidence
	}
	return absent
}

// weakestTarget returns the escalation target (schema.table[.column]) of the
// weakest declared dependency, ties broken by declaration order.
func (e *Engine) weakestTarget(deps []string) string {
	if len(deps) == 0 {
		return ""
	}
	bestDep, bestConf := deps[0], e.factConfidence(deps[0])
	for _, d := range deps[1:] {
		if c := e.factConfidence(d); !c.AtLeast(bestConf) { // strictly weaker
			bestDep, bestConf = d, c
		}
	}
	return e.factTarget(bestDep)
}

// factTarget reduces a fact path to the column (or table) `rowshape pull --exact`
// escalates: `public.users.email.unique` → `public.users.email`,
// `public.users.rows` → `public.users`.
func (e *Engine) factTarget(path string) string {
	table, rest, ok := e.splitTable(path)
	if !ok {
		return path
	}
	if rest == "" || rest == "rows" {
		return table
	}
	if col, _, ok := cut(rest); ok {
		return table + "." + col
	}
	return table + "." + rest
}

// splitTable finds the longest table key that prefixes path (table keys are
// qualified, e.g. "public.users") and returns it with the remaining selector.
func (e *Engine) splitTable(path string) (table, rest string, ok bool) {
	if e.fx == nil {
		return "", "", false
	}
	best := ""
	for k := range e.fx.Tables {
		if (path == k || strings.HasPrefix(path, k+".")) && len(k) > len(best) {
			best = k
		}
	}
	if best == "" {
		return "", "", false
	}
	if path == best {
		return best, "", true
	}
	return best, path[len(best)+1:], true
}

// cut splits "a.b.c" into ("a", "b.c") at the first dot.
func cut(s string) (head, tail string, ok bool) {
	i := strings.IndexByte(s, '.')
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+1:], true
}

// joinResolve appends the resolving command to a remediation as a "Resolve:"
// clause, matching the RFC §7.4 rendering.
func joinResolve(remediation, resolve string) string {
	clause := "Resolve: " + resolve
	if strings.TrimSpace(remediation) == "" {
		return clause
	}
	return remediation + " " + clause
}

// Combine folds the verdicts individual findings produce into the overall result
// verdict: FAIL dominates WARN dominates PASS (PRD §10). An empty set is PASS.
func Combine(verdicts ...string) string {
	worst := VerdictPass
	for _, v := range verdicts {
		if verdictRank(v) > verdictRank(worst) {
			worst = v
		}
	}
	return worst
}

func verdictRank(v string) int {
	switch v {
	case VerdictFail:
		return 2
	case VerdictWarn:
		return 1
	default:
		return 0
	}
}
