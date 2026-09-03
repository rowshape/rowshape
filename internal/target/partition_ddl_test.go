package target

import (
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
)

func partitionedFixture(strategy, key string, count int) *fixture.Fixture {
	return &fixture.Fixture{
		Tables: map[string]fixture.Table{
			"app.events": {
				Rows: fixture.Fact[int64]{Value: 40000},
				Columns: map[string]fixture.Column{
					"id":          {Type: "bigint"},
					"occurred_at": {Type: "timestamp with time zone"},
					"kind":        {Type: "text"},
				},
				Partitions: &fixture.Partitions{Count: count, Strategy: strategy, Key: key, Skew: 0.5},
			},
		},
	}
}

// TestDDLDeclaresPartitionedTable is the wrong-PASS regression. Rebuilt as an
// ordinary table, the target accepted `CREATE INDEX CONCURRENTLY`, which Postgres
// refuses OUTRIGHT on a partitioned parent — so a migration that cannot run at all
// in production came back PASS with no findings.
func TestDDLDeclaresPartitionedTable(t *testing.T) {
	stmt := findStatement(t, DDL(partitionedFixture("range", "occurred_at", 2)), "events")
	if !strings.Contains(stmt, "PARTITION BY RANGE (occurred_at)") {
		t.Errorf("parent not declared partitioned:\n%s", stmt)
	}
}

// TestPartitionsCoverEveryRow: a row matching no partition fails the COPY outright
// and takes the whole load down, so every strategy must produce partitions that
// span the key space.
func TestPartitionsCoverEveryRow(t *testing.T) {
	cases := []struct {
		strategy string
		want     string
		n        int
	}{
		// Modulus/remainder covers the key space by construction and needs no
		// knowledge of the values, so hash reproduces the count exactly.
		{"hash", "MODULUS 4, REMAINDER 0", 4},
		// Range and list bounds cannot be reconstructed from a fixture, so a DEFAULT
		// partition catches every row whatever the synthesised values are.
		{"range", "DEFAULT", 1},
		{"list", "DEFAULT", 1},
	}
	for _, tc := range cases {
		t.Run(tc.strategy, func(t *testing.T) {
			stmts := Partitions(partitionedFixture(tc.strategy, "occurred_at", 4))
			if len(stmts) != tc.n {
				t.Fatalf("%s produced %d partition(s), want %d:\n%s",
					tc.strategy, len(stmts), tc.n, strings.Join(stmts, "\n"))
			}
			if !strings.Contains(strings.Join(stmts, "\n"), tc.want) {
				t.Errorf("%s partitions missing %q:\n%s", tc.strategy, tc.want, strings.Join(stmts, "\n"))
			}
			for _, s := range stmts {
				if !strings.Contains(s, `PARTITION OF "app"."events"`) {
					t.Errorf("partition not attached to the parent: %s", s)
				}
			}
		})
	}
}

// TestPartitionCountDifferenceIsReported: partition count is not decoration — a
// DDL statement on a parent takes locks on EVERY partition, so a target standing
// one partition in for ninety understates the lock, which is the direction that
// turns a real finding into a clean run.
func TestPartitionCountDifferenceIsReported(t *testing.T) {
	got := UnreproducedPartitionCounts(partitionedFixture("range", "occurred_at", 90))
	if len(got) != 1 || !strings.Contains(got[0], "90") {
		t.Errorf("count difference not reported: %v", got)
	}
	// Hash reproduces the count exactly, so there is nothing to report.
	if got := UnreproducedPartitionCounts(partitionedFixture("hash", "tenant_id", 8)); len(got) != 0 {
		t.Errorf("hash count reported as unreproduced: %v", got)
	}
	// Neither is a single-partition table a difference.
	if got := UnreproducedPartitionCounts(partitionedFixture("range", "occurred_at", 1)); len(got) != 0 {
		t.Errorf("single-partition table reported as unreproduced: %v", got)
	}
}

// TestPartitionKeyAbsentFallsBackAndReports: a fixture written before `key`
// existed records the strategy and count but nothing to declare PARTITION BY with.
// Guessing a key would partition on the wrong column; the honest outcome is an
// ordinary table, reported.
func TestPartitionKeyAbsentFallsBackAndReports(t *testing.T) {
	f := partitionedFixture("range", "", 4)

	stmt := findStatement(t, DDL(f), "events")
	if strings.Contains(stmt, "PARTITION BY") {
		t.Errorf("partitioning declared with no recorded key:\n%s", stmt)
	}
	if got := Partitions(f); len(got) != 0 {
		t.Errorf("partitions created with no recorded key: %v", got)
	}
	got := UnreproducedPartitionCounts(f)
	if len(got) != 1 || !strings.Contains(got[0], "does not record a usable partition key") {
		t.Errorf("missing key not reported: %v", got)
	}
}

// TestUnknownPartitionStrategyIsNotGuessed: an unrecognized strategy must not be
// rendered into a PARTITION BY clause built from a guess.
func TestUnknownPartitionStrategyIsNotGuessed(t *testing.T) {
	f := partitionedFixture("quantum", "occurred_at", 3)
	if stmt := findStatement(t, DDL(f), "events"); strings.Contains(stmt, "PARTITION BY") {
		t.Errorf("unknown strategy rendered:\n%s", stmt)
	}
	if got := Partitions(f); len(got) != 0 {
		t.Errorf("partitions created for an unknown strategy: %v", got)
	}
}

// TestPartitionedUniqueMustContainTheKey: Postgres refuses the whole CREATE TABLE
// when a UNIQUE or PRIMARY KEY on a partitioned table does not include every
// partitioning column. Taking the table down is worse than the constraint being
// reported as unenforced, so it is skipped here rather than emitted to fail.
func TestPartitionedUniqueMustContainTheKey(t *testing.T) {
	f := partitionedFixture("range", "occurred_at", 2)
	tbl := f.Tables["app.events"]
	tbl.Constraints = []fixture.Constraint{
		{Name: "events_pkey", Kind: "primary_key", Columns: []string{"id"}},         // lacks the key
		{Name: "events_uq", Kind: "unique", Columns: []string{"id", "occurred_at"}}, // contains it
	}
	f.Tables["app.events"] = tbl

	stmt := findStatement(t, DDL(f), "events")
	if strings.Contains(stmt, "PRIMARY KEY") {
		t.Errorf("primary key without the partition key would make CREATE TABLE fail:\n%s", stmt)
	}
	if !strings.Contains(stmt, "UNIQUE") {
		t.Errorf("unique constraint containing the partition key was dropped:\n%s", stmt)
	}
}

// TestUnpartitionedTableIsUnaffected: none of this may change an ordinary table.
func TestUnpartitionedTableIsUnaffected(t *testing.T) {
	f := &fixture.Fixture{
		Tables: map[string]fixture.Table{
			"app.plain": {
				Rows:        fixture.Fact[int64]{Value: 10},
				Columns:     map[string]fixture.Column{"id": {Type: "bigint"}},
				Constraints: []fixture.Constraint{{Name: "plain_pkey", Kind: "primary_key", Columns: []string{"id"}}},
			},
		},
	}
	stmt := findStatement(t, DDL(f), "plain")
	if strings.Contains(stmt, "PARTITION BY") {
		t.Errorf("ordinary table declared partitioned:\n%s", stmt)
	}
	if !strings.Contains(stmt, "PRIMARY KEY") {
		t.Errorf("ordinary table lost its primary key:\n%s", stmt)
	}
	if got := Partitions(f); len(got) != 0 {
		t.Errorf("partitions created for an ordinary table: %v", got)
	}
	if got := UnreproducedPartitionCounts(f); len(got) != 0 {
		t.Errorf("ordinary table reported as a partitioning limitation: %v", got)
	}
}
