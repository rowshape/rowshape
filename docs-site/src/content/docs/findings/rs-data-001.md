---
title: 'RS-DATA-001 — SET NOT NULL against existing NULLs'
description: 'SET NOT NULL scans the table and rejects rows that are NULL. If the column''s null_fraction is above zero, the migration fails on contact.'
seoTitle: 'RS-DATA-001: SET NOT NULL against existing NULLs'
---

**Namespace:** `RS-DATA` · **Code:** `RS-DATA-001`

## What it means

SET NOT NULL scans the table and rejects rows that are NULL. If the column's null_fraction is above zero the migration fails; if the zero is only estimated, it cannot be certified safe.

## Why it matters

SET NOT NULL scans the whole table under ACCESS EXCLUSIVE and fails at the first NULL, so the migration both blocks the table and fails. Test databases rarely have NULLs where production does, which is why it passes everywhere else first.

## Example

Production shape: public.users has 400,000 rows and 3.2% of email is NULL (counted exactly). This migration produces `RS-DATA-001` against it:

```sql
ALTER TABLE public.users ALTER COLUMN email SET NOT NULL;
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
      email: {type: text, nullable: true, null_fraction: {value: 0.032, confidence: exact}}
    constraints: [{name: users_pkey, kind: primary_key, columns: [id]}]
```

</details>

## How to fix

Backfill or delete the NULL rows first, or add a DEFAULT; then SET NOT NULL. A validated CHECK (col IS NOT NULL) lets SET NOT NULL skip the full-table scan on PG 12+.

**For the example above.** Backfill or delete the NULL rows first (in batches — see RS-PERF-010), ship that, and re-pull the fixture so it records zero NULLs. Then the constraint is safe to add.

```sql
SET lock_timeout = '3s';
ALTER TABLE public.users ALTER COLUMN email SET NOT NULL;
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
      email: {type: text, nullable: true, null_fraction: {value: 0, confidence: exact}}
    constraints: [{name: users_pkey, kind: primary_key, columns: [id]}]
```

</details>

## Related codes

- RS-DEPLOY-003 — Dropping NOT NULL withdraws a guarantee running code relies on
- RS-APPLY-001 — Migration did not apply
- RS-PERF-010 — A qualified UPDATE/DELETE on a large table rewrites many rows in one statement

## References

- [Fixture spec §7.4: Verdict capping (the payoff)](/spec/)

---

This page is generated from the same catalog `rowshape explain RS-DATA-001` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool. The example is run against PostgreSQL 16 by rowshape's test suite: the first migration must produce RS-DATA-001 and the fix must not fail.
