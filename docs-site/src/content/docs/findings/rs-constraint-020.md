---
title: 'RS-CONSTRAINT-020 — ADD CONSTRAINT without NOT VALID scans the whole table under ACCESS EXCLUSIVE'
description: 'Adding a CHECK or FOREIGN KEY constraint without NOT VALID validates every existing row before the statement returns, holding ACCESS EXCLUSIVE for the whole scan — so reads and writes on the table block for its duration.'
seoTitle: 'RS-CONSTRAINT-020: ADD CONSTRAINT without NOT VALID'
---

**Namespace:** `RS-CONSTRAINT` · **Code:** `RS-CONSTRAINT-020`

Adding a CHECK or FOREIGN KEY constraint without NOT VALID validates every existing row before the statement returns, holding ACCESS EXCLUSIVE for the whole scan — so reads and writes on the table block for its duration. This SUCCEEDS when the data is valid; the cost is the lock, not the outcome, which is why applying it to a small or freshly-hydrated table reveals nothing about what it does in production.

## Remediation

Split it in two. First ADD CONSTRAINT ... NOT VALID, which takes only a brief lock and applies to new and updated rows immediately. Then, in a separate statement (and ideally a separate migration), VALIDATE CONSTRAINT, which scans the table under a SHARE UPDATE EXCLUSIVE lock that does not block reads or writes. Keep the two apart: doing both in one transaction holds the exclusive lock across the scan anyway and gains nothing (RS-CONSTRAINT-001).

## References

- PRD §10
- RFC §9.1

---

This page is generated from the same catalog `rowshape explain RS-CONSTRAINT-020` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool.
