---
title: 'RS-CONSTRAINT-001 — NOT VALID constraint validated in the same transaction'
description: 'Adding a constraint NOT VALID and validating it in the same transaction still runs the full scan under that transaction''s locks.'
seoTitle: 'RS-CONSTRAINT-001: NOT VALID validated in the same tx'
sidebar:
  label: 'RS-CONSTRAINT-001'
---

**Namespace:** `RS-CONSTRAINT` · **Code:** `RS-CONSTRAINT-001`

## What it means

Adding a constraint NOT VALID and VALIDATE-ing it in one transaction still runs the full validating scan under the transaction's locks — the two-step split whose entire purpose is to avoid a long lock buys nothing.

## Why it matters

NOT VALID exists to keep the validating scan out from under the ACCESS EXCLUSIVE lock. Validated in the same transaction, the scan runs while that lock is still held, so the table is blocked for the whole scan: the outage the split was written to avoid, in a migration that looks like it follows best practice.

## Example

Production shape: public.orders has 5,000,000 rows. This migration produces `RS-CONSTRAINT-001` against it:

```sql
BEGIN;
ALTER TABLE public.orders ADD CONSTRAINT orders_amount_positive CHECK (amount_cents > 0) NOT VALID;
ALTER TABLE public.orders VALIDATE CONSTRAINT orders_amount_positive;
COMMIT;
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

Split across transactions: ADD CONSTRAINT ... NOT VALID and COMMIT, then VALIDATE CONSTRAINT in a separate transaction. VALIDATE then takes only a SHARE UPDATE EXCLUSIVE lock and does not block reads or writes.

**For the example above.** Commit between the two steps. Migration runners wrap each file in its own transaction, so put the VALIDATE in the next migration file.

```sql title="Migration 1"
SET lock_timeout = '3s';
ALTER TABLE public.orders ADD CONSTRAINT orders_amount_positive CHECK (amount_cents > 0) NOT VALID;
```

```sql title="Migration 2: takes only SHARE UPDATE EXCLUSIVE, so reads and writes continue"
ALTER TABLE public.orders VALIDATE CONSTRAINT orders_amount_positive;
```

## Related codes

- RS-CONSTRAINT-020 — ADD CONSTRAINT without NOT VALID scans the whole table under ACCESS EXCLUSIVE
- RS-DATA-020 — FOREIGN KEY validated against pre-existing orphans
- RS-LOCK-010 — The migration takes ACCESS EXCLUSIVE without setting lock_timeout

## References

- [Fixture spec §6.4: Constraints](/spec/)
- [Fixture spec §9.1: Extrapolation is model-based, not linear](/spec/)

---

This page is generated from the same catalog `rowshape explain RS-CONSTRAINT-001` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool. The example is run against PostgreSQL 16 by rowshape's test suite: the first migration must produce RS-CONSTRAINT-001 and the fix must not fail.
