---
title: 'RS-REVERSE-001 — DROP COLUMN loses its data irreversibly'
description: 'Dropping a column permanently removes its values across every row. A down-migration can recreate the column, but not what it held.'
seoTitle: 'RS-REVERSE-001: DROP COLUMN loses its data irreversibly'
---

**Namespace:** `RS-REVERSE` · **Code:** `RS-REVERSE-001`

## What it means

Dropping a column permanently removes its values across every row. A down-migration can recreate the column, but not what it held — the rollback is lossy.

## Why it matters

A down-migration that recreates the column brings it back empty. If the drop was a mistake, or the release has to be rolled back, the values are gone unless you kept a copy.

## Example

Production shape: public.users has 400,000 rows, most with a legacy_notes value. This migration produces `RS-REVERSE-001` against it:

```sql
ALTER TABLE public.users DROP COLUMN legacy_notes;
```

<details>
<summary>The fixture this example is checked against</summary>

```yaml
rowshape_fixture: "1"
meta: {id: example, engine: {name: postgres, version: "16"}, privacy: standard, profile: {mode: fast, escalated: []}}
tables:
  public.users:
    rows: {value: 400000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: constraint}}
      legacy_notes: {type: text, nullable: true, null_fraction: {value: 0.2, confidence: estimated, via: pg_stats}}
    constraints: [{name: users_pkey, kind: primary_key, columns: [id]}]
```

</details>

## How to fix

Drop in two phases across separate deploys: first stop writing and reading the column and ship that, then drop it in a later migration once you are sure it is unused. Take a backup (or snapshot the column into an archive table) before the drop so the data is recoverable.

**For the example above.** Stop reading and writing the column and ship that first. Keep a copy of the values, then drop the column in a later migration.

```sql title="Migration 1: keep the values recoverable"
CREATE TABLE public.users_legacy_notes_archive AS
  SELECT id, legacy_notes FROM public.users WHERE legacy_notes IS NOT NULL;
```

```sql title="Migration 2, a later deploy, once nothing references the column:"
SET lock_timeout = '3s';
ALTER TABLE public.users DROP COLUMN legacy_notes;
```

:::note[rowshape still reports RS-REVERSE-001 here]
A DROP COLUMN always loses data, so it is always reported. What the archive changes is that a rollback can restore it.
:::

## Related codes

- RS-REVERSE-002 — DROP TABLE loses every row irreversibly
- RS-DEPLOY-001 — Renaming a column or table breaks running code that still uses the old name
- RS-REVERSE-003 — Narrowing a column type can truncate data irreversibly

---

This page is generated from the same catalog `rowshape explain RS-REVERSE-001` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool. The example is run against PostgreSQL 16 by rowshape's test suite: the first migration must produce RS-REVERSE-001 and the fix must not fail.
