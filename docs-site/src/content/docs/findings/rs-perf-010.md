---
title: 'RS-PERF-010 — A qualified UPDATE/DELETE on a large table rewrites many rows in one statement'
description: 'RS-PERF-002 only fires when a statement has NO WHERE clause, but the real-world hazard always has one: UPDATE users SET normalized = lower(email) WHERE normalized IS NULL is the canonical unbatched backfill — one statement, tens of millions of rows, one transaction, row locks held for the whole duration, table bloat, and a replication lag spike.'
seoTitle: 'RS-PERF-010: large single-statement UPDATE or DELETE'
---

**Namespace:** `RS-PERF` · **Code:** `RS-PERF-010`

RS-PERF-002 only fires when a statement has NO WHERE clause, but the real-world hazard always has one: UPDATE users SET normalized = lower(email) WHERE normalized IS NULL is the canonical unbatched backfill — one statement, tens of millions of rows, one transaction, row locks held for the whole duration, table bloat, and a replication lag spike. Where the fixture can size the predicate it does (null_fraction is exactly the selectivity of an IS NULL test); where it cannot, it reports the table's row count as an upper bound rather than falling silent.

## Remediation

Batch it. Loop over a bounded key range — UPDATE ... WHERE id BETWEEN :lo AND :hi AND <predicate> — committing each batch, so no single transaction holds locks over the whole table and autovacuum can keep up between batches. Ten to fifty thousand rows per batch is a common starting point. Run the backfill OUTSIDE the migration if your runner wraps migrations in one transaction, since batching inside a single transaction defeats the purpose. If the statement genuinely matches only a handful of rows, the upper bound reported here is conservative and you can disregard it.

## References

- PRD §10

---

This page is generated from the same catalog `rowshape explain RS-PERF-010` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool.
