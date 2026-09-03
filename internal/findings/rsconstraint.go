package findings

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/rowshape/rowshape/internal/estimate"
	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/validate"
	"github.com/rowshape/rowshape/internal/verdict"
)

func init() { validate.Register(rsConstraint{}) }

// rsConstraint detects constraint pathologies (RFC §6.4, §9.1, PRD §10):
//
//   - RS-CONSTRAINT-001: a constraint added NOT VALID and VALIDATE-d in the SAME
//     transaction — the validating O(n) scan runs while holding the lock the
//     NOT VALID split was meant to avoid, so the split buys nothing.
//   - RS-CONSTRAINT-010: a CHECK constraint that conflicts with the profiled data
//     shape (e.g. CHECK (c >= 0) on a column whose range dips below 0) — the
//     validation will fail on existing rows.
//
// Findings report the validation scan as a bucket, declare depends_on, are
// confidence-capped by the pipeline, and carry mandatory remediation.
type rsConstraint struct{}

// addInfo remembers a NOT VALID constraint add so a later VALIDATE in the same
// transaction can be recognized.
type addInfo struct {
	table string
	epoch int
	stmt  validate.Statement
}

func (rsConstraint) Analyze(f *fixture.Fixture, c *validate.Capture) []verdict.Finding {
	_, hasVersion := estimate.Major(f.Meta.Engine.Version)

	var out []verdict.Finding
	notValidAdds := map[string]addInfo{} // upper(constraint name) -> add
	epoch := 0                           // transaction epoch: increments on each COMMIT/ROLLBACK

	for i, st := range c.Statements {
		clean := collapseSpaces(stripSQLComments(st.SQL))
		upper := strings.ToUpper(clean)

		if name, table, kind, notValid, checkExpr, ok := parseAddConstraint(clean, upper); ok {
			table = resolveTable(f, table)
			if notValid {
				notValidAdds[strings.ToUpper(name)] = addInfo{table: table, epoch: epoch, stmt: st}
			}
			if kind == "CHECK" && checkExpr != "" {
				if fnd, ok := checkConflict(f, table, checkExpr); ok {
					out = append(out, fnd)
				}
			}
		}

		if vname, ok := parseValidateConstraint(upper); ok {
			if add, known := notValidAdds[strings.ToUpper(vname)]; known && add.epoch == epoch {
				out = append(out, sameTxFinding(f, c, i, add.table, vname, hasVersion))
			}
		}

		if isTxEnd(upper) {
			epoch++
		}
	}
	return out
}

// sameTxFinding reports a NOT VALID constraint validated in the same transaction
// (the VALIDATE is statement i).
func sameTxFinding(f *fixture.Fixture, c *validate.Capture, i int, table, name string, hasVersion bool) verdict.Finding {
	tbl := f.Tables[table]

	fnd := verdict.Finding{
		Code:        "RS-CONSTRAINT-001",
		Severity:    verdict.SeverityWarn,
		Title:       fmt.Sprintf("Constraint %s on %s is validated in the same transaction it is added NOT VALID", name, shortTable(table)),
		Detail:      "Adding a constraint NOT VALID and VALIDATE-ing it in one transaction still runs the full validating scan under the transaction's locks — the two-step split that avoids a long lock is defeated.",
		DependsOn:   []string{table + ".rows"},
		Remediation: remediation("RS-CONSTRAINT-001"),
		Explain:     "rowshape explain RS-CONSTRAINT-001",
	}
	fnd.Estimate = estimateFor(c, i, estimate.ConstraintValidation, table, tbl.Rows.Value, tbl.Rows.Confidence, tableKnown(f, table), hasVersion)
	return fnd
}

// checkConflict flags a CHECK constraint whose comparison conflicts with the
// column's profiled range (RFC §6.1/§6.4): CHECK (c >= K) against a range whose
// minimum is below K means existing rows violate the constraint.
func checkConflict(f *fixture.Fixture, table, expr string) (verdict.Finding, bool) {
	col, op, k, ok := parseComparison(expr)
	if !ok {
		return verdict.Finding{}, false
	}
	c, ok := f.Tables[table].Columns[col]
	if !ok || c.Range == nil {
		return verdict.Finding{}, false
	}
	lo, loOK := numeric(c.Range.Min)
	hi, hiOK := numeric(c.Range.Max)

	violated := false
	switch op {
	case ">":
		violated = loOK && lo <= k
	case ">=":
		violated = loOK && lo < k
	case "<":
		violated = hiOK && hi >= k
	case "<=":
		violated = hiOK && hi > k
	}

	dep := table + "." + col + ".range"
	if !violated {
		// NOT violated according to the recorded extremes — but if those extremes came
		// from a SAMPLE they understate the real spread, so "no conflict" may simply be
		// the sample not having seen the offending row.
		//
		// This is the case ordinary capping cannot reach. Capping caps findings that
		// EXIST; here the weak fact makes the finding NOT EXIST, and a missing finding
		// is a PASS nothing downstream can touch. It was reproduced: a column whose
		// true maximum was 60,000 was recorded from a TABLESAMPLE as 59,773, so
		// `CHECK (customer_id <= 59900)` looked satisfiable, no finding was emitted,
		// and the verdict was PASS — while the source database refused the statement
		// outright. So the ABSENCE of a conflict has to be reported when it rests on
		// sampled extremes.
		if near, ok := sampledBoundInconclusive(c.Range, op, lo, loOK, hi, hiOK); ok {
			return verdict.Finding{
				Code:     "RS-CONSTRAINT-010",
				Severity: verdict.SeverityWarn,
				Title:    fmt.Sprintf("CHECK (%s %s %s) on %s.%s cannot be confirmed from a sampled range", col, op, trimNum(k), shortTable(table), col),
				Detail: fmt.Sprintf("The column's range [%s, %s] was profiled from a SAMPLE, so it is a lower bound on the real spread, and the bound %s sits %s. Existing rows may already violate the constraint even though the recorded extremes do not. Re-profile with `rowshape pull --exact` to read the true extremes.",
					trimNum(lo), trimNum(hi), trimNum(k), near),
				Evidence:    map[string]any{"range_min": c.Range.Min, "range_max": c.Range.Max, "range_confidence": string(c.Range.Confidence), "check": expr},
				DependsOn:   []string{dep},
				Remediation: remediation("RS-CONSTRAINT-010"),
				Explain:     "rowshape explain RS-CONSTRAINT-010",
			}, true
		}
		return verdict.Finding{}, false
	}
	return verdict.Finding{
		Code:     "RS-CONSTRAINT-010",
		Severity: verdict.SeverityError,
		Title:    fmt.Sprintf("CHECK (%s %s %s) on %s.%s conflicts with existing data", col, op, trimNum(k), shortTable(table), col),
		Detail:   fmt.Sprintf("The column's profiled range [%s, %s] violates CHECK (%s %s %s); adding the constraint will fail on existing rows.", trimNum(lo), trimNum(hi), col, op, trimNum(k)),
		Evidence: map[string]any{"range_min": c.Range.Min, "range_max": c.Range.Max, "check": expr},
		// The conclusion rests on the profiled RANGE, not on the row count. It
		// used to declare `<table>.rows`, which is a fact this finding never
		// reads — false provenance in a DSSE-signed document, and it borrowed
		// that fact's confidence for a claim it does not support.
		//
		// `range` now HAS a case in verdict.factConfidence (§6.1 gained a
		// confidence — D-010 resolved), so this dependency resolves to `exact` for
		// extremes read over the whole column and `estimated` for sampled ones. It
		// does not weaken THIS finding: severity error → want FAIL, and Cap leaves
		// FAIL untouched. It is the WARN branch above, for the absence of a
		// conflict, that the confidence actually decides.
		DependsOn:   []string{dep},
		Remediation: remediation("RS-CONSTRAINT-010"),
		Explain:     "rowshape explain RS-CONSTRAINT-010",
	}, true
}

// sampledBoundInconclusive reports whether "no conflict" is a conclusion the
// recorded extremes cannot support, and describes which extreme it turned on.
//
// It fires whenever the extremes came from a SAMPLE and the comparison did not
// already find a violation, because in that situation the sample can always be
// the reason. The direction is one-way in both cases and always toward doubt:
//
//   - a sampled MINIMUM can only be too HIGH, so `col >= k` that looks satisfiable
//     may be violated by a lower row the sample never saw;
//   - a sampled MAXIMUM can only be too LOW, so `col <= k` that looks satisfiable
//     may be violated by a higher one.
//
// There is deliberately no "comfortably outside the range so it must be fine"
// escape. A sample gives no bound on how far past its extremes the real data goes,
// so any such rule would be a guess dressed as a threshold — and this is precisely
// the class of reasoning INV-CONFIDENCE-CAPPING exists to forbid. A fixture that
// wants a positive answer here can have one for the cost of `pull --exact`.
func sampledBoundInconclusive(rng *fixture.Range, op string, lo float64, loOK bool, hi float64, hiOK bool) (string, bool) {
	// An exact range is a real answer in both directions; only a sample understates.
	if rng == nil || rng.Confidence != fixture.Estimated {
		return "", false
	}
	switch op {
	case ">", ">=":
		if loOK {
			return fmt.Sprintf("below the sampled minimum %s, which a sample can only overstate", trimNum(lo)), true
		}
	case "<", "<=":
		if hiOK {
			return fmt.Sprintf("above the sampled maximum %s, which a sample can only understate", trimNum(hi)), true
		}
	}
	return "", false
}

// parseAddConstraint recognizes ALTER TABLE ... ADD CONSTRAINT <name> <kind> ...
// and returns the name, table, kind (CHECK/FK/UNIQUE/...), whether it is NOT
// VALID, and the CHECK expression (for a CHECK).
func parseAddConstraint(clean, upper string) (name, table, kind string, notValid bool, checkExpr string, ok bool) {
	if !strings.HasPrefix(upper, "ALTER TABLE") || !strings.Contains(upper, "ADD CONSTRAINT") {
		return "", "", "", false, "", false
	}
	table = alterTableTarget(clean)
	ci := strings.Index(upper, "ADD CONSTRAINT")
	rest := strings.Fields(clean[ci+len("ADD CONSTRAINT"):])
	if len(rest) == 0 {
		return "", "", "", false, "", false
	}
	name = strings.Trim(rest[0], `"`)
	notValid = strings.Contains(upper, "NOT VALID")

	switch {
	case strings.Contains(upper, "CHECK"):
		kind = "CHECK"
		checkExpr = parenAfter(clean, "CHECK")
	case strings.Contains(upper, "FOREIGN KEY"):
		kind = "FK"
	case strings.Contains(upper, "UNIQUE"):
		kind = "UNIQUE"
	case strings.Contains(upper, "EXCLUDE"):
		kind = "EXCLUDE"
	default:
		kind = "OTHER"
	}
	if table == "" {
		return "", "", "", false, "", false
	}
	return name, table, kind, notValid, checkExpr, true
}

// parseComparison extracts "<col> <op> <number>" from a CHECK body, op one of
// >, >=, <, <=. It handles a leading/trailing set of parentheses and spacing.
func parseComparison(expr string) (col, op string, k float64, ok bool) {
	e := strings.TrimSpace(strings.Trim(strings.TrimSpace(expr), "()"))
	// Longest operators first.
	for _, o := range []string{">=", "<=", ">", "<"} {
		if i := strings.Index(e, o); i >= 0 {
			left := strings.TrimSpace(e[:i])
			right := strings.TrimSpace(e[i+len(o):])
			n, err := strconv.ParseFloat(strings.Fields(right + " ")[0], 64)
			if err != nil {
				return "", "", 0, false
			}
			col = strings.Trim(lastField(left), `"`)
			if col == "" {
				return "", "", 0, false
			}
			return col, o, n, true
		}
	}
	return "", "", 0, false
}

// parenAfter returns the content of the first balanced parenthesized group
// following keyword (case-insensitive), e.g. after "CHECK" -> "amount_cents > 0".
func parenAfter(s, keyword string) string {
	up := strings.ToUpper(s)
	i := strings.Index(up, strings.ToUpper(keyword))
	if i < 0 {
		return ""
	}
	open := strings.IndexByte(s[i:], '(')
	if open < 0 {
		return ""
	}
	open += i
	depth := 0
	for j := open; j < len(s); j++ {
		switch s[j] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return strings.TrimSpace(s[open+1 : j])
			}
		}
	}
	return ""
}

// isTxEnd reports whether a statement ends a transaction (COMMIT / ROLLBACK / END).
func isTxEnd(upper string) bool {
	return strings.HasPrefix(upper, "COMMIT") || strings.HasPrefix(upper, "ROLLBACK") || strings.HasPrefix(upper, "END")
}

// numeric coerces a YAML-decoded range bound to a float, if it is a number.
func numeric(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	case string:
		f, err := strconv.ParseFloat(n, 64)
		return f, err == nil
	}
	return 0, false
}

func lastField(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return f[len(f)-1]
}

// trimNum renders a float without a trailing ".0" for whole numbers.
func trimNum(f float64) string {
	if f == float64(int64(f)) {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}
