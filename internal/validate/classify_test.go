package validate

import "testing"

// classifyIndexBuild is a DB-free, load-bearing helper that sat at 0% direct
// coverage — reached only incidentally through the live-DB apply path. It decides
// the CONCURRENTLY branch. docs/TESTING-GAPS.md item 3.
//
// (The transaction-control test moved to internal/sqlkind when isTxControl was
// de-duplicated out of this package and internal/plan — item 3b.)

func TestClassifyIndexBuild(t *testing.T) {
	cases := []struct {
		sql              string
		isIndex, concurr bool
	}{
		{"CREATE INDEX i ON t (c)", true, false},
		{"CREATE UNIQUE INDEX i ON t (c)", true, false},
		{"CREATE INDEX CONCURRENTLY i ON t (c)", true, true},
		{"create unique index concurrently i on t (c)", true, true},
		{"REINDEX INDEX i", false, false},
		{"CREATE TABLE index_stats (id int)", false, false}, // "INDEX" only in a name
		{"ALTER TABLE t ADD COLUMN c int", false, false},
		{"", false, false},
		// The splitter keeps a leading comment with its statement.
		{"-- Migration 1\nCREATE INDEX CONCURRENTLY i ON t (c)", true, true},
		{"/* x */ CREATE INDEX i ON t (c)", true, false},
	}
	for _, tc := range cases {
		gotIdx, gotConc := classifyIndexBuild(tc.sql)
		if gotIdx != tc.isIndex || gotConc != tc.concurr {
			t.Errorf("classifyIndexBuild(%q) = (%v,%v), want (%v,%v)",
				tc.sql, gotIdx, gotConc, tc.isIndex, tc.concurr)
		}
	}
}
