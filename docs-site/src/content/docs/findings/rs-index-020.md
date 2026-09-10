---
title: 'RS-INDEX-020 — Non-concurrent REINDEX rebuilds under lock'
description: 'A non-concurrent REINDEX rewrites the whole index while holding a lock that blocks writes.'
seoTitle: 'RS-INDEX-020: non-concurrent REINDEX rebuilds under lock'
---

**Namespace:** `RS-INDEX` · **Code:** `RS-INDEX-020`

A non-concurrent REINDEX rewrites the whole index while holding a lock that blocks writes. Its cost is driven by the index's on-disk size and bloat.

## Remediation

Use REINDEX INDEX CONCURRENTLY (PG 12+) so the rebuild does not block writes.

## References

- RFC §6.5
- PRD §10

---

This page is generated from the same catalog `rowshape explain RS-INDEX-020` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool.
