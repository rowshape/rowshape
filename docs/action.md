# The rowshape GitHub Action

Run `rowshape validate` in CI and gate a pull request on the verdict. The Action
is a **thin wrapper over the released `rowshape` binary** — it adds no finding
logic and renders the exact same [Verdict](../internal/verdict/verdict.go) the
CLI and MCP server produce (one struct, two marshalers; PRD §10,
INV-VERDICT-SHAPE). It needs **no production credential**: point it at a
disposable Postgres and `validate` hydrates a throwaway database from a committed
fixture, applies the migration, and drops it. It hard-refuses a target whose host
matches the fixture's source (INV-BLAST-RADIUS-ZERO).

## Quick start

```yaml
name: migration-check
on: [pull_request]

permissions:
  contents: read

jobs:
  rowshape:
    runs-on: ubuntu-latest
    services:
      postgres:
        image: postgres:16
        env:
          POSTGRES_PASSWORD: postgres
        ports: ["5432:5432"]
        options: >-
          --health-cmd pg_isready --health-interval 10s
          --health-timeout 5s --health-retries 5
    steps:
      - uses: actions/checkout@v4
      - uses: rowshape/rowshape@v1
        with:
          fixture: rowshape.yaml
          migrations: db/migrations
          ephemeral: postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable
```

A `FAIL` verdict fails the job. A `WARN` passes by default (set
`warn-as-fail: true` to block on it). A tool error (could not produce a verdict)
also fails the job.

> **On `sslmode=disable` above.** That is correct here and only here: the
> database is a `services:` container on `localhost`, reachable only from inside
> the job. Do **not** carry that query parameter over to a remote host. pgx
> defaults to `sslmode=prefer`, which attempts TLS and then *silently falls back
> to plaintext* if the server declines — so a remote connection can end up in the
> clear without saying so. rowshape warns on stderr when it connects to a
> non-loopback host without TLS; use `sslmode=require` or stronger there.

## Exit-code gating (PRD §10)

| `rowshape validate` | Verdict | Job outcome (default) | With `warn-as-fail: true` |
| ------------------- | ------- | --------------------- | ------------------------- |
| `0`                 | PASS    | pass                  | pass                      |
| `1`                 | FAIL    | fail                  | fail                      |
| `2`                 | WARN    | **pass**              | **fail**                  |
| `3`                 | tool error | fail               | fail                      |

The only remapping the Action performs is WARN-only: with `warn-as-fail: false`
a raw exit `2` becomes a passing job so a WARN informs review without blocking
the merge; with `warn-as-fail: true` the Action passes `--warn-fail` to
`validate`, which returns `1` for a WARN itself. FAIL and tool error always fail.

Exit `3` also covers the wrapper's own refusals, which are tool errors rather
than verdicts: a missing binary, an unparseable boolean input, and setting both
`target` and `ephemeral`.

### Debugging a failed run

`--log-level debug` (on any subcommand) adds structured detail on stderr — the
host, database, user and whether TLS is in use — without ever printing the
connection string.

Connection failures now name the failure *class* rather than reporting a generic
"could not connect":

```
rowshape pull: could not connect to the database: connection refused
rowshape pull: nothing is listening on that host and port; check the port and that the server is running
```

The classes are `connection refused`, `host not found`, `timed out`,
`cancelled`, `authentication failed`, `database does not exist`,
`TLS handshake failed`, `permission denied`, and `unknown`. These are rowshape's
own strings — the driver's message is inspected to derive the class and then
discarded, because it embeds the host, port, user and database.

### Timeouts and cancellation

Every connection rowshape opens carries server-side limits, sent as startup
parameters so they are in force for the first query:

| Limit | Default | Why |
| ----- | ------- | --- |
| `lock_timeout` | 5s | rowshape must never sit in a lock queue on your database. Failing fast beats blocking behind a migration. |
| `idle_in_transaction_session_timeout` | 60s | A wedged client cannot pin an old snapshot and block `VACUUM`. |
| connect timeout | 10s | A black-holed host fails in seconds, not minutes. |
| `statement_timeout` | *see below* | — |

`statement_timeout` is **not** set by default on read paths. `pull --exact` is a
full streaming pass documented as taking minutes to hours, and `plan`/`verify`
read catalogs on schemas that can be very large — capping those by default would
turn a slow-but-correct run into a mysterious partial failure. Fast-mode `pull`
does get a 10-minute cap, and `--statement-timeout` overrides either.

Anything you set yourself in the DSN wins; rowshape only fills in what you left
unset.

Cancellation reaches the database: `Ctrl-C` (or a CI job timeout sending
`SIGTERM`) cancels the in-flight query server-side and tears down any ephemeral
container, rather than killing the client and leaving the query running.

### Input strictness

Boolean inputs (`warn-as-fail`, `json`, `verify`) accept `true/yes/1/on` and
`false/no/0/off` in any case, and **reject anything else** rather than falling
back to `false`. These gate CI strictness, so an unrecognized value silently
becoming the permissive branch would mean a user asking for a stricter build
quietly getting a laxer one.

`target` and `ephemeral` are mutually exclusive and setting both is an error.
They used to be forwarded together and resolved silently in `target`'s favour —
the mode that writes to a *live* database winning a conflict the user never saw.

`verdict-json` is set only when the file was actually written, so it is empty
when `json: false` rather than naming a file that does not exist.

## Inputs

| Input          | Default            | Description |
| -------------- | ------------------ | ----------- |
| `fixture`      | `rowshape.yaml`    | Path to the committed fixture. |
| `migrations`   | `migrations`       | Migration `.sql` file or directory. |
| `ephemeral`    | ―                  | Admin URL of a disposable Postgres (a CI `services:` container). No production credential. |
| `target`       | ―                  | Validate against a live DB URL instead of hydrating (its data is ground truth). Mutually exclusive with `ephemeral`. |
| `warn-as-fail` | `false`            | Fail the job on a WARN-only verdict. |
| `json`         | `true`             | Capture the machine-readable JSON verdict for a downstream step (e.g. PR annotations, P4-T2). |
| `runner`       | ―                  | Override runner detection. **Only `rawsql` can be validated today** — `alembic`, `prisma` and `drizzle` projects are detected but their migrations cannot yet be captured, and `validate` refuses with a clear error. |
| `seed`         | ―                  | Deterministic hydration seed. |
| `scale`        | ―                  | Fraction of declared rows to hydrate (default `1.0`). |
| `args`         | ―                  | Extra space-separated flags passed through to `validate` (e.g. `--calibrate`). |
| `version`      | `latest`           | rowshape release to install (e.g. `v1.2.3`). Ignored when `binary` is set. |
| `binary`       | ―                  | Path to a prebuilt `rowshape` binary; skips the install step (brew/`go install`, or tests). |
| `repo`         | `rowshape/rowshape`| Advanced: repo to download the release from. |
| `verify`       | `true`             | Verify the downloaded archive against the release `checksums.txt` before running it. Fails closed. Set `false` only if you knowingly accept an unverified binary. |
| `verify-signature` | `auto`         | Verify the cosign keyless signature over `checksums.txt`. `auto` verifies when cosign is present and falls back to checksum-only; `true` requires it; `false` skips it. |

## Supply-chain verification

The archive is verified before it is executed. By default the Action downloads
`checksums.txt` from the same release and refuses to run a binary whose SHA-256
does not match — a missing `checksums.txt`, a missing entry for your platform's
archive, and a mismatch are all refusals, not warnings.

For the stronger guarantee, install cosign first and set `verify-signature:
true`. The signature is checked *before* the checksum, so a forged
`checksums.txt` cannot go on to validate a forged archive:

```yaml
- uses: sigstore/cosign-installer@v3
- uses: rowshape/rowshape@v1
  with:
    verify-signature: true
    ephemeral: postgres://postgres:postgres@localhost:5432/postgres
```

With `verify-signature: true` and cosign absent, the install fails rather than
silently degrading to checksum-only.

## Outputs

| Output         | Description |
| -------------- | ----------- |
| `verdict`      | `PASS`, `WARN`, or `FAIL` (empty on a tool error). |
| `exit-code`    | The raw `validate` exit code (`0/1/2/3`). |
| `verdict-json` | Path to the captured JSON verdict file (when `json: true`). |

The captured JSON is the same struct across CLI/MCP/Action; the Action's
annotate step renders file/line PR annotations and a check summary from it.

## PR annotations & check summary

After `validate` runs, the Action calls `rowshape annotate <verdict.json>`,
which renders the **same** Verdict struct (no bespoke formatter) into GitHub's
two review surfaces:

- **Inline annotations** — one workflow command per finding that carries a
  `location`, placed at the exact file and line (`::error`/`::warning`/`::notice`
  by severity). Findings without a location can't be placed inline; they still
  appear in the summary.
- **Check summary** — appended to `$GITHUB_STEP_SUMMARY`: the overall verdict,
  the fixture it was computed against, and a table of every finding's code,
  severity, estimate bucket, and remediation.

It runs even on a FAIL (so the findings are visible on the PR); the job's
pass/fail outcome was already decided by the run step. You can also use it
standalone: `rowshape validate ... --json | rowshape annotate`.

## Using a binary you install yourself

If you install rowshape another way (Homebrew, `go install`, a `curl` in an
earlier step), skip the download by pointing `binary` at it:

```yaml
- run: go install github.com/rowshape/rowshape@latest
- uses: rowshape/rowshape@v1
  with:
    binary: rowshape
    ephemeral: postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable
```

## Implementation & tests

- `action.yml` — the composite action (install step + run step).
- `.github/actions/rowshape/install.sh` — downloads the released archive for the
  runner (naming mirrors `.goreleaser.yaml` and `npm/install.js`).
- `.github/actions/rowshape/run.sh` — translates inputs to `validate` flags and
  maps the exit code onto the CI gate.
- `rowshape annotate` (`cmd/annotate.go`, `internal/annotate/`) — renders a JSON
  verdict into inline annotations + the check summary, reusing `verdict.Result`.
- `test/action/action_test.go` — hermetic wrapper tests (exit mapping, flag
  forwarding, installer naming) plus a DB-backed end-to-end run against corpus
  fixtures. Wired into CI by `.github/workflows/action-integration.yml`.
- `internal/annotate/annotate_test.go`, `cmd/annotate_test.go` — assert
  finding.location → file/line and that the summary carries codes + remediation.
