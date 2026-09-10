---
title: 'RS-LOCK-010 — The migration takes ACCESS EXCLUSIVE without setting lock_timeout'
description: 'With no lock_timeout, a DDL statement that cannot acquire its lock immediately WAITS — and while it waits, every new query on that table queues behind it, because a pending ACCESS EXCLUSIVE request blocks incoming readers too.'
seoTitle: 'RS-LOCK-010: ACCESS EXCLUSIVE without a lock_timeout'
---

**Namespace:** `RS-LOCK` · **Code:** `RS-LOCK-010`

With no lock_timeout, a DDL statement that cannot acquire its lock immediately WAITS — and while it waits, every new query on that table queues behind it, because a pending ACCESS EXCLUSIVE request blocks incoming readers too. A migration that is instant in isolation can therefore stall an entire table behind one long-running transaction it happened to collide with. This is the mechanism behind most 'one quick migration took the site down' incidents, and it is invisible to any per-statement rule because no single statement is wrong.

## Remediation

Set a short timeout at the head of the migration: SET lock_timeout = '3s'; so a statement that cannot get its lock fails fast instead of queueing every reader behind it. Pair it with a retry: a failed migration you can re-run is strictly better than a stalled table. For operations that must eventually succeed on a busy table, retry in a loop with backoff rather than raising the timeout. Note that SET LOCAL scopes it to the transaction, which is usually what you want inside a migration.

## References

- PRD §10

---

This page is generated from the same catalog `rowshape explain RS-LOCK-010` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool.
