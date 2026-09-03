---
title: 'RS-INDEX-002 — ADD PRIMARY KEY or UNIQUE builds an index under ACCESS EXCLUSIVE'
description: 'Adding a PRIMARY KEY or UNIQUE constraint over existing data builds a unique index while holding an ACCESS EXCLUSIVE lock — no reads or writes proceed for the whole O(n log n) build, and ADD PRIMARY KEY also scans the column for NULLs.'
---

**Namespace:** `RS-INDEX` · **Code:** `RS-INDEX-002`

Adding a PRIMARY KEY or UNIQUE constraint over existing data builds a unique index while holding an ACCESS EXCLUSIVE lock — no reads or writes proceed for the whole O(n log n) build, and ADD PRIMARY KEY also scans the column for NULLs. On a large table that is a full outage, not just a write block. This is the lock cost of building the constraint, separate from whether the data lets it build at all (RS-DATA-014).

## Remediation

Build the index first without the exclusive lock, then adopt it: CREATE UNIQUE INDEX CONCURRENTLY on the column(s), then attach it with ALTER TABLE ... ADD PRIMARY KEY/UNIQUE USING INDEX <name>, which holds the exclusive lock only briefly. For a PRIMARY KEY, ensure the column is already NOT NULL first (add a validated CHECK (col IS NOT NULL) if needed).

## References

- RFC §6.5
- RFC §9.1
- PRD §10

---

This page is generated from the same catalog `rowshape explain RS-INDEX-002` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool.
