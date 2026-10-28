---
title: 'RS-LOCK-001 — ACCESS EXCLUSIVE lock for a full table rewrite'
description: 'Adding a column with a volatile default, or changing a column''s type, rewrites every row while holding an ACCESS EXCLUSIVE lock.'
seoTitle: 'RS-LOCK-001: ACCESS EXCLUSIVE for a full table rewrite'
---

**Namespace:** `RS-LOCK` · **Code:** `RS-LOCK-001`

## What it means

Adding a column with a volatile default, or changing a column's type, rewrites every row while holding an ACCESS EXCLUSIVE lock — no reads or writes proceed until it finishes. On a large table that is a write outage.

## Why it matters

Every row is rewritten while the table is locked against reads and writes. The time scales with the table, so a change that takes a second in staging takes minutes in production, and all of it is downtime for that table.

## Example

Production shape: public.orders has 5,000,000 rows. This migration produces `RS-LOCK-001` against it:

```sql
ALTER TABLE public.orders ADD COLUMN token uuid NOT NULL DEFAULT gen_random_uuid();
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

Avoid the full-table ACCESS EXCLUSIVE rewrite. For a volatile default: ADD the column nullable with no default, backfill in batches, then attach the default and SET NOT NULL via a validated CHECK. For a type change: add a new column of the target type, backfill it in batches, swap reads/writes, and drop the old column. Each step is online.

**For the example above.** Add the column with no default, so no row is rewritten; give new rows a default; backfill existing rows in batches outside the migration (see RS-PERF-010); then enforce NOT NULL in a later migration (see RS-DATA-001).

```sql
SET lock_timeout = '3s';
ALTER TABLE public.orders ADD COLUMN token uuid;
ALTER TABLE public.orders ALTER COLUMN token SET DEFAULT gen_random_uuid();
```

## Related codes

- RS-LOCK-010 — The migration takes ACCESS EXCLUSIVE without setting lock_timeout
- RS-LOCK-011 — Several statements take ACCESS EXCLUSIVE on the same table
- RS-REVERSE-003 — Narrowing a column type can truncate data irreversibly
- RS-PERF-010 — A qualified UPDATE/DELETE on a large table rewrites many rows in one statement

## References

- [Fixture spec §9.1: Extrapolation is model-based, not linear](/spec/)

---

This page is generated from the same catalog `rowshape explain RS-LOCK-001` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool. The example is run against PostgreSQL 16 by rowshape's test suite: the first migration must produce RS-LOCK-001 and the fix must not fail.
