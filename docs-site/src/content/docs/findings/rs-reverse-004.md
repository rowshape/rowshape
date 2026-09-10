---
title: 'RS-REVERSE-004 — TRUNCATE removes every row irreversibly and locks the table'
description: 'TRUNCATE deletes every row in the table and cannot be rolled back once committed.'
seoTitle: 'RS-REVERSE-004: TRUNCATE is irreversible and locks'
---

**Namespace:** `RS-REVERSE` · **Code:** `RS-REVERSE-004`

TRUNCATE deletes every row in the table and cannot be rolled back once committed. It takes an ACCESS EXCLUSIVE lock, so every read and write on the table blocks for its duration, and it does not fire per-row DELETE triggers — so audit or soft-delete logic built on them is silently skipped. Because TRUNCATE succeeds instantly against a small or freshly-hydrated table, nothing about running it reveals how much production data it would destroy.

## Remediation

If you mean to discard the data, take a backup first and say so in the migration. If you mean to remove SOME rows, use DELETE with a WHERE clause. If you are resetting a table between deploys, consider renaming it aside (ALTER TABLE ... RENAME TO) so the rows remain recoverable. TRUNCATE ... CASCADE additionally empties every table with a foreign key into this one — check what that reaches before running it.

## References

- PRD §10
- PRD §12

---

This page is generated from the same catalog `rowshape explain RS-REVERSE-004` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool.
