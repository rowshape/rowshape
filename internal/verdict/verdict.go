// Package verdict defines the public verdict contract for rowshape.
//
// The verdict contract (fields, finding codes, exit codes) is the public API
// (INV-VERDICT-STABLE, PRD §10). Everything else can change; this cannot, once
// agents depend on it. This package is one of the two package boundaries —
// together with internal/fixture — that the phase-5 cloud API imports UNCHANGED
// so that verdict shape and exit-code semantics have exactly one implementation
// (PRD §9).
//
// There is one Result struct with two marshalers: JSON (this file's tags) and a
// human renderer (render_human.go), which is a RENDERING of the same struct,
// never a separate code path (INV-VERDICT-SHAPE). The struct is shaped as a
// signable in-toto/DSSE predicate from day one (dsse.go, INV-DSSE-SHAPE).
package verdict

import "github.com/rowshape/rowshape/internal/exitcode"

// Rowshape is the verdict contract version tag carried in every Result. It is
// the "rowshape" field of PRD §10 and bumps only on a breaking contract change.
const Rowshape = "1"

// Exit codes are part of the public API and are permanent (INV-VERDICT-STABLE,
// PRD §10). Every command maps its outcome onto exactly these.
// The names stay here because they are what every caller already uses; the
// VALUES now come from internal/exitcode, so this package and internal/toolerror
// cannot drift apart (CR-T23).
const (
	ExitPass      = exitcode.Pass      // PASS
	ExitFail      = exitcode.Fail      // FAIL
	ExitWarnOnly  = exitcode.WarnOnly  // WARN-only (configurable to fail — see ExitCode)
	ExitToolError = exitcode.ToolError // tool error (could not produce a verdict)
)

// Verdict values (PRD §10). A verdict is capped by the minimum confidence of the
// facts it rests on (INV-CONFIDENCE-CAPPING): a finding on estimated facts
// yields WARN, never PASS.
const (
	VerdictPass = "PASS"
	VerdictWarn = "WARN"
	VerdictFail = "FAIL"
)

// Severity values for a finding (PRD §10). remediation is mandatory on every
// finding of severity error (INV-VERDICT-STABLE, enforced by Validate).
const (
	SeverityError = "error"
	SeverityWarn  = "warn"
	SeverityInfo  = "info"
)

// Duration buckets (PRD §10, INV-DURATIONS-BUCKETS). Durations are reported as
// buckets with the extrapolation basis attached, never as point estimates.
const (
	BucketInstant    = "instant"
	BucketFast       = "fast"
	BucketNoticeable = "noticeable"
	BucketSlow       = "slow"
	BucketOutage     = "outage"
)

// Result is the top-level verdict value — the API of PRD §10. One struct,
// rendered by two marshalers (INV-VERDICT-SHAPE); human output is always a
// rendering of this struct, never a separate code path. It is also the DSSE
// predicate body (dsse.go).
type Result struct {
	Rowshape   string     `json:"rowshape"`    // contract version tag (== Rowshape)
	Verdict    string     `json:"verdict"`     // PASS | WARN | FAIL
	Fixture    FixtureRef `json:"fixture"`     // subject of the verdict
	DurationMs int64      `json:"duration_ms"` // wall time of the run
	Findings   []Finding  `json:"findings"`    // zero or more findings
}

// FixtureRef identifies the fixture a verdict was computed against. Its Digest
// is the first DSSE subject (dsse.go).
type FixtureRef struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
}

// Finding is a single result. Finding codes are permanent and namespaced by
// class (RS-LOCK, RS-DATA, RS-CONSTRAINT, RS-INDEX, RS-PERF, RS-REVERSE, RS-APPLY) with a
// numeric suffix, e.g. RS-LOCK-001 (INV-VERDICT-STABLE). Every finding declares
// DependsOn and carries Confidence — the minimum across the fixture facts it
// relied on (INV-CONFIDENCE-CAPPING). remediation is mandatory on every finding
// of severity error.
type Finding struct {
	Code        string    `json:"code"`
	Severity    string    `json:"severity"` // error | warn | info
	Title       string    `json:"title"`
	Location    *Location `json:"location,omitempty"`
	Detail      string    `json:"detail,omitempty"`
	Evidence    any       `json:"evidence,omitempty"`
	Estimate    *Estimate `json:"estimate,omitempty"`
	DependsOn   []string  `json:"depends_on,omitempty"` // fixture facts this rests on
	Confidence  string    `json:"confidence,omitempty"` // min confidence across DependsOn
	Remediation string    `json:"remediation,omitempty"`
	Explain     string    `json:"explain,omitempty"` // e.g. "rowshape explain RS-LOCK-001"
}

// Location points at the migration statement a finding is about. It is an object
// {file, line}, not a string — agents parse it (PRD §10).
type Location struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

// Estimate is the extrapolation basis behind a duration bucket (PRD §10,
// INV-DURATIONS-BUCKETS). Bucket is never a point estimate; Model/BasisRows/
// BasisMs/DeclaredRows record how it was extrapolated so the claim is defensible.
type Estimate struct {
	Bucket       string `json:"bucket"`               // instant|fast|noticeable|slow|outage
	Model        string `json:"model"`                // e.g. "linear"
	BasisRows    int64  `json:"basis_rows"`           // rows the basis measurement covered
	BasisMs      int64  `json:"basis_ms"`             // wall time of the basis measurement
	DeclaredRows int64  `json:"declared_rows"`        // rows the operation will actually touch
	Confidence   string `json:"confidence,omitempty"` // confidence of the basis facts
}

// ExitCode maps a Result onto the process exit code (PRD §10). PASS -> 0,
// FAIL -> 1, WARN-only -> 2 (or 1 when warnFails, the "configurable to fail"
// knob). A tool error is ExitToolError and is signalled by the caller, not here:
// a Result is only produced when a verdict exists.
func (r Result) ExitCode(warnFails bool) int {
	switch r.Verdict {
	case VerdictFail:
		return ExitFail
	case VerdictWarn:
		if warnFails {
			return ExitFail
		}
		return ExitWarnOnly
	default:
		return ExitPass
	}
}

// Validate enforces the parts of the contract a Result must always satisfy
// before it is emitted: a known verdict, and remediation present on every
// finding of severity error — "a finding an agent can't act on is a bug" (PRD
// §10, INV-VERDICT-STABLE). It is a guard against emitting a malformed verdict,
// not a substitute for the capping engine (P2-T4).
func (r Result) Validate() error {
	switch r.Verdict {
	case VerdictPass, VerdictWarn, VerdictFail:
	default:
		return &ContractError{Msg: "unknown verdict " + r.Verdict}
	}
	for i, f := range r.Findings {
		if f.Code == "" {
			return &ContractError{Msg: "finding has empty code", Index: i}
		}
		if !ValidSeverity(f.Severity) {
			// CR-T8: caught at emit time, so a severity nobody recognizes surfaces
			// as a loud contract error (exit 3, not a verdict) instead of quietly
			// steering the verdict decision in validate.wantFor.
			return &ContractError{Msg: "finding " + f.Code + " has unknown severity " + f.Severity, Index: i}
		}
		if f.Severity == SeverityError && f.Remediation == "" {
			return &ContractError{Msg: "error finding " + f.Code + " has no remediation", Index: i}
		}
	}
	return nil
}

// ContractError is a violation of the verdict contract detected by Validate.
type ContractError struct {
	Msg   string
	Index int
}

func (e *ContractError) Error() string { return "verdict contract: " + e.Msg }

// ValidSeverity reports whether s is one of the three severities the contract
// defines. An empty string is accepted as the historical "unset", which callers
// have always treated as info.
//
// It exists because severity is a plain string on the wire (INV-VERDICT-STABLE
// fixes the JSON shape, so it cannot become a typed enum without a contract
// break), and a value nobody recognizes must not be able to reach the
// PASS-permitting branch of a verdict decision — see validate.wantFor (CR-T8).
func ValidSeverity(s string) bool {
	switch s {
	case SeverityError, SeverityWarn, SeverityInfo, "":
		return true
	}
	return false
}
