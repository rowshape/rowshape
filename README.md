# rowshape

**The type-checker for database migrations — a human and an agent get the same answer through the same contract.**

Execute a proposed schema change against production-shaped data in a disposable
environment and return a machine-readable verdict.

- **Module path:** `github.com/rowshape/rowshape` (see [docs/DECISIONS.md](docs/DECISIONS.md))
- **License:** CLI + spec MIT.
- **Status:** early build-out. See [`prd.json`](prd.json) for the build loop and current progress.

## Build

Requires **Go 1.25.0 or newer** — the floor declared in `go.mod`, and the floor on
`go install github.com/rowshape/rowshape@latest`.

```sh
go build ./...        # builds the single `rowshape` binary
go vet ./...
```

## Test

```sh
go test ./...         # unit tests — the Postgres-backed tests SKIP silently
```

**A green `go test ./...` does not mean the suite ran.** Everything that touches a
real database — the catalog reads, `validate` end-to-end, the disposable-target
lifecycle, the corpus triples, and the Week-6 pathology gate — skips unless you
point it at a Postgres:

```sh
export ROWSHAPE_TEST_PG_DSN='postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable'
go test ./... -count=1
```

Without it those tests report nothing, which reads exactly like passing. CI sets
the DSN and runs the corpus across the PG 10–18 matrix
([`corpus.yml`](.github/workflows/corpus.yml)).

### A throwaway Postgres, without Docker

You don't need Docker, and you shouldn't point this at a database you care about.
Any Postgres install ships `initdb`, so a disposable cluster on a spare port costs
two commands and touches nothing else:

```sh
initdb -D /tmp/rowshape-pg -U postgres --auth=trust
pg_ctl -D /tmp/rowshape-pg -o "-p 5433" -l /tmp/rowshape-pg/server.log -w start

export ROWSHAPE_TEST_PG_DSN='postgres://postgres@localhost:5433/postgres?sslmode=disable'
go test ./... -count=1
```

`trust` auth is safe here precisely because the cluster is disposable and holds
nothing. Tear it down with `pg_ctl -D /tmp/rowshape-pg stop && rm -rf /tmp/rowshape-pg`.

With the DSN set, exactly one test should skip (`TestContainerLifecycle`, which
wants Docker). If more than that skips, your DSN isn't reaching the server — and
the suite will still say `ok`.

## Verifying everything

`go test ./...` covers the Go code — which is **not** the whole repo. Four other
surfaces verify things no Go test touches: the docs site build and its per-page
JS budget (findings pages are *generated* from `internal/findings/registry.go`,
so a Go change can break a page no Go test renders), the npm wrapper's naming
tests, `goreleaser check`, and whether the workflow YAML parses at all.

In CI those live in separate, path-filtered workflows, so no single run proves
the repo is sound. One command does:

```sh
scripts/verify-all.sh
```

It reports every check it **skipped** and exits non-zero when anything was
skipped, so a partial run cannot be mistaken for a full one — including the case
that matters most, `ROWSHAPE_TEST_PG_DSN` being unset, where the Postgres-backed
suites skip and `go test` still prints `ok`.

## Commands

`rowshape` exposes: `init`, `pull`, `hydrate`, `validate`, `explain`, `plan`,
`verify`, `inspect`, `annotate`, `mcp`. All of them are implemented — run
`rowshape <command> --help` for the flags each one takes.

What is *not* done is distribution and the launch surfaces (the GitHub Action,
the docs site, the recorded agent-loop demo), which are gated on owner-only
namespace and release work — see `docs/DECISIONS.md` D-008 and the `blocked`
stories in `prd.json`.

Exit codes are part of the public contract: `0` PASS · `1` FAIL · `2` WARN-only
· `3` tool error.

## For agents

`rowshape mcp` serves four tools over the Model Context Protocol — `describe_shape`,
`validate_migration`, `explain_finding`, `plan_against` — from this same binary, so
an agent can write a migration, validate it, read the failure, and fix it inside
its own turn. `rowshape init --agent` wires it up. See [docs/mcp.md](docs/mcp.md).

The schemas are deliberately thin. Every advertised tool is paid for in every
session whether or not it's called, so findings return as compact codes with
`explain_finding` as the only expansion path, no tool dumps a full fixture, and a
[budget test](cmd/mcp/schema_budget_test.go) fails the build if the four-tool
surface creeps past ~600 tokens.

## Related repos

- [`rowshape/fixture-spec`](https://github.com/rowshape/fixture-spec) — the Rowshape Fixture Spec (RFC-0001).
- [`rowshape/corpus`](https://github.com/rowshape/corpus) — `(migration, fixture, expected verdict)` triples.
- [MCPFold](https://github.com/dj-pearson/MCPFold) — one canonical MCP config, folded
  out to every client, loading only the tools each agent needs. rowshape's MCP server
  is built to the same discipline; MCPFold cuts the context-window tax across servers,
  rowshape keeps its own share of it small at the source.
