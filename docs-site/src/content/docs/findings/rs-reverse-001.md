---
title: 'RS-REVERSE-001 — DROP COLUMN loses its data irreversibly'
description: 'Dropping a column permanently removes its values across every row.'
seoTitle: 'RS-REVERSE-001: DROP COLUMN loses its data irreversibly'
---

**Namespace:** `RS-REVERSE` · **Code:** `RS-REVERSE-001`

Dropping a column permanently removes its values across every row. A down-migration can recreate the column, but not what it held — the rollback is lossy.

## Remediation

Drop in two phases across separate deploys: first stop writing and reading the column and ship that, then drop it in a later migration once you are sure it is unused. Take a backup (or snapshot the column into an archive table) before the drop so the data is recoverable.

## References

- PRD §10
- PRD §12

---

This page is generated from the same catalog `rowshape explain RS-REVERSE-001` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool.
