# rowshape corpus

An executable corpus of the documented ways a PostgreSQL migration breaks. Each
case is a **triple** — a migration, a rowshape fixture describing the production
data's shape, and the verdict rowshape is expected to return:

```
cases/<pathology>/
  migration.sql    # the proposed schema change
  fixture.yaml     # production shape (rowshape fixture, no row values)
  expected.json    # { verdict: PASS|WARN|FAIL, findings: [{code, severity}] }
```

This is a runnable harness, not prose. The corpus is written and proven **before**
the findings that consume it, so a finding can never be demoed on PG 16 and then
discovered wrong on PG 11 (PRD §14 ordering).

## Pathologies covered

| Case | What breaks |
|------|-------------|
| `volatile_default_rewrite` | `ADD COLUMN … DEFAULT gen_random_uuid()` rewrites every row under ACCESS EXCLUSIVE |
| `set_not_null_fullscan` | `SET NOT NULL` fails on the existing NULL rows (nullable-but-not-0%-null) |
| `unique_index_cant_build` | `CREATE UNIQUE INDEX` on a column proven non-unique |
| `validate_orphans` | `VALIDATE CONSTRAINT` trips on pre-existing FK orphans |
| `cascade_delete_fanout` | a cascade delete through a long-tailed fan-out becomes an outage |
| `not_valid_validated_same_tx` | `NOT VALID` then `VALIDATE` in one transaction defeats the split |

## Running

```
go test ./corpus/harness/...
```

The harness loads every triple, asserts it is well-formed, and checks the corpus
covers every documented pathology. Once `validate` lands (P2-T7) it also runs each
migration against its fixture and compares the verdict to `expected.json`.

The harness is parameterized by Postgres major version via `ROWSHAPE_PG_VERSION`
(default 16); CI drives it across PG 11–17 (P2-T13).

> This directory is the seed of the standalone `rowshape/corpus` repository
> (PRD §12); the Go module path is unaffected by where it eventually lives.

## Coverage shape (CR-T15)

Coverage is the credibility asset (PRD §12), so its **shape** matters, not just
its count. This table records which finding families have cases, which
severities they reach, and — the easiest thing to leave untested — whether the
**capping contract** is asserted: that a capped WARN's remediation names the
command which resolves it (RFC §7.4).

| family | cases | severities covered | capping contract (`resolve_contains`) |
|---|---|---|---|
| `RS-APPLY` | 1 | error | no |
| `RS-CONSTRAINT` | 5 | error, warn | no |
| `RS-DATA` | 6 | error, warn | yes (3) |
| `RS-DEPLOY` | 3 | warn | no |
| `RS-INDEX` | 11 | error, warn | yes (1) |
| `RS-LOCK` | 14 | warn | no |
| `RS-PERF` | 5 | warn | no |
| `RS-REVERSE` | 6 | warn | no |
| `RS-TX` | 2 | error, warn | no |
| _(negative cases: assert NO finding)_ | 3 | — | — |

**This table is hand-maintained and had drifted** before `RS-APPLY` was added:
it claimed 5 `RS-INDEX` cases against 9, and 4 negative cases against 3. The
numbers are corrected above, and the drift is recorded rather than quietly fixed
because it is the table's own failure mode — a coverage record nothing verifies
will describe the corpus it had when someone last remembered, not the corpus that
exists. `TestCoverageTableMatchesTheCorpus` now verifies every number in it against
`corpus/cases/*/expected.json` — case counts, severities, capping-contract
coverage, the negative-case count, and that a family with cases is not missing a
row entirely. The same discipline `TestFindingsDocsUpToDate` applies to the
generated finding pages. Update the table when you add a case; the test will tell
you if you forget.

`RS-APPLY` (D-023) is the newest family and the odd one out: every other family
names a HAZARD in a migration that RAN, while this one covers a migration that
did not. It has one case, `rsapply-statement-rejected`, and no
`resolve_contains` case — deliberately, because its remediation names no command
to re-run. Nothing resolves a typo but fixing it.

**Known remaining gaps**, recorded so the next one is visible rather than
rediscovered by audit: `RS-LOCK`, `RS-PERF`, `RS-REVERSE` and `RS-CONSTRAINT`
have no case asserting `resolve_contains`, so for those families the "here is how
to turn this WARN into a PASS" promise is exercised by unit tests but not by the
corpus. `RS-LOCK`, `RS-PERF` and `RS-REVERSE` also have no `error`-severity case,
which is correct today — those families are WARN by design (see D-009) — and is
listed so a future `error` code in them is noticed as new.

### What CR-T15 changed

The corpus had genuine duplication sitting next to genuine gaps:

- **Added `rsindex-unique-unproven-capped`** — `RS-INDEX-010`'s capped branch had
  no case at all. Before it, `resolve_contains` was asserted only on `RS-DATA`
  cases, so the capping promise was unverified for the whole `RS-INDEX` family.
- **`perf-cascade-fanout`** duplicated `cascade_delete_fanout` (a bulk `DELETE`
  differing only in table name and interval). It now covers **`TRUNCATE`**, which
  `deleteTarget` handles in a separate parser branch that had **zero** coverage.
- **`rsconstraint-not-valid-same-tx`** duplicated `not_valid_validated_same_tx`
  (both same-transaction, differing only in constraint and column name). It is now
  **`rsconstraint-not-valid-separate-tx`**, the **negative** case: the two-step
  split that `RS-CONSTRAINT-001`'s own remediation recommends must not itself be
  flagged. Nothing previously checked that — and a finding that fires on its own
  recommended fix is how a check gets switched off.

Both duplicates were **differentiated rather than deleted**: deleting them would
have removed regression coverage, while repurposing them converted wasted
duplication into branches nothing else exercised. Cases named in
`requiredPathologies` (PRD §12) keep their names and were not touched.
