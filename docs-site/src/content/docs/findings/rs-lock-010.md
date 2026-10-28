---
title: 'RS-LOCK-010 — The migration takes ACCESS EXCLUSIVE without setting lock_timeout'
description: 'Without a lock_timeout, a DDL statement that cannot take its lock waits — and every new query on that table queues behind it while it does.'
seoTitle: 'RS-LOCK-010: ACCESS EXCLUSIVE without a lock_timeout'
---

**Namespace:** `RS-LOCK` · **Code:** `RS-LOCK-010`

## What it means

With no lock_timeout, a DDL statement that cannot acquire its lock immediately WAITS — and while it waits, every new query on that table queues behind it, because a pending ACCESS EXCLUSIVE request blocks incoming readers too. A migration that is instant in isolation can therefore stall an entire table behind one long-running transaction it happened to collide with. This is the mechanism behind most 'one quick migration took the site down' incidents, and it is invisible to any per-statement rule because no single statement is wrong.

## Why it matters

A waiting ACCESS EXCLUSIVE request blocks every query that arrives after it. One long report or idle transaction holding a lock on the table is enough to turn a quick migration into a stalled site; lock_timeout makes the migration fail and retry instead. rowshape reports it where a statement also holds the lock for a scan, rewrite or build, where the stall is worst, but the advice applies to all DDL.

## Example

Production shape: public.orders has 5,000,000 rows and is queried constantly. This migration produces `RS-LOCK-010` against it:

```sql
ALTER TABLE public.orders ALTER COLUMN amount_cents TYPE bigint;
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

Set a short timeout at the head of the migration: SET lock_timeout = '3s'; so a statement that cannot get its lock fails fast instead of queueing every reader behind it. Pair it with a retry: a failed migration you can re-run is strictly better than a stalled table. For operations that must eventually succeed on a busy table, retry in a loop with backoff rather than raising the timeout. Note that SET LOCAL scopes it to the transaction, which is usually what you want inside a migration.

**For the example above.** Set a short lock_timeout at the head of the migration, and retry the migration if it times out. This fixes the queueing only: the rewrite itself is still RS-LOCK-001.

```sql
SET lock_timeout = '3s';
ALTER TABLE public.orders ALTER COLUMN amount_cents TYPE bigint;
```

## Related codes

- RS-LOCK-001 — ACCESS EXCLUSIVE lock for a full table rewrite
- RS-LOCK-011 — Several statements take ACCESS EXCLUSIVE on the same table
- RS-INDEX-003 — DROP INDEX without CONCURRENTLY takes ACCESS EXCLUSIVE on the table

---

This page is generated from the same catalog `rowshape explain RS-LOCK-010` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool. The example is run against PostgreSQL 16 by rowshape's test suite: the first migration must produce RS-LOCK-010 and the fix must not fail.
