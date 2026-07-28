package findings

import (
	"fmt"
	"strings"

	"github.com/rowshape/rowshape/internal/estimate"
	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/validate"
	"github.com/rowshape/rowshape/internal/verdict"
)

func init() { validate.Register(rsExclusive{}) }

// rsExclusive covers operations that SUCCEED and are still an outage.
//
// This closes the catalog's largest structural gap. Every analyzer before it
// asks a question of the form "will this fail?" — will the constraint build,
// does the data contradict the predicate, are there orphans. The product's
// promise is a different question: "is this safe to run on production?" A
// statement that succeeds after holding ACCESS EXCLUSIVE on a 200M-row table for
// twenty minutes is a successful outage, and the catalog was silent on every
// instance of it:
//
//	ALTER TABLE t ADD CONSTRAINT c CHECK (amount > 0);   -- valid data: PASS
//	ALTER TABLE t ADD CONSTRAINT f FOREIGN KEY ...;      -- no orphans: PASS
//	ALTER TABLE t ADD PRIMARY KEY (id);                  -- PASS
//	DROP INDEX idx;                                       -- PASS
//
// Each of those is fine in the sandbox precisely BECAUSE it succeeds — and the
// verdict floor only fires on failure, so nothing else caught them.
//
// Note the relationship to the existing constraint analyzers: RS-CONSTRAINT-010
// fires when the profiled range CONTRADICTS a CHECK, and RS-DATA-020 fires when
// a FK validation would trip on orphans. Both answer "will it fail?". These
// findings answer "what does it cost when it works?", so they are complementary
// rather than duplicates.
type rsExclusive struct{}

func (rsExclusive) Analyze(f *fixture.Fixture, c *validate.Capture) []verdict.Finding {
	_, hasVersion := estimate.Major(f.Meta.Engine.Version)

	var out []verdict.Finding
	for i, st := range c.Statements {
		clean := collapseSpaces(stripSQLComments(st.SQL))
		upper := strings.ToUpper(clean)

		switch {
		case strings.HasPrefix(upper, "DROP INDEX"):
			if strings.Contains(upper, "CONCURRENTLY") {
				continue // the safe form: no exclusive lock on the table
			}
			if fnd, ok := dropIndexFinding(f, clean, upper); ok {
				out = append(out, fnd)
			}

		case strings.HasPrefix(upper, "ALTER TABLE") && strings.Contains(upper, "ADD PRIMARY KEY"):
			if fnd, ok := indexBuildFinding(f, c, i, clean, "PRIMARY KEY", hasVersion); ok {
				out = append(out, fnd)
			}

		case strings.HasPrefix(upper, "ALTER TABLE") && strings.Contains(upper, "ADD UNIQUE"):
			if fnd, ok := indexBuildFinding(f, c, i, clean, "UNIQUE", hasVersion); ok {
				out = append(out, fnd)
			}

		case strings.HasPrefix(upper, "ALTER TABLE") && strings.Contains(upper, "ATTACH PARTITION"):
			out = append(out, attachPartitionFinding(f, clean))

		case strings.HasPrefix(upper, "ALTER TABLE") && strings.Contains(upper, "ADD CONSTRAINT"):
			if strings.Contains(upper, "NOT VALID") {
				continue // the safe form: the scan is deferred to VALIDATE
			}
			switch {
			case strings.Contains(upper, "CHECK"):
				if fnd, ok := validatingScanFinding(f, c, i, clean, "CHECK", hasVersion); ok {
					out = append(out, fnd)
				}
			case strings.Contains(upper, "FOREIGN KEY"):
				if fnd, ok := validatingScanFinding(f, c, i, clean, "FOREIGN KEY", hasVersion); ok {
					out = append(out, fnd)
				}
			// PRIMARY KEY and UNIQUE via ADD CONSTRAINT are index builds, not
			// validating scans. The bare `ADD PRIMARY KEY` / `ADD UNIQUE` spellings
			// are matched by the cases above, but `ADD CONSTRAINT <name> PRIMARY KEY
			// (...)` reaches here — and it is the form alembic, Rails and most
			// hand-written migrations emit, because it is the only one that can name
			// the constraint. It used to fall through to a `continue` commented
			// "handled above", which was true only of the bare spelling: the COMMON
			// form produced no index-build finding at all.
			case strings.Contains(upper, "PRIMARY KEY"):
				if fnd, ok := indexBuildFinding(f, c, i, clean, "PRIMARY KEY", hasVersion); ok {
					out = append(out, fnd)
				}
			case strings.Contains(upper, "UNIQUE"):
				if fnd, ok := indexBuildFinding(f, c, i, clean, "UNIQUE", hasVersion); ok {
					out = append(out, fnd)
				}
			}
		}
	}
	return out
}

// validatingScanFinding reports a constraint added without NOT VALID.
func validatingScanFinding(f *fixture.Fixture, c *validate.Capture, idx int, clean, kind string, hasVersion bool) (verdict.Finding, bool) {
	table := resolveTable(f, alterTableTarget(clean))
	if table == "" {
		return verdict.Finding{}, false
	}
	tbl, ok := f.Tables[table]
	if !ok {
		return verdict.Finding{}, false
	}
	rows := tbl.Rows.Value

	fnd := verdict.Finding{
		Code:     "RS-CONSTRAINT-020",
		Severity: verdict.SeverityWarn,
		Title: fmt.Sprintf("ADD CONSTRAINT %s on %s scans all %s rows under ACCESS EXCLUSIVE",
			kind, shortTable(table), humanCount(rows)),
		Detail: fmt.Sprintf(
			"Adding a %s constraint without NOT VALID validates every existing row before the statement "+
				"returns, and holds ACCESS EXCLUSIVE for the whole scan — so reads and writes on the table "+
				"block for its duration. This SUCCEEDS: the data is fine. The cost is the lock, not the "+
				"outcome, which is why applying it to a small or freshly-hydrated table reveals nothing.",
			kind),
		Evidence:    map[string]any{"rows": rows, "constraint_kind": kind},
		DependsOn:   []string{table + ".rows"},
		Remediation: remediation("RS-CONSTRAINT-020"),
		Explain:     "rowshape explain RS-CONSTRAINT-020",
	}
	fnd.Estimate = estimateFor(c, idx, estimate.ConstraintValidation, table, rows, tbl.Rows.Confidence, true, hasVersion)
	return fnd, true
}

// addPrimaryKeyFinding reports a PRIMARY KEY added to an existing table.
func addPrimaryKeyFinding(f *fixture.Fixture, c *validate.Capture, idx int, clean string, hasVersion bool) (verdict.Finding, bool) {
	table := resolveTable(f, alterTableTarget(clean))
	if table == "" {
		return verdict.Finding{}, false
	}
	tbl, ok := f.Tables[table]
	if !ok {
		return verdict.Finding{}, false
	}
	rows := tbl.Rows.Value

	fnd := verdict.Finding{
		Code:     "RS-LOCK-002",
		Severity: verdict.SeverityWarn,
		Title: fmt.Sprintf("ADD PRIMARY KEY on %s builds an index over %s rows under ACCESS EXCLUSIVE",
			shortTable(table), humanCount(rows)),
		Detail: "ADD PRIMARY KEY builds a unique index over every row and holds ACCESS EXCLUSIVE for the " +
			"whole build, blocking reads and writes. There is no CONCURRENTLY form of ADD PRIMARY KEY — the " +
			"safe route is to build the unique index concurrently first and then adopt it.",
		Evidence:    map[string]any{"rows": rows},
		DependsOn:   []string{table + ".rows"},
		Remediation: remediation("RS-LOCK-002"),
		Explain:     "rowshape explain RS-LOCK-002",
	}
	fnd.Estimate = estimateFor(c, idx, estimate.BTreeBuild, table, rows, tbl.Rows.Confidence, true, hasVersion)
	return fnd, true
}

// dropIndexFinding reports a non-concurrent DROP INDEX.
func dropIndexFinding(f *fixture.Fixture, clean, upper string) (verdict.Finding, bool) {
	name := identAfter(clean, upper, "DROP INDEX")
	name = strings.TrimSuffix(strings.Trim(name, `";`), ";")
	if name == "" {
		return verdict.Finding{}, false
	}
	table, _, found := findIndex(f, name, false)

	on := "its table"
	if found {
		on = shortTable(table)
	}
	fnd := verdict.Finding{
		Code:     "RS-INDEX-002",
		Severity: verdict.SeverityWarn,
		Title:    fmt.Sprintf("DROP INDEX %s takes ACCESS EXCLUSIVE on %s", name, on),
		Detail: "A non-concurrent DROP INDEX takes ACCESS EXCLUSIVE on the TABLE, not just the index — so " +
			"every read and write on the table queues behind it, and behind anything already holding a " +
			"conflicting lock. The drop itself is fast, which is exactly why it looks harmless in a sandbox: " +
			"the risk is the lock queue on a busy table, not the work.",
		Evidence:    map[string]any{"index": name},
		Remediation: remediation("RS-INDEX-002"),
		Explain:     "rowshape explain RS-INDEX-002",
	}
	// Cite the table only when the index actually resolved; an unresolved name
	// must not leave a dangling provenance path in a signed document.
	if found {
		fnd.DependsOn = []string{table + ".rows"}
	}
	return fnd, true
}

// attachPartitionFinding reports the lock and scan cost of ATTACH PARTITION.
//
// The lock is on the PARENT, which is the part that surprises people: every
// query against every partition blocks, not just the one being attached. And
// unless a matching CHECK already proves the bound, Postgres scans the incoming
// table to verify it while holding that lock.
func attachPartitionFinding(f *fixture.Fixture, clean string) verdict.Finding {
	parent := resolveTable(f, alterTableTarget(clean))
	return verdict.Finding{
		Code:     "RS-LOCK-003",
		Severity: verdict.SeverityWarn,
		Title:    fmt.Sprintf("ATTACH PARTITION locks the parent %s and may scan the incoming rows", shortTable(parent)),
		Detail: "ATTACH PARTITION takes ACCESS EXCLUSIVE on the PARENT — blocking every query against every " +
			"partition, not just the one being attached — and then scans the incoming table to prove every row " +
			"satisfies the partition bound, unless a matching CHECK constraint already exists. On a large " +
			"incoming partition that scan is the outage, and it runs while the whole partitioned table is locked. " +
			"Note also that rowshape's hydrated schema does not reproduce partitioning, so applying this " +
			"statement against the sandbox exercises a plain table and proves nothing about the real lock.",
		Evidence:    map[string]any{"parent": parent},
		DependsOn:   nil,
		Remediation: remediation("RS-LOCK-003"),
		Explain:     "rowshape explain RS-LOCK-003",
	}
}
