---
title: Frequently asked questions
description: 'How rowshape differs from squawk, strong_migrations and pgroll, what data leaves your machine, MySQL support, and whether it needs production access.'
---

<!--
  Claims about other tools are kept to what each project says about itself in
  its own README, and to what they are designed to do rather than how well. If
  one of them changes scope, this page is the thing that is wrong. Claims about
  rowshape are checked against the CLI (--help, cmd/, internal/).
-->

## How is rowshape different from squawk or strong_migrations?

They answer different questions, and they work well together.

[squawk](https://github.com/sbdchd/squawk) is a linter for PostgreSQL
migrations: it parses your SQL and flags statements that are known to be risky —
an index built without `CONCURRENTLY`, a column added `NOT NULL` with a default
on older Postgres, a constraint added without `NOT VALID`. It reads the SQL, so
it is fast, needs no database, and fits anywhere in a pipeline.

[strong_migrations](https://github.com/ankane/strong_migrations) does a similar
job for Rails: it catches unsafe operations in Active Record migrations while
they run in development, stops them, and explains a safer way to write them.

Both reason about the **operation**. rowshape reasons about the operation
**against the shape of your data**. It hydrates a disposable Postgres with
synthesized rows matching your production statistics, actually applies the
migration, and reports what happened there: that `SET NOT NULL` fails because
3% of the column is NULL, that a unique index cannot build because the column
is not unique, that a rewrite on a 5,000,000-row table holds `ACCESS EXCLUSIVE`
for long enough to matter. The same statement can be fine on a small table and
an outage on a big one; a rule that sees only the SQL has to be strict about
both, and rowshape can tell them apart.

The cost is that rowshape needs a fixture and a scratch Postgres, where a linter
needs neither. Running a linter as well costs little.

## How is it different from pgroll?

[pgroll](https://github.com/xataio/pgroll) is a migration **executor**: it
applies schema changes to your real database with zero downtime and keeps old
and new schema versions available at once, using an expand/contract approach
with migrations written in its own format.

rowshape does not apply anything to your database. It checks a migration before
it ships, in a throwaway copy, and returns a verdict. If you use pgroll, the
question rowshape answers is still open — whether the data permits the change —
but note that today rowshape validates plain `.sql` migrations only (see below),
not pgroll's migration format.

## What data leaves my machine?

rowshape has no telemetry. It connects to the database URLs you give it, and
`rowshape mcp` talks to your agent over stdio.

What you might share is the fixture, `rowshape.yaml`, because you commit it. A
fixture contains no rows; it contains statistics computed from them, and at the
default `--privacy standard` some of those reveal the extremes of numeric and
date columns. `--privacy strict` emits none of those, and
`rowshape inspect --leaks` lists every field in a fixture that is derived from a
real value. The exact claim, and what each level reveals, is on the
[Privacy](/privacy/) page.

## Does it need access to production?

Once, read-only, to take the shape — and never from CI.

- `rowshape pull` reads the catalog and runs bounded profiling aggregates in a
  read-only transaction, with a read-only role, and refuses to run as a
  superuser unless you pass `--i-know`. It never runs `SELECT *` on a user
  table. [What `pull` does to the database it reads](/privacy/#what-pull-does-to-the-database-it-reads)
  lists its timeouts. A staging database with production-sized
  data works too, as long as its shape is the one you care about.
- `rowshape validate` never touches the database the fixture came from. It
  records that host only as a salted hash and refuses a target that matches it.
- The CI job needs only the committed fixture and a disposable Postgres. See
  [rowshape in CI](/guides/ci/).

The fixture format is an open spec, [RFC-0001](/spec/), so a fixture can also
be written by another tool or by hand.

## Does it support MySQL?

Not yet. MySQL is on the roadmap, behind the fixture spec's vocabulary; today
rowshape is PostgreSQL only. It supports PostgreSQL 10 through 18, and the full
test corpus runs against every one of those majors in CI.

## Which migration tools does it work with?

`validate` applies plain `.sql` files (the `rawsql` runner): a single file or a
directory of them. `rowshape init` also detects Alembic, Prisma and Drizzle
projects, but validating their migrations is not implemented yet, and
`validate` refuses with a clear error rather than producing a verdict. If your
tool can emit the SQL it would run, validate that.

## Is a PASS a guarantee?

It is a statement about the shape in the fixture, at the confidence the fixture
records. A verdict is capped by the weakest fact it rests on: where rowshape
could only estimate something, it says WARN rather than PASS. The
[Fidelity](/fidelity/) page lists what a fixture can and cannot capture —
read it before treating a PASS as proof.
