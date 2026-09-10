---
title: 'RS-LOCK-011 — Several statements take ACCESS EXCLUSIVE on the same table'
description: 'Each lock is acquired separately, so the table is unavailable across the whole sequence rather than for the longest single statement.'
seoTitle: 'RS-LOCK-011: repeated ACCESS EXCLUSIVE on one table'
---

**Namespace:** `RS-LOCK` · **Code:** `RS-LOCK-011`

Each lock acquisition queues independently, so the table is unavailable across the whole sequence rather than for the duration of the longest single statement — and between them, traffic that built up during one lock competes for the next. The individual statements can each look perfectly reasonable; the cost is in the repetition.

## Remediation

Combine the changes into a single ALTER TABLE with comma-separated actions — ALTER TABLE t ADD COLUMN a int, ADD COLUMN b int, ALTER COLUMN c SET NOT NULL — which takes the lock once. Where the operations genuinely cannot be combined, split them into separate migrations so each takes its lock independently and a failure does not leave the table half-changed.

## References

- PRD §10

---

This page is generated from the same catalog `rowshape explain RS-LOCK-011` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool.
