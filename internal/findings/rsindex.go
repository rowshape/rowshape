package findings

import (
	"fmt"
	"strings"

	"github.com/rowshape/rowshape/internal/estimate"
	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/validate"
	"github.com/rowshape/rowshape/internal/verdict"
)

func init() { validate.Register(rsIndex{}) }

// rsIndex detects index pathologies (RFC §6.5, §9.1, PRD §10):
//
//   - RS-INDEX-001: a non-concurrent CREATE INDEX takes a lock that blocks writes
//     for the whole O(n log n) build; recommend CREATE INDEX CONCURRENTLY.
//   - RS-INDEX-010: a CREATE UNIQUE INDEX on a column whose profiled uniqueness is
//     violated (proven duplicates) cannot build; when uniqueness is merely
//     unproven, capping declines to certify (WARN).
//   - RS-INDEX-020: a non-concurrent REINDEX rebuilds under lock; its duration is
//     bucketed from the index's on-disk bytes/bloat (RFC §6.5).
type rsIndex struct{}

func (rsIndex) Analyze(f *fixture.Fixture, c *validate.Capture) []verdict.Finding {
	_, hasVersion := estimate.Major(f.Meta.Engine.Version)

	var out []verdict.Finding
	for i, st := range c.Statements {
		clean := collapseSpaces(stripSQLComments(st.SQL))
		upper := strings.ToUpper(clean)

		if ix, ok := parseCreateIndex(clean, upper); ok {
			// Resolve at the caller (RFC §5), as every other analyzer does. Without
			// it `CREATE INDEX ON orders (...)` missed the fixture key
			// `public.orders`, and the zero-value table meant rows=0 — reported as
			// an `instant` build for what may be a huge table.
			ix.table = resolveTable(f, ix.table)
			if !ix.concurrent {
				out = append(out, nonConcurrentFinding(f, c, i, ix.table, hasVersion))
			}
			if ix.unique {
				out = append(out, indexUniqueFinding(f, ix))
			}
			continue
		}
		if table, kind, ok := parseAddIndexConstraint(clean, upper); ok {
			out = append(out, addIndexConstraintFinding(f, c, i, resolveTable(f, table), kind, hasVersion))
			continue
		}
		if name, scope, concurrent, ok := parseReindex(clean, upper); ok && !concurrent {
			// Only REINDEX TABLE names a table key to resolve. REINDEX INDEX names an
			// index (matched by index name), and SCHEMA/DATABASE name a schema/db —
			// none of those are fixture table keys.
			if scope == reindexTable {
				name = resolveTable(f, name)
			}
			if fnd, ok := reindexFinding(f, name, scope, hasVersion); ok {
				out = append(out, fnd)
			}
		}
	}
	return out
}

// nonConcurrentFinding flags a plain CREATE INDEX (statement i) and recommends
// CONCURRENTLY.
func nonConcurrentFinding(f *fixture.Fixture, c *validate.Capture, i int, table string, hasVersion bool) verdict.Finding {
	tbl := f.Tables[table]
	fnd := verdict.Finding{
		Code:        "RS-INDEX-001",
		Severity:    verdict.SeverityWarn,
		Title:       fmt.Sprintf("Non-concurrent CREATE INDEX on %s locks out writes during the build", shortTable(table)),
		Detail:      "A plain CREATE INDEX holds a SHARE lock that blocks writes for the whole build.",
		DependsOn:   []string{table + ".rows"},
		Remediation: remediation("RS-INDEX-001"),
		Explain:     "rowshape explain RS-INDEX-001",
	}
	fnd.Estimate = estimateFor(c, i, estimate.BTreeBuild, table, tbl.Rows.Value, tbl.Rows.Confidence, tableKnown(f, table), hasVersion)
	return fnd
}

// addIndexConstraintFinding flags an ALTER TABLE ... ADD PRIMARY KEY / ADD UNIQUE
// (statement i). Both build a unique index while holding ACCESS EXCLUSIVE — a
// stronger lock than a plain CREATE INDEX's SHARE (reads block too) — at the same
// O(n log n) cost, so the duration uses the same B-tree model.
//
// Nothing flagged the lock before: rsLock excludes ADD PRIMARY / ADD UNIQUE,
// rsConstraint files PRIMARY KEY under OTHER, and rsData's RS-DATA-014 speaks to
// whether an ADD UNIQUE will BUILD (uniqueness of the data), not to the lock it
// takes while building. The two are distinct hazards on the same statement: a
// proven-unique ADD UNIQUE still locks the table for the whole build.
func addIndexConstraintFinding(f *fixture.Fixture, c *validate.Capture, i int, table, kind string, hasVersion bool) verdict.Finding {
	tbl := f.Tables[table]
	label := "ADD PRIMARY KEY"
	scans := "scans the column for NULLs and builds"
	if kind == "UNIQUE" {
		label, scans = "ADD UNIQUE constraint", "builds"
	}
	fnd := verdict.Finding{
		Code:        "RS-INDEX-002",
		Severity:    verdict.SeverityWarn,
		Title:       fmt.Sprintf("%s on %s builds a unique index under ACCESS EXCLUSIVE, blocking reads and writes", label, shortTable(table)),
		Detail:      fmt.Sprintf("ALTER TABLE ... %s %s a unique index while holding an ACCESS EXCLUSIVE lock, so neither reads nor writes proceed until it finishes.", label, scans),
		Evidence:    map[string]any{"lock_mode": "ACCESS EXCLUSIVE", "constraint": kind},
		DependsOn:   []string{table + ".rows"},
		Remediation: remediation("RS-INDEX-002"),
		Explain:     "rowshape explain RS-INDEX-002",
	}
	fnd.Estimate = estimateFor(c, i, estimate.BTreeBuild, table, tbl.Rows.Value, tbl.Rows.Confidence, tableKnown(f, table), hasVersion)
	return fnd
}

// parseAddIndexConstraint recognizes the TABLE-CONSTRAINT forms
// `ALTER TABLE <t> ADD [CONSTRAINT <name>] {PRIMARY KEY|UNIQUE} (<cols>)`, which
// build an index over EXISTING data under ACCESS EXCLUSIVE. It returns the target
// table and the constraint kind ("PRIMARY KEY" or "UNIQUE").
//
// It deliberately does NOT match:
//   - the COLUMN form `ADD COLUMN c type PRIMARY KEY` (a new column);
//   - the adopt form `ADD PRIMARY KEY USING INDEX i` / `ADD UNIQUE USING INDEX i`
//     — a prebuilt index attached cheaply, which is exactly what this finding's
//     remediation recommends; a check that fired on its own fix would train people
//     to switch it off. Both are excluded because they carry no `(col)` list.
func parseAddIndexConstraint(clean, upper string) (table, kind string, ok bool) {
	if !strings.HasPrefix(upper, "ALTER TABLE") || !strings.Contains(upper, " ADD ") {
		return "", "", false
	}
	if strings.Contains(upper, "ADD COLUMN") {
		return "", "", false
	}
	for _, k := range []string{"PRIMARY KEY", "UNIQUE"} {
		if keywordBeforeParen(upper, k) {
			return alterTableTarget(clean), k, true
		}
	}
	return "", "", false
}

// keywordBeforeParen reports whether kw appears as a standalone constraint keyword
// — a token boundary on its left and a "(" column list on its right. The left
// boundary keeps `UNIQUE` inside a constraint NAME (e.g. `users_unique_key`) from
// matching, and the required "(" keeps the USING INDEX adopt form out.
func keywordBeforeParen(upper, kw string) bool {
	for from := 0; ; {
		i := strings.Index(upper[from:], kw)
		if i < 0 {
			return false
		}
		i += from
		leftOK := i == 0 || upper[i-1] == ' '
		if leftOK && strings.HasPrefix(strings.TrimSpace(upper[i+len(kw):]), "(") {
			return true
		}
		from = i + len(kw)
	}
}

// indexUniqueFinding certifies (or refuses) a CREATE UNIQUE INDEX by the column's
// profiled uniqueness, reusing the shared uniqueness classification.
func indexUniqueFinding(f *fixture.Fixture, ix createIndexStmt) verdict.Finding {
	table, cols := ix.table, ix.cols
	dep, target := uniqueDependency(table, cols)

	// A partial or expression index is not described by the column's uniqueness
	// fact, so neither branch below may be taken: the fixture cannot prove the
	// build fails OR that it succeeds. Emit WARN explicitly rather than routing a
	// PASS through capping — capping would read the whole-column fact, find it
	// `exact`, and certify a PASS for a subset nobody measured.
	if ix.undecidable() {
		scope, why := "partial", fmt.Sprintf("its predicate (WHERE %s) selects only some rows", ix.predicate)
		if ix.expression {
			scope, why = "expression", "it indexes an expression, not the column itself"
		}
		return verdict.Finding{
			Code:     "RS-INDEX-010",
			Severity: verdict.SeverityWarn,
			Title:    fmt.Sprintf("Uniqueness of this %s unique index on %s cannot be decided from the fixture", scope, shortTable(table)),
			Detail: fmt.Sprintf(
				"The fixture records uniqueness for %s as a whole, but this index is %s, so %s. "+
					"A duplicate in the column does not mean the index fails (the duplicates may all be in rows the "+
					"index excludes), and a unique column does not mean it succeeds. rowshape declines to guess in "+
					"either direction (INV-UNIQUENESS).", target, scope, why),
			Evidence: map[string]any{"partial_predicate": ix.predicate, "expression_index": ix.expression},
			// Deliberately NOT the whole-column `unique` fact: that fact does not
			// support this conclusion, and citing it would put a false provenance
			// trail into a signed document.
			DependsOn: nil,
			// The catalog's text is included VERBATIM and then extended with the
			// check for this specific index. `rowshape explain RS-INDEX-010` and
			// the finding must not drift apart (they share one source, pinned by
			// cmd.TestExplainCoversEmittedCodes), but the catalog cannot know the
			// predicate — so the general advice is quoted and the specific query
			// appended, rather than replaced.
			Remediation: remediation("RS-INDEX-010") + fmt.Sprintf(
				" For this partial/expression index, check the INDEXED SET rather than the whole column: "+
					"SELECT %s FROM %s%s GROUP BY %s HAVING count(*) > 1 — if it returns no rows, the index will build.",
				strings.Join(cols, ", "), table, partialWhere(ix.predicate), strings.Join(cols, ", ")),
			Explain: "rowshape explain RS-INDEX-010",
		}
	}

	if uniquenessState(f, table, cols) == uniqViolated {
		return verdict.Finding{
			Code:        "RS-INDEX-010",
			Severity:    verdict.SeverityError,
			Title:       fmt.Sprintf("CREATE UNIQUE INDEX on %s will fail: the column has duplicate values", target),
			Detail:      fmt.Sprintf("%s is proven non-unique (unique=false, exact); the unique index cannot build.", target),
			Evidence:    map[string]any{"unique": false},
			DependsOn:   []string{dep},
			Remediation: remediation("RS-INDEX-010"),
			Explain:     "rowshape explain RS-INDEX-010",
		}
	}
	return verdict.Finding{
		Code:        "RS-INDEX-010",
		Severity:    verdict.SeverityInfo,
		Title:       fmt.Sprintf("Uniqueness of %s not confirmed for CREATE UNIQUE INDEX", target),
		Detail:      fmt.Sprintf("A unique index on %s can only PASS if uniqueness is proven exact; a sample never establishes it (INV-UNIQUENESS).", target),
		DependsOn:   []string{dep},
		Remediation: remediation("RS-INDEX-010"),
		Explain:     "rowshape explain RS-INDEX-010",
	}
}

// partialWhere renders the predicate back into the verification query, or
// nothing when the index covers every row.
func partialWhere(predicate string) string {
	if predicate == "" {
		return ""
	}
	return " WHERE " + predicate
}

// reindexScope is what a REINDEX rebuilds: a single index, or every index of a
// table, schema, or database. The bucket must reflect the TOTAL work: REINDEX
// TABLE rebuilds ALL of a table's indexes sequentially, so estimating only the
// largest one understated the duration; REINDEX SCHEMA / DATABASE rebuild
// everything and previously produced no finding at all (they fell through the
// index-name branch, matched nothing, and were silent).
type reindexScope int

const (
	reindexIndex reindexScope = iota
	reindexTable
	reindexSchema
	reindexDatabase
)

// reindexFinding flags a non-concurrent REINDEX and buckets its duration from the
// TOTAL on-disk bytes of every index it rebuilds (RFC §6.5).
func reindexFinding(f *fixture.Fixture, name string, scope reindexScope, hasVersion bool) (verdict.Finding, bool) {
	bytes, count, bloat, bloatKnown, ok := reindexTotal(f, name, scope)
	if !ok {
		return verdict.Finding{}, false
	}
	label := reindexLabel(scope, name, count)
	// An ABSENT bloat estimate must not render as a measured 0%. bloat_estimate
	// has no emitter today, so on every fixture a real `rowshape pull` produces
	// the field is nil — and defaulting it to 0.0 put a number nobody measured,
	// presented as a measurement, into the EVIDENCE map of a document shaped as a
	// signable in-toto predicate. Absent means absent; the finding stands on the
	// bytes, which ARE measured.
	bloatText := ""
	evidence := map[string]any{"index_bytes": bytes, "index_count": count}
	if bloatKnown && scope == reindexIndex {
		bloatText = fmt.Sprintf(", bloat estimate %.0f%%", bloat*100)
		evidence["bloat_estimate"] = bloat
	}

	fnd := verdict.Finding{
		Code:     "RS-INDEX-020",
		Severity: verdict.SeverityWarn,
		Title:    fmt.Sprintf("Non-concurrent REINDEX of %s rebuilds under lock (%s)", label, estimate.BucketFromBytes(bytes)),
		Detail:   fmt.Sprintf("REINDEX rebuilds %s (%d bytes total%s) while holding a lock that blocks writes.", label, bytes, bloatText),
		Evidence: evidence,
		// The duration rests entirely on the indexes' on-disk BYTES — an exact catalog
		// read (pg_total_relation_size), not the row count. Declaring `<table>.rows`
		// was false provenance in a signed document: a fact this finding never reads,
		// whose confidence it borrowed for a claim about bytes (the CR-CONSTRAINT-010
		// bug class). There is no factConfidence path for index bytes, and they are
		// exact, so the finding rests on no uncertain fact — an empty depends_on,
		// which the engine treats as exact (as RS-INDEX-010 already does when it
		// deliberately declines a fact). The bucket itself is a model over bytes, so
		// the Estimate carries `estimated`, exactly as the row-based model does.
		DependsOn:   nil,
		Estimate:    &verdict.Estimate{Bucket: estimate.BucketFromBytes(bytes), Model: "reindex_bytes", Confidence: string(fixture.Estimated)},
		Remediation: remediation("RS-INDEX-020"),
		Explain:     "rowshape explain RS-INDEX-020",
	}

	// RFC §9.1: refuse to extrapolate without engine.version. This analyzer built
	// its Estimate literal directly instead of going through estimateFor, so it
	// skipped the gate that CR-T21 consolidated into ONE enforcement point
	// precisely so a later analyzer could not forget it — and then a later
	// analyzer forgot it. The gate cannot live in estimateFor for this finding
	// (that function extrapolates from a MEASURED basis, while this cost model
	// reads a fixture fact), so it is enforced here explicitly.
	//
	// The finding still stands without the estimate: a non-concurrent REINDEX
	// holds its lock regardless of how long it takes, and an absent dependency
	// caps the verdict to WARN.
	if !hasVersion {
		fnd.Estimate = nil
		fnd.Title = fmt.Sprintf("Non-concurrent REINDEX of %s rebuilds under lock", label)
	}
	return fnd, true
}

// reindexTotal sums the on-disk bytes of every index a REINDEX rebuilds and
// returns the total, the index count, and the worst bloat among them. A single
// index is matched by name; a table/schema/database sums the indexes it owns.
func reindexTotal(f *fixture.Fixture, name string, scope reindexScope) (bytes int64, count int, maxBloat float64, bloatKnown bool, ok bool) {
	add := func(ix fixture.Index) {
		bytes += ix.Bytes
		count++
		if ix.BloatEstimate != nil {
			bloatKnown = true
			if *ix.BloatEstimate > maxBloat {
				maxBloat = *ix.BloatEstimate
			}
		}
	}
	switch scope {
	case reindexIndex:
		// Sorted, not map order: Postgres permits the same index name in two
		// schemas, and an unsorted scan returned an arbitrary one of them.
		for _, tname := range sortedTableNames(f) {
			for _, ix := range f.Tables[tname].Indexes {
				if strings.EqualFold(ix.Name, name) {
					add(ix)
					return bytes, count, maxBloat, bloatKnown, true
				}
			}
		}
		return 0, 0, 0, false, false
	case reindexTable:
		tbl, exists := f.Tables[name]
		if !exists {
			return 0, 0, 0, false, false
		}
		for _, ix := range tbl.Indexes {
			add(ix)
		}
	case reindexSchema:
		prefix := name + "."
		for key, tbl := range f.Tables {
			if strings.HasPrefix(key, prefix) {
				for _, ix := range tbl.Indexes {
					add(ix)
				}
			}
		}
	case reindexDatabase:
		for _, tbl := range f.Tables {
			for _, ix := range tbl.Indexes {
				add(ix)
			}
		}
	}
	return bytes, count, maxBloat, bloatKnown, count > 0
}

// reindexLabel describes what is being rebuilt, for the finding title/detail.
func reindexLabel(scope reindexScope, name string, count int) string {
	switch scope {
	case reindexTable:
		return fmt.Sprintf("all %d index(es) of %s", count, shortTable(name))
	case reindexSchema:
		return fmt.Sprintf("all %d index(es) in schema %s", count, name)
	case reindexDatabase:
		return fmt.Sprintf("all %d index(es) in the database", count)
	default:
		return name
	}
}

// createIndexStmt is a parsed CREATE INDEX. It is a struct rather than a long
// return list because CR-T5 added two more things the analyzer must know about
// (the partial predicate and whether the target is an expression), and seven
// positional results are a bug waiting to happen.
type createIndexStmt struct {
	unique     bool
	concurrent bool
	table      string
	cols       []string
	// predicate is the WHERE clause of a PARTIAL index, empty when the index
	// covers every row.
	predicate string
	// expression is true when the index target is an expression such as
	// lower(email) rather than a plain column list.
	expression bool
}

// undecidable reports whether the fixture's column-level facts can say anything
// about THIS index's uniqueness.
//
// They cannot for a partial or expression index, and the distinction is not
// pedantic. `unique: {value: false, confidence: exact}` is a proven statement
// about the whole column; a partial index constrains only the rows its predicate
// selects. The textbook case is soft deletes — `CREATE UNIQUE INDEX ... WHERE
// deleted_at IS NULL` over a column whose duplicates live entirely in the
// soft-deleted rows. That index builds fine, and rowshape used to call it a
// confident FAIL. An expression index is the same problem one step removed: a
// fact about `email` says nothing about `lower(email)`.
func (ix createIndexStmt) undecidable() bool {
	return ix.predicate != "" || ix.expression
}

// parseCreateIndex recognizes CREATE [UNIQUE] INDEX [CONCURRENTLY] name ON table
// (cols) [WHERE predicate].
func parseCreateIndex(clean, upper string) (createIndexStmt, bool) {
	var ix createIndexStmt
	if !strings.HasPrefix(upper, "CREATE") || (!strings.Contains(upper, "CREATE INDEX") && !strings.Contains(upper, "CREATE UNIQUE INDEX")) {
		return ix, false
	}
	ix.unique = strings.Contains(upper, "UNIQUE INDEX")
	ix.concurrent = strings.Contains(upper, "CONCURRENTLY")
	i := strings.Index(upper, " ON ")
	if i < 0 {
		return ix, false
	}
	after := strings.Fields(clean[i+4:])
	if len(after) == 0 {
		return ix, false
	}
	ix.table = strings.Trim(strings.TrimRight(after[0], "("), `"`)
	ix.cols = colsAfter(clean[i:], "ON")
	if ix.table == "" || len(ix.cols) == 0 {
		return ix, false
	}

	// A WHERE clause in a CREATE INDEX can only be the partial-index predicate.
	if w := strings.Index(upper, " WHERE "); w >= 0 {
		ix.predicate = strings.TrimSuffix(strings.TrimSpace(clean[w+7:]), ";")
	}
	// colsAfter splits on the FIRST ')', so an expression target such as
	// lower(email) arrives as the fragment "lower(email" — an unbalanced paren is
	// the tell, and it is enough to know we must not answer from column facts.
	for _, c := range ix.cols {
		if strings.ContainsAny(c, "()") {
			ix.expression = true
			break
		}
	}
	return ix, true
}

// parseReindex recognizes REINDEX [INDEX|TABLE|SCHEMA|DATABASE|SYSTEM]
// [CONCURRENTLY] name and returns the rebuild scope. SYSTEM (catalog maintenance,
// not a user-schema outage) falls through to the index branch and matches no user
// index, so it produces no finding — deliberate.
func parseReindex(clean, upper string) (name string, scope reindexScope, concurrent, ok bool) {
	if !strings.HasPrefix(upper, "REINDEX") {
		return "", 0, false, false
	}
	fields := strings.Fields(clean)
	if len(fields) < 2 {
		return "", 0, false, false
	}
	concurrent = strings.Contains(upper, "CONCURRENTLY")
	switch {
	case strings.Contains(upper, " SCHEMA "):
		scope = reindexSchema
	case strings.Contains(upper, " DATABASE "):
		scope = reindexDatabase
	case strings.Contains(upper, " TABLE "):
		scope = reindexTable
	default:
		scope = reindexIndex // INDEX (or SYSTEM, which matches no user index)
	}
	name = strings.Trim(strings.TrimRight(fields[len(fields)-1], ";"), `"`)
	return name, scope, concurrent, true
}
