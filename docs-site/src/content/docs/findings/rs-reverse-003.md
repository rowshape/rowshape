---
title: 'RS-REVERSE-003 — Narrowing a column type can truncate data irreversibly'
description: 'Narrowing a column''s type (a wider integer to a smaller one, an unbounded string to a length-limited one, or a fractional number to an integer) can truncate or round values.'
seoTitle: 'RS-REVERSE-003: narrowing a column type truncates data'
---

**Namespace:** `RS-REVERSE` · **Code:** `RS-REVERSE-003`

Narrowing a column's type (a wider integer to a smaller one, an unbounded string to a length-limited one, or a fractional number to an integer) can truncate or round values. Widening back cannot restore the lost precision.

## Remediation

Keep the wider type, or migrate without loss: add a new column of the target type, backfill it while checking every value fits, swap reads and writes over, then drop the old column in a later migration. Take a backup before any in-place narrowing.

## References

- PRD §10
- PRD §12

---

This page is generated from the same catalog `rowshape explain RS-REVERSE-003` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool.
