package findings

import (
	"fmt"
	"strings"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/sqlkind"
	"github.com/rowshape/rowshape/internal/validate"
	"github.com/rowshape/rowshape/internal/verdict"
)

func init() { validate.Register(rsReverse{}) }

// rsReverse detects reversibility hazards — migrations whose DOWN-migration would
// lose data or could not execute (PRD §10 RS-REVERSE namespace):
//
//   - RS-REVERSE-001: DROP COLUMN permanently loses the column's data; a rollback
//     can recreate the column but not its rows.
//   - RS-REVERSE-002: DROP TABLE permanently loses every row; a rollback cannot
//     restore them.
//   - RS-REVERSE-003: a narrowing column type change truncates values and cannot
//     be reversed without the original data.
//
// Each finding declares depends_on (the table's rows — what is lost) and carries
// mandatory remediation, and is confidence-capped like every other class.
type rsReverse struct{}

func (rsReverse) Analyze(f *fixture.Fixture, c *validate.Capture) []verdict.Finding {
	var out []verdict.Finding
	for _, st := range c.Statements {
		clean := collapseSpaces(stripSQLComments(st.SQL))
		upper := strings.ToUpper(clean)

		switch {
		case strings.HasPrefix(upper, "DROP TABLE"):
			out = append(out, dropTableFinding(f, clean, upper))
		case strings.HasPrefix(upper, "TRUNCATE"):
			// TRUNCATE reached only rsperf's deleteTarget, which exists solely to
			// look for long-tailed CASCADING children — so a TRUNCATE with no
			// cascading child produced ZERO findings and the verdict was PASS.
			// Total irreversible data loss under ACCESS EXCLUSIVE, certified
			// clean, and it succeeds against the hydrated table too, so the
			// apply-failure floor never fires either.
			out = append(out, truncateFinding(f, clean, upper))
		case strings.HasPrefix(upper, "ALTER TABLE") && strings.Contains(upper, "DROP COLUMN"):
			if fnd, ok := dropColumnFinding(f, clean, upper); ok {
				out = append(out, fnd)
			}
		case strings.HasPrefix(upper, "ALTER TABLE") && (strings.Contains(upper, " TYPE ") || strings.Contains(upper, "SET DATA TYPE")):
			if fnd, ok := narrowTypeFinding(f, clean, upper); ok {
				out = append(out, fnd)
			}
		}
	}
	return out
}

func dropTableFinding(f *fixture.Fixture, clean, upper string) verdict.Finding {
	// Resolve like its two siblings do (RFC §5). Unresolved, `DROP TABLE users`
	// missed the fixture key `public.users`, so rows read 0 — the finding then
	// announced "all 0 rows are lost" for a table that may hold millions, and
	// cited a depends_on path that resolves to nothing in a signed document.
	table := resolveTable(f, dropTableTarget(clean, upper))
	rows := f.Tables[table].Rows.Value
	return verdict.Finding{
		Code:     "RS-REVERSE-002",
		Severity: verdict.SeverityWarn,
		Title:    fmt.Sprintf("DROP TABLE %s is irreversible: all %s rows are lost", shortTable(table), humanCount(rows)),
		Detail: "Dropping a table permanently removes every row; a down-migration can recreate the table but " +
			"not its data. " + irreversibleGateNote,
		Evidence:    map[string]any{"rows": rows},
		DependsOn:   []string{table + ".rows"},
		Remediation: remediation("RS-REVERSE-002"),
		Explain:     "rowshape explain RS-REVERSE-002",
	}
}

// truncateFinding reports the irreversibility and lock cost of a TRUNCATE.
//
// Sized from the fixture's DECLARED rows rather than anything observed: running
// TRUNCATE against a hydrated table says nothing about how much production data
// it would destroy, which is precisely why executing the migration cannot
// surface this hazard and a static rule must.
func truncateFinding(f *fixture.Fixture, clean, upper string) verdict.Finding {
	table := resolveTable(f, truncateTarget(clean, upper))
	rows := f.Tables[table].Rows.Value
	cascade := strings.Contains(upper, "CASCADE")

	detail := "TRUNCATE removes every row and cannot be rolled back once committed. It holds ACCESS EXCLUSIVE for its duration, blocking all reads and writes, and does not fire per-row DELETE triggers."
	if cascade {
		detail += " CASCADE additionally empties every table with a foreign key into this one."
	}
	detail += " " + irreversibleGateNote
	ev := map[string]any{"rows": rows, "cascade": cascade}

	return verdict.Finding{
		Code:        "RS-REVERSE-004",
		Severity:    verdict.SeverityWarn,
		Title:       fmt.Sprintf("TRUNCATE %s is irreversible: all %s rows are lost", shortTable(table), humanCount(rows)),
		Detail:      detail,
		Evidence:    ev,
		DependsOn:   []string{table + ".rows"},
		Remediation: remediation("RS-REVERSE-004"),
		Explain:     "rowshape explain RS-REVERSE-004",
	}
}

// truncateTarget extracts the first table named by a TRUNCATE. The optional
// TABLE keyword and the ONLY qualifier are both stripped, matching
// alterTableTarget's handling.
func truncateTarget(clean, upper string) string {
	rest := strings.TrimSpace(clean[len("TRUNCATE"):])
	restUp := strings.ToUpper(rest)
	for _, kw := range []string{"TABLE ", "ONLY "} {
		if strings.HasPrefix(restUp, kw) {
			rest = strings.TrimSpace(rest[len(kw):])
			restUp = strings.ToUpper(rest)
		}
	}
	// TRUNCATE accepts a comma-separated list; the first name is enough to size
	// and locate the finding.
	if i := strings.IndexAny(rest, " ,;"); i > 0 {
		rest = rest[:i]
	}
	return strings.Trim(rest, `";`)
}

func dropColumnFinding(f *fixture.Fixture, clean, upper string) (verdict.Finding, bool) {
	table := resolveTable(f, alterTableTarget(clean))
	col := identAfter(clean, upper, "DROP COLUMN")
	if table == "" || col == "" {
		return verdict.Finding{}, false
	}
	rows := f.Tables[table].Rows.Value
	return verdict.Finding{
		Code:     "RS-REVERSE-001",
		Severity: verdict.SeverityWarn,
		Title:    fmt.Sprintf("DROP COLUMN %s.%s loses its data irreversibly", shortTable(table), col),
		Detail: "Dropping a column permanently removes its values across all rows; a down-migration can " +
			"recreate the column but not what it held. " + irreversibleGateNote,
		Evidence:    map[string]any{"rows": rows},
		DependsOn:   []string{table + ".rows"},
		Remediation: remediation("RS-REVERSE-001"),
		Explain:     "rowshape explain RS-REVERSE-001",
	}, true
}

func narrowTypeFinding(f *fixture.Fixture, clean, upper string) (verdict.Finding, bool) {
	table := resolveTable(f, alterTableTarget(clean))
	col := columnBeforeTypeChange(clean, upper)
	newType := typeAfter(clean, upper)
	if table == "" || col == "" || newType == "" {
		return verdict.Finding{}, false
	}
	c, ok := f.Tables[table].Columns[col]
	if !ok || !isNarrowing(c.Type, newType) {
		return verdict.Finding{}, false
	}
	return verdict.Finding{
		Code:        "RS-REVERSE-003",
		Severity:    verdict.SeverityWarn,
		Title:       fmt.Sprintf("Narrowing %s.%s from %s to %s can truncate data irreversibly", shortTable(table), col, c.Type, newType),
		Detail:      "Narrowing a column's type can truncate or lose values, and cannot be reversed without the original data.",
		Evidence:    map[string]any{"from": c.Type, "to": newType},
		DependsOn:   []string{table + ".rows"},
		Remediation: remediation("RS-REVERSE-003"),
		Explain:     "rowshape explain RS-REVERSE-003",
	}, true
}

// dropTableTarget extracts the table from DROP TABLE [IF EXISTS] <table>.
func dropTableTarget(clean, upper string) string {
	fields := strings.Fields(clean)
	i := 2 // DROP TABLE
	if i < len(fields) && strings.EqualFold(fields[i], "IF") {
		i += 2 // IF EXISTS
	}
	if i < len(fields) {
		return strings.Trim(strings.TrimRight(fields[i], ";"), `"`)
	}
	return ""
}

// identAfter returns the identifier following a keyword (case-insensitive).
func identAfter(clean, upper, keyword string) string {
	i := strings.Index(upper, strings.ToUpper(keyword))
	if i < 0 {
		return ""
	}
	fields := strings.Fields(clean[i+len(keyword):])
	// Skip the optional IF EXISTS / IF NOT EXISTS that Postgres allows between
	// the keyword and the identifier. Without this, `DROP COLUMN IF EXISTS c`
	// returned "IF" as the column name, so the finding described a column that
	// does not exist — wrong evidence on a DSSE-signed document, even where the
	// reversibility conclusion happens to stay correct.
	fields = skipIfExists(fields)
	if len(fields) == 0 {
		return ""
	}
	return strings.Trim(strings.TrimRight(fields[0], ";,"), `"`)
}

// skipIfExists drops a leading IF EXISTS or IF NOT EXISTS, case-insensitively
// and whatever the spacing, since strings.Fields has already collapsed it.
func skipIfExists(fields []string) []string {
	if len(fields) >= 2 && strings.EqualFold(fields[0], "IF") {
		switch {
		case strings.EqualFold(fields[1], "EXISTS"):
			return fields[2:]
		case len(fields) >= 3 && strings.EqualFold(fields[1], "NOT") && strings.EqualFold(fields[2], "EXISTS"):
			return fields[3:]
		}
	}
	return fields
}

// columnBeforeTypeChange returns the column of an ALTER COLUMN ... TYPE clause.
func columnBeforeTypeChange(clean, upper string) string {
	i := strings.Index(upper, " TYPE ")
	if i < 0 {
		if j := strings.Index(upper, "SET DATA TYPE"); j >= 0 {
			i = j
		} else {
			return ""
		}
	}
	fields := strings.Fields(clean[:i])
	if len(fields) == 0 {
		return ""
	}
	return strings.Trim(fields[len(fields)-1], `"`)
}

// typeAfter returns the target type of a TYPE / SET DATA TYPE clause.
func typeAfter(clean, upper string) string {
	key := " TYPE "
	i := strings.Index(upper, key)
	if i < 0 {
		if j := strings.Index(upper, "SET DATA TYPE"); j >= 0 {
			i, key = j, "SET DATA TYPE"
		} else {
			return ""
		}
	}
	rest := strings.TrimSpace(clean[i+len(key):])
	// Cut at the next clause boundary (USING, ;, ,).
	for _, stop := range []string{" USING", ";", ","} {
		if k := strings.Index(strings.ToUpper(rest), strings.ToUpper(stop)); k >= 0 {
			rest = rest[:k]
		}
	}
	return strings.TrimSpace(rest)
}

// intRank orders integer types by width for narrowing detection.
var intRank = map[string]int{
	"smallint": 1, "int2": 1,
	"integer": 2, "int": 2, "int4": 2,
	"bigint": 3, "int8": 3,
}

// isNarrowing reports whether changing oldType to newType can lose data: an
// integer narrowing, a string whose length cap shrinks (or gains one), or a
// numeric/float to an integer.
func isNarrowing(oldType, newType string) bool {
	o := strings.ToLower(strings.TrimSpace(baseSQLType(oldType)))
	n := strings.ToLower(strings.TrimSpace(baseSQLType(newType)))

	if ro, ok := intRank[o]; ok {
		if rn, ok := intRank[n]; ok {
			return rn < ro
		}
	}
	if isBoundedStringBase(o) {
		// A string change truncates only when the NEW type imposes a cap the old
		// values could exceed. Comparing lengths — not merely "the new type has a
		// modifier" — is what separates a truncating shrink from a harmless widen:
		//
		//   varchar(100) -> varchar(200)   widen, loses nothing        (not flagged)
		//   varchar(200) -> varchar(100)   shrink, truncates           (flagged)
		//   text         -> varchar(255)   gains a cap, can truncate    (flagged)
		//   varchar(100) -> text           drops the cap, widens        (not flagged)
		newLen, newBounded := typeLength(newType)
		if !newBounded {
			return false // widening to an unbounded type never truncates
		}
		oldLen, oldBounded := typeLength(oldType)
		if !oldBounded {
			return true // unbounded old (text / unadorned varchar) -> a cap can truncate
		}
		return newLen < oldLen // both capped: only a smaller cap truncates
	}
	if (o == "numeric" || o == "decimal" || o == "double precision" || o == "real") && (n == "integer" || n == "bigint" || n == "smallint" || n == "int") {
		return true
	}
	return false
}

// The character-type readers below are thin aliases over internal/sqlkind, which
// is the single home for this parsing now that internal/hydrate needs the same
// reading of the same type strings (it must keep a synthesized value inside the
// column it is COPYed into). The aliases stay so the analyzer code reads as it
// did; the parsing itself has exactly one implementation.

// isBoundedStringBase reports whether a base type is a character string type that
// can carry a length modifier.
func isBoundedStringBase(base string) bool { return sqlkind.IsBoundedStringBase(base) }

// typeLength extracts the length modifier from a character type: typeLength(
// "varchar(200)") is (200, true); typeLength("text") is (0, false). Only the
// first modifier is read, so a stray precision list cannot mislead it.
func typeLength(t string) (int, bool) { return sqlkind.TypeLength(t) }

// baseSQLType strips a type's length/precision modifier ("varchar(255)" ->
// "varchar"), also lowercasing and trimming it.
func baseSQLType(t string) string { return sqlkind.BaseType(t) }

// irreversibleGateNote is appended to findings about IRREVERSIBLE data loss.
//
// These are SeverityWarn, and the Action's default is warn-as-fail:false — so
// the default configuration of the default surface lets irreversible data loss
// merge without blocking. That may well be the right default (a WARN that
// blocks would make the tool unusable on legitimate teardown migrations), but it
// must not be a surprise. Whether the severity itself should change is a product
// decision; saying plainly what the current default does is not.
const irreversibleGateNote = "Note: this is a WARN, and the GitHub Action does not block on WARN unless " +
	"warn-as-fail is set — so with the default configuration this will merge."
