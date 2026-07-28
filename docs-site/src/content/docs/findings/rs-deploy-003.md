---
title: 'RS-DEPLOY-003 — Dropping NOT NULL withdraws a guarantee running code relies on'
description: 'The migration is instant and safe for the database.'
---

**Namespace:** `RS-DEPLOY` · **Code:** `RS-DEPLOY-003`

The migration is instant and safe for the database. The hazard is the contract: application code, ORM models, serializers and downstream schemas were written against a column that could never be null, and none of them are re-checked when that guarantee is withdrawn. Nothing fails at migration time — the first null arrives later, at runtime, somewhere else. This is the reverse of RS-DATA-001, which covers adding the constraint.

## Remediation

Ship the code that TOLERATES a null first, then relax the constraint in a later migration. That is the same expand/contract ordering a rename needs, in the other direction: make the readers safe before the writers are allowed to produce the new shape. If the column is exposed through an API or a downstream schema, check those contracts too — a nullable column is a breaking change to a consumer that declared it required.

## References

- PRD §10

---

This page is generated from the same catalog `rowshape explain RS-DEPLOY-003` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool.
