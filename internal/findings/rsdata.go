package findings

import (
	"fmt"
	"strings"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/validate"
	"github.com/rowshape/rowshape/internal/verdict"
)

func init() { validate.Register(rsData{}) }

// rsData detects data-shape pathologies whose safety depends on what the data
// actually contains (RFC §7.4, §6.6, PRD §10):
//
//   - ADD CONSTRAINT UNIQUE on a column whose uniqueness is not PROVEN exact —
//     capped to a resolving WARN, never PASS (RS-DATA-014, INV-UNIQUENESS).
//   - SET NOT NULL on a column with a nonzero null_fraction — the migration will
//     fail on the existing NULLs (RS-DATA-001); when the zero is only estimated,
//     capping declines to certify (WARN).
//   - VALIDATE of a FOREIGN KEY against a nonzero orphan_fraction — the validation
//     scan trips on pre-existing orphans (RS-DATA-020).
//
// Every finding declares depends_on and is confidence-capped by the pipeline.
type rsData struct{}

func (rsData) Analyze(f *fixture.Fixture, c *validate.Capture) []verdict.Finding {
	var out []verdict.Finding
	fkCols := map[string]fkRef{} // constraint name -> (table, column) for VALIDATE

	for _, st := range c.Statements {
		// One collapsed, comment-free rendering so upper and clean share offsets.
		clean := collapseSpaces(stripSQLComments(st.SQL))
		upper := strings.ToUpper(clean)

		// Independent ifs, not a switch. One ALTER TABLE can legitimately do both
		// — `ADD CONSTRAINT u UNIQUE (c), ALTER COLUMN c SET NOT NULL` — and a
		// switch reported only the first, silently dropping a real finding about
		// the second. (The "never double-flag" note below is about UNIQUE vs the
		// RS-INDEX family, which is a different concern and still holds.)
		if strings.Contains(upper, "UNIQUE") && strings.Contains(upper, "ADD") {
			// ADD CONSTRAINT UNIQUE (the constraint form). CREATE UNIQUE INDEX is
			// RS-INDEX's, so the two families never double-flag one statement.
			if fnd, ok := uniqueFinding(f, clean, upper); ok {
				out = append(out, fnd)
			}
		}
		if strings.Contains(upper, "SET NOT NULL") {
			if fnd, ok := notNullFinding(f, clean, upper); ok {
				out = append(out, fnd)
			}
		}

		// Track FK constraints so a later VALIDATE can resolve its column, and
		// check an immediately-validated FK at ADD time. Names are keyed
		// upper-cased so ADD and VALIDATE match regardless of quoting/case.
		if name, ref, ok := parseAddForeignKey(clean, upper); ok {
			ref.table = resolveTable(f, ref.table)
			fkCols[strings.ToUpper(name)] = ref
			if !strings.Contains(upper, "NOT VALID") {
				if fnd, ok := orphanFinding(f, ref); ok {
					out = append(out, fnd)
				}
			}
		}
		if name, ok := parseValidateConstraint(upper); ok {
			if ref, known := fkCols[strings.ToUpper(name)]; known {
				if fnd, ok := orphanFinding(f, ref); ok {
					out = append(out, fnd)
				}
			}
		}
	}
	return out
}

// uniqueFinding certifies (or declines to certify) an ADD UNIQUE. Uniqueness is
// PROVEN exact or it is not certified at all — a sample never establishes it
// (INV-UNIQUENESS). The finding wants PASS and rests on the column's `unique`
// fact; capping downgrades it to a resolving WARN when uniqueness is unproven.
func uniqueFinding(f *fixture.Fixture, sql, upper string) (verdict.Finding, bool) {
	table := resolveTable(f, alterTableTarget(sql))
	cols := colsAfter(sql, "UNIQUE")
	if table == "" || len(cols) == 0 {
		return verdict.Finding{}, false
	}
	dep, target := uniqueDependency(table, cols)

	if uniquenessState(f, table, cols) == uniqViolated {
		// Uniqueness is proven FALSE (exact): the constraint cannot be created.
		return verdict.Finding{
			Code:        "RS-DATA-014",
			Severity:    verdict.SeverityError,
			Title:       fmt.Sprintf("ADD UNIQUE on %s will fail: the column has duplicate values", target),
			Detail:      fmt.Sprintf("%s is proven non-unique (unique=false, exact); ADD CONSTRAINT UNIQUE cannot build.", target),
			Evidence:    map[string]any{"unique": false},
			DependsOn:   []string{dep},
			Remediation: remediation("RS-DATA-014"),
			Explain:     "rowshape explain RS-DATA-014",
		}, true
	}
	// Proven-unique certifies PASS; unproven is capped to a resolving WARN.
	return verdict.Finding{
		Code:        "RS-DATA-014",
		Severity:    verdict.SeverityInfo,
		Title:       fmt.Sprintf("Uniqueness of %s not confirmed for ADD UNIQUE", target),
		Detail:      fmt.Sprintf("ADD CONSTRAINT UNIQUE on %s can only PASS if uniqueness is proven exact; a sample never establishes it (INV-UNIQUENESS).", target),
		DependsOn:   []string{dep},
		Remediation: remediation("RS-DATA-014"),
		Explain:     "rowshape explain RS-DATA-014",
	}, true
}

// uniqState classifies whether a column can carry a UNIQUE constraint/index.
type uniqState int

const (
	uniqProven   uniqState = iota // unique=true, exact → safe (PASS)
	uniqViolated                  // unique=false, exact → duplicates exist (FAIL)
	uniqUnproven                  // unique absent → cannot certify (WARN via capping)
)

// uniquenessState reads the profiled uniqueness of a single-column key. `unique`
// is exact or absent (INV-UNIQUENESS): present-true is proven, present-false is a
// proven violation, absent is unproven. A composite key cannot be certified from
// per-column stats, so it is always unproven.
func uniquenessState(f *fixture.Fixture, table string, cols []string) uniqState {
	if len(cols) != 1 {
		return uniqUnproven
	}
	c, ok := f.Tables[table].Columns[cols[0]]
	if !ok || c.Unique == nil {
		return uniqUnproven
	}
	if c.Unique.Value {
		return uniqProven
	}
	return uniqViolated
}

// notNullFinding flags a SET NOT NULL. A nonzero null_fraction means the
// migration will fail on the existing NULLs (an error/FAIL when known, a WARN
// when only sampled). A zero null_fraction wants PASS and is capped: an exact
// zero certifies, an estimated zero declines (WARN).
func notNullFinding(f *fixture.Fixture, sql, upper string) (verdict.Finding, bool) {
	table := resolveTable(f, alterTableTarget(sql))
	col := columnBeforeSetNotNull(sql, upper)
	if table == "" || col == "" {
		return verdict.Finding{}, false
	}
	dep := table + "." + col + ".null_fraction"
	nf := nullFraction(f, table, col)

	if nf != nil && nf.Value > 0 {
		sev := verdict.SeverityWarn
		verb := "likely fails"
		if nf.Confidence.AtLeast(fixture.Measured) {
			sev = verdict.SeverityError
			verb = "will fail"
		}
		return verdict.Finding{
			Code:        "RS-DATA-001",
			Severity:    sev,
			Title:       fmt.Sprintf("SET NOT NULL on %s.%s %s: %.2g%% of rows are NULL", shortTable(table), col, verb, nf.Value*100),
			Detail:      fmt.Sprintf("null_fraction is %.4g (%s); SET NOT NULL rejects the existing NULL rows.", nf.Value, nf.Confidence),
			Evidence:    map[string]any{"null_fraction": nf.Value},
			DependsOn:   []string{dep},
			Remediation: remediation("RS-DATA-001"),
			Explain:     "rowshape explain RS-DATA-001",
		}, true
	}

	// null_fraction is zero (or absent): certify PASS, let capping decide whether
	// the fact is strong enough (exact) or must WARN (estimated/absent).
	return verdict.Finding{
		Code:        "RS-DATA-001",
		Severity:    verdict.SeverityInfo,
		Title:       fmt.Sprintf("SET NOT NULL on %s.%s not confirmed safe", shortTable(table), col),
		Detail:      "SET NOT NULL can only PASS if the column is proven to contain no NULLs.",
		DependsOn:   []string{dep},
		Remediation: remediation("RS-DATA-001"),
		Explain:     "rowshape explain RS-DATA-001",
	}, true
}

// orphanFinding flags validating a FOREIGN KEY against pre-existing orphans. A
// nonzero orphan_fraction means the VALIDATE scan will fail (RFC §6.6). A zero
// orphan_fraction wants PASS and is capped by its confidence.
func orphanFinding(f *fixture.Fixture, ref fkRef) (verdict.Finding, bool) {
	orphan := orphanFraction(f, ref.table, ref.column)
	dep := ref.table + "." + ref.column + ".orphan_fraction"

	if orphan != nil && orphan.Value > 0 {
		sev := verdict.SeverityWarn
		verb := "may fail"
		if orphan.Confidence.AtLeast(fixture.Measured) {
			sev = verdict.SeverityError
			verb = "will fail"
		}
		return verdict.Finding{
			Code:        "RS-DATA-020",
			Severity:    sev,
			Title:       fmt.Sprintf("VALIDATE of FK on %s.%s %s: %.2g%% of rows are orphaned", shortTable(ref.table), ref.column, verb, orphan.Value*100),
			Detail:      fmt.Sprintf("orphan_fraction is %.4g (%s); the FK validation scan trips on rows with no matching parent.", orphan.Value, orphan.Confidence),
			Evidence:    map[string]any{"orphan_fraction": orphan.Value},
			DependsOn:   []string{dep},
			Remediation: remediation("RS-DATA-020"),
			Explain:     "rowshape explain RS-DATA-020",
		}, true
	}

	return verdict.Finding{
		Code:        "RS-DATA-020",
		Severity:    verdict.SeverityInfo,
		Title:       fmt.Sprintf("FK validation on %s.%s not confirmed safe", shortTable(ref.table), ref.column),
		Detail:      "Validating the FK can only PASS if no orphaned rows are proven to exist.",
		DependsOn:   []string{dep},
		Remediation: remediation("RS-DATA-020"),
		Explain:     "rowshape explain RS-DATA-020",
	}, true
}

// fkRef is a foreign key's local table and column.
type fkRef struct{ table, column string }

// uniqueDependency returns the capping dependency and escalation target for an
// ADD UNIQUE. A single-column constraint rests on that column's `unique` fact; a
// composite one rests on a tuple key the fixture cannot carry, so it stays
// unresolved (WARN) — composite uniqueness is never certified from column stats.
func uniqueDependency(table string, cols []string) (dep, target string) {
	if len(cols) == 1 {
		return table + "." + cols[0] + ".unique", table + "." + cols[0]
	}
	return table + ".(" + strings.Join(cols, ",") + ").unique", table + " (" + strings.Join(cols, ", ") + ")"
}

func nullFraction(f *fixture.Fixture, table, col string) *fixture.Fact[float64] {
	if c, ok := f.Tables[table].Columns[col]; ok {
		return c.NullFraction
	}
	return nil
}

func orphanFraction(f *fixture.Fixture, table, col string) *fixture.Fact[float64] {
	for _, ref := range f.Tables[table].References {
		if ref.Column == col {
			return ref.OrphanFraction
		}
	}
	return nil
}

// colsAfter returns the column list of the first parenthesized group following
// keyword (case-insensitive), e.g. after "UNIQUE" -> ["email"].
func colsAfter(sql, keyword string) []string {
	up := strings.ToUpper(sql)
	i := strings.Index(up, strings.ToUpper(keyword))
	if i < 0 {
		return nil
	}
	open := strings.IndexByte(sql[i:], '(')
	if open < 0 {
		return nil
	}
	open += i
	close := strings.IndexByte(sql[open:], ')')
	if close < 0 {
		return nil
	}
	inner := sql[open+1 : open+close]
	var cols []string
	for _, c := range strings.Split(inner, ",") {
		c = strings.TrimSpace(strings.Trim(strings.TrimSpace(c), `"`))
		// Drop any opclass/sort suffix (e.g. "email text_pattern_ops").
		if sp := strings.IndexByte(c, ' '); sp >= 0 {
			c = c[:sp]
		}
		if c != "" {
			cols = append(cols, c)
		}
	}
	return cols
}

// columnBeforeSetNotNull returns the column of a SET NOT NULL clause: the token
// immediately before "SET NOT NULL". Works with or without the optional COLUMN
// keyword ("ALTER COLUMN c SET NOT NULL" and "ALTER c SET NOT NULL").
func columnBeforeSetNotNull(sql, upper string) string {
	i := strings.Index(upper, "SET NOT NULL")
	if i < 0 {
		return ""
	}
	fields := strings.Fields(sql[:i])
	if len(fields) == 0 {
		return ""
	}
	return strings.Trim(fields[len(fields)-1], `"`)
}

// parseAddForeignKey recognizes ADD [CONSTRAINT <name>] FOREIGN KEY (<col>) and
// returns the constraint name (may be "") plus the local table/column.
func parseAddForeignKey(sql, upper string) (name string, ref fkRef, ok bool) {
	if !strings.HasPrefix(upper, "ALTER TABLE") || !strings.Contains(upper, "FOREIGN KEY") {
		return "", fkRef{}, false
	}
	table := alterTableTarget(sql)
	if ci := strings.Index(upper, "ADD CONSTRAINT"); ci >= 0 {
		rest := strings.Fields(sql[ci+len("ADD CONSTRAINT"):])
		if len(rest) > 0 {
			name = strings.Trim(rest[0], `"`)
		}
	}
	cols := colsAfter(sql, "FOREIGN KEY")
	if table == "" || len(cols) == 0 {
		return "", fkRef{}, false
	}
	return name, fkRef{table: table, column: cols[0]}, true
}

// parseValidateConstraint recognizes ALTER TABLE ... VALIDATE CONSTRAINT <name>.
func parseValidateConstraint(upper string) (name string, ok bool) {
	i := strings.Index(upper, "VALIDATE CONSTRAINT")
	if i < 0 {
		return "", false
	}
	rest := strings.Fields(upper[i+len("VALIDATE CONSTRAINT"):])
	if len(rest) == 0 {
		return "", false
	}
	// Return the lower-cased name (matched against the ADD CONSTRAINT name, which
	// we also upper-cased when scanning). Normalize both to the raw token.
	return strings.Trim(strings.TrimRight(rest[0], ";"), `"`), true
}
