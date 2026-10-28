---
title: rowshape in CI
description: 'Gate pull requests on a rowshape verdict: exit codes, warn-as-fail, PR annotations, and a complete GitHub Actions workflow you can copy.'
sidebar:
  order: 1
---

<!--
  Every flag, input, output and exit code here is checked against
  cmd/validate.go, internal/exitcode, action.yml and
  .github/actions/rowshape/run.sh; the annotate output below is from a real run.
  There is deliberately no section on severity thresholds: the CLI and the
  Action have no such knob today, only warn-as-fail.
-->

A CI job needs three things from rowshape: a verdict it can branch on, a way to
decide whether WARN blocks the merge, and findings a reviewer can see on the
pull request. This page covers all three, then puts them in one workflow.

The job never needs a production credential. It validates against the
committed `rowshape.yaml` in a disposable Postgres — a `services:` container is
enough — and `validate` refuses outright to run against the host the fixture
was pulled from.

## Exit codes

`rowshape validate` reports its verdict as the process exit code. The codes are
part of the public contract and do not change:

| Code | Verdict | Meaning |
|:----:|---------|---------|
| `0`  | PASS    | No findings. |
| `1`  | FAIL    | A finding proved the migration unsafe on this data. |
| `2`  | WARN    | Findings exist but none is an error: it works, and it costs something. |
| `3`  | —       | Tool error: rowshape could not produce a verdict (bad fixture, unreachable database, refused target). |

A tool error is deliberately not a FAIL. Exit `3` means "no answer", and a
pipeline should treat it as a broken job, not as a verdict about the migration.

## Deciding whether WARN blocks

WARN covers things like a full-table rewrite under `ACCESS EXCLUSIVE`
(RS-LOCK-001) — the migration succeeds, and production pays for it. Whether
that should block a merge is your call, and it is the one gating knob there is:

- **CLI:** `--warn-fail` makes a WARN-only verdict exit `1` instead of `2`.
  Without it, a plain `set -e` script already fails on WARN, because `2` is
  non-zero.
- **GitHub Action:** WARN passes the job by default. `warn-as-fail: true`
  passes `--warn-fail` to `validate`, so WARN fails the job.

| `validate` exit | Action, default | Action, `warn-as-fail: true` |
|:---:|---|---|
| `0` PASS | pass | pass |
| `1` FAIL | fail | fail |
| `2` WARN | **pass** | **fail** |
| `3` tool error | fail | fail |

There is no per-code allow-list or severity threshold: FAIL always fails, a
tool error always fails, and WARN is the one outcome you choose for.

One WARN worth knowing about in CI: a migration statement that runs past
`--statement-timeout` (1 minute by default) is cancelled and the verdict is
floored to WARN — never PASS, because it was not shown to be safe. A
deliberately long backfill needs a higher ceiling; pass it through the Action's
`args` input, for example `args: --statement-timeout 5m`.

## Findings on the pull request

`rowshape annotate` turns a JSON verdict into GitHub's two review surfaces: one
workflow command per finding that has a file and line, which GitHub shows inline
on the diff, and a Markdown summary appended to `$GITHUB_STEP_SUMMARY`. The
Action runs it for you, even when the verdict is FAIL. By hand:

```sh
rowshape validate rowshape.yaml -m migrations/ --ephemeral "$SCRATCH_PG" --json > verdict.json
rowshape annotate verdict.json
```

For the failing migration in the [quickstart](/quickstart/), the inline
annotation it prints starts like this (truncated here):

```text
::error file=migrations/001_email_not_null.sql,line=1,title=RS-APPLY-001::Migration did not apply: column "…" of relation "…" contains null values [relation users, column email]%0A%0A…
```

and the check summary is a table of every finding — including those with no
file and line, such as RS-DATA-001, which cannot be placed inline:

```md
## rowshape: FAIL

Fixture `fixture@2026-10-28` · 1ms

| Code | Severity | Estimate | Remediation |
| --- | --- | --- | --- |
| RS-APPLY-001 | error | — | Read the SQLSTATE and message in the finding's evidence: … |
| RS-DATA-001 | warn | — | Backfill or delete the NULL rows first, or add a DEFAULT; then SET NOT NULL. … |
```

## A complete GitHub Actions workflow

```yaml title=".github/workflows/migrations.yml"
name: migrations
on:
  pull_request:
    paths:
      - "migrations/**"
      - "rowshape.yaml"

permissions:
  contents: read

jobs:
  rowshape:
    runs-on: ubuntu-latest
    services:
      postgres:
        # Match the major version production runs.
        image: postgres:16
        env:
          POSTGRES_PASSWORD: postgres
        ports: ["5432:5432"]
        options: >-
          --health-cmd pg_isready --health-interval 10s
          --health-timeout 5s --health-retries 5
    steps:
      - uses: actions/checkout@v4

      - id: rowshape
        uses: rowshape/rowshape@v1
        with:
          fixture: rowshape.yaml
          migrations: migrations
          # A disposable database on the service container; no production credential.
          ephemeral: postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable
          # Block the merge on WARN as well as FAIL.
          warn-as-fail: true

      - if: always()
        run: echo "verdict=${{ steps.rowshape.outputs.verdict }} exit=${{ steps.rowshape.outputs.exit-code }}"
```

The Action's outputs are `verdict` (`PASS`, `WARN` or `FAIL`; empty on a tool
error), `exit-code` (the raw `validate` code) and `verdict-json` (the path of
the captured JSON verdict, set only when the file was written). Every input is
listed in the [GitHub Action reference](/reference/github-action/).

:::note[Before the first tagged release]
`rowshape/rowshape@v1` and the release binary the Action downloads both arrive
with the first tag. Until then, install the binary yourself in an earlier step
(`go install github.com/rowshape/rowshape@latest`), pass `binary: rowshape`, and
reference the Action by a commit SHA. See [Install](/install/).
:::

## Other CI systems

Anything that can run a Postgres container and a binary can gate on the exit
code. The whole job is one command:

```sh
rowshape validate rowshape.yaml -m migrations/ --ephemeral "$SCRATCH_PG" --warn-fail
```

Drop `--warn-fail` to let WARN through — but then remember that `2` is still
non-zero, so your script has to allow it explicitly. The
[Docker image](/install/) (`ghcr.io/rowshape/rowshape`) works where installing
a binary is awkward.
