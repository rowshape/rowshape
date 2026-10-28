---
title: 'RS-APPLY-001 — Migration did not apply'
description: 'A statement in the migration was rejected by the database, so nothing downstream was evaluated.'
seoTitle: 'RS-APPLY-001: the migration did not apply'
---

**Namespace:** `RS-APPLY` · **Code:** `RS-APPLY-001`

## What it means

A statement in the migration was rejected by the database, so nothing downstream was evaluated. The verdict carries the engine's own SQLSTATE and message, and the file and line the statement came from.

## Why it matters

When a statement is rejected nothing after it was checked, because Postgres stops there too. If the SQLSTATE is a class-23 code, the data rejected it: production has the same rows, so the deploy will fail at the same statement.

## Example

Production shape: public.users has 400,000 rows. This migration produces `RS-APPLY-001` against it:

```sql
-- Fails on any table that already has rows: every existing row would
-- get NULL in a NOT NULL column (SQLSTATE 23502).
ALTER TABLE public.users ADD COLUMN signup_source text NOT NULL;
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

Read the SQLSTATE and message in the finding's evidence: they are the database's own words about what it refused. Fix the statement at the reported file and line, then re-run validate. A class-23 code (23505 unique_violation, 23502 not_null_violation, 23514 check_violation) means production-shaped DATA rejected it — the migration is syntactically fine and the data does not permit it. A class-42 code (42P01 undefined_table, 42703 undefined_column, 42601 syntax_error) means the statement does not match the schema it was written against.

**For the example above.** Give existing rows a value in the same statement. A constant DEFAULT is a catalog-only change on PostgreSQL 11 and later, so it does not rewrite the table.

```sql
SET lock_timeout = '3s';
ALTER TABLE public.users ADD COLUMN signup_source text NOT NULL DEFAULT 'unknown';
```

## Related codes

- RS-DATA-001 — SET NOT NULL against existing NULLs
- RS-DATA-014 — ADD UNIQUE without proven uniqueness
- RS-CONSTRAINT-010 — CHECK constraint conflicts with existing data
- RS-TX-001 — This statement cannot run inside a transaction block

## References

- [Fixture spec §13: Conformance](/spec/)

---

This page is generated from the same catalog `rowshape explain RS-APPLY-001` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool. The example is run against PostgreSQL 16 by rowshape's test suite: the first migration must produce RS-APPLY-001 and the fix must not fail.
