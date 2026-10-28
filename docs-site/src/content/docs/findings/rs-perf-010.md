---
title: 'RS-PERF-010 — A qualified UPDATE/DELETE on a large table rewrites many rows in one statement'
description: 'A qualified UPDATE or DELETE that touches tens of millions of rows in one statement: row locks held throughout, bloat, replication lag.'
seoTitle: 'RS-PERF-010: large single-statement UPDATE or DELETE'
sidebar:
  label: 'RS-PERF-010'
---

**Namespace:** `RS-PERF` · **Code:** `RS-PERF-010`

## What it means

RS-PERF-002 only fires when a statement has NO WHERE clause, but the real-world hazard always has one: UPDATE users SET normalized = lower(email) WHERE normalized IS NULL is the canonical unbatched backfill — one statement, tens of millions of rows, one transaction, row locks held for the whole duration, table bloat, and a replication lag spike. Where the fixture can size the predicate it does (null_fraction is exactly the selectivity of an IS NULL test); where it cannot, it reports the table's row count as an upper bound rather than falling silent.

## Why it matters

A WHERE clause makes it look targeted. When the predicate matches most of a large table it is the same single huge transaction: row locks held throughout, bloat, and replication lag.

## Example

Production shape: public.users has 50,000,000 rows and about 90% of normalized is NULL. This migration produces `RS-PERF-010` against it:

```sql
UPDATE public.users SET normalized = lower(email) WHERE normalized IS NULL;
```

<details>
<summary>The fixture this example is checked against</summary>

```yaml
rowshape_fixture: "1"
meta: {id: example, engine: {name: postgres, version: "16"}, privacy: standard, profile: {mode: fast, escalated: []}}
tables:
  public.users:
    rows: {value: 50000000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: constraint}}
      email: {type: text, nullable: false}
      normalized: {type: text, nullable: true, null_fraction: {value: 0.9, confidence: estimated, via: pg_stats}}
    constraints: [{name: users_pkey, kind: primary_key, columns: [id]}]
```

</details>

## How to fix

Batch it. Loop over a bounded key range — UPDATE ... WHERE id BETWEEN :lo AND :hi AND <predicate> — committing each batch, so no single transaction holds locks over the whole table and autovacuum can keep up between batches. Ten to fifty thousand rows per batch is a common starting point. Run the backfill OUTSIDE the migration if your runner wraps migrations in one transaction, since batching inside a single transaction defeats the purpose. If the statement genuinely matches only a handful of rows, the upper bound reported here is conservative and you can disregard it.

**For the example above.** Batch it by key range, committing each batch, from a job outside the migration. 10,000 to 50,000 rows per batch is a common starting point.

```sql
-- Repeat with the next id range until the table is covered:
UPDATE public.users SET normalized = lower(email)
WHERE id BETWEEN 1 AND 50000 AND normalized IS NULL;
```

## Related codes

- RS-PERF-002 — Unqualified UPDATE/DELETE touches every row
- RS-LOCK-001 — ACCESS EXCLUSIVE lock for a full table rewrite
- RS-DATA-001 — SET NOT NULL against existing NULLs

---

This page is generated from the same catalog `rowshape explain RS-PERF-010` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool. The example is run against PostgreSQL 16 by rowshape's test suite: the first migration must produce RS-PERF-010 and the fix must not fail.
