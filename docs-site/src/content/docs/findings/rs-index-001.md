---
title: 'RS-INDEX-001 — Non-concurrent CREATE INDEX blocks writes'
description: 'A plain CREATE INDEX holds a lock that blocks writes for the whole O(n log n) build.'
seoTitle: 'RS-INDEX-001: non-concurrent CREATE INDEX blocks writes'
---

**Namespace:** `RS-INDEX` · **Code:** `RS-INDEX-001`

A plain CREATE INDEX holds a lock that blocks writes for the whole O(n log n) build. On a large table that is a long write outage.

## Remediation

Use CREATE INDEX CONCURRENTLY: it builds in two passes without an exclusive lock, so writes continue. Run it outside a transaction block.

## References

- RFC §6.5
- RFC §9.1
- PRD §10

---

This page is generated from the same catalog `rowshape explain RS-INDEX-001` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool.
