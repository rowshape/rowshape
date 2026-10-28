---
title: 'RS-DATA-020 — FOREIGN KEY validated against pre-existing orphans'
description: 'Validating a foreign key scans every child row for a matching parent. If orphaned rows already exist, the VALIDATE fails when it reaches them.'
seoTitle: 'RS-DATA-020: FOREIGN KEY hits pre-existing orphan rows'
---

**Namespace:** `RS-DATA` · **Code:** `RS-DATA-020`

## What it means

Validating a foreign key scans every child row for a matching parent. If the reference's orphan_fraction is above zero, rows already violate the key and the VALIDATE fails.

## Why it matters

Orphans build up wherever the key was never enforced: deletes from another service, a bulk import, a partial restore. Validation scans every row and fails on the first orphan, and a test database never has them.

## Example

Production shape: public.orders has 800,000 rows and 0.4% of them point at a user_id that no longer exists in public.users. This migration produces `RS-DATA-020` against it:

```sql
ALTER TABLE public.orders
  ADD CONSTRAINT orders_user_fk FOREIGN KEY (user_id) REFERENCES public.users (id);
```

<details>
<summary>The fixture this example is checked against</summary>

```yaml
rowshape_fixture: "1"
meta: {id: example, engine: {name: postgres, version: "16"}, privacy: standard, profile: {mode: fast, escalated: []}}
tables:
  public.users:
    rows: {value: 100000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: constraint}}
    constraints: [{name: users_pkey, kind: primary_key, columns: [id]}]
  public.orders:
    rows: {value: 800000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: constraint}}
      user_id: {type: bigint, nullable: false, distinct: 99000}
    constraints: [{name: orders_pkey, kind: primary_key, columns: [id]}]
    references:
      - column: user_id
        to: public.users.id
        on_delete: cascade
        orphan_fraction: {value: 0.004, confidence: exact, via: scan}
```

</details>

## How to fix

Delete or repair the orphaned rows before validating the foreign key: ADD the constraint NOT VALID, clean up the orphans, then VALIDATE CONSTRAINT.

**For the example above.** Add the key NOT VALID so new rows are checked from now on, then delete or repair the orphaned rows and ship that. Once a re-pulled fixture records no orphans, validate.

```sql title="Migration 1"
SET lock_timeout = '3s';
ALTER TABLE public.orders
  ADD CONSTRAINT orders_user_fk FOREIGN KEY (user_id) REFERENCES public.users (id) NOT VALID;
```

```sql title="Migration 2, after the orphans are gone"
ALTER TABLE public.orders VALIDATE CONSTRAINT orders_user_fk;
```

<details>
<summary>The re-pulled fixture the fix is checked against</summary>

```yaml
rowshape_fixture: "1"
meta: {id: example, engine: {name: postgres, version: "16"}, privacy: standard, profile: {mode: fast, escalated: []}}
tables:
  public.users:
    rows: {value: 100000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: constraint}}
    constraints: [{name: users_pkey, kind: primary_key, columns: [id]}]
  public.orders:
    rows: {value: 800000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: constraint}}
      user_id: {type: bigint, nullable: false, distinct: 99000}
    constraints: [{name: orders_pkey, kind: primary_key, columns: [id]}]
    references:
      - column: user_id
        to: public.users.id
        on_delete: cascade
        orphan_fraction: {value: 0, confidence: exact, via: scan}
```

</details>

## Related codes

- RS-CONSTRAINT-020 — ADD CONSTRAINT without NOT VALID scans the whole table under ACCESS EXCLUSIVE
- RS-CONSTRAINT-001 — NOT VALID constraint validated in the same transaction
- RS-PERF-001 — DELETE cascades through a long-tailed fan-out

## References

- [Fixture spec §6.6: References and fan-out](/spec/)

---

This page is generated from the same catalog `rowshape explain RS-DATA-020` reads, so the remediation here is byte-identical to the one a verdict carries — they cannot drift. An agent can read it with the `explain_finding` MCP tool. The example is run against PostgreSQL 16 by rowshape's test suite: the first migration must produce RS-DATA-020 and the fix must not fail.
