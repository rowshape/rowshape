---
title: 'RS-DATA-001 — SET NOT NULL against existing NULLs'
description: 'SET NOT NULL scans the table and rejects rows that are NULL.'
seoTitle: 'RS-DATA-001: SET NOT NULL against existing NULLs'
---

**Namespace:** `RS-DATA` · **Code:** `RS-DATA-001`

SET NOT NULL scans the table and rejects rows that are NULL. If the column's null_fraction is above zero the migration fails; if the zero is only estimated, it cannot be certified safe.

## Remediation

Backfill or delete the NULL rows first, or add a DEFAULT; then SET NOT NULL. A validated CHECK (col IS NOT NULL) lets SET NOT NULL skip the full-table scan on PG 12+.

## References

- RFC §7.4
- PRD §10

---

This page is generated from the same catalog `rowshape explain RS-DATA-001` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool.
