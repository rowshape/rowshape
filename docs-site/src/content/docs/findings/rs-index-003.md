---
title: 'RS-INDEX-003 — DROP INDEX without CONCURRENTLY takes ACCESS EXCLUSIVE on the table'
description: 'A non-concurrent DROP INDEX takes ACCESS EXCLUSIVE on the table, not just the index, so every read and write on it queues behind you.'
seoTitle: 'RS-INDEX-003: DROP INDEX without CONCURRENTLY locks'
---

**Namespace:** `RS-INDEX` · **Code:** `RS-INDEX-003`

## What it means

A non-concurrent DROP INDEX takes ACCESS EXCLUSIVE on the TABLE, not merely on the index — so every read and write on the table queues behind it, and behind anything already holding a conflicting lock. The drop itself is fast, which is exactly why it looks harmless in a sandbox: the risk is the lock queue on a busy table, not the work.

## Why it matters

The drop is instant once it has the lock. The danger is getting it: it waits behind any long-running query on the table, and every query that arrives after it waits behind the drop.

## Example

Production shape: public.events has 20,000,000 rows and a 400 MB index, events_note_idx. This migration produces `RS-INDEX-003` against it:

```sql
DROP INDEX events_note_idx;
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

Use DROP INDEX CONCURRENTLY, which takes only SHARE UPDATE EXCLUSIVE and does not block reads or writes. It cannot run inside a transaction block (see RS-TX-001), so it needs its own migration with the runner's transaction wrapping disabled. Set a short lock_timeout either way, so a drop that cannot get its lock fails fast instead of queueing every query behind it.

**For the example above.** Drop it CONCURRENTLY, in its own migration with the runner's transaction wrapping turned off.

```sql
SET lock_timeout = '3s';
DROP INDEX CONCURRENTLY events_note_idx;
```

## Related codes

- RS-LOCK-010 — The migration takes ACCESS EXCLUSIVE without setting lock_timeout
- RS-TX-001 — This statement cannot run inside a transaction block
- RS-DEPLOY-002 — Changing REPLICA IDENTITY changes what downstream consumers can identify

---

This page is generated from the same catalog `rowshape explain RS-INDEX-003` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool. The example is run against PostgreSQL 16 by rowshape's test suite: the first migration must produce RS-INDEX-003 and the fix must not fail.
