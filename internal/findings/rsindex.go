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
		if name, isTable, concurrent, ok := parseReindex(clean, upper); ok && !concurrent {
			// Only REINDEX TABLE names a table. REINDEX INDEX names an INDEX, which
			// findIndex matches against index names rather than fixture table keys,
			// so resolving it there would be wrong.
			if isTable {
				name = resolveTable(f, name)
			}
			if fnd, ok := reindexFinding(f, name, isTable, hasVersion); ok {
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

// reindexFinding flags a non-concurrent REINDEX and buckets its duration from the
// index's on-disk bytes/bloat (RFC §6.5).
func reindexFinding(f *fixture.Fixture, name string, isTable bool, hasVersion bool) (verdict.Finding, bool) {
	table, idx, ok := findIndex(f, name, isTable)
	if !ok {
		return verdict.Finding{}, false
	}
	bytes := idx.Bytes

	// An ABSENT bloat estimate must not render as a measured 0%.
	//
	// bloat_estimate has no emitter anywhere: internal/profile contains no bloat,
	// n_dead_tup or pgstattuple query, so on every fixture a real `rowshape pull`
	// produces the field is nil. Defaulting it to 0.0 meant every REINDEX finding
	// asserted "bloat estimate 0%" — the exact opposite of the condition that
	// motivates a REINDEX — and put bloat_estimate:0 into the EVIDENCE map, which
	// is part of the Verdict, and the Verdict is shaped as a signable in-toto
	// predicate. A number nobody measured, presented as a measurement, inside a
	// document meant to be attested.
	//
	// Absent now means absent: omitted from the text and from the evidence. The
	// finding stands on the bytes, which ARE measured.
	detail := fmt.Sprintf("REINDEX rewrites the whole index (%d bytes) while holding a lock that blocks writes.", bytes)
	evidence := map[string]any{"index_bytes": bytes}
	if idx.BloatEstimate != nil {
		detail = fmt.Sprintf("REINDEX rewrites the whole index (%d bytes, bloat estimate %.0f%%) while holding a lock that blocks writes.", bytes, *idx.BloatEstimate*100)
		evidence["bloat_estimate"] = *idx.BloatEstimate
	}

	fnd := verdict.Finding{
		Code:     "RS-INDEX-020",
		Severity: verdict.SeverityWarn,
		Title:    fmt.Sprintf("Non-concurrent REINDEX of %s rebuilds under lock (%s)", name, estimate.BucketFromBytes(bytes)),
		Detail:   detail,
		Evidence: evidence,
		// The index's BYTES, not the table's rows. This estimate is derived
		// entirely from idx.Bytes via a throughput constant; citing table.rows
		// put a fact the conclusion does not rest on into a signed document —
		// the same false-provenance trail indexUniqueFinding above explicitly
		// refuses to leave.
		DependsOn:   []string{table + ".indexes." + idx.Name + ".bytes"},
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
	if hasVersion {
		fnd.Estimate = &verdict.Estimate{Bucket: estimate.BucketFromBytes(bytes), Model: "reindex_bytes"}
	} else {
		fnd.Title = fmt.Sprintf("Non-concurrent REINDEX of %s rebuilds under lock", name)
	}
	return fnd, true
}

// findIndex locates an index by name (REINDEX INDEX) or the largest index of a
// table (REINDEX TABLE), returning the owning table and the index.
func findIndex(f *fixture.Fixture, name string, isTable bool) (string, fixture.Index, bool) {
	if isTable {
		tbl, ok := f.Tables[name]
		if !ok || len(tbl.Indexes) == 0 {
			return "", fixture.Index{}, false
		}
		big := tbl.Indexes[0]
		for _, ix := range tbl.Indexes[1:] {
			if ix.Bytes > big.Bytes {
				big = ix
			}
		}
		return name, big, true
	}
	// Sorted, not map order. Postgres permits the same index name in two
	// schemas, so an unsorted scan returned an arbitrary one of them — and the
	// table name it returns feeds the finding's DependsOn, making the recorded
	// provenance unstable too.
	for _, tname := range sortedTableNames(f) {
		tbl := f.Tables[tname]
		for _, ix := range tbl.Indexes {
			if strings.EqualFold(ix.Name, name) {
				return tname, ix, true
			}
		}
	}
	return "", fixture.Index{}, false
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

// parseReindex recognizes REINDEX [INDEX|TABLE|...] [CONCURRENTLY] name.
func parseReindex(clean, upper string) (name string, isTable, concurrent bool, ok bool) {
	if !strings.HasPrefix(upper, "REINDEX") {
		return "", false, false, false
	}
	fields := strings.Fields(clean)
	if len(fields) < 2 {
		return "", false, false, false
	}
	concurrent = strings.Contains(upper, "CONCURRENTLY")
	isTable = strings.Contains(upper, " TABLE ")
	name = strings.Trim(strings.TrimRight(fields[len(fields)-1], ";"), `"`)
	return name, isTable, concurrent, true
}
