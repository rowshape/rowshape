package findings

import (
	"fmt"
	"strings"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/validate"
	"github.com/rowshape/rowshape/internal/verdict"
)

func init() { validate.Register(rsPerf{}) }

// rsPerf detects performance pathologies (PRD §10 RS-PERF namespace). Query-plan
// regression is out of v1 (PRD §13); these are the data-shape perf pathologies a
// summary statistic hides (RFC §6.6):
//
//   - RS-PERF-001: a DELETE (or TRUNCATE) on a parent table referenced ON DELETE
//     CASCADE with a long-tailed fan-out. The mean fan-out looks harmless while
//     the tail (max) turns one parent delete into a huge, slow, lock-holding
//     cascade — an outage a uniform mean would completely hide.
//   - RS-PERF-002: an unqualified UPDATE or DELETE (no WHERE) on a large table —
//     it rewrites/removes every row, a slow, bloat-inducing, lock-holding scan
//     that is almost never what was intended.
type rsPerf struct{}

// massDMLThreshold is the row count above which an unqualified UPDATE/DELETE is
// worth warning about; below it, touching every row is cheap.
const massDMLThreshold = 100_000

func (rsPerf) Analyze(f *fixture.Fixture, c *validate.Capture) []verdict.Finding {
	var out []verdict.Finding
	for _, st := range c.Statements {
		// Strip comments before identifier extraction: a comment mentioning
		// "UPDATE" or "DELETE FROM" must not be parsed as the statement.
		clean := collapseSpaces(stripSQLComments(st.SQL))
		upper := strings.ToUpper(clean)
		// Resolution happens here, at the caller: the parsers stay pure and every
		// SQL-derived table name is mapped onto the fixture's own key before it is
		// compared or looked up (RFC §5). Without it `DELETE FROM accounts` never
		// matches the fixture's `public.accounts`, and RS-PERF-001 is silently
		// dropped rather than reported.
		if parent, ok := deleteTarget(upper, clean); ok {
			out = append(out, cascadeFanoutFindings(f, resolveTable(f, parent))...)
		}
		if fnd, ok := massDMLFinding(f, upper, clean); ok {
			out = append(out, fnd)
		}
	}
	return out
}

// massDMLFinding flags an unqualified UPDATE/DELETE (no WHERE) on a large table.
func massDMLFinding(f *fixture.Fixture, upper, sql string) (verdict.Finding, bool) {
	table, verb, ok := unqualifiedDML(upper, sql)
	if !ok {
		return verdict.Finding{}, false
	}
	// An unresolved name misses f.Tables entirely, and the !ok branch below reads
	// that as "not a large table" — so an unqualified UPDATE on a 6M-row table
	// produced no finding at all. Resolving also puts the canonical fixture key
	// into DependsOn, which is what the attestation should cite.
	table = resolveTable(f, table)
	tbl, ok := f.Tables[table]
	if !ok || tbl.Rows.Value < massDMLThreshold {
		return verdict.Finding{}, false
	}
	return verdict.Finding{
		Code:        "RS-PERF-002",
		Severity:    verdict.SeverityWarn,
		Title:       fmt.Sprintf("Unqualified %s on %s touches all %s rows", verb, shortTable(table), humanCount(tbl.Rows.Value)),
		Detail:      fmt.Sprintf("%s with no WHERE clause rewrites or removes every one of %s's %d rows — a slow, bloat-inducing, lock-holding scan.", verb, shortTable(table), tbl.Rows.Value),
		Evidence:    map[string]any{"rows": tbl.Rows.Value, "verb": verb},
		DependsOn:   []string{table + ".rows"},
		Remediation: remediation("RS-PERF-002"),
		Explain:     "rowshape explain RS-PERF-002",
	}, true
}

// unqualifiedDML recognizes an UPDATE or DELETE with no WHERE clause and returns
// the target table and the verb.
func unqualifiedDML(upper, sql string) (table, verb string, ok bool) {
	switch {
	case strings.HasPrefix(upper, "DELETE FROM ") && !hasWhereClause(upper):
		return firstIdentAfter(sql, "DELETE FROM "), "DELETE", true
	case strings.HasPrefix(upper, "UPDATE ") && strings.Contains(upper, " SET ") && !hasWhereClause(upper):
		return firstIdentAfter(sql, "UPDATE "), "UPDATE", true
	}
	return "", "", false
}

// hasWhereClause reports whether an upper-cased statement carries a WHERE.
//
// Testing for the literal " WHERE " missed `DELETE FROM t WHERE(id=1)`, which is
// valid SQL — collapseSpaces normalizes whitespace runs but does not insert a
// space before "(" — so a perfectly well-qualified DELETE was reported as
// unqualified full-table DML. A FALSE finding on safe SQL is the expensive kind:
// it blocks a correct migration.
//
// The check is boundary-aware rather than a substring test so that an identifier
// merely containing "WHERE" (a column named `nowhere`, say) cannot satisfy it.
func hasWhereClause(upper string) bool {
	const kw = "WHERE"
	for i := 0; ; {
		j := strings.Index(upper[i:], kw)
		if j < 0 {
			return false
		}
		j += i
		beforeOK := j == 0 || !isIdentByte(upper[j-1])
		after := j + len(kw)
		afterOK := after >= len(upper) || !isIdentByte(upper[after])
		if beforeOK && afterOK {
			return true
		}
		i = j + len(kw)
	}
}

// isIdentByte reports whether c can appear inside a SQL identifier.
func isIdentByte(c byte) bool {
	return c == '_' || c == '$' ||
		(c >= '0' && c <= '9') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= 'a' && c <= 'z')
}

// cascadeFanoutFindings returns a finding for each ON DELETE CASCADE reference
// into parent whose fan-out is long-tailed.
func cascadeFanoutFindings(f *fixture.Fixture, parent string) []verdict.Finding {
	var out []verdict.Finding
	// Sorted, not map order. A parent with two or more long-tailed cascading
	// children emitted its findings in whatever order Go's map iteration chose,
	// so the same fixture and migration produced a different Findings slice run
	// to run — and the verdict is shaped as a signable in-toto predicate
	// (internal/verdict/dsse.go), so an unstable slice means an unstable
	// attestation over identical inputs.
	for _, childName := range sortedTableNames(f) {
		child := f.Tables[childName]
		for _, ref := range child.References {
			if refParent(ref.To) != parent || !strings.EqualFold(ref.OnDelete, "cascade") || ref.Fanout == nil {
				continue
			}
			if !longTailed(ref.Fanout) {
				continue
			}
			out = append(out, verdict.Finding{
				Code:        "RS-PERF-001",
				Severity:    verdict.SeverityWarn,
				Title:       fmt.Sprintf("DELETE on %s cascades through a long-tailed fan-out (max %s vs mean %s children)", shortTable(parent), trimNum(ref.Fanout.Max), trimNum(ref.Fanout.Mean)),
				Detail:      fmt.Sprintf("%s.%s references %s ON DELETE CASCADE; one parent can cascade to as many as %s child rows while the mean is only %s — deleting the wrong parents is a slow, lock-holding cascade.", shortTable(childName), ref.Column, shortTable(parent), trimNum(ref.Fanout.Max), trimNum(ref.Fanout.Mean)),
				Evidence:    map[string]any{"fanout_mean": ref.Fanout.Mean, "fanout_max": ref.Fanout.Max, "cascade_from": childName + "." + ref.Column},
				DependsOn:   []string{childName + ".rows"},
				Remediation: remediation("RS-PERF-001"),
				Explain:     "rowshape explain RS-PERF-001",
			})
		}
	}
	return out
}

// deleteTarget returns the table a DELETE (or TRUNCATE) targets.
func deleteTarget(upper, sql string) (string, bool) {
	switch {
	case strings.HasPrefix(upper, "DELETE FROM "):
		return firstIdentAfter(sql, "DELETE FROM "), true
	case strings.HasPrefix(upper, "TRUNCATE "):
		// TRUNCATE [TABLE] <name>
		rest := "TRUNCATE "
		if strings.HasPrefix(upper, "TRUNCATE TABLE ") {
			rest = "TRUNCATE TABLE "
		}
		return firstIdentAfter(sql, rest), true
	}
	return "", false
}

// firstIdentAfter returns the first identifier following a case-insensitive
// prefix, stripped of a trailing clause/semicolon.
func firstIdentAfter(sql, prefix string) string {
	up := strings.ToUpper(sql)
	i := strings.Index(up, strings.ToUpper(prefix))
	if i < 0 {
		return ""
	}
	fields := strings.Fields(sql[i+len(prefix):])
	if len(fields) == 0 {
		return ""
	}
	return strings.Trim(strings.TrimRight(fields[0], ";"), `"`)
}

// refParent reduces a reference target ("public.users.id") to its table
// ("public.users").
func refParent(to string) string {
	if i := strings.LastIndexByte(to, '.'); i >= 0 {
		return to[:i]
	}
	return to
}

// longTailed reports whether a fan-out has a heavy tail: the max dwarfs the mean,
// so a uniform mean would hide the risk (RFC §6.6). Requires a meaningful
// absolute tail so tiny tables do not trip it.
func longTailed(fo *fixture.Fanout) bool {
	if fo.Mean <= 0 {
		return fo.Max >= 100
	}
	return fo.Max >= 100 && fo.Max >= fo.Mean*10
}
