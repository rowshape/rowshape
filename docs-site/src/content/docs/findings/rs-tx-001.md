---
title: 'RS-TX-001 — This statement cannot run inside a transaction block'
description: 'Postgres refuses a handful of statements inside a transaction block with SQLSTATE 25001: CREATE/DROP INDEX CONCURRENTLY, REINDEX CONCURRENTLY, VACUUM, CLUSTER, CREATE/DROP DATABASE and TABLESPACE, ALTER SYSTEM, and (before PostgreSQL 12) ALTER TYPE .'
---

**Namespace:** `RS-TX` · **Code:** `RS-TX-001`

Postgres refuses a handful of statements inside a transaction block with SQLSTATE 25001: CREATE/DROP INDEX CONCURRENTLY, REINDEX CONCURRENTLY, VACUUM, CLUSTER, CREATE/DROP DATABASE and TABLESPACE, ALTER SYSTEM, and (before PostgreSQL 12) ALTER TYPE ... ADD VALUE. Most migration runners — Alembic, Django, Rails, Flyway — wrap each migration file in a transaction by default, so a file that looks fine standalone fails under the runner. rowshape's own sandbox does not execute your BEGIN/COMMIT, applying each statement in its own transaction so locks can be inspected, so a clean apply is NOT evidence that this works.

## Remediation

Move the statement into its own migration that runs outside a transaction, and tell your runner. Alembic: set transaction_per_migration and use an autocommit block. Django: set atomic = False on the Migration class. Rails: disable_ddl_transaction!. Flyway: use a script-based migration or set executeInTransaction to false. golang-migrate: it does not wrap by default, so no change is needed. If you opened the transaction yourself with BEGIN, split the statement out of that block.

## References

- PRD §10
- PRD §13

---

This page is generated from the same catalog `rowshape explain RS-TX-001` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool.
