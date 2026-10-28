---
title: 'RS-DEPLOY-002 — Changing REPLICA IDENTITY changes what downstream consumers can identify'
description: 'REPLICA IDENTITY controls what a logical decoding stream can identify a changed row BY.'
seoTitle: 'RS-DEPLOY-002: REPLICA IDENTITY change breaks consumers'
---

**Namespace:** `RS-DEPLOY` · **Code:** `RS-DEPLOY-002`

## What it means

REPLICA IDENTITY controls what a logical decoding stream can identify a changed row BY. Changing it raises no error and does not affect queries, so nothing about applying the migration reveals a problem — but logical replication subscribers, CDC pipelines and analytics mirrors can silently start receiving UPDATE and DELETE events they cannot match to a row. NOTHING is the extreme case: those events carry no old-row identity at all. FULL is the safe-but-costly direction, writing the entire old row into WAL on every UPDATE and DELETE.

## Why it matters

Nothing fails where you can see it. A logical replication subscriber or CDC pipeline can start rejecting or skipping updates and deletes it can no longer match to a row, and the first symptom is data drift somewhere downstream.

## Example

Production shape: public.events has 20,000,000 rows; whether anything consumes its changes is not something a fixture records. This migration produces `RS-DEPLOY-002` against it:

```sql
ALTER TABLE public.events REPLICA IDENTITY NOTHING;
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

Check what consumes this table's changes before changing its identity: SELECT * FROM pg_publication_tables WHERE tablename = '<table>'; and check for active replication slots with SELECT * FROM pg_replication_slots. If you are dropping the index that backs REPLICA IDENTITY USING INDEX, set a new identity BEFORE dropping it, not after. If you need FULL, size the WAL increase first — it is proportional to update volume, not table size.

**For the example above.** Check what consumes the table's changes before changing its identity, on the production database.

```sql
-- Run these first, against production:
--   SELECT pubname FROM pg_publication_tables WHERE schemaname = 'public' AND tablename = 'events';
--   SELECT slot_name, plugin, active FROM pg_replication_slots;
-- Only if nothing consumes the table (or every consumer is ready):
ALTER TABLE public.events REPLICA IDENTITY NOTHING;
```

:::note[rowshape still reports RS-DEPLOY-002 here]
rowshape cannot see your subscribers or CDC pipelines, so it reports every REPLICA IDENTITY change. The check above is the fix, not a different statement.
:::

## Related codes

- RS-INDEX-003 — DROP INDEX without CONCURRENTLY takes ACCESS EXCLUSIVE on the table
- RS-DEPLOY-001 — Renaming a column or table breaks running code that still uses the old name
- RS-DEPLOY-003 — Dropping NOT NULL withdraws a guarantee running code relies on

---

This page is generated from the same catalog `rowshape explain RS-DEPLOY-002` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool. The example is run against PostgreSQL 16 by rowshape's test suite: the first migration must produce RS-DEPLOY-002 and the fix must not fail.
