---
title: 'RS-INDEX-003 — DROP INDEX without CONCURRENTLY takes ACCESS EXCLUSIVE on the table'
description: 'A non-concurrent DROP INDEX takes ACCESS EXCLUSIVE on the TABLE, not merely on the index — so every read and write on the table queues behind it, and behind anything already holding a conflicting lock.'
---

**Namespace:** `RS-INDEX` · **Code:** `RS-INDEX-003`

A non-concurrent DROP INDEX takes ACCESS EXCLUSIVE on the TABLE, not merely on the index — so every read and write on the table queues behind it, and behind anything already holding a conflicting lock. The drop itself is fast, which is exactly why it looks harmless in a sandbox: the risk is the lock queue on a busy table, not the work.

## Remediation

Use DROP INDEX CONCURRENTLY, which takes only SHARE UPDATE EXCLUSIVE and does not block reads or writes. It cannot run inside a transaction block (see RS-TX-001), so it needs its own migration with the runner's transaction wrapping disabled. Set a short lock_timeout either way, so a drop that cannot get its lock fails fast instead of queueing every query behind it.

## References

- PRD §10

---

This page is generated from the same catalog `rowshape explain RS-INDEX-003` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool.
