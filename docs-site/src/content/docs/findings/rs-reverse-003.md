---
title: 'RS-REVERSE-003 — Narrowing a column type can truncate data irreversibly'
description: 'Narrowing a column''s type — a wider integer to a smaller one, or a fractional number to an integer — can truncate or round values.'
seoTitle: 'RS-REVERSE-003: narrowing a column type truncates data'
---

**Namespace:** `RS-REVERSE` · **Code:** `RS-REVERSE-003`

## What it means

Narrowing a column's type (a wider integer to a smaller one, an unbounded string to a length-limited one, or a fractional number to an integer) can truncate or round values. Widening back cannot restore the lost precision.

## Why it matters

Where Postgres can convert a value, it does so silently: 19.99 becomes 20. Where it cannot, such as a bigint too large for integer, the migration fails instead. Either way, widening the column back does not restore what was lost.

## Example

Production shape: public.products has 2,000,000 rows and price is numeric(12,2), e.g. 19.99. This migration produces `RS-REVERSE-003` against it:

```sql
-- 19.99 silently becomes 20: the cast rounds, and nothing raises.
ALTER TABLE public.products ALTER COLUMN price TYPE integer;
```

<details>
<summary>The fixture this example is checked against</summary>

```yaml
rowshape_fixture: "1"
meta: {id: example, engine: {name: postgres, version: "16"}, privacy: standard, profile: {mode: fast, escalated: []}}
tables:
  public.products:
    rows: {value: 2000000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: constraint}}
      price: {type: 'numeric(12,2)', nullable: false}
    constraints: [{name: products_pkey, kind: primary_key, columns: [id]}]
```

</details>

## How to fix

Keep the wider type, or migrate without loss: add a new column of the target type, backfill it while checking every value fits, swap reads and writes over, then drop the old column in a later migration. Take a backup before any in-place narrowing.

**For the example above.** Keep the precision: add a column whose type holds every value exactly, backfill it in batches, switch reads and writes over, and drop the old column in a later migration.

```sql
SET lock_timeout = '3s';
ALTER TABLE public.products ADD COLUMN price_cents bigint;
-- then, in batches outside the migration:
--   UPDATE public.products SET price_cents = round(price * 100) WHERE id BETWEEN ...;
```

## Related codes

- RS-REVERSE-001 — DROP COLUMN loses its data irreversibly
- RS-LOCK-001 — ACCESS EXCLUSIVE lock for a full table rewrite
- RS-APPLY-001 — Migration did not apply

---

This page is generated from the same catalog `rowshape explain RS-REVERSE-003` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool. The example is run against PostgreSQL 16 by rowshape's test suite: the first migration must produce RS-REVERSE-003 and the fix must not fail.
