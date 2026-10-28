---
title: 'RS-DEPLOY-003 — Dropping NOT NULL withdraws a guarantee running code relies on'
description: 'Dropping NOT NULL is instant for the database, but it withdraws a guarantee that application code, ORMs and downstream schemas were written against.'
seoTitle: 'RS-DEPLOY-003: dropping NOT NULL breaks running code'
---

**Namespace:** `RS-DEPLOY` · **Code:** `RS-DEPLOY-003`

## What it means

The migration is instant and safe for the database. The hazard is the contract: application code, ORM models, serializers and downstream schemas were written against a column that could never be null, and none of them are re-checked when that guarantee is withdrawn. Nothing fails at migration time — the first null arrives later, at runtime, somewhere else. This is the reverse of RS-DATA-001, which covers adding the constraint.

## Why it matters

Code written against a NOT NULL column does not check for NULL. Once the constraint is gone, the first NULL written, by a new code path, an import or a backfill, surfaces as an error or a wrong answer somewhere else, long after the migration that allowed it.

## Example

Production shape: public.events has 20,000,000 rows and note has never been NULL. This migration produces `RS-DEPLOY-003` against it:

```sql
ALTER TABLE public.events ALTER COLUMN note DROP NOT NULL;
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

Ship the code that TOLERATES a null first, then relax the constraint in a later migration. That is the same expand/contract ordering a rename needs, in the other direction: make the readers safe before the writers are allowed to produce the new shape. If the column is exposed through an API or a downstream schema, check those contracts too — a nullable column is a breaking change to a consumer that declared it required.

**For the example above.** Ship the application code that tolerates a NULL note first, including any API schema or downstream consumer that declared the field required. Then run the same migration.

```sql
-- Only after every reader of events.note handles NULL:
SET lock_timeout = '3s';
ALTER TABLE public.events ALTER COLUMN note DROP NOT NULL;
```

:::note[rowshape still reports RS-DEPLOY-003 here]
The fix is the deploy order, which a migration cannot show, so rowshape still reports the code as a reminder.
:::

## Related codes

- RS-DATA-001 — SET NOT NULL against existing NULLs
- RS-DEPLOY-001 — Renaming a column or table breaks running code that still uses the old name

---

This page is generated from the same catalog `rowshape explain RS-DEPLOY-003` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool. The example is run against PostgreSQL 16 by rowshape's test suite: the first migration must produce RS-DEPLOY-003 and the fix must not fail.
