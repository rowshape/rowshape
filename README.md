<h1 align="center">rowshape</h1>

<p align="center">
  <strong>The type-checker for database migrations.</strong><br>
  Run a proposed schema change against production-shaped data in a disposable
  database, and get back a machine-readable verdict.
</p>

<p align="center">
  <a href="https://www.npmjs.com/package/rowshape"><img alt="npm" src="https://img.shields.io/npm/v/rowshape?color=%232E4A7D&label=npm"></a>
  <a href="https://www.npmjs.com/package/rowshape"><img alt="npm downloads" src="https://img.shields.io/npm/dm/rowshape?color=%232E4A7D&label=downloads%2Fmonth"></a>
  <a href="https://pkg.go.dev/github.com/rowshape/rowshape"><img alt="Go Reference" src="https://pkg.go.dev/badge/github.com/rowshape/rowshape.svg"></a>
  <a href="https://github.com/rowshape/rowshape/actions/workflows/corpus.yml"><img alt="Corpus (PG 10–18)" src="https://github.com/rowshape/rowshape/actions/workflows/corpus.yml/badge.svg"></a>
  <a href="LICENSE"><img alt="License: MIT" src="https://img.shields.io/badge/license-MIT-green"></a>
</p>

<p align="center">
  <a href="https://rowshape.com">rowshape.com</a> ·
  <a href="https://rowshape.com/install/">Install</a> ·
  <a href="https://rowshape.com/findings/">Finding catalog</a> ·
  <a href="https://rowshape.com/agent/">For agents</a>
</p>

---

## The problem

A migration that passes review and passes on a seeded dev database can still take
production down, because the hazard is not in the SQL — it is in the **shape of
the data the SQL meets**. A `NOT NULL` column added with a volatile default
rewrites every row under `ACCESS EXCLUSIVE`. A unique index cannot build if
duplicates already exist. Both look fine in a sandbox with a thousand rows,
precisely because a thousand rows is not the problem.

rowshape closes that gap without giving anyone a copy of production.

## How it works

```
  pull ──────────────► rowshape.yaml ──────────────► hydrate ──────────► validate
  read the shape        committable, no rows          a disposable        a verdict
  (read-only)           just statistics               Postgres           PASS/WARN/FAIL
```

1. **`pull`** reads your production database *through catalog views only* — never
   `SELECT *` on a user table — and writes a committable `rowshape.yaml`.
2. **`hydrate`** rebuilds a disposable database from that file, deterministically.
3. **`validate`** applies your migration to it and reports what happened.

The fixture is the interesting part. It is small enough to commit, contains no
row values, and every fact in it carries a **confidence** — so rowshape can
decline to certify what it cannot prove instead of guessing.

```yaml
rowshape_fixture: "1"
tables:
  public.users:
    rows: { value: 5000000, confidence: exact }
    columns:
      email:
        type: text
        null_fraction: { value: 0.03, confidence: estimated }
        unique:        { value: true, confidence: exact, via: constraint }
```

## Example

Add a `NOT NULL` email column to a 5,000,000-row table:

```sql
ALTER TABLE public.users
  ADD COLUMN email text NOT NULL DEFAULT (gen_random_uuid()::text || '@users.invalid');
```

```console
$ rowshape validate rowshape.yaml -m migrations/ --ephemeral "$PG"

[WARN]  WARN  (fixture demo-users)

  !  RS-LOCK-001  ACCESS EXCLUSIVE lock on users, outage rewrite of 5.0M rows
     ADD COLUMN with a volatile default holds ACCESS EXCLUSIVE and rewrites all
     5000000 rows.
     duration: outage (extrapolated from 1000 rows in 23ms, linear model)
     rests on: public.users.rows [confidence: exact]
     fix: ADD the column nullable with no default, backfill in batches, then
          SET NOT NULL via a validated CHECK.
```

Rewrite it as the three online steps it suggests, and the same command returns
`PASS`. That loop — and closing it without a human in the middle — is the point.

## Install

Every channel delivers the same single static binary. No runtime, no services.

```sh
# Homebrew (macOS, Linux)
brew install rowshape/tap/rowshape

# Go
go install github.com/rowshape/rowshape@latest

# npm / npx
npx rowshape --help

# Docker (CI)
docker run --rm ghcr.io/rowshape/rowshape:latest --help
```

Or download a binary for macOS, Linux or Windows on amd64/arm64 from
[Releases](https://github.com/rowshape/rowshape/releases). Every release ships an
SBOM and a [cosign](https://github.com/sigstore/cosign) signature.

> **Pre-release note.** The npm package currently holds the name with a
> placeholder `0.0.0`; `npx rowshape` starts working with the first tagged
> release. Homebrew and the Docker image land at that same tag.

## Quickstart

```sh
# 1. Read the shape of a database (read-only; needs only a read-only role)
rowshape pull "$PRODUCTION_URL" -o rowshape.yaml

# 2. Commit it. It contains no rows.
git add rowshape.yaml

# 3. Check a migration against it, in a disposable database
rowshape validate rowshape.yaml -m migrations/ --ephemeral "$SCRATCH_PG"
```

`validate` never touches the database the fixture came from — it refuses, by
comparing a salted hash of the source host.

## Exit codes

Part of the public contract, so a CI job or an agent can branch on them:

| Code | Meaning |
|:----:|---------|
| `0`  | **PASS** — no findings |
| `1`  | **FAIL** — the migration breaks on this data |
| `2`  | **WARN** only — it works, and it costs something worth knowing |
| `3`  | Tool error — rowshape could not decide, and says so |

`WARN` is its own outcome on purpose. A statement that succeeds after holding
`ACCESS EXCLUSIVE` on a 200M-row table for twenty minutes is a successful outage,
and calling that `PASS` would be a lie.

## For agents

```sh
rowshape init --agent
```

writes an MCP config, a versioned agent rule, and a pre-commit backstop, so an
agent reaches for rowshape unprompted. `rowshape mcp` then serves four tools over
the Model Context Protocol from this same binary — `describe_shape`,
`validate_migration`, `explain_finding`, `plan_against` — so an agent can write a
migration, validate it, read the failure, and fix it inside its own turn.

The schemas are deliberately thin. Every advertised tool is paid for in every
session whether or not it is called, so findings return as compact codes with
`explain_finding` as the only expansion path, no tool dumps a full fixture, and a
[budget test](cmd/mcp/schema_budget_test.go) fails the build if the four-tool
surface creeps past ~600 tokens. See [docs/mcp.md](docs/mcp.md).

## In CI

```yaml
- uses: rowshape/rowshape@v1
  with:
    fixture: rowshape.yaml
    migrations: migrations/
```

Findings render as PR annotations at the exact file and line, plus a check
summary — from the same `Verdict` struct the CLI and the MCP server return, not a
second formatter. See [docs/action.md](docs/action.md).

## Privacy

> A fixture contains no rows from your database; it contains statistics computed
> from them; at `--privacy standard` some of those reveal the extremes of numeric
> and date columns; at `--privacy strict` none do.

That is the entire claim. The broader one — that *no production values leave your
database* — is false, and rowshape will not make it. `rowshape inspect --leaks`
shows you exactly which fields in a fixture carry a real value.

## Commands

`init` · `pull` · `hydrate` · `validate` · `explain` · `plan` · `verify` ·
`inspect` · `annotate` · `mcp`

Run `rowshape <command> --help` for the flags each one takes, or see the
[CLI reference](https://rowshape.com/reference/).

## Supported PostgreSQL

**10 through 18.** Every major runs the full corpus in CI on every push, because
a rule that is right on one major can be wrong on another — the catalog reads
branch on the server version, and that is exactly where they break. A new major
goes into the matrix in the same commit that claims support.

MySQL is on the roadmap, behind the fixture spec's vocabulary.

## Development

```sh
go build ./...
go test ./...        # the Postgres-backed tests SKIP silently
```

**A green `go test ./...` does not mean the suite ran.** Everything touching a
real database skips unless you point it at one:

```sh
export ROWSHAPE_TEST_PG_DSN='postgres://postgres@localhost:5433/postgres?sslmode=disable'
go test ./... -count=1
```

You do not need Docker. Any Postgres install ships `initdb`, so a disposable
cluster on a spare port costs two commands and touches nothing else:

```sh
initdb -D /tmp/rowshape-pg -U postgres --auth=trust
pg_ctl -D /tmp/rowshape-pg -o "-p 5433" -l /tmp/rowshape-pg/server.log -w start
```

`trust` auth is safe here precisely because the cluster is disposable and holds
nothing. Tear it down with `pg_ctl -D /tmp/rowshape-pg stop && rm -rf /tmp/rowshape-pg`.

### Verifying everything

`go test ./...` covers the Go code, which is **not** the whole repo — the docs
site build and its per-page JS budget, the npm wrapper's naming tests,
`goreleaser check`, and whether the workflow YAML parses are all outside it. One
command covers the lot:

```sh
scripts/verify-all.sh
```

It reports every check it **skipped** and exits non-zero when anything was, so a
partial run cannot be mistaken for a full one — including the case that matters
most, `ROWSHAPE_TEST_PG_DSN` being unset, where the Postgres suites skip and
`go test` still prints `ok`.

## Related

- [`rowshape/fixture-spec`](https://github.com/rowshape/fixture-spec) — the
  Rowshape Fixture Spec (RFC-0001). The format is deliberately not owned by any
  one tool: anyone can emit a fixture, and a fixture emitted by anyone should
  hydrate anywhere.
- [`rowshape/homebrew-tap`](https://github.com/rowshape/homebrew-tap) — the cask,
  generated on every release.
- [MCPFold](https://github.com/dj-pearson/MCPFold) — one canonical MCP config,
  folded out to every client, loading only the tools each agent needs. rowshape's
  MCP server is built to the same discipline: MCPFold cuts the context-window tax
  across servers, rowshape keeps its own share of it small at the source.

## License

MIT — the CLI and the spec both, so neither can be a gate on the other.
