---
title: 'RS-DEPLOY-002 — Changing REPLICA IDENTITY changes what downstream consumers can identify'
description: 'REPLICA IDENTITY controls what a logical decoding stream can identify a changed row BY.'
seoTitle: 'RS-DEPLOY-002: REPLICA IDENTITY change breaks consumers'
---

**Namespace:** `RS-DEPLOY` · **Code:** `RS-DEPLOY-002`

REPLICA IDENTITY controls what a logical decoding stream can identify a changed row BY. Changing it raises no error and does not affect queries, so nothing about applying the migration reveals a problem — but logical replication subscribers, CDC pipelines and analytics mirrors can silently start receiving UPDATE and DELETE events they cannot match to a row. NOTHING is the extreme case: those events carry no old-row identity at all. FULL is the safe-but-costly direction, writing the entire old row into WAL on every UPDATE and DELETE.

## Remediation

Check what consumes this table's changes before changing its identity: SELECT * FROM pg_publication_tables WHERE tablename = '<table>'; and check for active replication slots with SELECT * FROM pg_replication_slots. If you are dropping the index that backs REPLICA IDENTITY USING INDEX, set a new identity BEFORE dropping it, not after. If you need FULL, size the WAL increase first — it is proportional to update volume, not table size.

## References

- PRD §10

---

This page is generated from the same catalog `rowshape explain RS-DEPLOY-002` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool.
