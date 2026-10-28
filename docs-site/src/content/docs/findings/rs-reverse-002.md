---
title: 'RS-REVERSE-002 — DROP TABLE loses every row irreversibly'
description: 'Dropping a table permanently removes every row. A down-migration can recreate the table structure, but not the data it contained.'
seoTitle: 'RS-REVERSE-002: DROP TABLE loses every row irreversibly'
sidebar:
  label: 'RS-REVERSE-002'
---

**Namespace:** `RS-REVERSE` · **Code:** `RS-REVERSE-002`

## What it means

Dropping a table permanently removes every row. A down-migration can recreate the table structure, but not its data — the rollback cannot restore what was there.

## Why it matters

There is no down-migration for the data. If anything still read the table, or the release has to be rolled back, the only way back is a restore.

## Example

Production shape: public.audit_log has 88,000,000 rows. This migration produces `RS-REVERSE-002` against it:

```sql
DROP TABLE public.audit_log;
```

<details>
<summary>The fixture this example is checked against</summary>

```yaml
rowshape_fixture: "1"
meta: {id: example, engine: {name: postgres, version: "16"}, privacy: standard, profile: {mode: fast, escalated: []}}
tables:
  public.audit_log:
    rows: {value: 88000000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: constraint}}
      action: {type: text, nullable: false}
    constraints: [{name: audit_log_pkey, kind: primary_key, columns: [id]}]
```

</details>

## How to fix

Drop in two phases across separate deploys: first stop using the table and ship that, then drop it in a later migration once you are sure it is unreferenced. Take a backup (or rename it aside — ALTER TABLE ... RENAME TO — rather than dropping) so the data is recoverable.

**For the example above.** Stop using the table and ship that. Then rename it aside rather than dropping it, and drop it for good only once you are sure nothing needs it. (rowshape reports the rename as RS-DEPLOY-001, which is expected: by then nothing should reference the old name.)

```sql
SET lock_timeout = '3s';
ALTER TABLE public.audit_log RENAME TO audit_log_retired;
```

## Related codes

- RS-REVERSE-001 — DROP COLUMN loses its data irreversibly
- RS-REVERSE-004 — TRUNCATE removes every row irreversibly and locks the table
- RS-DEPLOY-001 — Renaming a column or table breaks running code that still uses the old name

---

This page is generated from the same catalog `rowshape explain RS-REVERSE-002` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool. The example is run against PostgreSQL 16 by rowshape's test suite: the first migration must produce RS-REVERSE-002 and the fix must not fail.
