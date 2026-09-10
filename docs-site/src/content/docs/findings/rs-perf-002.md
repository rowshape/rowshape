---
title: 'RS-PERF-002 — Unqualified UPDATE/DELETE touches every row'
description: 'An UPDATE or DELETE with no WHERE clause rewrites or removes every row of a large table — a slow, lock-holding, bloat-inducing full scan.'
seoTitle: 'RS-PERF-002: unqualified UPDATE or DELETE hits every row'
---

**Namespace:** `RS-PERF` · **Code:** `RS-PERF-002`

An UPDATE or DELETE with no WHERE clause rewrites or removes every row of a large table — a slow, lock-holding, bloat-inducing full scan that is almost never intended.

## Remediation

Add a WHERE clause to scope the change. For a genuine full-table update, run it in bounded batches (by primary-key range) and VACUUM afterward to reclaim the bloat.

## References

- PRD §10

---

This page is generated from the same catalog `rowshape explain RS-PERF-002` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool.
