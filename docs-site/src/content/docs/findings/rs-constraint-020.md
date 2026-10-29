---
title: 'RS-CONSTRAINT-020 — ADD CONSTRAINT without NOT VALID scans the whole table under ACCESS EXCLUSIVE'
description: 'ADD CONSTRAINT without NOT VALID validates every existing row before the statement returns, holding ACCESS EXCLUSIVE for the whole scan.'
seoTitle: 'RS-CONSTRAINT-020: ADD CONSTRAINT without NOT VALID'
sidebar:
  label: 'RS-CONSTRAINT-020'
---

**Namespace:** `RS-CONSTRAINT` · **Code:** `RS-CONSTRAINT-020`

## What it means

Adding a CHECK or FOREIGN KEY constraint without NOT VALID validates every existing row before the statement returns, holding ACCESS EXCLUSIVE for the whole scan — so reads and writes on the table block for its duration. This SUCCEEDS when the data is valid; the cost is the lock, not the outcome, which is why applying it to a small or freshly-hydrated table reveals nothing about what it does in production.

## Why it matters

The statement succeeds, which is why it passes review and every test. The cost is the lock: while it scans, every read and write on the table waits, and on a large table the scan takes minutes.

## Example

Production shape: public.orders has 5,000,000 rows, all with a positive amount. This migration produces `RS-CONSTRAINT-020` against it:

```sql
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

Split it in two. First ADD CONSTRAINT ... NOT VALID, which takes only a brief lock and applies to new and updated rows immediately. Then, in a separate statement (and ideally a separate migration), VALIDATE CONSTRAINT, which scans the table under a SHARE UPDATE EXCLUSIVE lock that does not block reads or writes. Keep the two apart: doing both in one transaction holds the exclusive lock across the scan anyway and gains nothing (RS-CONSTRAINT-001).

**For the example above.** Add the constraint NOT VALID — new and updated rows are checked immediately — and validate the existing rows in a separate migration.

```sql title="Migration 1: a brief lock, no scan"
SET lock_timeout = '3s';
ALTER TABLE public.orders ADD CONSTRAINT orders_amount_positive CHECK (amount_cents > 0) NOT VALID;
```

```sql title="Migration 2: scans under SHARE UPDATE EXCLUSIVE, which does not block reads or writes"
ALTER TABLE public.orders VALIDATE CONSTRAINT orders_amount_positive;
```

## Related codes

- RS-CONSTRAINT-001 — NOT VALID constraint validated in the same transaction
- RS-CONSTRAINT-010 — CHECK constraint conflicts with existing data
- RS-DATA-020 — FOREIGN KEY validated against pre-existing orphans
- RS-LOCK-010 — The migration takes ACCESS EXCLUSIVE without setting lock_timeout

## References

- [Fixture spec §9.1: Extrapolation is model-based, not linear](/spec/)

---

This page is generated from the same catalog `rowshape explain RS-CONSTRAINT-020` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool. The example is run against PostgreSQL 16 by rowshape's test suite: the first migration must produce RS-CONSTRAINT-020 and the fix must not fail.
