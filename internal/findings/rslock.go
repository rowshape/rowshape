// Package findings holds the RS-* analyzers that turn a fixture plus the capture
// of applying a migration into verdict findings. Each analyzer registers itself
// with the validate pipeline; importing this package for side effects wires them
// all in (see cmd/root.go). Finding codes are permanent and namespaced
// (INV-VERDICT-STABLE).
package findings

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/rowshape/rowshape/internal/estimate"
	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/validate"
	"github.com/rowshape/rowshape/internal/verdict"
)

func init() { validate.Register(rsLock{}) }

// rsLock detects lock-mode and table-rewrite pathologies (PRD §10 RS-LOCK-001):
// an ACCESS EXCLUSIVE lock held for a full table rewrite — a volatile-default
// ADD COLUMN or a column type change. It reports the lock mode, the rows
// rewritten, a bucketed duration extrapolated to DECLARED rows, and the
// mandatory expand/backfill/contract remediation. It is version-conditional:
// a non-volatile default is a catalog-only fast-path on PG 11+ and does not fire
// (RFC §9.1, PRD §12).
type rsLock struct{}

func (rsLock) Analyze(f *fixture.Fixture, c *validate.Capture) []verdict.Finding {
	major, hasVersion := estimate.Major(f.Meta.Engine.Version)

	var out []verdict.Finding
	for i, st := range c.Statements {
		op, table, kind, rewrites := classifyRewrite(st.SQL, major, hasVersion)
		table = resolveTable(f, table)
		if !rewrites {
			continue
		}
		// classifyRewrite reports every ALTER COLUMN ... TYPE as a rewrite from the
		// SQL alone, but a binary-coercible string widening — varchar(n)->varchar(m>=n)
		// or ->text — is a catalog-only change on PG >= 9.2 (no rewrite, no scan). The
		// fixture's CURRENT type is what tells a widening from a rewrite, so the check
		// lives here rather than in the pure-SQL classifier. Reporting an extrapolated
		// `outage` for a metadata-only change is a fabricated finding.
		if kind == alterColumnTypeKind && isNoRewriteTypeChange(f, table, st.SQL) {
			continue
		}
		out = append(out, rsLock{}.finding(f, c, i, st, op, table, kind, hasVersion))
	}
	return out
}

// finding builds one RS-LOCK-001 finding for a rewrite statement at index i.
func (rsLock) finding(f *fixture.Fixture, c *validate.Capture, i int, st validate.Statement, op estimate.OpClass, table, kind string, hasVersion bool) verdict.Finding {
	tbl := f.Tables[table]
	declared := tbl.Rows.Value

	lockMode := humanLock(st.LockMode)
	if lockMode == "" {
		lockMode = "ACCESS EXCLUSIVE" // a rewrite always takes it, even if unobserved
	}

	fnd := verdict.Finding{
		Code:      "RS-LOCK-001",
		Severity:  verdict.SeverityWarn,
		Location:  locationFor(st),
		Evidence:  map[string]any{"lock_mode": lockMode, "rows_rewritten": declared},
		DependsOn: []string{table + ".rows"},
		// The rewrite recipe is mandatory on this finding (INV-VERDICT-STABLE): a
		// finding an agent can't act on is a bug. It comes from the shared catalog
		// so it never drifts from `rowshape explain RS-LOCK-001`.
		Remediation: remediation("RS-LOCK-001"),
		Explain:     "rowshape explain RS-LOCK-001",
	}

	// Durations are buckets with the basis attached, never point estimates
	// (INV-DURATIONS-BUCKETS). Extrapolation refuses without an engine version.
	known := tableKnown(f, table)
	est := estimateFor(c, i, op, table, declared, tbl.Rows.Confidence, known, hasVersion)
	switch {
	case est != nil:
		fnd.Estimate = est
		fnd.Title = fmt.Sprintf("%s lock on %s, %s rewrite of %s rows", lockMode, shortTable(table), est.Bucket, humanCount(declared))
		fnd.Detail = fmt.Sprintf("%s holds %s and rewrites all %d rows.", kind, lockMode, declared)
	case !known:
		// The lock is real — it is read off the SQL — but the row count is not
		// ours to guess. Say the table is missing instead of reporting the
		// `instant` that rows=0 would produce.
		fnd.Title = fmt.Sprintf("%s lock on %s rewrites the table (duration not extrapolated: %s is not in the fixture)", lockMode, shortTable(table), table)
		fnd.Detail = fmt.Sprintf("%s holds %s and rewrites the whole table. %s carries no facts in this fixture, so the row count is unknown and the duration is not extrapolated. Re-run `rowshape pull` to include it, or qualify the table name if it exists under a different schema.", kind, lockMode, table)
	default:
		fnd.Title = fmt.Sprintf("%s lock on %s rewrites %s rows (duration not extrapolated: no engine version)", lockMode, shortTable(table), humanCount(declared))
		fnd.Detail = fmt.Sprintf("%s holds %s and rewrites all %d rows. meta.engine.version is absent, so the duration is not extrapolated (RFC §9.1).", kind, lockMode, declared)
	}
	return fnd
}

// classifyRewrite recognizes a rewrite-causing ALTER TABLE and returns its cost
// class, the target table, a human description of the change, and whether it
// rewrites. It is version-conditional for ADD COLUMN ... DEFAULT (RFC §9.1).
func classifyRewrite(rawSQL string, major int, hasVersion bool) (op estimate.OpClass, table, kind string, rewrites bool) {
	sql := stripSQLComments(rawSQL)
	upper := strings.ToUpper(collapseSpaces(sql))
	if !strings.HasPrefix(upper, "ALTER TABLE") {
		return 0, "", "", false
	}
	table = alterTableTarget(sql)

	switch {
	case strings.Contains(upper, "ADD COLUMN") || addsColumn(upper):
		// A STORED generated column is computed for every existing row at ADD
		// time, so it rewrites the table exactly as a volatile default does — and
		// it carries no DEFAULT clause, so the defaultExpr check below never saw
		// it and the statement produced no finding at all.
		if strings.Contains(upper, "GENERATED ALWAYS AS") && strings.Contains(upper, "STORED") {
			return estimate.TableRewrite, table, "ADD COLUMN GENERATED ALWAYS AS ... STORED", true
		}
		def, hasDefault := defaultExpr(sql)
		if !hasDefault {
			return 0, "", "", false // ADD COLUMN without a default does not rewrite
		}
		switch volatilityOf(def) {
		case volatileYes:
			return estimate.TableRewrite, table, "ADD COLUMN with a volatile default", true
		case volatileUnknown:
			// An expression rowshape does not recognize. Reporting it as
			// non-volatile - which is what a two-state check did - means NO
			// FINDING AT ALL on PG 11+, so a user-defined volatile function, or
			// any of the many volatile builtins not on the list, silently
			// certified a full table rewrite as safe. Fail-open on the single
			// hazard this tool is best known for.
			//
			// The honest answer for an unrecognized expression is "I cannot
			// tell", which the confidence model already knows how to express.
			// The caller emits the finding without an estimate.
			return estimate.TableRewrite, table, "ADD COLUMN with a default whose volatility rowshape cannot determine", true
		}
		// Known non-volatile: catalog fast-path on PG 11+, rewrite before that.
		// Without an engine version, assume the worst (a rewrite) rather than a
		// recent default (RFC §9.1).
		m := major
		if !hasVersion {
			m = 0
		}
		op = estimate.ClassifyAddColumnDefault(false, m)
		return op, table, "ADD COLUMN with a non-volatile default", op == estimate.TableRewrite
	case strings.Contains(upper, "ALTER COLUMN") && (strings.Contains(upper, " TYPE ") || strings.Contains(upper, "SET DATA TYPE")):
		return estimate.TableRewrite, table, alterColumnTypeKind, true
	}
	return 0, "", "", false
}

// alterColumnTypeKind is the human description classifyRewrite returns for a
// column type change; it also gates the string-widening no-rewrite check.
const alterColumnTypeKind = "ALTER COLUMN ... TYPE"

// isNoRewriteTypeChange reports whether an ALTER COLUMN ... TYPE is a
// binary-coercible string widening that Postgres performs as a catalog-only
// change (no table rewrite, no verify scan) on every version rowshape supports.
//
// Restricted to the varchar/text pair widening upward:
//
//	varchar(n) -> varchar(m>=n)   metadata-only   (suppressed)
//	varchar(n) -> text            metadata-only   (suppressed)
//	varchar(200) -> varchar(100)  scans/rewrites  (kept)
//	text -> varchar(n)            verify scan      (kept — imposes a cap)
//	char(...) / non-string types  conservative     (kept)
func isNoRewriteTypeChange(f *fixture.Fixture, table, sql string) bool {
	clean := collapseSpaces(stripSQLComments(sql))
	upper := strings.ToUpper(clean)
	col := columnBeforeTypeChange(clean, upper)
	newType := typeAfter(clean, upper)
	c, ok := f.Tables[table].Columns[col]
	if !ok {
		return false // unknown current type: keep the conservative rewrite finding
	}
	return isStringWidening(c.Type, newType)
}

// isStringWidening reports whether oldType -> newType is a length-increasing (or
// cap-removing) change within the varchar/text family — Postgres's documented
// no-rewrite fast path (§9.1). char is deliberately excluded: it is blank-padded,
// so a length change can re-pad and rewrite.
func isStringWidening(oldType, newType string) bool {
	strOK := func(t string) bool {
		b := strings.ToLower(strings.TrimSpace(baseSQLType(t)))
		return b == "text" || b == "varchar" || b == "character varying"
	}
	if !strOK(oldType) || !strOK(newType) {
		return false
	}
	newLen, newBounded := typeLength(newType)
	if !newBounded {
		return true // -> text / unbounded varchar never truncates or rewrites
	}
	oldLen, oldBounded := typeLength(oldType)
	if !oldBounded {
		return false // text -> varchar(n) imposes a cap and needs a verify scan
	}
	return newLen >= oldLen // varchar(n) -> varchar(m>=n)
}

// addsColumn matches "ADD <col>" without the optional COLUMN keyword.
func addsColumn(upper string) bool {
	return strings.Contains(upper, " ADD ") && !strings.Contains(upper, "ADD CONSTRAINT") &&
		!strings.Contains(upper, "ADD PRIMARY") && !strings.Contains(upper, "ADD UNIQUE") &&
		!strings.Contains(upper, "ADD FOREIGN") && !strings.Contains(upper, "ADD CHECK")
}

// defaultExpr extracts the DEFAULT expression of an ADD COLUMN, if present.
func defaultExpr(sql string) (string, bool) {
	up := strings.ToUpper(sql)
	i := strings.Index(up, "DEFAULT ")
	if i < 0 {
		return "", false
	}
	rest := strings.TrimSpace(sql[i+len("DEFAULT "):])
	// Cut at the end of the column definition (next clause or statement end).
	for _, stop := range []string{";", " NOT NULL", " NULL", ","} {
		if j := strings.Index(strings.ToUpper(rest), strings.ToUpper(stop)); j >= 0 {
			rest = rest[:j]
		}
	}
	return strings.TrimSpace(rest), true
}

// volatility is the three-state answer to "does this DEFAULT force a rewrite?".
//
// A two-state answer was the bug: anything unrecognized fell to "not volatile",
// which on PG 11+ means the catalog fast-path and therefore NO FINDING. A
// user-defined volatile function, uuid_generate_v7(), gen_random_bytes(),
// statement_timestamp() - all silently certified a full table rewrite as safe,
// on the single hazard this tool is best known for.
type volatility int

const (
	volatileNo volatility = iota
	volatileYes
	volatileUnknown
)

// volatileFns are volatile expressions whose column default forces a full table
// rewrite on every Postgres version (they cannot live in the catalog as a single
// constant). The list is NOT exhaustive - it cannot be, since a user can define
// their own - which is exactly why volatilityOf falls to volatileUnknown rather
// than to volatileNo.
var volatileFns = []string{
	"gen_random_uuid", "uuid_generate_v1", "uuid_generate_v4",
	"random(", "clock_timestamp", "timeofday", "nextval",
}

// stableFns are expressions known NOT to be volatile, so a default using one can
// take the PG 11+ catalog fast-path. now() and current_timestamp are STABLE, not
// volatile: they are fixed for the duration of the statement, so the default is a
// single constant and no rewrite is needed.
var stableFns = []string{
	"now(", "current_timestamp", "current_date", "current_time",
	"localtimestamp", "localtime", "current_user", "session_user", "current_schema",
}

// volatilityOf classifies a DEFAULT expression.
func volatilityOf(expr string) volatility {
	lower := strings.ToLower(strings.TrimSpace(expr))
	if lower == "" {
		return volatileNo
	}
	for _, fn := range volatileFns {
		if strings.Contains(lower, fn) {
			return volatileYes
		}
	}
	for _, fn := range stableFns {
		if strings.Contains(lower, fn) {
			return volatileNo
		}
	}
	// A LITERAL is definitely not volatile: a number, a quoted string, a boolean,
	// NULL, or any of those with a cast. This is the common, safe case and it must
	// stay silent or the rule becomes noise on every ADD COLUMN ... DEFAULT 0.
	if isLiteralDefault(lower) {
		return volatileNo
	}
	// Anything else - a function call rowshape does not recognize, or a bare
	// identifier - cannot be decided from the text.
	return volatileUnknown
}

// isLiteralDefault reports whether an expression is a plain literal, optionally
// cast. It deliberately accepts only shapes that CANNOT be a function call.
func isLiteralDefault(lower string) bool {
	e := lower
	// Strip a trailing cast: '0'::bigint, 'x'::text.
	if i := strings.Index(e, "::"); i >= 0 {
		e = strings.TrimSpace(e[:i])
	}
	e = strings.TrimSpace(strings.Trim(e, "()"))
	switch e {
	case "null", "true", "false":
		return true
	}
	// A quoted string literal with no embedded call.
	if len(e) >= 2 && e[0] == 0x27 && e[len(e)-1] == 0x27 {
		return true
	}
	// A number.
	hasDigit := false
	for i := 0; i < len(e); i++ {
		c := e[i]
		switch {
		case c >= '0' && c <= '9':
			hasDigit = true
		case c == '.' || c == '-' || c == '+' || c == 'e':
			// part of a numeric literal
		default:
			return false
		}
	}
	return hasDigit
}

// alterTableTarget extracts the (possibly schema-qualified) table name from an
// ALTER TABLE statement, dropping an optional ONLY.
func alterTableTarget(sql string) string {
	fields := strings.Fields(collapseSpaces(sql))
	// fields: ALTER TABLE [ONLY] <table> ...
	i := 2
	if i < len(fields) && strings.EqualFold(fields[i], "ONLY") {
		i++
	}
	if i < len(fields) {
		return strings.Trim(fields[i], `"`)
	}
	return ""
}

// humanLock renders a pg_locks mode ("AccessExclusiveLock") as the SQL lock name
// ("ACCESS EXCLUSIVE"), matching the PRD §10 evidence.
func humanLock(mode string) string {
	switch mode {
	case "AccessExclusiveLock":
		return "ACCESS EXCLUSIVE"
	case "ExclusiveLock":
		return "EXCLUSIVE"
	case "ShareRowExclusiveLock":
		return "SHARE ROW EXCLUSIVE"
	case "ShareLock":
		return "SHARE"
	case "ShareUpdateExclusiveLock":
		return "SHARE UPDATE EXCLUSIVE"
	case "RowExclusiveLock":
		return "ROW EXCLUSIVE"
	case "RowShareLock":
		return "ROW SHARE"
	case "AccessShareLock":
		return "ACCESS SHARE"
	}
	return ""
}

// locationFor reports the migration location of a statement. The per-file line
// is not plumbed to the analyzer yet, so this stays nil; the evidence and title
// carry the actionable detail. (Location is populated when validate threads the
// source file, a follow-up.)
// locationFor turns a statement's origin into the finding's `location` (PRD §10).
//
// This returned nil unconditionally — a stub, with a discarded parameter — so
// `location` was never populated on any finding, ever, while sitting in the
// documented verdict contract and in PRD §10's own example. The human renderer
// already prints "at file:line" when it is set, and P4-T2's whole job is to turn
// it into a PR annotation at the offending line: that story would have been built
// on a field that is always empty.
//
// Inline SQL (an agent handing over the migration it just wrote, unsaved) has no
// file, and nil is the honest answer there rather than a fabricated path.
func locationFor(st validate.Statement) *verdict.Location {
	if st.File == "" || st.Line <= 0 {
		return nil
	}
	// Forward slashes, always. The verdict is the public contract
	// (INV-VERDICT-STABLE) and is shaped to be DSSE-signable (INV-DSSE-SHAPE), so
	// a path that reads migrations.sql on Windows and migrations/001.sql on
	// Linux makes the same migration produce two different documents — and GitHub
	// annotations (P4-T2) want repo-style paths regardless of the runner's OS.
	return &verdict.Location{File: filepath.ToSlash(st.File), Line: st.Line}
}

// shortTable drops the schema qualifier for a compact title ("public.orders" ->
// "orders"), matching the PRD §10 example title.
func shortTable(table string) string {
	if i := strings.LastIndexByte(table, '.'); i >= 0 {
		return table[i+1:]
	}
	return table
}

// humanCount renders a row count compactly (1200000 -> "1.2M").
func humanCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.0fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// collapseSpaces normalizes runs of whitespace (incl. newlines) to single spaces.
func collapseSpaces(s string) string { return strings.Join(strings.Fields(s), " ") }

// stripSQLComments removes -- line comments and /* */ block comments so a
// statement's leading keyword can be recognized even when the migration is
// documented with a comment header.
func stripSQLComments(sql string) string {
	var b strings.Builder
	runes := []rune(sql)
	for i, n := 0, len(runes); i < n; {
		switch {
		case runes[i] == '-' && i+1 < n && runes[i+1] == '-':
			for i < n && runes[i] != '\n' {
				i++
			}
		case runes[i] == '/' && i+1 < n && runes[i+1] == '*':
			i += 2
			for i < n && !(runes[i] == '*' && i+1 < n && runes[i+1] == '/') {
				i++
			}
			i += 2
		default:
			b.WriteRune(runes[i])
			i++
		}
	}
	return b.String()
}

// resolveTable maps a table name as written in the migration onto the fixture's
// own key (RFC §5 keys tables by qualified name; migrations say `users`).
//
// Every analyzer routes SQL-derived table names through this before touching the
// fixture, because a miss is silent and dangerous: the lookup yields the zero
// value, so an unresolved 50M-row table reads as rows=0 and its rewrite is
// reported as `instant` rather than `outage`.
//
// When the fixture cannot resolve the name — genuinely absent, or the same name
// in two schemas — the raw name is returned unchanged. That keeps today's
// behavior for the caller (no facts found, dependency unresolvable, capped to
// WARN) rather than silently answering from the wrong table.
func resolveTable(f *fixture.Fixture, raw string) string {
	if resolved, ok := f.ResolveTable(raw); ok {
		return resolved
	}
	return raw
}
