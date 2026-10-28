---
title: 'RS-CONSTRAINT-010 — CHECK constraint conflicts with existing data'
description: 'The column''s profiled range violates the CHECK predicate, so existing rows already fail it and adding the constraint (or validating it) fails.'
seoTitle: 'RS-CONSTRAINT-010: CHECK conflicts with existing data'
---

**Namespace:** `RS-CONSTRAINT` · **Code:** `RS-CONSTRAINT-010`

## What it means

The column's profiled range violates the CHECK predicate, so existing rows already fail it and adding the constraint (or validating it) fails.

## Why it matters

Postgres checks every existing row when a CHECK is added or validated and fails on the first one that violates it. The migration fails in production, after holding its lock for as long as the scan took to get there, and a test database built from clean fixtures never has the bad rows.

## Example

Production shape: public.accounts has 250,000 rows and balance_cents ranges from -4,500 to 980,000, so some balances are already negative. This migration produces `RS-CONSTRAINT-010` against it:

```sql
ALTER TABLE public.accounts ADD CONSTRAINT accounts_balance_nonneg CHECK (balance_cents >= 0);
```

<details>
<summary>The fixture this example is checked against</summary>

```yaml
rowshape_fixture: "1"
meta: {id: example, engine: {name: postgres, version: "16"}, privacy: standard, profile: {mode: exact, escalated: []}}
tables:
  public.accounts:
    rows: {value: 250000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: constraint}}
      balance_cents: {type: integer, nullable: false, range: {min: -4500, max: 980000}}
    constraints: [{name: accounts_pkey, kind: primary_key, columns: [id]}]
```

</details>

## How to fix

Repair or exclude the rows that violate the predicate before adding the CHECK (or widen the predicate). Add the constraint NOT VALID, fix the data, then VALIDATE.

**For the example above.** Repair the negative balances first — what "repair" means is a business decision — ship that, and re-pull the fixture. Once its range starts at zero, add the constraint in two steps so neither holds a long lock.

```sql title="Migration 1"
SET lock_timeout = '3s';
ALTER TABLE public.accounts ADD CONSTRAINT accounts_balance_nonneg CHECK (balance_cents >= 0) NOT VALID;
```

```sql title="Migration 2"
ALTER TABLE public.accounts VALIDATE CONSTRAINT accounts_balance_nonneg;
```

<details>
<summary>The re-pulled fixture the fix is checked against</summary>

```yaml
rowshape_fixture: "1"
meta: {id: example, engine: {name: postgres, version: "16"}, privacy: standard, profile: {mode: exact, escalated: []}}
tables:
  public.accounts:
    rows: {value: 250000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: constraint}}
      balance_cents: {type: integer, nullable: false, range: {min: 0, max: 980000}}
    constraints: [{name: accounts_pkey, kind: primary_key, columns: [id]}]
```

</details>

## Related codes

- RS-CONSTRAINT-020 — ADD CONSTRAINT without NOT VALID scans the whole table under ACCESS EXCLUSIVE
- RS-CONSTRAINT-001 — NOT VALID constraint validated in the same transaction
- RS-APPLY-001 — Migration did not apply

## References

- [Fixture spec §6.1: Universal fields](/spec/)
- [Fixture spec §6.4: Constraints](/spec/)

---

This page is generated from the same catalog `rowshape explain RS-CONSTRAINT-010` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool. The example is run against PostgreSQL 16 by rowshape's test suite: the first migration must produce RS-CONSTRAINT-010 and the fix must not fail.
