package findings

import (
	"fmt"
	"strings"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/validate"
	"github.com/rowshape/rowshape/internal/verdict"
)

func init() { validate.Register(rsBackfill{}) }

// rsBackfill covers the DML the catalog exempted BY CONSTRUCTION.
//
// RS-PERF-002 fires only when a statement has NO WHERE clause. The real-world
// hazard always has one:
//
//	UPDATE users SET normalized = lower(email) WHERE normalized IS NULL;
//
// That is the canonical unbatched backfill — one statement, 50M rows, one
// transaction, one very long lock on every row it touches, and a replication lag
// spike. It carried a WHERE, so it was exempt, and returned PASS.
//
// The hard part is SELECTIVITY: `WHERE x IS NULL` might match 50M rows or 10, and
// nothing in the SQL says which. But the fixture often knows — null_fraction is
// exactly the selectivity of an IS NULL predicate, and it is profiled for every
// column. Where the fixture can answer, this sizes the statement from real facts.
// Where it cannot, it says so rather than falling silent, because silence here is
// what produced the gap.
type rsBackfill struct{}

// backfillThreshold is the estimated affected-row count above which a qualified
// DML statement is worth batching. It matches massDMLThreshold so the qualified
// and unqualified rules agree about what "large" means.
const backfillThreshold = massDMLThreshold

func (rsBackfill) Analyze(f *fixture.Fixture, c *validate.Capture) []verdict.Finding {
	var out []verdict.Finding
	for _, st := range c.Statements {
		clean := collapseSpaces(stripSQLComments(st.SQL))
		upper := strings.ToUpper(clean)

		verb := ""
		switch {
		case strings.HasPrefix(upper, "UPDATE ") && strings.Contains(upper, " SET "):
			verb = "UPDATE"
		case strings.HasPrefix(upper, "DELETE FROM "):
			verb = "DELETE"
		default:
			continue
		}
		// The unqualified form is RS-PERF-002's; this rule is only about the
		// qualified one, so the two never double-flag a statement.
		if !hasWhereClause(upper) {
			continue
		}
		if fnd, ok := backfillFinding(f, clean, upper, verb); ok {
			out = append(out, fnd)
		}
	}
	return out
}

func backfillFinding(f *fixture.Fixture, clean, upper, verb string) (verdict.Finding, bool) {
	raw := firstIdentAfter(clean, "UPDATE ")
	if verb == "DELETE" {
		raw = firstIdentAfter(clean, "DELETE FROM ")
	}
	table := resolveTable(f, raw)
	tbl, known := f.Tables[table]
	if !known {
		// The fixture has no facts for this table, so nothing about the
		// statement's cost can be decided. RS-PERF-002 read that same condition
		// as "not a large table" and fell silent — an unresolved name silently
		// downgraded a mass DML to PASS. Say it instead: an absent fact is a
		// reason to decline, never a reason to certify.
		return verdict.Finding{
			Code:     "RS-PERF-010",
			Severity: verdict.SeverityWarn,
			Title:    fmt.Sprintf("%s on %s cannot be sized: the fixture has no facts for that table", verb, raw),
			Detail: fmt.Sprintf(
				"%s touches an unknown number of rows because %q does not resolve to any table in the "+
					"fixture — a name the pull never saw, a schema/search_path mismatch, or a typo. rowshape "+
					"declines to certify a statement it cannot size rather than reading the absence as safety.",
				verb, raw),
			Evidence:    map[string]any{"unresolved_table": raw, "verb": verb},
			DependsOn:   nil,
			Remediation: remediation("RS-PERF-010"),
			Explain:     "rowshape explain RS-PERF-010",
		}, true
	}

	rows := tbl.Rows.Value
	if rows < backfillThreshold {
		return verdict.Finding{}, false // genuinely small: touching all of it is cheap
	}

	// A predicate that pins a UNIQUE column to one value matches at most one row.
	// This has to be checked before the unknown-selectivity fallback, or
	// `UPDATE users SET status = 'x' WHERE id = 42` — a single-row update by
	// primary key — is reported as a 50M-row backfill. That is the noisiest
	// possible false positive: it fires on the most ordinary statement there is.
	if matchesUniqueEquality(tbl, clean, upper) {
		return verdict.Finding{}, false
	}

	affected, conf, explained := estimateAffected(tbl, clean, upper, rows)
	if explained && affected < backfillThreshold {
		return verdict.Finding{}, false // the predicate provably narrows it enough
	}

	title := fmt.Sprintf("%s on %s may rewrite up to %s rows in one statement",
		verb, shortTable(table), humanCount(rows))
	detail := fmt.Sprintf(
		"A single %s over a large table holds row locks and one transaction for its whole duration, bloats "+
			"the table, and can stall replication. rowshape cannot tell from the SQL how many rows this "+
			"predicate matches, so it reports the upper bound: %s's %s rows.",
		verb, shortTable(table), humanCount(rows))
	ev := map[string]any{"table_rows": rows, "verb": verb, "selectivity": "unknown"}

	if explained {
		title = fmt.Sprintf("%s on %s rewrites about %s rows in one statement",
			verb, shortTable(table), humanCount(affected))
		detail = fmt.Sprintf(
			"The predicate matches roughly %s of %s's %s rows (from the column's profiled null_fraction, %s). "+
				"A single %s over that many rows holds row locks and one transaction for its whole duration, "+
				"bloats the table, and can stall replication.",
			humanCount(affected), shortTable(table), humanCount(rows), conf, verb)
		ev["estimated_rows"] = affected
		ev["selectivity"] = "null_fraction"
	}

	return verdict.Finding{
		Code:        "RS-PERF-010",
		Severity:    verdict.SeverityWarn,
		Title:       title,
		Detail:      detail,
		Evidence:    ev,
		DependsOn:   []string{table + ".rows"},
		Remediation: remediation("RS-PERF-010"),
		Explain:     "rowshape explain RS-PERF-010",
	}, true
}

// estimateAffected derives how many rows a predicate matches, when the fixture
// can say.
//
// `WHERE col IS NULL` is the canonical backfill predicate, and null_fraction is
// precisely its selectivity — a fact rowshape already profiles for every column.
// `IS NOT NULL` is its complement. Anything else is left unexplained rather than
// guessed at: a wrong estimate presented confidently is worse than an honest
// upper bound.
func estimateAffected(tbl fixture.Table, clean, upper string, rows int64) (int64, fixture.Confidence, bool) {
	col, negated, ok := nullPredicateColumn(clean, upper)
	if !ok {
		return 0, "", false
	}
	c, ok := tbl.Columns[col]
	if !ok || c.NullFraction == nil {
		return 0, "", false
	}
	frac := c.NullFraction.Value
	if negated {
		frac = 1 - frac
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	return int64(float64(rows) * frac), c.NullFraction.Confidence, true
}

// nullPredicateColumn extracts the column from a `<col> IS [NOT] NULL` predicate.
// It requires the predicate to be the WHOLE clause: a compound condition changes
// the selectivity in ways this cannot model, and reporting a confident number for
// one conjunct would be worse than declining.
func nullPredicateColumn(clean, upper string) (col string, negated, ok bool) {
	i := strings.Index(upper, "WHERE")
	if i < 0 {
		return "", false, false
	}
	cond := strings.TrimSpace(clean[i+len("WHERE"):])
	cond = strings.TrimSuffix(strings.TrimSpace(strings.Trim(cond, ";")), ";")
	condUp := strings.ToUpper(cond)

	// A compound predicate is not modelled.
	if strings.Contains(condUp, " AND ") || strings.Contains(condUp, " OR ") {
		return "", false, false
	}
	switch {
	case strings.HasSuffix(condUp, "IS NOT NULL"):
		negated = true
		col = strings.TrimSpace(cond[:len(cond)-len("IS NOT NULL")])
	case strings.HasSuffix(condUp, "IS NULL"):
		col = strings.TrimSpace(cond[:len(cond)-len("IS NULL")])
	default:
		return "", false, false
	}
	col = strings.Trim(col, `"()`)
	if col == "" || strings.ContainsAny(col, " (),") {
		return "", false, false
	}
	return col, negated, true
}

// matchesUniqueEquality reports whether the WHERE clause pins a column the
// fixture knows to be UNIQUE to a single value, which bounds the statement to at
// most one row.
//
// Only an exact-confidence uniqueness fact counts. An estimated one is not proof,
// and this is a silencing rule — the direction where being wrong means missing a
// real backfill, so it must rest on a fact that was actually proven.
func matchesUniqueEquality(tbl fixture.Table, clean, upper string) bool {
	i := strings.Index(upper, "WHERE")
	if i < 0 {
		return false
	}
	cond := strings.TrimSpace(strings.Trim(strings.TrimSpace(clean[i+len("WHERE"):]), ";"))
	condUp := strings.ToUpper(cond)
	// A compound predicate is not modelled: an OR can widen it back out.
	if strings.Contains(condUp, " OR ") {
		return false
	}
	// Take the first conjunct: an AND can only narrow further, so proving one
	// conjunct bounds the whole predicate to one row.
	if j := strings.Index(condUp, " AND "); j >= 0 {
		cond = strings.TrimSpace(cond[:j])
	}
	eq := strings.Index(cond, "=")
	if eq <= 0 {
		return false
	}
	// Exclude comparison operators that merely contain "=".
	if strings.ContainsAny(string(cond[eq-1]), "<>!") {
		return false
	}
	col := strings.TrimSpace(strings.Trim(strings.TrimSpace(cond[:eq]), `"`))
	if col == "" || strings.ContainsAny(col, " (),") {
		return false
	}
	c, ok := tbl.Columns[col]
	if !ok || c.Unique == nil {
		return false
	}
	return c.Unique.Value && c.Unique.Confidence == fixture.Exact
}
