package validate

import (
	"errors"
	"strings"
	"sync"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/profile"
	"github.com/rowshape/rowshape/internal/verdict"
)

// Analyzer turns a fixture plus the capture of applying a migration into
// findings. The RS-LOCK / RS-DATA / RS-CONSTRAINT / RS-INDEX analyzers
// (P2-T8..T11) implement this and Register themselves; P2-T7 ships the pipeline
// and an empty registry, so validate runs and emits a well-formed Verdict before
// any finding rule exists.
type Analyzer interface {
	// Analyze returns findings for a migration. Each finding declares the fixture
	// facts it rests on in DependsOn and carries the severity it argues for; the
	// pipeline caps its verdict by those facts' confidence (RFC §7.4).
	Analyze(f *fixture.Fixture, c *Capture) []verdict.Finding
}

// registered holds the analyzers plugged in by later phase-2 tasks.
//
// Guarded, and Registered returns a COPY. In the CLI only init functions ever
// write here, so this was safe in practice — but Register is EXPORTED and the
// stated phase-5 goal is a cloud API importing this package, where a
// request-time Register would be an unsynchronized append (a data race, and a
// torn slice header) and a caller holding the live backing array could observe
// a concurrent append mid-write while BuildResult was ranging it.
//
// The cost is one RLock and one slice copy per validate run, against an
// analyzer set of well under a hundred entries. That is not a price worth
// arguing about for removing a data race from a package that is meant to be
// imported by a server.
var (
	registryMu sync.RWMutex
	registered []Analyzer
)

// Register adds an analyzer to the default registry. Analyzers call this from an
// init function so `validate` picks them up without the CLI knowing each one.
func Register(a Analyzer) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registered = append(registered, a)
}

// Registered returns the default analyzer set.
//
// The returned slice is a copy: handing out the package's own backing array let
// a caller observe a concurrent append, and let a caller mutate the registry by
// writing through the slice it was given.
func Registered() []Analyzer {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]Analyzer, len(registered))
	copy(out, registered)
	return out
}

// ErrHostMatchesSource is the hard refusal that keeps validate's blast radius at
// zero: the target host hashes to the fixture's source host, so validate would
// be about to touch the very database the fixture was pulled from
// (INV-BLAST-RADIUS-ZERO, PRD §11).
var ErrHostMatchesSource = errors.New("validate: refusing to run against the fixture's source host — validate only ever touches a disposable or provided target, never production")

// ErrNoFixtureSource is the refusal when a fixture carries no meta.source, so
// the host-match guard has nothing to compare against.
var ErrNoFixtureSource = errors.New(
	"validate: this fixture records no source host (meta.source), so rowshape cannot verify that the " +
		"target is not the database it was pulled from")

// CheckHost enforces the host-match refusal (PRD §11). fixtureSource is
// meta.source (a salted host hash); targetHost is the plain host of the target
// URL. It refuses when the target is the host the fixture was pulled from. Empty
// inputs are safe (an ephemeral local target has no source to collide with).
//
// A host is not a string. Comparing one hash of the target against the source
// missed every equivalent spelling, and the bypass was not hypothetical: a
// fixture pulled from `localhost`, validated with --ephemeral against
// `127.0.0.1`, sailed past this refusal and began creating a database on the very
// server the fixture came from. `DB.Internal` vs `db.internal` (DNS is
// case-insensitive) and a trailing FQDN dot did the same.
//
// The fix is in two halves, and it needs both. profile.HashSource normalizes the
// host before hashing, so one machine hashes to one value however it was spelled
// — that half has to be at emit time, because a hash cannot be inverted, and if
// the odd spelling is the one already recorded in meta.source, no work here can
// recover it. This half then hashes the target under every spelling that is
// definitionally the same machine, which normalization alone cannot unify:
// `localhost` and `127.0.0.1` normalize to themselves and are still one host.
// Any match refuses.
//
// What this deliberately does NOT do is resolve DNS. `db.internal` and the IP it
// points at are the same machine, but finding that out means a network call from
// a safety check, and an answer that can change between the check and the
// connection. The refusal is the last line, not the only one: it is why
// `--ephemeral` wants a disposable server, not a hostname that merely looks
// different from production.
//
// A missing source is permitted HERE, and that is a deliberate split rather than
// an oversight — see CheckWriteTarget, which does not permit it. The two paths
// differ enormously in stakes: --ephemeral creates and drops a throwaway
// database, while --target COMMITS the migration to whatever it is pointed at.
// Refusing every source-less fixture on both paths would break hand-authored and
// vendored fixtures for no proportionate gain on the disposable one.
func CheckHost(fixtureSource, targetHost string) error {
	if fixtureSource == "" || targetHost == "" {
		return nil
	}
	for _, alias := range hostAliases(targetHost) {
		if profile.HashSource(alias) == fixtureSource {
			return ErrHostMatchesSource
		}
	}
	return nil
}

// hostAliases returns the spellings under which targetHost must be checked: the
// host exactly as given (so a fixture written from that spelling still matches),
// its DNS-normalized form, and — when it is loopback — every other way of naming
// this machine.
func hostAliases(host string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(h string) {
		if h != "" && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}

	add(host) // verbatim: matches fixtures already written with this spelling
	add(profile.NormalizeHost(host))

	if isLoopback(profile.NormalizeHost(host)) {
		// These are the same machine by definition, so a fixture pulled from any
		// of them must refuse a target named by any other.
		for _, l := range []string{"localhost", "127.0.0.1", "::1", "[::1]"} {
			add(l)
		}
	}
	return out
}

// isLoopback reports whether h names this machine. 127.0.0.0/8 is loopback in
// its entirety, not just 127.0.0.1.
func isLoopback(h string) bool {
	return h == "localhost" || h == "::1" || h == "0:0:0:0:0:0:0:1" || strings.HasPrefix(h, "127.")
}

// BuildResult assembles the Verdict from a capture: it runs each analyzer,
// confidence-caps every finding against the fixture (RFC §7.4), and combines the
// produced verdicts. When groundTruth is set — validate ran against a provided
// live branch whose data is production itself (PRD §15) — the evidence is exact,
// so capping cannot downgrade a finding: the facts were observed, not sampled.
func BuildResult(f *fixture.Fixture, c *Capture, analyzers []Analyzer, groundTruth bool) verdict.Result {
	eng := verdict.NewEngine(f)
	var findings []verdict.Finding
	var verdicts []string

	for _, a := range analyzers {
		for _, fnd := range a.Analyze(f, c) {
			want := wantFor(fnd.Severity)
			var got string
			if groundTruth {
				// Validated against real data: exact evidence, capping cannot fire.
				got = want
				fnd.Confidence = string(fixture.Exact)
			} else {
				got, fnd = eng.Cap(want, fnd)
			}
			verdicts = append(verdicts, got)
			// A clean certification is the silent default — only surface findings
			// that are actionable (a WARN/FAIL, or an error/warn severity).
			if got == verdict.VerdictPass && fnd.Severity == verdict.SeverityInfo {
				continue
			}
			findings = append(findings, fnd)
		}
	}

	overall := verdict.Combine(verdicts...)
	// A migration that did not apply cleanly is never a PASS — floor it to FAIL
	// so a broken apply cannot be certified even before the RS-* detectors that
	// name the specific problem exist (P2-T8+; broken-tool vs unsafe-migration is
	// refined in P2-T17).
	if !c.Success {
		overall = verdict.Combine(overall, verdict.VerdictFail)
		// And SAY WHY. Flooring to FAIL without a finding left the verdict carrying no
		// code, no location and no remediation, with the engine's message going to
		// stderr only — so `--json` reported `{"verdict":"FAIL","findings":null}` and a
		// consumer had nothing at all. That breaks the wedge directly: the MCP tool and
		// the GitHub Action both render this struct and nothing else, so an agent was
		// told FAIL and given nothing to act on, which is exactly the hand-waving the
		// agent-rule harness scores against. INV-VERDICT-STABLE also requires
		// remediation on every error.
		//
		// This is the most common real failure of all — a migration with a typo — and
		// it was the one case the contract said nothing about. The finding itself is
		// built by an ANALYZER (internal/findings, RS-APPLY-001): analyzers already
		// receive the Capture, and building it here would need this package to import
		// the finding registry, which imports this one.
		findings = dropGenericApplyFailure(findings)
	}
	// A statement cancelled by the apply ceiling is a THIRD outcome, and it floors
	// to WARN rather than FAIL. Nothing rejected the migration — it simply did not
	// finish inside the ceiling, which is evidence its duration is at least that
	// (the `outage` bucket, INV-DURATIONS-BUCKETS), not evidence that it is broken.
	// FAIL would assert a defect nobody observed; PASS would certify a statement
	// that never completed as safe. WARN is the only honest floor.
	if c.TimedOut {
		overall = verdict.Combine(overall, verdict.VerdictWarn)
	}

	// [] rather than null, so a consumer can iterate without a nil check. A JSON
	// contract that sometimes yields null for "none" makes every reader write the
	// same guard, and some of them forget.
	if findings == nil {
		findings = []verdict.Finding{}
	}

	return verdict.Result{
		Rowshape:   verdict.Rowshape,
		Verdict:    overall,
		Fixture:    verdict.FixtureRef{ID: f.Meta.ID, Digest: f.Meta.Digest},
		DurationMs: c.DurationMs,
		Findings:   findings,
	}
}

// applyFailureCode is the generic "the migration did not apply" finding. It is
// named here rather than imported because internal/findings imports THIS package.
const applyFailureCode = "RS-APPLY-001"

// dropGenericApplyFailure removes the generic apply-failure finding when a
// specific analyzer has already explained the same failure.
//
// RS-APPLY-001 is a FLOOR, not an addition. When a migration is rejected because
// the data does not permit it, RS-DATA-001 already says so in the column's own
// terms and its remediation tells you what to do about the NULLs — the generic
// finding then adds a second entry for one event and dilutes the actionable
// advice with "read the SQLSTATE". Five corpus cases regressed exactly this way
// the moment the generic finding existed.
//
// An analyzer cannot make this call: they run independently and none of them can
// see what the others produced. Here is where the whole set is known.
//
// Only ERROR findings suppress it. A warning about the migration is not an
// explanation of why it was rejected, so a capture that failed with nothing but
// warnings still gets the generic account of the failure.
func dropGenericApplyFailure(findings []verdict.Finding) []verdict.Finding {
	explained := false
	for _, f := range findings {
		if f.Code != applyFailureCode && f.Severity == verdict.SeverityError {
			explained = true
			break
		}
	}
	if !explained {
		return findings
	}
	out := findings[:0]
	for _, f := range findings {
		if f.Code == applyFailureCode {
			continue
		}
		out = append(out, f)
	}
	return out
}

// wantFor maps a finding's severity to the verdict it argues for: an error is a
// detected FAIL, a warn is a WARN, and an info finding is a clean PASS that
// capping may downgrade if it rests on weak facts.
func wantFor(severity string) string {
	switch severity {
	case verdict.SeverityError:
		return verdict.VerdictFail
	case verdict.SeverityWarn:
		return verdict.VerdictWarn
	case verdict.SeverityInfo, "":
		// Info (and the historical unset) argue for a clean PASS, which capping
		// downgrades if the facts underneath are weak.
		return verdict.VerdictPass
	}

	// CR-T8. Anything else is a value nobody recognizes — a typo'd constant in a
	// future analyzer, or a severity from a newer contract this build predates.
	// The old `default` sent it to PASS, i.e. the single most permissive outcome
	// was the fallback for "I do not know what this is", and nothing downstream
	// would have caught it: capping can only weaken a PASS that rests on weak
	// FACTS, and a typo'd severity says nothing about facts.
	//
	// WARN is the safe direction: it can never certify, and it is visible. It is
	// deliberately not FAIL — inventing a failure from a string we failed to
	// parse would be its own kind of wrong answer.
	return verdict.VerdictWarn
}

// MarkExact upgrades every fact in f to `exact` confidence. It is used when the
// facts come from a PROVIDED live target — a real database or a branch — where
// the data is ground truth rather than a sample, so uniqueness, null fractions,
// orphan fractions, and fan-outs read there are exact (PRD §15, the Neon
// branching complementarity: `--target $NEON_BRANCH_URL` upgrades facts to exact).
// MarkExact MUTATES f IN PLACE, including through shared *Fact pointers.
//
// SINGLE-OWNER CONTRACT: the caller must own f exclusively for the duration.
// This is not a data race in the CLI, where each run parses its own fixture, but
// it is the reason a parsed fixture cannot be CACHED and shared across
// concurrent requests in the planned phase-5 cloud API: two requests marking the
// same fixture would race on Confidence, and a request that only READS the
// fixture would see another request's mutation. profile.ApplyPrivacy has the
// same property.
//
// If a server ever wants to share one parsed fixture, it needs a deep copy first
// — a shallow copy is not enough, because the Confidence writes below go through
// pointers the copy would still share.
func MarkExact(f *fixture.Fixture) {
	if f == nil {
		return
	}
	for name, tbl := range f.Tables {
		tbl.Rows.Confidence = fixture.Exact
		f.Tables[name] = tbl
		for _, col := range tbl.Columns {
			if col.NullFraction != nil {
				col.NullFraction.Confidence = fixture.Exact
			}
			if col.Distinct != nil {
				col.Distinct.Confidence = fixture.Exact
			}
			if col.Unique != nil {
				col.Unique.Confidence = fixture.Exact
			}
		}
		for i := range tbl.References {
			if tbl.References[i].OrphanFraction != nil {
				tbl.References[i].OrphanFraction.Confidence = fixture.Exact
			}
			if tbl.References[i].Fanout != nil {
				tbl.References[i].Fanout.Confidence = fixture.Exact
			}
		}
	}
}

// Located is a statement together with where it came from. The origin is what
// lets a finding carry `location` (PRD §10) — the field a PR annotation needs to
// point at the offending line (P4-T2).
type Located struct {
	SQL  string
	File string // as given to SplitStatementsIn; "" for inline SQL
	Line int    // 1-based line where the statement text begins
}

// SplitStatements splits a SQL script into individual statements on top-level
// semicolons, skipping semicolons inside line/block comments, single- and
// double-quoted strings, and dollar-quoted bodies ($$...$$, $tag$...$tag$). It
// is enough to apply a raw-SQL migration statement-by-statement for capture; it
// does not validate SQL.
func SplitStatements(sql string) []string {
	loc := SplitStatementsIn("", sql)
	out := make([]string, 0, len(loc))
	for _, l := range loc {
		out = append(out, l.SQL)
	}
	return out
}

// SplitStatementsIn is SplitStatements, keeping each statement's origin.
//
// Line is where the statement's text starts, counting a leading comment as part
// of the statement — that is where a reviewer's eye goes, and it is what the
// splitter already has without a second parse.
func SplitStatementsIn(file, sql string) []Located {
	var stmts []Located
	var buf strings.Builder
	runes := []rune(sql)
	i, n := 0, len(runes)

	// Prefix count of newlines, so a rune index maps to a line in O(1).
	nl := make([]int, n+1)
	c := 0
	for k := 0; k < n; k++ {
		nl[k] = c
		if runes[k] == '\n' {
			c++
		}
	}
	nl[n] = c
	lineAt := func(idx int) int {
		if idx > n {
			idx = n
		}
		if idx < 0 {
			idx = 0
		}
		return nl[idx] + 1
	}
	bufStart := 0

	flush := func() {
		raw := buf.String()
		s := strings.TrimSpace(raw)
		if s != "" {
			// Where the trimmed text actually begins, so leading blank lines
			// between statements do not shift the reported line.
			lead := len([]rune(raw)) - len([]rune(strings.TrimLeft(raw, " \t\r\n")))
			stmts = append(stmts, Located{SQL: s, File: file, Line: lineAt(bufStart + lead)})
		}
		buf.Reset()
	}

	for i < n {
		if buf.Len() == 0 {
			bufStart = i
		}
		c := runes[i]
		switch {
		case c == '-' && i+1 < n && runes[i+1] == '-': // line comment
			for i < n && runes[i] != '\n' {
				buf.WriteRune(runes[i])
				i++
			}
		case c == '/' && i+1 < n && runes[i+1] == '*': // block comment
			buf.WriteString("/*")
			i += 2
			for i < n && !(runes[i] == '*' && i+1 < n && runes[i+1] == '/') {
				buf.WriteRune(runes[i])
				i++
			}
			if i < n {
				buf.WriteString("*/")
				i += 2
			}
		case c == '\'' || c == '"': // quoted string / identifier
			quote := c
			// A leading E marks a Postgres escape string (E'...'), where a
			// backslash escapes the next character. This is deliberately NOT
			// applied to an ordinary '...' literal: standard_conforming_strings
			// has been on by default since PG 9.1, so a backslash there is a plain
			// character, and treating it as an escape everywhere would break
			// `SELECT 'C:\'` by swallowing its own closing quote — trading this bug
			// for a new one.
			escapes := quote == '\'' && opensEscapeString(runes, i)
			buf.WriteRune(c)
			i++
			for i < n {
				r := runes[i]
				if escapes && r == '\\' && i+1 < n {
					// The escaped character cannot end the literal, whatever it is:
					// \' is a quote, \\ is a backslash that must not then escape a
					// following quote.
					buf.WriteRune(r)
					buf.WriteRune(runes[i+1])
					i += 2
					continue
				}
				if r == quote {
					// A doubled quote is an escaped quote, not the end of the
					// literal. This previously worked by accident — the scanner
					// closed the string and immediately reopened it on the second
					// quote, which preserved the text and so preserved the split —
					// but only as long as nothing depended on where the literal
					// actually ended. Handling it explicitly costs nothing.
					if i+1 < n && runes[i+1] == quote {
						buf.WriteRune(r)
						buf.WriteRune(runes[i+1])
						i += 2
						continue
					}
					buf.WriteRune(r)
					i++
					break
				}
				buf.WriteRune(r)
				i++
			}
		case c == '$': // possible dollar-quote
			if tag, ok := dollarTag(runes, i); ok {
				tagLen := len([]rune(tag))
				buf.WriteString(tag)
				i += tagLen
				for i < n {
					// Match the closing tag rune-by-rune. Building string(runes[i:])
					// here allocated the WHOLE remaining input on every character of
					// the dollar body, making a large migration's split O(n^2) in
					// time and memory (a benchmark caught it: ~0.5s / 117 MB for a
					// ~1000-statement script). A prefix compare over the rune slice
					// is O(tag) and allocates nothing.
					if hasRunesPrefix(runes, i, tag) {
						buf.WriteString(tag)
						i += tagLen
						break
					}
					buf.WriteRune(runes[i])
					i++
				}
			} else {
				buf.WriteRune(c)
				i++
			}
		case c == ';':
			flush()
			i++
		default:
			buf.WriteRune(c)
			i++
		}
	}
	flush()
	return stmts
}

// opensEscapeString reports whether the quote at index i opens a Postgres escape
// string, i.e. is immediately preceded by a standalone E (as in E'it\'s').
//
// The E must be its own token: in `SOME'x'` the preceding rune is also a letter,
// but that is the tail of an identifier, not an escape-string prefix. Requiring
// the character before the E to be a non-identifier rune keeps the two apart.
func opensEscapeString(runes []rune, i int) bool {
	if i == 0 || (runes[i-1] != 'E' && runes[i-1] != 'e') {
		return false
	}
	if i-2 >= 0 && (isAlnum(runes[i-2]) || runes[i-2] == '_') {
		return false
	}
	return true
}

// hasRunesPrefix reports whether the rune slice starting at i begins with s,
// comparing rune-by-rune without allocating (unlike string(runes[i:])).
func hasRunesPrefix(runes []rune, i int, s string) bool {
	for _, r := range s {
		if i >= len(runes) || runes[i] != r {
			return false
		}
		i++
	}
	return true
}

// dollarTag reads a dollar-quote opening tag ($$ or $tag$) starting at i,
// returning it and true if one is present.
func dollarTag(runes []rune, i int) (string, bool) {
	n := len(runes)
	if i >= n || runes[i] != '$' {
		return "", false
	}
	j := i + 1
	for j < n && (runes[j] == '_' || isAlnum(runes[j])) {
		j++
	}
	if j < n && runes[j] == '$' {
		return string(runes[i : j+1]), true
	}
	return "", false
}

func isAlnum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// CheckWriteTarget guards the one path that WRITES to a database the user
// nominated: `validate --target`, which opens a transaction, executes the
// migration DDL and COMMITS it.
//
// It fails CLOSED on a missing meta.source. The original guard returned nil
// whenever the source was empty, which meant the single check standing between
// this command and a production database silently switched itself off for any
// hand-written, vendored or hand-edited fixture — precisely the fixtures least
// likely to have come from a careful `rowshape pull`. An absent fact is a reason
// to decline, never a reason to proceed. That is the rule the confidence model
// applies everywhere else in this codebase, and the place it mattered most was
// the one place it was not applied.
//
// iKnow is the deliberate override, mirroring the precedent `pull` already sets
// for its superuser refusal: rowshape declines by default and lets someone who
// understands the situation say so explicitly.
//
// KNOWN LIMIT, recorded rather than papered over: even with a source present,
// this compares HOST HASHES. Pulling through a pgBouncer endpoint or a replica
// CNAME and then targeting the primary yields different hashes and passes. The
// robust comparison is the server's own system_identifier from
// pg_control_system(), which survives DNS games — but recording that at pull
// time touches the fixture schema and therefore the canonical digest, so it is a
// deliberate follow-up rather than something to decide in passing.
func CheckWriteTarget(fixtureSource, targetHost string, iKnow bool) error {
	if err := CheckHost(fixtureSource, targetHost); err != nil {
		return err // the target IS the source: refuse regardless of iKnow
	}
	if fixtureSource == "" && !iKnow {
		return ErrNoFixtureSource
	}
	return nil
}
