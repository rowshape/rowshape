---
title: 'RS-LOCK-003 — ATTACH PARTITION validates the incoming rows under a lock on the parent'
description: 'ATTACH PARTITION takes ACCESS EXCLUSIVE on the parent table, blocking every partition, then scans the incoming rows against the bound.'
seoTitle: 'RS-LOCK-003: ATTACH PARTITION locks the parent table'
---

**Namespace:** `RS-LOCK` · **Code:** `RS-LOCK-003`

## What it means

ATTACH PARTITION takes ACCESS EXCLUSIVE on the PARENT table — blocking every query against every partition — and then scans the incoming table to prove every row satisfies the partition bound, unless a matching CHECK constraint already exists. On a large incoming partition that scan is the outage, and it happens while the whole partitioned table is locked. Non-concurrent DETACH takes the same lock on the parent.

## Why it matters

The lock is on the parent, so queries against every partition wait, not only queries on the new one, and the scan that checks the bound runs while they wait.

## Example

Production shape: public.events is partitioned by month on created_at, and public.events_2026_10 is an ordinary 20,000,000-row table about to become October's partition. This migration produces `RS-LOCK-003` against it:

```sql
ALTER TABLE public.events ATTACH PARTITION public.events_2026_10
  FOR VALUES FROM ('2026-10-01') TO ('2026-11-01');
```

:::note
`rowshape validate --ephemeral` refuses a migration that attaches or detaches a partition, because the disposable database it hydrates does not reproduce partitioning. Validate it with `--target` against a database that has the real schema.
:::

<details>
<summary>The fixture this example is checked against</summary>

```yaml
rowshape_fixture: "1"
meta: {id: example, engine: {name: postgres, version: "16"}, privacy: standard, profile: {mode: fast, escalated: []}}
tables:
  public.events:
    rows: {value: 200000000, confidence: exact}
    partitions: {count: 12, strategy: range, key: created_at}
    columns:
      id: {type: bigint, nullable: false}
      created_at: {type: timestamp with time zone, nullable: false}
  public.events_2026_10:
    rows: {value: 20000000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false}
      created_at: {type: timestamp with time zone, nullable: false, range: {min: "2026-10-01T00:00:00Z", max: "2026-10-31T23:59:59Z"}}
```

</details>

## How to fix

Add a CHECK constraint matching the partition bound to the incoming table BEFORE attaching, and validate it separately: ALTER TABLE incoming ADD CONSTRAINT c CHECK (<bound>) NOT VALID; then VALIDATE CONSTRAINT c; then ATTACH. With the constraint already proven, ATTACH skips the scan and the lock is brief. On PostgreSQL 14+, prefer DETACH PARTITION CONCURRENTLY for the reverse direction.

**For the example above.** Prove the bound on the incoming table first with a CHECK that matches it, validated without the exclusive lock. ATTACH then skips its scan, so the lock on the parent is brief.

```sql title="Migration 1"
SET lock_timeout = '3s';
ALTER TABLE public.events_2026_10 ADD CONSTRAINT events_2026_10_bound
  CHECK (created_at >= '2026-10-01' AND created_at < '2026-11-01') NOT VALID;
```

```sql title="Migration 2"
ALTER TABLE public.events_2026_10 VALIDATE CONSTRAINT events_2026_10_bound;
```

```sql title="Migration 3"
SET lock_timeout = '3s';
ALTER TABLE public.events ATTACH PARTITION public.events_2026_10
  FOR VALUES FROM ('2026-10-01') TO ('2026-11-01');
```

:::note[rowshape still reports RS-LOCK-003 here]
rowshape reports every ATTACH PARTITION, because the lock on the parent is taken regardless; what the CHECK removes is the scan under it.
:::

## Related codes

- RS-CONSTRAINT-020 — ADD CONSTRAINT without NOT VALID scans the whole table under ACCESS EXCLUSIVE
- RS-LOCK-010 — The migration takes ACCESS EXCLUSIVE without setting lock_timeout

## References

- [Fixture spec §14.2: Partitioned tables](/spec/)

---

This page is generated from the same catalog `rowshape explain RS-LOCK-003` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool. The example is run against PostgreSQL 16 by rowshape's test suite: the first migration must produce RS-LOCK-003 and the fix must not fail.
