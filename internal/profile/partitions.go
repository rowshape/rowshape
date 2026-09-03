package profile

import (
	"context"
	"strings"

	"github.com/rowshape/rowshape/internal/fixture"
)

// measurePartitions describes a partitioned table's shape (RFC §14.2): the
// parent declares count, strategy, and skew, with NO per-partition entries. A
// partitioning migration is reasoned about from this block — partition count and
// per-partition skew change lock behavior materially, and nothing else captures
// it.
func (r *reader) measurePartitions(ctx context.Context, t tableRef) (*fixture.Partitions, error) {
	if t.relkind != "p" {
		return nil, nil // not a partitioned parent
	}

	strategy, err := r.partitionStrategy(ctx, t.oid)
	if err != nil {
		return nil, err
	}
	count, skew, _, err := r.partitionCountSkew(ctx, t.oid)
	if err != nil {
		return nil, err
	}
	key, err := r.partitionKey(ctx, t.oid)
	if err != nil {
		return nil, err
	}
	return &fixture.Partitions{Count: count, Strategy: strategy, Key: key, Skew: round6(skew)}, nil
}

// partitionKey reads the partition key — the part inside the parentheses of
// `PARTITION BY <strategy> (...)`.
//
// pg_get_partkeydef renders the whole clause ("RANGE (occurred_at)"), and the
// strategy is already a field of its own, so only the parenthesised key is kept:
// duplicating the strategy would be one more thing that can disagree with itself.
// Available since PG 10, so no version gate is needed.
func (r *reader) partitionKey(ctx context.Context, oid uint32) (string, error) {
	const q = `SELECT COALESCE(pg_get_partkeydef($1), '')`
	var def string
	if err := r.tx.QueryRow(ctx, q, oid).Scan(&def); err != nil {
		return "", err
	}
	open := strings.Index(def, "(")
	closeAt := strings.LastIndex(def, ")")
	if open < 0 || closeAt <= open {
		// An unrecognized rendering is left empty rather than mangled: a consumer then
		// reports that it cannot rebuild the partitioning, which is honest, instead of
		// declaring PARTITION BY over a key that is not the real one.
		return "", nil
	}
	return strings.TrimSpace(def[open+1 : closeAt]), nil
}

// partitionTotalBytes sums the on-disk size of a partitioned parent and every
// partition beneath it.
//
// pg_total_relation_size on the parent alone returns its OWN storage, which for a
// partitioned table is zero — the rows live in the partitions. A 400GB partitioned
// table therefore reported `bytes: 0`, and every size-driven duration bucket for
// it extrapolated from nothing, understating exactly the migrations that matter
// most.
//
// A recursive walk of pg_inherits rather than pg_partition_tree: the latter
// arrived in PG 12 and the supported matrix starts at 10. It also handles
// sub-partitioning, which a single-level query would silently undercount.
func (r *reader) partitionTotalBytes(ctx context.Context, oid uint32) (int64, error) {
	const q = `
WITH RECURSIVE tree AS (
    SELECT $1::oid AS oid
  UNION ALL
    SELECT i.inhrelid FROM pg_inherits i JOIN tree t ON i.inhparent = t.oid
)
SELECT COALESCE(sum(pg_total_relation_size(oid)), 0)::bigint FROM tree`
	var total int64
	if err := r.tx.QueryRow(ctx, q, oid).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

// partitionTotalRows returns the summed planner row estimate across a
// partitioned parent's direct partitions. A partitioned parent stores no rows
// itself, so its declared count must come from the partitions (RFC §9).
func (r *reader) partitionTotalRows(ctx context.Context, oid uint32) (int64, error) {
	_, _, sum, err := r.partitionCountSkew(ctx, oid)
	if err != nil {
		return 0, err
	}
	if sum < 0 {
		sum = 0
	}
	return int64(sum), nil
}

// partitionStrategy reads the partitioning strategy (range | list | hash).
func (r *reader) partitionStrategy(ctx context.Context, oid uint32) (string, error) {
	const q = `SELECT partstrat::text FROM pg_partitioned_table WHERE partrelid = $1`
	var s string
	if err := r.tx.QueryRow(ctx, q, oid).Scan(&s); err != nil {
		return "", err
	}
	switch s {
	case "r":
		return "range", nil
	case "l":
		return "list", nil
	case "h":
		return "hash", nil
	default:
		return s, nil
	}
}

// partitionCountSkew returns the number of direct partitions and the fraction of
// rows held by the largest one (1/count is uniform; near 1 means one partition
// dominates). Row counts come from the planner estimate, so skew is estimated.
func (r *reader) partitionCountSkew(ctx context.Context, oid uint32) (count int, skew, sum float64, err error) {
	const q = `
SELECT count(*),
       COALESCE(max(GREATEST(c.reltuples, 0)), 0),
       COALESCE(sum(GREATEST(c.reltuples, 0)), 0)
FROM pg_inherits i
JOIN pg_class c ON c.oid = i.inhrelid
WHERE i.inhparent = $1`
	var maxTuples float64
	if err = r.tx.QueryRow(ctx, q, oid).Scan(&count, &maxTuples, &sum); err != nil {
		return 0, 0, 0, err
	}
	if sum > 0 {
		skew = maxTuples / sum
	}
	return count, skew, sum, nil
}
