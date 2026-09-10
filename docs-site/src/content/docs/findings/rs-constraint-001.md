---
title: 'RS-CONSTRAINT-001 — NOT VALID constraint validated in the same transaction'
description: 'Adding a constraint NOT VALID and VALIDATE-ing it in one transaction still runs the full validating scan under the transaction''s locks — the two-step split whose entire purpose is to avoid a long lock buys nothing.'
seoTitle: 'RS-CONSTRAINT-001: NOT VALID validated in the same tx'
---

**Namespace:** `RS-CONSTRAINT` · **Code:** `RS-CONSTRAINT-001`

Adding a constraint NOT VALID and VALIDATE-ing it in one transaction still runs the full validating scan under the transaction's locks — the two-step split whose entire purpose is to avoid a long lock buys nothing.

## Remediation

Split across transactions: ADD CONSTRAINT ... NOT VALID and COMMIT, then VALIDATE CONSTRAINT in a separate transaction. VALIDATE then takes only a SHARE UPDATE EXCLUSIVE lock and does not block reads or writes.

## References

- RFC §6.4
- RFC §9.1
- PRD §10

---

This page is generated from the same catalog `rowshape explain RS-CONSTRAINT-001` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool.
