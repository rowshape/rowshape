---
title: 'RS-DATA-014 — ADD UNIQUE without proven uniqueness'
description: 'ADD CONSTRAINT UNIQUE can only succeed if the column is actually unique.'
seoTitle: 'RS-DATA-014: ADD UNIQUE without proven uniqueness'
---

**Namespace:** `RS-DATA` · **Code:** `RS-DATA-014`

## What it means

ADD CONSTRAINT UNIQUE can only succeed if the column is actually unique. rowshape never infers uniqueness from a sample, so uniqueness that is not proven cannot certify a PASS, and proven duplicates make the constraint fail to build.

## Why it matters

If the column has duplicates the constraint fails to build, and the migration fails after building most of a unique index under ACCESS EXCLUSIVE. rowshape treats "probably unique" as unknown, because a sample cannot see the two rows that collide.

## Example

Production shape: public.users has 400,000 rows and nothing proves email is unique: there is no unique index on it, and uniqueness is never guessed from a sample. This migration produces `RS-DATA-014` against it:

```sql
ALTER TABLE public.users ADD CONSTRAINT users_email_key UNIQUE (email);
```

<details>
<summary>The fixture this example is checked against</summary>

```yaml
rowshape_fixture: "1"
meta: {id: example, engine: {name: postgres, version: "16"}, privacy: standard, profile: {mode: exact, escalated: []}}
tables:
  public.users:
    rows: {value: 400000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: constraint}}
      email: {type: text, nullable: true}
    constraints: [{name: users_pkey, kind: primary_key, columns: [id]}]
```

</details>

## How to fix

Prove uniqueness before adding the constraint. If duplicates already exist, de-duplicate the column first (remove or merge the duplicate rows).

**For the example above.** Prove uniqueness with `rowshape pull --exact public.users.email` (de-duplicate first if it finds duplicates). With uniqueness proven, build the index without the exclusive lock and attach it.

```sql title="Migration 1: its own file, outside a transaction (see RS-TX-001)"
CREATE UNIQUE INDEX CONCURRENTLY users_email_key ON public.users (email);
```

```sql title="Migration 2"
SET lock_timeout = '3s';
ALTER TABLE public.users ADD CONSTRAINT users_email_key UNIQUE USING INDEX users_email_key;
```

<details>
<summary>The re-pulled fixture the fix is checked against</summary>

```yaml
rowshape_fixture: "1"
meta: {id: example, engine: {name: postgres, version: "16"}, privacy: standard, profile: {mode: exact, escalated: []}}
tables:
  public.users:
    rows: {value: 400000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: constraint}}
      email: {type: text, nullable: true, unique: {value: true, confidence: exact, via: probe}}
    constraints: [{name: users_pkey, kind: primary_key, columns: [id]}]
```

</details>

## Related codes

- RS-INDEX-002 — ADD PRIMARY KEY or UNIQUE builds an index under ACCESS EXCLUSIVE
- RS-INDEX-010 — CREATE UNIQUE INDEX without proven uniqueness
- RS-TX-001 — This statement cannot run inside a transaction block

## References

- [Fixture spec §7.2: Uniqueness is never estimated](/spec/)
- [Fixture spec §7.4: Verdict capping (the payoff)](/spec/)

---

This page is generated from the same catalog `rowshape explain RS-DATA-014` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool. The example is run against PostgreSQL 16 by rowshape's test suite: the first migration must produce RS-DATA-014 and the fix must not fail.
