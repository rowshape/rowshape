---
title: 'RS-DATA-014 — ADD UNIQUE without proven uniqueness'
description: 'ADD CONSTRAINT UNIQUE can only succeed if the column is actually unique.'
seoTitle: 'RS-DATA-014: ADD UNIQUE without proven uniqueness'
---

**Namespace:** `RS-DATA` · **Code:** `RS-DATA-014`

ADD CONSTRAINT UNIQUE can only succeed if the column is actually unique. rowshape never infers uniqueness from a sample, so uniqueness that is not proven cannot certify a PASS, and proven duplicates make the constraint fail to build.

## Remediation

Prove uniqueness before adding the constraint. If duplicates already exist, de-duplicate the column first (remove or merge the duplicate rows).

## References

- [Fixture spec §7.2: Uniqueness is never estimated](/spec/)
- [Fixture spec §7.4: Verdict capping (the payoff)](/spec/)

---

This page is generated from the same catalog `rowshape explain RS-DATA-014` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool.
