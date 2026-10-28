---
title: 'RS-TX-001 — This statement cannot run inside a transaction block'
description: 'Postgres refuses some statements inside a transaction block with SQLSTATE 25001: CREATE INDEX CONCURRENTLY, VACUUM, ALTER SYSTEM and more.'
seoTitle: 'RS-TX-001: statement cannot run in a transaction block'
sidebar:
  label: 'RS-TX-001'
---

**Namespace:** `RS-TX` · **Code:** `RS-TX-001`

## What it means

Postgres refuses a handful of statements inside a transaction block with SQLSTATE 25001: CREATE/DROP INDEX CONCURRENTLY, REINDEX CONCURRENTLY, VACUUM, CLUSTER, CREATE/DROP DATABASE and TABLESPACE, ALTER SYSTEM, and (before PostgreSQL 12) ALTER TYPE ... ADD VALUE. Most migration runners — Alembic, Django, Rails, Flyway — wrap each migration file in a transaction by default, so a file that looks fine standalone fails under the runner. rowshape's own sandbox does not execute your BEGIN/COMMIT, applying each statement in its own transaction so locks can be inspected, so a clean apply is NOT evidence that this works.

## Why it matters

The file works when you run it by hand and fails under the migration runner, which wraps it in a transaction, so it usually fails for the first time in CI or in production.

## Example

Production shape: public.orders has 5,000,000 rows. This migration produces `RS-TX-001` against it:

```sql
BEGIN;
CREATE INDEX CONCURRENTLY orders_user_id_idx ON public.orders (user_id);
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

Move the statement into its own migration that runs outside a transaction, and tell your runner. Alembic: set transaction_per_migration and use an autocommit block. Django: set atomic = False on the Migration class. Rails: disable_ddl_transaction!. Flyway: use a script-based migration or set executeInTransaction to false. golang-migrate: it does not wrap by default, so no change is needed. If you opened the transaction yourself with BEGIN, split the statement out of that block.

**For the example above.** Put the statement in a migration of its own with no BEGIN/COMMIT, and turn off your runner's per-migration transaction for that file: Django `atomic = False`, Rails `disable_ddl_transaction!`, Alembic an autocommit block, Flyway `executeInTransaction=false`.

```sql
CREATE INDEX CONCURRENTLY orders_user_id_idx ON public.orders (user_id);
```

:::note[rowshape still reports RS-TX-001 here]
rowshape cannot see your runner's settings, so a file that does not open its own transaction is still reported, as a warning rather than an error, until you confirm the runner will not wrap it.
:::

## Related codes

- RS-INDEX-001 — Non-concurrent CREATE INDEX blocks writes
- RS-INDEX-003 — DROP INDEX without CONCURRENTLY takes ACCESS EXCLUSIVE on the table
- RS-INDEX-020 — Non-concurrent REINDEX rebuilds under lock
- RS-DATA-014 — ADD UNIQUE without proven uniqueness

---

This page is generated from the same catalog `rowshape explain RS-TX-001` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool. The example is run against PostgreSQL 16 by rowshape's test suite: the first migration must produce RS-TX-001 and the fix must not fail.
