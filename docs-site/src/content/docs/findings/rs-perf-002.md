---
title: 'RS-PERF-002 — Unqualified UPDATE/DELETE touches every row'
description: 'An UPDATE or DELETE with no WHERE clause rewrites or removes every row of a large table — a slow, lock-holding, bloat-inducing full scan.'
seoTitle: 'RS-PERF-002: unqualified UPDATE or DELETE hits every row'
sidebar:
  label: 'RS-PERF-002'
---

**Namespace:** `RS-PERF` · **Code:** `RS-PERF-002`

## What it means

An UPDATE or DELETE with no WHERE clause rewrites or removes every row of a large table — a slow, lock-holding, bloat-inducing full scan that is almost never intended.

## Why it matters

One statement changes every row in one transaction: row locks on the whole table until it commits, a table's worth of dead tuples for vacuum, and a burst of WAL that replicas have to replay.

## Example

Production shape: public.users has 400,000 rows. This migration produces `RS-PERF-002` against it:

```sql
UPDATE public.users SET email = lower(email);
```

<details>
<summary>The fixture this example is checked against</summary>

```yaml
rowshape_fixture: "1"
meta: {id: example, engine: {name: postgres, version: "16"}, privacy: standard, profile: {mode: exact, escalated: []}}
tables:
  public.users:
    rows: {value: 400000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: constraint}}
      email: {type: text, nullable: true}
    constraints: [{name: users_pkey, kind: primary_key, columns: [id]}]
```

</details>

## How to fix

Add a WHERE clause to scope the change. For a genuine full-table update, run it in bounded batches (by primary-key range) and VACUUM afterward to reclaim the bloat.

**For the example above.** Scope the statement to the rows that need it, and run a genuine full-table change in bounded batches by primary-key range — from a job, not the migration — then VACUUM.

```sql
-- Repeat for each id range until the table is covered:
UPDATE public.users SET email = lower(email)
WHERE id BETWEEN 1 AND 50000 AND email <> lower(email);
```

## Related codes

- RS-PERF-010 — A qualified UPDATE/DELETE on a large table rewrites many rows in one statement
- RS-PERF-001 — DELETE cascades through a long-tailed fan-out

---

This page is generated from the same catalog `rowshape explain RS-PERF-002` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool. The example is run against PostgreSQL 16 by rowshape's test suite: the first migration must produce RS-PERF-002 and the fix must not fail.
