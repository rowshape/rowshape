---
title: 'RS-DEPLOY-001 — Renaming a column or table breaks running code that still uses the old name'
description: 'The migration is safe and fast — this is not a lock or a data problem.'
seoTitle: 'RS-DEPLOY-001: renaming a column or table breaks code'
---

**Namespace:** `RS-DEPLOY` · **Code:** `RS-DEPLOY-001`

## What it means

The migration is safe and fast — this is not a lock or a data problem. The hazard is ordering: from the instant it commits, every application instance still running the previous release references a name that no longer exists, and fails. Rolling deploys and blue/green make that a certainty rather than a race, because old and new code run simultaneously by design. Nothing about applying the migration to a database reveals it, because the database is fine.

## Why it matters

During a rolling or blue/green deploy the old and new releases run side by side. From the moment the rename commits, every request the old release serves that touches the old name fails, until the last old instance is gone.

## Example

Production shape: public.users has 400,000 rows and the running application reads users.email. This migration produces `RS-DEPLOY-001` against it:

```sql
ALTER TABLE public.users RENAME COLUMN email TO email_address;
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

Use expand/contract across separate deploys rather than renaming in place. EXPAND: add the new column, and have the application write BOTH names. BACKFILL: copy the existing values across. SWITCH: ship a release that reads the new name. CONTRACT: once no running code references the old name, drop it in a later migration. For a table, the same shape works with a view carrying the old name over the new table while the switch lands.

**For the example above.** Expand, then contract, across separate deploys. This migration is the expand step; the application then writes both columns, a batched job copies the old values across, a release switches reads to email_address, and a later migration drops email.

```sql
SET lock_timeout = '3s';
ALTER TABLE public.users ADD COLUMN email_address text;
```

## Related codes

- RS-DEPLOY-003 — Dropping NOT NULL withdraws a guarantee running code relies on
- RS-REVERSE-001 — DROP COLUMN loses its data irreversibly
- RS-REVERSE-002 — DROP TABLE loses every row irreversibly

---

This page is generated from the same catalog `rowshape explain RS-DEPLOY-001` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool. The example is run against PostgreSQL 16 by rowshape's test suite: the first migration must produce RS-DEPLOY-001 and the fix must not fail.
