---
title: 'RS-LOCK-001 — ACCESS EXCLUSIVE lock for a full table rewrite'
description: 'Adding a column with a volatile default, or changing a column''s type, rewrites every row while holding an ACCESS EXCLUSIVE lock.'
seoTitle: 'RS-LOCK-001: ACCESS EXCLUSIVE for a full table rewrite'
---

**Namespace:** `RS-LOCK` · **Code:** `RS-LOCK-001`

Adding a column with a volatile default, or changing a column's type, rewrites every row while holding an ACCESS EXCLUSIVE lock — no reads or writes proceed until it finishes. On a large table that is a write outage.

## Remediation

Avoid the full-table ACCESS EXCLUSIVE rewrite. For a volatile default: ADD the column nullable with no default, backfill in batches, then attach the default and SET NOT NULL via a validated CHECK. For a type change: add a new column of the target type, backfill it in batches, swap reads/writes, and drop the old column. Each step is online.

## References

- RFC §9.1
- PRD §10

---

This page is generated from the same catalog `rowshape explain RS-LOCK-001` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool.
