---
title: 'RS-APPLY-001 — Migration did not apply'
description: 'A statement in the migration was rejected by the database, so nothing downstream was evaluated.'
---

**Namespace:** `RS-APPLY` · **Code:** `RS-APPLY-001`

A statement in the migration was rejected by the database, so nothing downstream was evaluated. The verdict carries the engine's own SQLSTATE and message, and the file and line the statement came from.

## Remediation

Read the SQLSTATE and message in the finding's evidence: they are the database's own words about what it refused. Fix the statement at the reported file and line, then re-run validate. A class-23 code (23505 unique_violation, 23502 not_null_violation, 23514 check_violation) means production-shaped DATA rejected it — the migration is syntactically fine and the data does not permit it. A class-42 code (42P01 undefined_table, 42703 undefined_column, 42601 syntax_error) means the statement does not match the schema it was written against.

## References

- PRD §10
- RFC §13

---

This page is generated from the same catalog `rowshape explain RS-APPLY-001` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool.
