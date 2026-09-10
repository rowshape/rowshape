---
title: 'RS-DEPLOY-001 — Renaming a column or table breaks running code that still uses the old name'
description: 'The migration is safe and fast — this is not a lock or a data problem.'
seoTitle: 'RS-DEPLOY-001: renaming a column or table breaks code'
---

**Namespace:** `RS-DEPLOY` · **Code:** `RS-DEPLOY-001`

The migration is safe and fast — this is not a lock or a data problem. The hazard is ordering: from the instant it commits, every application instance still running the previous release references a name that no longer exists, and fails. Rolling deploys and blue/green make that a certainty rather than a race, because old and new code run simultaneously by design. Nothing about applying the migration to a database reveals it, because the database is fine.

## Remediation

Use expand/contract across separate deploys rather than renaming in place. EXPAND: add the new column, and have the application write BOTH names. BACKFILL: copy the existing values across. SWITCH: ship a release that reads the new name. CONTRACT: once no running code references the old name, drop it in a later migration. For a table, the same shape works with a view carrying the old name over the new table while the switch lands.

## References

- PRD §10
- PRD §12

---

This page is generated from the same catalog `rowshape explain RS-DEPLOY-001` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool.
