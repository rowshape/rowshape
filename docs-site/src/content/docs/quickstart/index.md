---
title: Quickstart
description: 'From install to your first verdict: pull a fixture, validate a migration that fails on real data, read the finding, fix it, and get a PASS.'
sidebar:
  order: 1
---

<!--
  Every command on this page was run against a real Postgres 16 and every block
  of output is pasted from that run, not written by hand. The example table was
  200,000 rows with about 3% NULL emails. If the output format changes, re-run
  and re-paste.
-->

This walks one migration from "looks fine" to a verdict, a fix, and a PASS. It
takes about ten minutes and needs two things: the `rowshape` binary, and a
PostgreSQL server you can throw away.

## 1. Install

Follow [Install](/install/). Today, before the first tagged release, the channel
that works is `go install`:

```sh
go install github.com/rowshape/rowshape@latest
rowshape --help
```

## 2. Start a scratch Postgres

`validate` needs somewhere disposable to rebuild your data's shape and run the
migration. Any Postgres you can create databases on will do; Docker is the
quickest:

```sh
docker run --name rowshape-scratch -e POSTGRES_PASSWORD=postgres -p 5432:5432 -d postgres:16
export SCRATCH_PG='postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable'
```

You pass that URL to `--ephemeral`. rowshape treats it as an admin connection:
it creates a throwaway database on that server, hydrates the fixture into it,
applies the migration, and drops the database afterwards. Nothing else on the
server is touched. (`sslmode=disable` is fine for a container on `localhost`;
do not carry it to a remote host.)

:::note[The scratch server must not be your source database's host]
`validate` refuses to run against the host the fixture was pulled from, and
every loopback spelling (`localhost`, `127.0.0.1`, `::1`) counts as the same
host. If the database you pull from is also on `localhost`, the scratch server
has to live somewhere else. The refusal is a tool error, exit code `3`:

```text wrap
tool error [bad_usage]: validate: refusing to run against the fixture's source host — validate only ever touches a disposable or provided target, never production
  hint: point --target/--ephemeral at a disposable or non-production host
```
:::

## 3. Pull a fixture

Point `pull` at the database whose shape you care about — production, or a
staging copy with production-sized data. It reads the catalog and runs bounded
profiling aggregates inside a read-only transaction — never `SELECT *` on your
tables — and it refuses to run as a superuser unless you pass `--i-know`. Use a read-only role, for example (on
Postgres 14 and later):

```sql
CREATE ROLE rowshape_ro LOGIN PASSWORD '…';
GRANT pg_read_all_data TO rowshape_ro;
```

```sh
rowshape pull "postgres://rowshape_ro:…@db.internal:5432/app" -o rowshape.yaml
```

```text
rowshape pull: wrote rowshape.yaml (1 tables)
```

The connection can also come from the standard libpq variables (`PGHOST`,
`PGUSER`, …) instead of a URL.

## 4. Review it and commit it

`rowshape.yaml` holds statistics — row counts, null fractions, distinct counts,
ranges — and no rows. At the default `--privacy standard` some fields do carry
real values, such as the minimum and maximum of a numeric or date column.
List exactly which ones before you commit:

```sh
rowshape inspect rowshape.yaml --leaks
git add rowshape.yaml
git commit -m "Add rowshape fixture"
```

[Privacy](/privacy/) explains each level; `--privacy strict` emits none of
those value-derived fields.

## 5. Validate a migration that will fail

The example database has a `users` table with an `email` column that is about 3%
NULL. Here is a migration that makes it required:

```sql title="migrations/001_email_not_null.sql"
ALTER TABLE users ALTER COLUMN email SET NOT NULL;
```

```sh
rowshape validate rowshape.yaml -m migrations/ --ephemeral "$SCRATCH_PG"
```

```text title="Terminal" frame="terminal" wrap
rowshape validate: migration did not apply cleanly: column "…" of relation "…" contains null values [relation users, column email] (23502)
[FAIL]  FAIL  (fixture fixture@2026-10-28)

  x  RS-APPLY-001  Migration did not apply: column "…" of relation "…" contains null values [relation users, column email]
    at migrations/001_email_not_null.sql:1
    The database rejected this statement with SQLSTATE 23502: column "…" of relation "…" contains null values [relation users, column email]. Nothing after it was evaluated, so this verdict describes the failure and not the migration's other effects.
    fix: Read the SQLSTATE and message in the finding's evidence: they are the database's own words about what it refused. Fix the statement at the reported file and line, then re-run validate. A class-23 code (23505 unique_violation, 23502 not_null_violation, 23514 check_violation) means production-shaped DATA rejected it — the migration is syntactically fine and the data does not permit it. A class-42 code (42P01 undefined_table, 42703 undefined_column, 42601 syntax_error) means the statement does not match the schema it was written against.
    why: rowshape explain RS-APPLY-001

  !  RS-DATA-001  SET NOT NULL on users.email likely fails: 3.1% of rows are NULL
    null_fraction is 0.03133 (estimated); SET NOT NULL rejects the existing NULL rows.
    rests on: public.users.email.null_fraction [confidence: estimated]
    fix: Backfill or delete the NULL rows first, or add a DEFAULT; then SET NOT NULL. A validated CHECK (col IS NOT NULL) lets SET NOT NULL skip the full-table scan on PG 12+.
    why: rowshape explain RS-DATA-001

duration: 0ms
```

The exit code is `1`: FAIL. The statement would have failed in production too.
The fixture's header shows its `meta.id`, which defaults to the pull date.

## 6. Read the finding

Two findings, two kinds of evidence:

- **RS-APPLY-001** (`x`, an error) is what Postgres itself said when the
  migration ran against the hydrated copy: SQLSTATE `23502`, NULLs in
  `users.email`.
- **RS-DATA-001** (`!`, a warning) is what the fixture predicted before running
  anything: 3.1% of rows are NULL, and that number is `estimated`, which the
  verdict says out loud.

Every finding carries its fix. The same text, without the run, is one command
away, and on the finding's page:

```sh
rowshape explain RS-DATA-001
```

```text wrap
RS-DATA-001  SET NOT NULL against existing NULLs

SET NOT NULL scans the table and rejects rows that are NULL. If the column's null_fraction is above zero the migration fails; if the zero is only estimated, it cannot be certified safe.

Remediation:
  Backfill or delete the NULL rows first, or add a DEFAULT; then SET NOT NULL. A validated CHECK (col IS NOT NULL) lets SET NOT NULL skip the full-table scan on PG 12+.

References: RFC §7.4
```

## 7. Fix it and get a PASS

Backfill the NULLs, then enforce the rule with a `CHECK` added `NOT VALID` and
validated in its own transaction, so the long scan does not hold a lock that
blocks writes:

```sql title="migrations/001_email_not_null.sql"
-- Every user must have an email from now on.
-- Backfill the existing NULLs, then enforce it with a validated CHECK.
UPDATE users SET email = 'unknown+' || id || '@example.invalid' WHERE email IS NULL;

ALTER TABLE users
  ADD CONSTRAINT users_email_not_null CHECK (email IS NOT NULL) NOT VALID;

COMMIT;

ALTER TABLE users VALIDATE CONSTRAINT users_email_not_null;
```

```sh
rowshape validate rowshape.yaml -m migrations/ --ephemeral "$SCRATCH_PG"
```

```text title="Terminal" frame="terminal"
[PASS]  PASS  (fixture fixture@2026-10-28)
  no findings

duration: 78ms
```

Exit code `0`.

Why not just put the `UPDATE` in front of the original `SET NOT NULL`? That
migration applies cleanly, but the verdict is WARN, exit `2`: RS-DATA-001 is
judged against the committed fixture, which still says 3.1% of `email` is
NULL, so rowshape cannot certify that the backfill caught every row. `VALIDATE
CONSTRAINT` checks the real rows, which is a guarantee rowshape can stand
behind.

## 8. Exit codes

The exit code is the contract a script, a CI job or an agent branches on:

| Code | Verdict | Meaning |
|:----:|---------|---------|
| `0`  | PASS    | No findings. |
| `1`  | FAIL    | The migration breaks on this data. |
| `2`  | WARN    | It works, and it costs something worth knowing. `--warn-fail` turns this into `1`. |
| `3`  | —       | Tool error: rowshape could not produce a verdict, and says why. |

Add `--json` to get the same verdict as a machine-readable document.

## Next

- Run it on every pull request with the [GitHub Action](/reference/github-action/).
- Give an agent the same loop with [`rowshape init --agent`](/agent/).
- Browse every code a verdict can carry in the [finding catalog](/findings/).
- Read [Fidelity](/fidelity/) before trusting a PASS: it says what rowshape can
  and cannot know from a fixture.
