---
title: 'RS-LOCK-003 — ATTACH PARTITION validates the incoming rows under a lock on the parent'
description: 'ATTACH PARTITION takes ACCESS EXCLUSIVE on the PARENT table — blocking every query against every partition — and then scans the incoming table to prove every row satisfies the partition bound, unless a matching CHECK constraint already exists.'
---

**Namespace:** `RS-LOCK` · **Code:** `RS-LOCK-003`

ATTACH PARTITION takes ACCESS EXCLUSIVE on the PARENT table — blocking every query against every partition — and then scans the incoming table to prove every row satisfies the partition bound, unless a matching CHECK constraint already exists. On a large incoming partition that scan is the outage, and it happens while the whole partitioned table is locked. Non-concurrent DETACH takes the same lock on the parent.

## Remediation

Add a CHECK constraint matching the partition bound to the incoming table BEFORE attaching, and validate it separately: ALTER TABLE incoming ADD CONSTRAINT c CHECK (<bound>) NOT VALID; then VALIDATE CONSTRAINT c; then ATTACH. With the constraint already proven, ATTACH skips the scan and the lock is brief. On PostgreSQL 14+, prefer DETACH PARTITION CONCURRENTLY for the reverse direction.

## References

- PRD §10
- RFC §14.2

---

This page is generated from the same catalog `rowshape explain RS-LOCK-003` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool.
