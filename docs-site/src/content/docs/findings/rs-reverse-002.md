---
title: 'RS-REVERSE-002 — DROP TABLE loses every row irreversibly'
description: 'Dropping a table permanently removes every row. A down-migration can recreate the table structure, but not the data it contained.'
seoTitle: 'RS-REVERSE-002: DROP TABLE loses every row irreversibly'
---

**Namespace:** `RS-REVERSE` · **Code:** `RS-REVERSE-002`

Dropping a table permanently removes every row. A down-migration can recreate the table structure, but not its data — the rollback cannot restore what was there.

## Remediation

Drop in two phases across separate deploys: first stop using the table and ship that, then drop it in a later migration once you are sure it is unreferenced. Take a backup (or rename it aside — ALTER TABLE ... RENAME TO — rather than dropping) so the data is recoverable.

## References

- PRD §10
- PRD §12

---

This page is generated from the same catalog `rowshape explain RS-REVERSE-002` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool.
