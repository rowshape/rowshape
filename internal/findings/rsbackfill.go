package findings

import (
	"fmt"
	"strconv"
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

	// A BOUNDED KEY RANGE is what batching looks like, and it has to be
	// recognized here or this rule dead-ends its own remediation: RS-PERF-010
	// tells the reader to "loop over a bounded key range — UPDATE ... WHERE id
	// BETWEEN :lo AND :hi", and without this the rewritten statement warns
	// exactly as loudly as the unbatched one it replaced. A WARN that persists
	// after the reader does the recommended thing teaches them to ignore it, and
	// for an agent it is worse than noise: the loop cannot close, so it either
	// gives up or hand-waves the verdict.
	if bounded, span, known := boundedRange(tbl, clean, upper); bounded {
		// Literal bounds: the window is computable, so decide on it rather than on
		// the table. A window at or above the threshold is still a mass write and
		// still reported — batching is not a magic word, it is a small window.
		if known && span < backfillThreshold {
			return verdict.Finding{}, false
		}
		if !known {
			// Placeholder or expression bounds ($1, :lo, a subquery). The window
			// size is chosen by the caller at run time and is not in the SQL, so
			// the honest reading is that this statement is windowed — NOT that it
			// may rewrite the whole table, which is the claim the finding below
			// would make and which is false for a batch loop. rowshape cannot
			// verify the caller keeps the window small; no static check can, and
			// declining to certify the whole table on a statement that plainly
			// cannot touch it would be the same over-reach in the other direction.
			return verdict.Finding{}, false
		}
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

// boundedRange reports whether the WHERE clause confines the statement to a
// bounded window on a single column — the shape of a batched backfill.
//
// It recognizes `col BETWEEN a AND b` and a conjunction giving the SAME column
// both a lower bound (> or >=) and an upper bound (< or <=). ONE-SIDED bounds are
// deliberately not enough: `WHERE id >= 1000` matches everything above the
// bound, which is the whole table minus a prefix, and reading that as batched
// would fail open on the exact statement this rule exists to catch.
//
// span is the number of key values the window covers, and known says whether it
// could be computed at all: literal bounds give a number, while `$1` / `:lo` /
// an expression give a window whose size lives outside the SQL.
//
// An OR anywhere disqualifies the clause: it can widen the window back out, and
// a bound that holds for one branch says nothing about the other.
func boundedRange(tbl fixture.Table, clean, upper string) (bounded bool, span int64, known bool) {
	i := strings.Index(upper, "WHERE")
	if i < 0 {
		return false, 0, false
	}
	cond := strings.TrimSpace(strings.Trim(strings.TrimSpace(clean[i+len("WHERE"):]), ";"))
	if strings.Contains(strings.ToUpper(cond), " OR ") {
		return false, 0, false
	}

	type bound struct {
		lo, hi         string
		loOpen, hiOpen bool // a strict > or <, which excludes the endpoint
		haveLo, haveHi bool
	}
	bounds := map[string]*bound{}
	get := func(col string) *bound {
		col = normalizeColumn(col)
		if col == "" {
			return nil
		}
		if _, ok := tbl.Columns[col]; !ok {
			return nil
		}
		if bounds[col] == nil {
			bounds[col] = &bound{}
		}
		return bounds[col]
	}

	for _, conj := range splitConjuncts(cond) {
		cu := strings.ToUpper(conj)
		if j := strings.Index(cu, " BETWEEN "); j > 0 {
			b := get(conj[:j])
			if b == nil {
				continue
			}
			rest := conj[j+len(" BETWEEN "):]
			ru := strings.ToUpper(rest)
			k := strings.Index(ru, " AND ")
			if k < 0 {
				continue
			}
			b.lo, b.hi = strings.TrimSpace(rest[:k]), strings.TrimSpace(rest[k+len(" AND "):])
			b.haveLo, b.haveHi = true, true
			b.loOpen, b.hiOpen = false, false // BETWEEN is inclusive at both ends
			continue
		}
		col, op, val, ok := comparison(conj)
		if !ok {
			continue
		}
		b := get(col)
		if b == nil {
			continue
		}
		switch op {
		case ">", ">=":
			b.lo, b.haveLo, b.loOpen = val, true, op == ">"
		case "<", "<=":
			b.hi, b.haveHi, b.hiOpen = val, true, op == "<"
		}
	}

	for _, b := range bounds {
		if !b.haveLo || !b.haveHi {
			continue
		}
		lo, loOK := literalInt(b.lo)
		hi, hiOK := literalInt(b.hi)
		if loOK && hiOK {
			if b.loOpen {
				lo++
			}
			if b.hiOpen {
				hi--
			}
			if hi < lo {
				return true, 0, true
			}
			return true, hi - lo + 1, true
		}
		return true, 0, false
	}
	return false, 0, false
}

// splitConjuncts splits a WHERE clause on top-level AND, ignoring one inside
// parentheses or a quoted string.
func splitConjuncts(cond string) []string {
	var out []string
	depth, start, inStr := 0, 0, false
	betweenClosed := map[int]bool{}
	up := strings.ToUpper(cond)
	for i := 0; i < len(cond); i++ {
		switch {
		case cond[i] == '\'':
			inStr = !inStr
		case inStr:
		case cond[i] == '(':
			depth++
		case cond[i] == ')':
			depth--
		case depth == 0 && strings.HasPrefix(up[i:], " AND "):
			// `x BETWEEN a AND b` spends an AND of its own. The first AND after an
			// unclosed BETWEEN belongs to it, not to the conjunction — splitting
			// there tore the window in half and left both sides one-sided, so a
			// batched statement read as unbounded.
			if strings.Contains(up[start:i], " BETWEEN ") && !betweenClosed[start] {
				betweenClosed[start] = true
				i += len(" AND ") - 1
				continue
			}
			out = append(out, strings.TrimSpace(cond[start:i]))
			i += len(" AND ") - 1
			start = i + 1
		}
	}
	return append(out, strings.TrimSpace(cond[start:]))
}

// comparison splits `<col> <op> <value>` for the four range operators.
func comparison(conj string) (col, op, val string, ok bool) {
	for _, o := range []string{">=", "<=", ">", "<"} {
		if i := strings.Index(conj, o); i > 0 {
			// Exclude <> and >= / <= when scanning for the single-character forms.
			if o == ">" && (conj[i+1] == '=' || conj[i-1] == '<') {
				continue
			}
			if o == "<" && (conj[i+1] == '=' || conj[i+1] == '>') {
				continue
			}
			return strings.TrimSpace(conj[:i]), o, strings.TrimSpace(conj[i+len(o):]), true
		}
	}
	return "", "", "", false
}

// normalizeColumn strips quoting and a table qualifier from a column reference.
func normalizeColumn(raw string) string {
	c := strings.TrimSpace(raw)
	if i := strings.LastIndex(c, "."); i >= 0 {
		c = c[i+1:]
	}
	c = strings.Trim(strings.TrimSpace(c), `"`)
	if c == "" || strings.ContainsAny(c, " (),") {
		return ""
	}
	return c
}

// literalInt reads an integer literal bound, with an optional cast stripped.
// A placeholder ($1, :lo, ?) or an expression yields ok=false, which is the
// "windowed but unsized" case boundedRange reports.
func literalInt(v string) (int64, bool) {
	t := strings.TrimSpace(v)
	if i := strings.Index(t, "::"); i > 0 {
		t = strings.TrimSpace(t[:i])
	}
	t = strings.Trim(t, "'")
	n, err := strconv.ParseInt(t, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
