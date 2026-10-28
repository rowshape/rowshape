---
title: 'RS-INDEX-002 — ADD PRIMARY KEY or UNIQUE builds an index under ACCESS EXCLUSIVE'
description: 'Adding a PRIMARY KEY or UNIQUE constraint builds a unique index under an ACCESS EXCLUSIVE lock — no reads or writes for the whole build.'
seoTitle: 'RS-INDEX-002: ADD PRIMARY KEY or UNIQUE locks the table'
sidebar:
  label: 'RS-INDEX-002'
---

**Namespace:** `RS-INDEX` · **Code:** `RS-INDEX-002`

## What it means

Adding a PRIMARY KEY or UNIQUE constraint over existing data builds a unique index while holding an ACCESS EXCLUSIVE lock — no reads or writes proceed for the whole O(n log n) build, and ADD PRIMARY KEY also scans the column for NULLs. On a large table that is a full outage, not just a write block. This is the lock cost of building the constraint, separate from whether the data lets it build at all (RS-DATA-014).

## Why it matters

Unlike CREATE INDEX, adding the constraint blocks reads as well as writes, for the whole build. The data can be perfectly unique and the migration still takes the table offline for as long as the index takes to build.

## Example

Production shape: public.events has 20,000,000 rows, its id is proven unique, and it has no primary key yet. This migration produces `RS-INDEX-002` against it:

```sql
ALTER TABLE public.events ADD CONSTRAINT events_pkey PRIMARY KEY (id);
```

<details>
<summary>The fixture this example is checked against</summary>

```yaml
rowshape_fixture: "1"
meta: {id: example, engine: {name: postgres, version: "16"}, privacy: standard, profile: {mode: fast, escalated: []}}
tables:
  public.events:
    rows: {value: 20000000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: probe}}
      note: {type: text, nullable: false}
      created_at: {type: timestamp with time zone, nullable: false}
    indexes: [{name: events_note_idx, columns: [note], bytes: 400000000}]
```

</details>

## How to fix

Build the index first without the exclusive lock, then adopt it: CREATE UNIQUE INDEX CONCURRENTLY on the column(s), then attach it with ALTER TABLE ... ADD PRIMARY KEY/UNIQUE USING INDEX <name>, which holds the exclusive lock only briefly. For a PRIMARY KEY, ensure the column is already NOT NULL first (add a validated CHECK (col IS NOT NULL) if needed).

**For the example above.** Build the unique index first without the exclusive lock, then adopt it. Adopting holds ACCESS EXCLUSIVE only briefly.

```sql title="Migration 1: its own file, outside a transaction (see RS-TX-001)"
CREATE UNIQUE INDEX CONCURRENTLY events_pkey_idx ON public.events (id);
```

```sql title="Migration 2"
SET lock_timeout = '3s';
ALTER TABLE public.events ADD CONSTRAINT events_pkey PRIMARY KEY USING INDEX events_pkey_idx;
```

## Related codes

- RS-DATA-014 — ADD UNIQUE without proven uniqueness
- RS-INDEX-010 — CREATE UNIQUE INDEX without proven uniqueness
- RS-INDEX-001 — Non-concurrent CREATE INDEX blocks writes
- RS-TX-001 — This statement cannot run inside a transaction block

## References

- [Fixture spec §6.5: Indexes](/spec/)
- [Fixture spec §9.1: Extrapolation is model-based, not linear](/spec/)

---

This page is generated from the same catalog `rowshape explain RS-INDEX-002` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool. The example is run against PostgreSQL 16 by rowshape's test suite: the first migration must produce RS-INDEX-002 and the fix must not fail.
