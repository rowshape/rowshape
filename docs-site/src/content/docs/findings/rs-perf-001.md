---
title: 'RS-PERF-001 — DELETE cascades through a long-tailed fan-out'
description: 'Deleting from a parent table referenced ON DELETE CASCADE cascades to its children.'
seoTitle: 'RS-PERF-001: DELETE cascades through a long fan-out'
sidebar:
  label: 'RS-PERF-001'
---

**Namespace:** `RS-PERF` · **Code:** `RS-PERF-001`

## What it means

Deleting from a parent table referenced ON DELETE CASCADE cascades to its children. When the fan-out is long-tailed (the max dwarfs the mean), deleting the wrong parents cascades to a huge, slow, lock-holding delete — an outage a uniform mean hides.

## Why it matters

The mean says a parent has a handful of children; the tail says some have thousands. Deleting one of those parents deletes all of its children in the same statement, holding locks until it finishes.

## Example

Production shape: public.users has 1,200,000 rows; public.orders references it ON DELETE CASCADE with a mean of 8 orders per user but a maximum of 12,902. This migration produces `RS-PERF-001` against it:

```sql
DELETE FROM public.users WHERE last_seen_at < now() - interval '2 years';
```

<details>
<summary>The fixture this example is checked against</summary>

```yaml
rowshape_fixture: "1"
meta: {id: example, engine: {name: postgres, version: "16"}, privacy: standard, profile: {mode: fast, escalated: []}}
tables:
  public.users:
    rows: {value: 1200000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: constraint}}
      last_seen_at: {type: timestamp with time zone, nullable: true, null_fraction: {value: 0.01, confidence: exact}}
    constraints: [{name: users_pkey, kind: primary_key, columns: [id]}]
  public.orders:
    rows: {value: 10000000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: constraint}}
      user_id: {type: bigint, nullable: false, distinct: 1198000}
    constraints: [{name: orders_pkey, kind: primary_key, columns: [id]}]
    references:
      - column: user_id
        to: public.users.id
        on_delete: cascade
        fanout: {mean: 8.4, p50: 3, p95: 41, max: 12902, confidence: measured}
        orphan_fraction: {value: 0, confidence: exact, via: constraint}
```

</details>

## How to fix

Delete in bounded batches (by primary-key range), and check the fan-out tail before deleting parents with many cascaded children. Consider detaching or soft-deleting children first so the cascade is bounded.

**For the example above.** Move the delete out of the migration into a job that removes the children first, in bounded batches, so no single statement cascades through a long tail. Run each statement until it deletes nothing.

```sql
-- 1. Children first, 10,000 at a time:
DELETE FROM public.orders WHERE id IN (
  SELECT o.id FROM public.orders o JOIN public.users u ON u.id = o.user_id
  WHERE u.last_seen_at < now() - interval '2 years'
  LIMIT 10000);

-- 2. Then the parents, which now cascade to nothing:
DELETE FROM public.users WHERE id IN (
  SELECT id FROM public.users
  WHERE last_seen_at < now() - interval '2 years'
  LIMIT 10000);
```

:::note[rowshape still reports RS-PERF-001 here]
rowshape sizes a cascade from the fixture's fan-out profile, not from which children an earlier statement removed, so the parent delete is still reported. What changed is that every statement is now bounded.
:::

## Related codes

- RS-PERF-010 — A qualified UPDATE/DELETE on a large table rewrites many rows in one statement
- RS-PERF-002 — Unqualified UPDATE/DELETE touches every row
- RS-REVERSE-004 — TRUNCATE removes every row irreversibly and locks the table

## References

- [Fixture spec §6.6: References and fan-out](/spec/)

---

This page is generated from the same catalog `rowshape explain RS-PERF-001` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool. The example is run against PostgreSQL 16 by rowshape's test suite: the first migration must produce RS-PERF-001 and the fix must not fail.
