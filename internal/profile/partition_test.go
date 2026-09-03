package profile

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

const partSchema = "rowshape_part_test"

func seedPartitions(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	ctx := context.Background()
	stmts := []string{
		`DROP SCHEMA IF EXISTS ` + partSchema + ` CASCADE`,
		`CREATE SCHEMA ` + partSchema,
		`CREATE TABLE ` + partSchema + `.events (
			id bigserial, occurred_at timestamptz NOT NULL, kind text NOT NULL
		) PARTITION BY RANGE (occurred_at)`,
		`CREATE TABLE ` + partSchema + `.events_2025 PARTITION OF ` + partSchema + `.events
		   FOR VALUES FROM ('2025-01-01') TO ('2026-01-01')`,
		`CREATE TABLE ` + partSchema + `.events_2026 PARTITION OF ` + partSchema + `.events
		   FOR VALUES FROM ('2026-01-01') TO ('2027-01-01')`,
		`INSERT INTO ` + partSchema + `.events (occurred_at, kind)
		   SELECT '2025-06-01'::timestamptz + (g || ' seconds')::interval, 'click'
		     FROM generate_series(1, 20000) g`,
		`ANALYZE ` + partSchema + `.events`,
	}
	for _, s := range stmts {
		if _, err := conn.Exec(ctx, s); err != nil {
			t.Fatalf("seed partitions (%s): %v", s, err)
		}
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+partSchema+` CASCADE`)
	})
}

// TestPullRecordsPartitionKey: count and strategy DESCRIBE a partitioned table;
// the KEY is what lets one be rebuilt. Without it the parent was recreated as an
// ordinary table, and the target then accepted `CREATE INDEX CONCURRENTLY`, which
// Postgres refuses outright on a partitioned parent.
func TestPullRecordsPartitionKey(t *testing.T) {
	conn := adminConn(t)
	seedPartitions(t, conn)

	f, err := Fast(context.Background(), conn, Options{Schemas: []string{partSchema}})
	if err != nil {
		t.Fatalf("Fast: %v", err)
	}
	p := f.Tables[partSchema+".events"].Partitions
	if p == nil {
		t.Fatal("partitioned parent recorded no partitions block")
	}
	if p.Key != "occurred_at" {
		t.Errorf("partition key = %q, want occurred_at", p.Key)
	}
	if p.Strategy != "range" || p.Count != 2 {
		t.Errorf("strategy/count = %q/%d, want range/2", p.Strategy, p.Count)
	}
	// The partitions are excluded from the fixture as tables in their own right
	// (RFC §14.2 is parent-only); only the parent appears.
	for name := range f.Tables {
		if name != partSchema+".events" {
			t.Errorf("partition %q recorded as a table of its own", name)
		}
	}
}

// TestPullSumsPartitionedTableBytes: pg_total_relation_size on a partitioned
// parent measures the PARENT's own storage, which is zero — the rows live in the
// partitions. A 400GB partitioned table therefore reported `bytes: 0`, and every
// size-driven duration bucket for it extrapolated from nothing, understating
// exactly the migrations whose cost matters most.
func TestPullSumsPartitionedTableBytes(t *testing.T) {
	conn := adminConn(t)
	seedPartitions(t, conn)
	ctx := context.Background()

	f, err := Fast(ctx, conn, Options{Schemas: []string{partSchema}})
	if err != nil {
		t.Fatalf("Fast: %v", err)
	}
	got := f.Tables[partSchema+".events"].Bytes

	var parentOnly int64
	if err := conn.QueryRow(ctx,
		`SELECT pg_total_relation_size($1::regclass)`, partSchema+".events").Scan(&parentOnly); err != nil {
		t.Fatalf("parent size: %v", err)
	}
	var tree int64
	if err := conn.QueryRow(ctx, `
WITH RECURSIVE t AS (
    SELECT $1::regclass::oid AS oid
  UNION ALL SELECT i.inhrelid FROM pg_inherits i JOIN t ON i.inhparent = t.oid
) SELECT COALESCE(sum(pg_total_relation_size(oid)), 0)::bigint FROM t`,
		partSchema+".events").Scan(&tree); err != nil {
		t.Fatalf("tree size: %v", err)
	}

	if got <= parentOnly {
		t.Errorf("bytes = %d, no more than the parent's own %d — the partitions' storage is missing", got, parentOnly)
	}
	if got != tree {
		t.Errorf("bytes = %d, want %d (the whole partition tree)", got, tree)
	}
}
