package hydrate

import (
	"strconv"
	"strings"

	"github.com/rowshape/rowshape/internal/fixture"
)

// This file teaches the synthesis engine to read a table's CHECK constraints
// (RFC §6.4) as what they are: a declaration of which values a column may hold.
//
// It exists because of the objection that kept CHECKs out of the disposable
// database in the first place — "a CHECK can carry domain logic that
// obviously-fake values needn't satisfy". That was true and the consequence was
// worse than the problem: dropping the constraint meant a migration writing a
// status the constraint forbids came back PASS while the source database refused
// it outright.
//
// The fix is the one the engine already applies to enums. An enum column draws
// from the type's labels because the type says those are the only legal values; a
// column under `CHECK (status IN ('pending','paid','shipped'))` is an enum in all
// but name, and that shape — a literal set, or a simple numeric bound — is what
// the overwhelming majority of production CHECKs are. Reading them turns the
// constraint from something the fixture records and the target cannot enforce
// into something both sides agree on.
//
// It is deliberately NOT a SQL expression evaluator. Anything it does not
// recognize with certainty yields no domain, generation is unchanged, and the
// constraint fails inside its savepoint and is REPORTED — the honest degradation,
// not a guess that would constrain hydrated data in a way production does not.
//
// Using the literal values is consistent with what the fixture already publishes:
// a CHECK expression is DDL and is emitted verbatim at `standard` (§6.4). Under
// `privacy:strict` it is `opaque`, which parses to nothing, so strict fixtures
// draw no values from it.

// checkDomain is what a table's CHECK constraints say about one column: either an
// explicit set of legal values, or numeric bounds, or both.
type checkDomain struct {
	// Values is the set a column is restricted to, from `IN (...)` / `= ANY (ARRAY[...])`.
	Values []string
	// Min and Max are inclusive numeric bounds accumulated from comparisons.
	Min, Max *float64
}

// checkDomains extracts a per-column domain from a table's CHECK constraints.
// Constraints that name no column, name more than one, or use a shape this does
// not recognize contribute nothing.
func checkDomains(tbl fixture.Table) map[string]checkDomain {
	out := map[string]checkDomain{}
	for _, con := range tbl.Constraints {
		if con.Kind != "check" || con.Expression == "" || con.Expression == "opaque" {
			continue
		}
		// A NOT VALID constraint is not enforced against existing rows, so production
		// itself may hold values that violate it. Constraining synthesis to satisfy it
		// would make the target STRICTER than production, which is its own wrong
		// verdict — so it is recorded (and recreated NOT VALID) but not obeyed here.
		if con.Validated != nil && !*con.Validated {
			continue
		}
		// A CHECK is a conjunction at the top level; each conjunct is considered on its
		// own so `CHECK (a > 0 AND a < 100)` yields both bounds.
		for _, part := range splitConjuncts(con.Expression) {
			col, dom, ok := parseCheckAtom(part)
			if !ok {
				continue
			}
			out[col] = mergeDomain(out[col], dom)
		}
	}
	for col, dom := range out {
		if len(dom.Values) == 0 && dom.Min == nil && dom.Max == nil {
			delete(out, col)
		}
	}
	return out
}

// mergeDomain intersects two domains for the same column. Multiple CHECKs on one
// column are ANDed by the engine, so the narrowest wins.
func mergeDomain(a, b checkDomain) checkDomain {
	if len(b.Values) > 0 {
		if len(a.Values) == 0 {
			a.Values = b.Values
		} else {
			a.Values = intersect(a.Values, b.Values)
		}
	}
	if b.Min != nil && (a.Min == nil || *b.Min > *a.Min) {
		a.Min = b.Min
	}
	if b.Max != nil && (a.Max == nil || *b.Max < *a.Max) {
		a.Max = b.Max
	}
	return a
}

func intersect(a, b []string) []string {
	in := make(map[string]bool, len(b))
	for _, s := range b {
		in[s] = true
	}
	var out []string
	for _, s := range a {
		if in[s] {
			out = append(out, s)
		}
	}
	return out
}

// splitConjuncts splits a CHECK body on top-level AND, respecting parentheses and
// string literals. OR is not split: a disjunction constrains nothing on its own,
// and treating either branch as required would over-constrain.
func splitConjuncts(expr string) []string {
	expr = stripOuterParens(expr)
	var parts []string
	depth, inStr, start := 0, false, 0
	for i := 0; i < len(expr); i++ {
		switch c := expr[i]; {
		case c == '\'':
			// '' inside a literal is an escaped quote, and the second one flips the
			// state back on the next iteration — which is the same as not flipping.
			inStr = !inStr
		case inStr:
			// Inside a literal nothing else is structural.
		case c == '(':
			depth++
		case c == ')':
			depth--
		case depth == 0 && matchesKeyword(expr, i, "AND"):
			parts = append(parts, expr[start:i])
			i += 2
			start = i + 1
		}
	}
	return append(parts, expr[start:])
}

// stripOuterParens removes enclosing parentheses that wrap the WHOLE expression,
// repeatedly. pg_get_constraintdef wraps a CHECK body in them, so `((qty >= 5) AND
// (qty <= 9))` has its AND at depth 1 and would never split.
//
// It must not touch `(a) AND (b)`, where the first '(' does not pair with the last
// ')' — hence the balance walk rather than a TrimPrefix/TrimSuffix pair, which
// would silently produce `a) AND (b`.
func stripOuterParens(s string) string {
	for {
		s = strings.TrimSpace(s)
		if len(s) < 2 || s[0] != '(' || s[len(s)-1] != ')' {
			return s
		}
		depth, inStr, wraps := 0, false, true
		for i := 0; i < len(s); i++ {
			switch c := s[i]; {
			case c == '\'':
				inStr = !inStr
			case inStr:
			case c == '(':
				depth++
			case c == ')':
				depth--
				if depth == 0 && i != len(s)-1 {
					wraps = false
				}
			}
			if !wraps {
				break
			}
		}
		if !wraps {
			return s
		}
		s = s[1 : len(s)-1]
	}
}

// matchesKeyword reports whether a SQL keyword starts at i and stands alone.
func matchesKeyword(s string, i int, kw string) bool {
	if i+len(kw) > len(s) || !strings.EqualFold(s[i:i+len(kw)], kw) {
		return false
	}
	if i > 0 && !isSpace(s[i-1]) && s[i-1] != ')' {
		return false
	}
	j := i + len(kw)
	return j >= len(s) || isSpace(s[j]) || s[j] == '('
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

// parseCheckAtom recognizes the two shapes worth reading off a single conjunct:
// a membership test against literals, and a comparison against a numeric literal.
func parseCheckAtom(part string) (string, checkDomain, bool) {
	s := stripOuterParens(part)

	// `col = ANY (ARRAY['a'::text, 'b'::text])` is how Postgres renders both
	// `col IN ('a','b')` and an explicit ANY, so handling it covers both spellings.
	if i := indexKeyword(s, "= ANY"); i >= 0 {
		col, ok := plainColumn(s[:i])
		if !ok {
			return "", checkDomain{}, false
		}
		vals, ok := arrayLiterals(s[i:])
		if !ok || len(vals) == 0 {
			return "", checkDomain{}, false
		}
		return col, checkDomain{Values: vals}, true
	}

	for _, op := range []string{">=", "<=", "<>", ">", "<", "="} {
		i := strings.Index(s, op)
		if i < 0 {
			continue
		}
		col, ok := plainColumn(s[:i])
		if !ok {
			return "", checkDomain{}, false
		}
		lit := strings.TrimSpace(s[i+len(op):])
		// `<>` and `=` against a single literal are not usefully generative here: `=`
		// pins every row to one value (destroying the recorded cardinality) and `<>`
		// only forbids one, which random synthesis almost never picks anyway.
		if op == "<>" || op == "=" {
			return "", checkDomain{}, false
		}
		n, ok := numericLiteral(lit)
		if !ok {
			return "", checkDomain{}, false
		}
		var dom checkDomain
		switch op {
		case ">":
			// Bounds here are INCLUSIVE, so a strict comparison is nudged inside it. The
			// step is 1 because the columns this shape guards in practice are integral
			// (counts, quantities, ids); a fractional column merely gets a slightly
			// tighter floor than required, which still satisfies the constraint.
			v := n + 1
			dom.Min = &v
		case ">=":
			v := n
			dom.Min = &v
		case "<":
			v := n - 1
			dom.Max = &v
		case "<=":
			v := n
			dom.Max = &v
		}
		return col, dom, true
	}
	return "", checkDomain{}, false
}

// indexKeyword finds a keyword at top level, case-insensitively, allowing any run
// of whitespace where the pattern has a single space.
func indexKeyword(s, kw string) int {
	fields := strings.Fields(kw)
	for i := 0; i < len(s); i++ {
		j, ok := i, true
		for k, f := range fields {
			for j < len(s) && isSpace(s[j]) {
				j++
			}
			if j+len(f) > len(s) || !strings.EqualFold(s[j:j+len(f)], f) {
				ok = false
				break
			}
			j += len(f)
			if k == 0 && i != 0 && !isSpace(s[i-1]) {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

// plainColumn extracts a bare column name from the left side of a comparison,
// tolerating the parentheses and casts pg_get_constraintdef adds — `(status)::text`
// is still the column `status`.
//
// It returns false for anything else, notably a function call or a second column,
// because a domain can only be attributed to a column the engine generates
// independently.
func plainColumn(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "::"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	s = stripOuterParens(s)
	if s == "" || strings.ContainsAny(s, " ()'\",+-*/") {
		return "", false
	}
	if strings.HasPrefix(s, `"`) {
		return "", false // quoted identifiers are not unquoted here; skip rather than guess
	}
	return s, true
}

// arrayLiterals pulls the string literals out of an `ARRAY[...]` rendering,
// dropping the per-element casts Postgres adds.
func arrayLiterals(s string) ([]string, bool) {
	open := strings.Index(s, "[")
	closeAt := strings.LastIndex(s, "]")
	if open < 0 || closeAt < open {
		return nil, false
	}
	body := s[open+1 : closeAt]

	var out []string
	var cur strings.Builder
	inStr := false
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c == '\'' && inStr && i+1 < len(body) && body[i+1] == '\'':
			cur.WriteByte('\'')
			i++
		case c == '\'':
			if inStr {
				out = append(out, cur.String())
				cur.Reset()
			}
			inStr = !inStr
		case inStr:
			cur.WriteByte(c)
		}
	}
	if inStr {
		return nil, false // unbalanced: refuse rather than return a truncated set
	}
	return out, true
}

// numericLiteral parses a trailing numeric literal, ignoring a cast.
func numericLiteral(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "::"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	s = stripOuterParens(s)
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
