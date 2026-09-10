---
title: 'RS-CONSTRAINT-010 — CHECK constraint conflicts with existing data'
description: 'The column''s profiled range violates the CHECK predicate, so existing rows already fail it and adding the constraint (or validating it) fails.'
seoTitle: 'RS-CONSTRAINT-010: CHECK conflicts with existing data'
---

**Namespace:** `RS-CONSTRAINT` · **Code:** `RS-CONSTRAINT-010`

The column's profiled range violates the CHECK predicate, so existing rows already fail it and adding the constraint (or validating it) fails.

## Remediation

Repair or exclude the rows that violate the predicate before adding the CHECK (or widen the predicate). Add the constraint NOT VALID, fix the data, then VALIDATE.

## References

- RFC §6.1
- RFC §6.4
- PRD §10

---

This page is generated from the same catalog `rowshape explain RS-CONSTRAINT-010` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool.
