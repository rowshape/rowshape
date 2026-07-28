---
title: 'RS-LOCK-002 — ADD PRIMARY KEY builds an index under ACCESS EXCLUSIVE'
description: 'ADD PRIMARY KEY builds a unique index over every row and holds ACCESS EXCLUSIVE for the whole build, blocking reads and writes.'
---

**Namespace:** `RS-LOCK` · **Code:** `RS-LOCK-002`

ADD PRIMARY KEY builds a unique index over every row and holds ACCESS EXCLUSIVE for the whole build, blocking reads and writes. There is no CONCURRENTLY form of ADD PRIMARY KEY, so the operation cannot be made online directly.

## Remediation

Build the index first and then adopt it: CREATE UNIQUE INDEX CONCURRENTLY idx ON t (id); then ALTER TABLE t ADD CONSTRAINT t_pkey PRIMARY KEY USING INDEX idx; The second statement still takes ACCESS EXCLUSIVE but only briefly, because the index already exists and does not have to be built under the lock. The column must already be NOT NULL — add that separately, and check RS-DATA-001 for how.

## References

- PRD §10
- RFC §9.1

---

This page is generated from the same catalog `rowshape explain RS-LOCK-002` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool.
