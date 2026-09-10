---
title: 'RS-PERF-001 — DELETE cascades through a long-tailed fan-out'
description: 'Deleting from a parent table referenced ON DELETE CASCADE cascades to its children.'
seoTitle: 'RS-PERF-001: DELETE cascades through a long fan-out'
---

**Namespace:** `RS-PERF` · **Code:** `RS-PERF-001`

Deleting from a parent table referenced ON DELETE CASCADE cascades to its children. When the fan-out is long-tailed (the max dwarfs the mean), deleting the wrong parents cascades to a huge, slow, lock-holding delete — an outage a uniform mean hides.

## Remediation

Delete in bounded batches (by primary-key range), and check the fan-out tail before deleting parents with many cascaded children. Consider detaching or soft-deleting children first so the cascade is bounded.

## References

- RFC §6.6
- PRD §10

---

This page is generated from the same catalog `rowshape explain RS-PERF-001` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool.
