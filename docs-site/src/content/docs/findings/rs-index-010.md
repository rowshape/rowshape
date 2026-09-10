---
title: 'RS-INDEX-010 — CREATE UNIQUE INDEX without proven uniqueness'
description: 'A unique index can only build if the indexed set is actually unique.'
seoTitle: 'RS-INDEX-010: CREATE UNIQUE INDEX, uniqueness unproven'
---

**Namespace:** `RS-INDEX` · **Code:** `RS-INDEX-010`

A unique index can only build if the indexed set is actually unique. Uniqueness is never inferred from a sample (INV-UNIQUENESS): unproven uniqueness cannot certify PASS, and proven duplicates make the build fail. A PARTIAL index (WHERE ...) or an EXPRESSION index (lower(email)) is a special case: the fixture records uniqueness for the whole column, which describes neither the predicate-selected subset nor the expression, so rowshape declines to decide in either direction and warns instead — duplicates in soft-deleted rows do not stop a `WHERE deleted_at IS NULL` index from building.

## Remediation

Prove uniqueness before creating the unique index. If duplicates already exist, de-duplicate the column first (remove or merge the duplicate rows).

## References

- RFC §6.5
- RFC §7.2
- PRD §10

---

This page is generated from the same catalog `rowshape explain RS-INDEX-010` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool.
