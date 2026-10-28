---
title: 'RS-INDEX-010 — CREATE UNIQUE INDEX without proven uniqueness'
description: 'A unique index only builds if the indexed set really is unique, and rowshape never infers uniqueness from a sample — unproven cannot be a PASS.'
seoTitle: 'RS-INDEX-010: CREATE UNIQUE INDEX, uniqueness unproven'
---

**Namespace:** `RS-INDEX` · **Code:** `RS-INDEX-010`

## What it means

A unique index can only build if the indexed set is actually unique. rowshape never infers uniqueness from a sample, so uniqueness that is not proven cannot certify a PASS, and proven duplicates make the build fail. A PARTIAL index (WHERE ...) or an EXPRESSION index (lower(email)) is a special case: the fixture records uniqueness for the whole column, which describes neither the predicate-selected subset nor the expression, so rowshape declines to decide in either direction and warns instead — duplicates in soft-deleted rows do not stop a `WHERE deleted_at IS NULL` index from building.

## Why it matters

A unique index build that meets a duplicate fails after doing most of the work. Built CONCURRENTLY, it also leaves an INVALID index behind that has to be dropped by hand before the migration can be retried.

## Example

Production shape: public.users has 400,000 rows and a probe proved email is NOT unique: some addresses appear more than once. This migration produces `RS-INDEX-010` against it:

```sql
CREATE UNIQUE INDEX CONCURRENTLY users_email_key ON public.users (email);
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
      email: {type: text, nullable: true, unique: {value: false, confidence: exact, via: probe}}
    constraints: [{name: users_pkey, kind: primary_key, columns: [id]}]
```

</details>

## How to fix

Prove uniqueness before creating the unique index. If duplicates already exist, de-duplicate the column first (remove or merge the duplicate rows).

**For the example above.** Find the duplicates, merge or delete them, ship that, and re-pull with `rowshape pull --exact public.users.email` so the fixture proves uniqueness. Then the same statement builds.

```sql
-- Find them first:
--   SELECT email, count(*) FROM public.users GROUP BY email HAVING count(*) > 1;
CREATE UNIQUE INDEX CONCURRENTLY users_email_key ON public.users (email);
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

- RS-DATA-014 — ADD UNIQUE without proven uniqueness
- RS-INDEX-002 — ADD PRIMARY KEY or UNIQUE builds an index under ACCESS EXCLUSIVE
- RS-TX-001 — This statement cannot run inside a transaction block

## References

- [Fixture spec §6.5: Indexes](/spec/)
- [Fixture spec §7.2: Uniqueness is never estimated](/spec/)

---

This page is generated from the same catalog `rowshape explain RS-INDEX-010` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool. The example is run against PostgreSQL 16 by rowshape's test suite: the first migration must produce RS-INDEX-010 and the fix must not fail.
