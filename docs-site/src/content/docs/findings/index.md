---
title: Finding catalog
description: Every finding code rowshape can return, what it means, and how to fix it.
---

Every finding rowshape returns carries a permanent, namespaced code, and every
error-severity finding carries remediation. A finding an agent cannot act on is a
bug.

You can read any of this from the CLI with `rowshape explain <CODE>`, or from an
agent with the `explain_finding` MCP tool. These pages are generated from that
same catalog, so they never drift from what the tool returns.

## RS-APPLY

- [`RS-APPLY-001`](./rs-apply-001/) — Migration did not apply

## RS-CONSTRAINT — Constraints that cannot be added or validated

- [`RS-CONSTRAINT-001`](./rs-constraint-001/) — NOT VALID constraint validated in the same transaction
- [`RS-CONSTRAINT-010`](./rs-constraint-010/) — CHECK constraint conflicts with existing data
- [`RS-CONSTRAINT-020`](./rs-constraint-020/) — ADD CONSTRAINT without NOT VALID scans the whole table under ACCESS EXCLUSIVE

## RS-DATA — Existing data that contradicts the change

- [`RS-DATA-001`](./rs-data-001/) — SET NOT NULL against existing NULLs
- [`RS-DATA-014`](./rs-data-014/) — ADD UNIQUE without proven uniqueness
- [`RS-DATA-020`](./rs-data-020/) — FOREIGN KEY validated against pre-existing orphans

## RS-DEPLOY

- [`RS-DEPLOY-001`](./rs-deploy-001/) — Renaming a column or table breaks running code that still uses the old name
- [`RS-DEPLOY-002`](./rs-deploy-002/) — Changing REPLICA IDENTITY changes what downstream consumers can identify
- [`RS-DEPLOY-003`](./rs-deploy-003/) — Dropping NOT NULL withdraws a guarantee running code relies on

## RS-INDEX — Index builds that fail or block

- [`RS-INDEX-001`](./rs-index-001/) — Non-concurrent CREATE INDEX blocks writes
- [`RS-INDEX-002`](./rs-index-002/) — ADD PRIMARY KEY or UNIQUE builds an index under ACCESS EXCLUSIVE
- [`RS-INDEX-003`](./rs-index-003/) — DROP INDEX without CONCURRENTLY takes ACCESS EXCLUSIVE on the table
- [`RS-INDEX-010`](./rs-index-010/) — CREATE UNIQUE INDEX without proven uniqueness
- [`RS-INDEX-020`](./rs-index-020/) — Non-concurrent REINDEX rebuilds under lock

## RS-LOCK — Locks a migration takes, and for how long

- [`RS-LOCK-001`](./rs-lock-001/) — ACCESS EXCLUSIVE lock for a full table rewrite
- [`RS-LOCK-003`](./rs-lock-003/) — ATTACH PARTITION validates the incoming rows under a lock on the parent
- [`RS-LOCK-010`](./rs-lock-010/) — The migration takes ACCESS EXCLUSIVE without setting lock_timeout
- [`RS-LOCK-011`](./rs-lock-011/) — Several statements take ACCESS EXCLUSIVE on the same table

## RS-PERF — Rewrites and scans that cost more than they look like they do

- [`RS-PERF-001`](./rs-perf-001/) — DELETE cascades through a long-tailed fan-out
- [`RS-PERF-002`](./rs-perf-002/) — Unqualified UPDATE/DELETE touches every row
- [`RS-PERF-010`](./rs-perf-010/) — A qualified UPDATE/DELETE on a large table rewrites many rows in one statement

## RS-REVERSE — Changes that cannot be safely reversed

- [`RS-REVERSE-001`](./rs-reverse-001/) — DROP COLUMN loses its data irreversibly
- [`RS-REVERSE-002`](./rs-reverse-002/) — DROP TABLE loses every row irreversibly
- [`RS-REVERSE-003`](./rs-reverse-003/) — Narrowing a column type can truncate data irreversibly
- [`RS-REVERSE-004`](./rs-reverse-004/) — TRUNCATE removes every row irreversibly and locks the table

## RS-TX

- [`RS-TX-001`](./rs-tx-001/) — This statement cannot run inside a transaction block

