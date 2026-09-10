---
title: 'RS-DATA-020 — FOREIGN KEY validated against pre-existing orphans'
description: 'Validating a foreign key scans every child row for a matching parent.'
seoTitle: 'RS-DATA-020: FOREIGN KEY hits pre-existing orphan rows'
---

**Namespace:** `RS-DATA` · **Code:** `RS-DATA-020`

Validating a foreign key scans every child row for a matching parent. If the reference's orphan_fraction is above zero, rows already violate the key and the VALIDATE fails.

## Remediation

Delete or repair the orphaned rows before validating the foreign key: ADD the constraint NOT VALID, clean up the orphans, then VALIDATE CONSTRAINT.

## References

- RFC §6.6
- PRD §10

---

This page is generated from the same catalog `rowshape explain RS-DATA-020` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool.
