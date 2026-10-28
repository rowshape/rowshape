---
title: 'RS-LOCK-011 — Several statements take ACCESS EXCLUSIVE on the same table'
description: 'Each lock is acquired separately, so the table is unavailable across the whole sequence rather than for the longest single statement.'
seoTitle: 'RS-LOCK-011: repeated ACCESS EXCLUSIVE on one table'
---

**Namespace:** `RS-LOCK` · **Code:** `RS-LOCK-011`

## What it means

Each lock acquisition queues independently, so the table is unavailable across the whole sequence rather than for the duration of the longest single statement — and between them, traffic that built up during one lock competes for the next. The individual statements can each look perfectly reasonable; the cost is in the repetition.

## Why it matters

Each statement queues for the lock separately, so the table is unavailable across the whole sequence, and the traffic that built up behind one lock hits the next one.

## Example

Production shape: public.orders has 5,000,000 rows. This migration produces `RS-LOCK-011` against it:

```sql
SET lock_timeout = '3s';
ALTER TABLE public.orders ALTER COLUMN amount_cents TYPE bigint;
ALTER TABLE public.orders ADD CONSTRAINT orders_amount_positive CHECK (amount_cents > 0);
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

Combine the changes into a single ALTER TABLE with comma-separated actions — ALTER TABLE t ADD COLUMN a int, ADD COLUMN b int, ALTER COLUMN c SET NOT NULL — which takes the lock once. Where the operations genuinely cannot be combined, split them into separate migrations so each takes its lock independently and a failure does not leave the table half-changed.

**For the example above.** Combine the actions into one ALTER TABLE. It takes the lock once, and Postgres checks the new constraint during the same pass that rewrites the table.

```sql
SET lock_timeout = '3s';
ALTER TABLE public.orders
  ALTER COLUMN amount_cents TYPE bigint,
  ADD CONSTRAINT orders_amount_positive CHECK (amount_cents > 0);
```

## Related codes

- RS-LOCK-001 — ACCESS EXCLUSIVE lock for a full table rewrite
- RS-LOCK-010 — The migration takes ACCESS EXCLUSIVE without setting lock_timeout
- RS-CONSTRAINT-020 — ADD CONSTRAINT without NOT VALID scans the whole table under ACCESS EXCLUSIVE

---

This page is generated from the same catalog `rowshape explain RS-LOCK-011` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool. The example is run against PostgreSQL 16 by rowshape's test suite: the first migration must produce RS-LOCK-011 and the fix must not fail.
