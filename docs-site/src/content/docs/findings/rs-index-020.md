---
title: 'RS-INDEX-020 — Non-concurrent REINDEX rebuilds under lock'
description: 'A non-concurrent REINDEX rewrites the whole index while holding a lock that blocks writes.'
seoTitle: 'RS-INDEX-020: non-concurrent REINDEX rebuilds under lock'
---

**Namespace:** `RS-INDEX` · **Code:** `RS-INDEX-020`

## What it means

A non-concurrent REINDEX rewrites the whole index while holding a lock that blocks writes. Its cost is driven by the index's on-disk size and bloat.

## Why it matters

A plain REINDEX blocks writes to the table, and reads that would use the index, for the whole rebuild. A bloated index is the one you want to reindex and also the slowest one to rebuild.

## Example

Production shape: public.orders has 9,000,000 rows and a 4.2 GB index on customer_id that is 45% bloat. This migration produces `RS-INDEX-020` against it:

```sql
REINDEX INDEX orders_customer_idx;
```

<details>
<summary>The fixture this example is checked against</summary>

```yaml
rowshape_fixture: "1"
meta: {id: example, engine: {name: postgres, version: "16"}, privacy: standard, profile: {mode: fast, escalated: []}}
tables:
  public.orders:
    rows: {value: 9000000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: constraint}}
      customer_id: {type: bigint, nullable: false}
    constraints: [{name: orders_pkey, kind: primary_key, columns: [id]}]
    indexes:
      - {name: orders_customer_idx, method: btree, columns: [customer_id], bytes: 4200000000, bloat_estimate: 0.45}
```

</details>

## How to fix

Use REINDEX INDEX CONCURRENTLY (PG 12+) so the rebuild does not block writes.

**For the example above.** Rebuild it CONCURRENTLY (PostgreSQL 12 and later), outside a transaction block.

```sql
REINDEX INDEX CONCURRENTLY orders_customer_idx;
```

## Related codes

- RS-INDEX-001 — Non-concurrent CREATE INDEX blocks writes
- RS-TX-001 — This statement cannot run inside a transaction block
- RS-LOCK-010 — The migration takes ACCESS EXCLUSIVE without setting lock_timeout

## References

- [Fixture spec §6.5: Indexes](/spec/)

---

This page is generated from the same catalog `rowshape explain RS-INDEX-020` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool. The example is run against PostgreSQL 16 by rowshape's test suite: the first migration must produce RS-INDEX-020 and the fix must not fail.
