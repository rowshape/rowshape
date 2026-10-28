---
title: 'RS-REVERSE-004 — TRUNCATE removes every row irreversibly and locks the table'
description: 'TRUNCATE deletes every row in the table and cannot be rolled back once committed.'
seoTitle: 'RS-REVERSE-004: TRUNCATE is irreversible and locks'
sidebar:
  label: 'RS-REVERSE-004'
---

**Namespace:** `RS-REVERSE` · **Code:** `RS-REVERSE-004`

## What it means

TRUNCATE deletes every row in the table and cannot be rolled back once committed. It takes an ACCESS EXCLUSIVE lock, so every read and write on the table blocks for its duration, and it does not fire per-row DELETE triggers — so audit or soft-delete logic built on them is silently skipped. Because TRUNCATE succeeds instantly against a small or freshly-hydrated table, nothing about running it reveals how much production data it would destroy.

## Why it matters

TRUNCATE is instant, so it looks harmless against a test database. In production it empties the table, cannot be undone once committed, locks the table while it runs, and skips per-row DELETE triggers.

## Example

Production shape: public.events has 20,000,000 rows. This migration produces `RS-REVERSE-004` against it:

```sql
TRUNCATE public.events;
```

<details>
<summary>The fixture this example is checked against</summary>

```yaml
rowshape_fixture: "1"
meta: {id: example, engine: {name: postgres, version: "16"}, privacy: standard, profile: {mode: fast, escalated: []}}
tables:
  public.events:
    rows: {value: 20000000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: probe}}
      note: {type: text, nullable: false}
      created_at: {type: timestamp with time zone, nullable: false}
    indexes: [{name: events_note_idx, columns: [note], bytes: 400000000}]
```

</details>

## How to fix

If you mean to discard the data, take a backup first and say so in the migration. If you mean to remove SOME rows, use DELETE with a WHERE clause. If you are resetting a table between deploys, consider renaming it aside (ALTER TABLE ... RENAME TO) so the rows remain recoverable. TRUNCATE ... CASCADE additionally empties every table with a foreign key into this one — check what that reaches before running it.

**For the example above.** If the intent is to remove old rows, not all of them, say so with a WHERE clause and delete in bounded batches from a job.

```sql
-- Repeat with the next id range until the old rows are gone:
DELETE FROM public.events
WHERE id BETWEEN 1 AND 50000 AND created_at < now() - interval '90 days';
```

## Related codes

- RS-REVERSE-002 — DROP TABLE loses every row irreversibly
- RS-PERF-001 — DELETE cascades through a long-tailed fan-out
- RS-PERF-002 — Unqualified UPDATE/DELETE touches every row

---

This page is generated from the same catalog `rowshape explain RS-REVERSE-004` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool. The example is run against PostgreSQL 16 by rowshape's test suite: the first migration must produce RS-REVERSE-004 and the fix must not fail.
