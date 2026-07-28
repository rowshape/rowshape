---
title: What rowshape can and cannot know
description: Where the fixture and the disposable database stop resembling production, and what that means for a verdict.
sidebar:
  order: 1
---

rowshape answers a question about production using two things that are not
production: a **fixture** (statistics, no rows) and a **disposable database**
hydrated from it. Both are lossy on purpose. This page says where.

The PRD puts it plainly, and this page exists to honour that:

> Fixture fidelity is a tarpit. Synthetic data will never fully reproduce prod
> pathology. Say so in the docs. A fixture catching 80% of lock and constraint
> failures is worth shipping; chasing 100% kills the project.

## The verdict rests on two different things

It is worth separating them, because they have very different strength.

**Fixture facts.** Row counts, null fractions, cardinality, uniqueness, fan-out.
These come from your real database, carry an explicit `confidence`
(`exact` / `estimated`), and every finding declares the facts it rests on in
`depends_on`. A finding resting on an `estimated` fact cannot certify a PASS —
the confidence-capping engine downgrades it to WARN. This half is well defended.

**What the disposable database did.** Did the migration apply, and how long did
each statement take. This half is weaker, because the hydrated database is not
physically like production.

Most findings are computed from the *fixture*, not from the hydrated run. The
hydrated run contributes the apply/fail outcome and the duration basis.

## What the hydrated database does not reproduce

| Not reproduced | Consequence |
| --- | --- |
| **Bloat and dead tuples** | A freshly `COPY`'d table is densely packed. An index build on a 3×-bloated production heap takes materially longer than the same build on hydrated rows, so a duration estimate can understate. |
| **Physical row order / clustering** | Production has whatever correlation years of writes produced. Hydrated rows are written in order. |
| **CHECK constraints and foreign keys** | Not created in the disposable schema. A migration that backfills values violating an existing CHECK applies cleanly here and fails in production. |
| **Partitioning** | Not created. rowshape refuses to validate `ATTACH`/`DETACH PARTITION` against the sandbox rather than report a verdict about its own limitation — use `--target` for those. |
| **TOAST / very wide values** | Column *lengths* are profiled, but generated values are short. A rewrite that detoasts megabytes per page in production moves bytes per page here. |
| **Correlation between columns** | Columns are profiled and generated independently. A CHECK spanning two columns can fail on hydrated data that production satisfies. |
| **Multi-column uniqueness** | Not profiled. `ADD CONSTRAINT UNIQUE (a, b)` is reported as undecidable rather than guessed. |

## Where a duration estimate is refused outright

rowshape declines rather than guessing when the basis will not support a
prediction. You will see the finding without an `estimate` when:

- the fixture declares no `meta.engine.version` — cost models are
  version-conditional, so there is nothing to extrapolate with;
- the table is not in the fixture — the row count is not rowshape's to invent;
- the statement was never measured;
- the hydrated basis is too small for the extrapolation — a handful of rows
  scaled to millions is arithmetic on noise, not an estimate.

An absent estimate is a deliberate answer. The lock, the irreversibility and the
constraint reasoning are all still reported; only the *duration* is withheld.

## `--target` is a different mode

With `--target`, rowshape applies the migration to a database **you** nominate
and the facts come from real data rather than synthesis. That is the strongest
mode, and it is what the Neon-branch workflow is for.

Two things to know:

- **It writes.** `--target` opens a transaction, executes your migration and
  commits it. rowshape refuses when the target's host matches the fixture's
  source, and refuses a fixture with no recorded source unless you pass
  `--i-know-target-is-writable`. That guard compares *host hashes*, so it does
  not see through a connection pooler or a replica CNAME — point it somewhere
  disposable.
- **Confidence is upgraded.** Validating against a branch upgrades the relevant
  facts to `exact`. That is right for a branch *of production*; it is optimistic
  for a hand-built staging database holding a tenth of the data. rowshape cannot
  tell the two apart, so the accuracy of that claim rests on what you point it at.

## The honest summary

rowshape is built to catch **lock behaviour, irreversibility, constraint
violations against real data shape, and ordering hazards**. Those are the failure
modes it models directly, and where it is worth trusting.

It is *not* a performance simulator. A duration bucket is an order-of-magnitude
signal derived from a row-count model, with the basis attached so you can judge
it — not a prediction of wall-clock time on your hardware under your load.

When rowshape cannot answer, it says so: an absent estimate, a WARN it declines
to upgrade to PASS, or a tool error. Those are answers, and they are more useful
than a confident number that happens to be wrong.
