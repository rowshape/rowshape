---
title: 'RS-INDEX-001 — Non-concurrent CREATE INDEX blocks writes'
description: 'A plain CREATE INDEX holds a lock that blocks writes for the whole O(n log n) build.'
seoTitle: 'RS-INDEX-001: non-concurrent CREATE INDEX blocks writes'
---

**Namespace:** `RS-INDEX` · **Code:** `RS-INDEX-001`

## What it means

A plain CREATE INDEX holds a lock that blocks writes for the whole O(n log n) build. On a large table that is a long write outage.

## Why it matters

A plain CREATE INDEX takes a SHARE lock: reads continue, but every INSERT, UPDATE and DELETE on the table waits until the build finishes. On a large table that is minutes of queued or failed writes.

## Example

Production shape: public.orders has 5,000,000 rows. This migration produces `RS-INDEX-001` against it:

```sql
CREATE INDEX orders_created_at_idx ON public.orders (created_at);
```

<details>
<summary>The fixture this example is checked against</summary>

```yaml
rowshape_fixture: "1"
meta: {id: example, engine: {name: postgres, version: "16"}, privacy: standard, profile: {mode: fast, escalated: []}}
tables:
  public.orders:
    rows: {value: 5000000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: constraint}}
      user_id: {type: bigint, nullable: false}
      amount_cents: {type: integer, nullable: false, range: {min: 1, max: 2500000}}
      created_at: {type: timestamp with time zone, nullable: false}
    constraints: [{name: orders_pkey, kind: primary_key, columns: [id]}]
```

</details>

## How to fix

Use CREATE INDEX CONCURRENTLY: it builds in two passes without an exclusive lock, so writes continue. Run it outside a transaction block.

**For the example above.** Build it CONCURRENTLY, in its own migration with the runner's transaction wrapping turned off (see RS-TX-001, which rowshape reports on this as a reminder).

```sql
CREATE INDEX CONCURRENTLY orders_created_at_idx ON public.orders (created_at);
```

## Related codes

- RS-TX-001 — This statement cannot run inside a transaction block
- RS-INDEX-002 — ADD PRIMARY KEY or UNIQUE builds an index under ACCESS EXCLUSIVE
- RS-INDEX-020 — Non-concurrent REINDEX rebuilds under lock

## References

- [Fixture spec §6.5: Indexes](/spec/)
- [Fixture spec §9.1: Extrapolation is model-based, not linear](/spec/)

---

This page is generated from the same catalog `rowshape explain RS-INDEX-001` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool. The example is run against PostgreSQL 16 by rowshape's test suite: the first migration must produce RS-INDEX-001 and the fix must not fail.
