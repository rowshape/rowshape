# Testing gap analysis

*A map of every way rowshape can be tested, what each surface actually verifies
today, and where the gaps are — with evidence, not vibes.*

This is a point-in-time audit (baseline: whole-suite statement coverage **77.1%**,
measured with `-coverpkg=./...` against a live PG 16). It is meant to be
actionable: every gap cites `file:line` and is ranked by the blast radius of the
regression it would let ship silently.

## How the audit was run

```sh
# disposable PG 16, then the honest coverage number (whole suite, cross-package)
export ROWSHAPE_TEST_PG_DSN='postgres://postgres@localhost:5433/postgres?sslmode=disable'
go test ./... -count=1 -coverpkg=./... -coverprofile=cover.out   # 77.1% total
go tool cover -func=cover.out
```

Two coverage numbers matter and they tell different stories:

- **Per-package** coverage (`go test ./...`) measures each package by *its own*
  tests. This is what shows `internal/plan` at **11%**.
- **Whole-suite** coverage (`-coverpkg=./...`) measures each package by the
  *entire* suite, including the end-to-end `cmd` tests. This lifts
  `internal/plan` to **59%**.

The delta between them is the most important signal in this document: **code
that is covered *only* end-to-end has no direct test**. It moves as a side
effect of an unrelated e2e path and will keep reporting "covered" right up until
someone refactors that path and the classifier underneath silently rots. `plan`
and `validate/capture.go` are the two big offenders.

## Verified baseline facts

- With `ROWSHAPE_TEST_PG_DSN` set, **exactly one test skips**
  (`TestContainerLifecycle`, wants Docker). The README's claim holds. ✔
- Without the DSN, the DB-backed suites skip and `go test` still prints `ok` —
  the documented "green that hides coverage." `scripts/verify-all.sh` is the
  guard against mistaking it for a full run. ✔
- **Every finding code** in `internal/findings/registry.go` is produced by at
  least one unit test, and — since phase-cr4 — by at least one *corpus* case too,
  which is what exercises it against a real Postgres in CI. ✔

  This used to read "all 14 finding codes". It was true when written and stopped
  being true silently: by the time there were 26 codes, six were covered only by
  unit tests. A prose claim in a markdown file cannot notice when it expires, so
  it is now enforced by `TestEveryFindingCodeHasACorpusCase` in
  `corpus/harness/coverage_test.go`. Codes with a deliberate exemption are listed
  there with the reason, and the test fails if an exempt code *becomes* covered —
  so the list cannot rot in either direction.

## The testing surfaces — what exists

| # | Surface | Where | Verifies | State |
|---|---------|-------|----------|-------|
| 1 | Unit (statement) | `*_test.go` per package | logic, in isolation | strong (77% whole-suite) |
| 2 | End-to-end CLI | `cmd/*_test.go`, `test/{demo,action,pathology}` | command wiring, exit codes | uneven — see §CLI |
| 3 | Real-Postgres integration | anything reading `ROWSHAPE_TEST_PG_DSN` | catalog reads, apply/capture | gated; skips silently without a DSN |
| 4 | PG version matrix (10–17) | `.github/workflows/corpus.yml` | version-conditional findings | corpus only, not the CLI |
| 5 | Cross-platform / cross-arch | `ci.yml` canonical + determinism jobs | byte-identical hydrate output | strong (linux/mac/win + amd64/arm64) |
| 6 | Conformance (RFC-0001) | `fixture-spec/conformance` | emitter/hydrator/validator MUSTs | strong |
| 7 | JSON Schema | `conformance.yml` | every fixture validates; a bad one is rejected | present |
| 8 | Corpus regression triples | `corpus/`, `corpus/harness` | (migration, fixture, verdict) | present but **DSN-gated at runtime** |
| 9 | MCP tool surface | `cmd/mcp/*_test.go` | 4 tools + token budget | good happy-path, thin on errors |
| 10 | Token-budget guard | `cmd/mcp/schema_budget_test.go` | the 4-tool surface stays small | present |
| 11 | Non-Go surfaces | `verify-all.sh`, path-filtered workflows | docs JS budget, npm naming, goreleaser, workflow YAML | present |

## The testing surfaces — what is entirely absent

These are whole *categories* of testing with zero presence in the repo. Each is
a gap in its own right, independent of line coverage.

| Technique | Present? | Why it matters here |
|-----------|----------|---------------------|
| **Fuzz testing** (`func Fuzz*`) | **none** | The tool's whole job is parsing adversarial SQL and YAML. The hand-written SQL splitter and the YAML fact decoder are exactly what native Go fuzzing exists to harden. |
| **Property-based** (`testing/quick`, rapid) | **none** | Invariants like "any accepted fact never fabricates a stronger confidence than declared" and "split-then-rejoin preserves statements" are properties, not examples. |
| **Benchmarks** (`func Benchmark*`) | **none** | `predictMs`/`estimate` make cost predictions and the hydrate path has an FMA-sensitivity story, yet no benchmark pins throughput or guards a perf regression. |
| **Race detector** (`go test -race`) | **not in CI** | No workflow runs `-race`. The apply/capture path holds pgx connections; a data race there would never surface. |
| **Mutation testing** | **none** | 77% coverage says lines *ran*, not that a test would *fail* if they broke. Mutation testing is the only thing that measures assertion strength — relevant because several packages are "covered" only end-to-end. |

## Per-command CLI behavioral gaps

Exit codes are a public contract (`0 PASS · 1 FAIL · 2 WARN-only · 3 tool error`).
The mapping is well-tested as a pure function
(`internal/exitcode/parity_test.go:51`) but **thinly tested through actual
command invocations**.

| Command | e2e test | Notable gap |
|---------|----------|-------------|
| **pull** | **none — no `pull_test.go` exists** | The one command that touches production and writes the committed fixture. Privacy redaction, source-host hashing, superuser refusal, and all seven `toolError()` paths (`pull.go:66-129`) could regress on a green build. |
| validate | yes, **DSN-gated** | FAIL→exit 1 and WARN-only→exit 2 are **never asserted through the command**. `--target` live branch, `--warn-fail`, `--calibrate`, `--seed`, `--max-rows`, `--runner` untouched. |
| verify | yes, DSN-gated | **Drift→exit 1 is not asserted** — `TestVerifyDriftReadOnly` prints "DRIFT" but discards the returned error (`plan_verify_test.go:177`). |
| plan | yes, DSN-gated | `conflict`/`missing-target` diff marks (`plan.go:73-78`), bad/missing migration path, unreachable target — none exercised. |
| hydrate | partial | SQL-emit path (its default mode), `--out` file, `--seed`/`--max-rows` determinism — untested; only target-write refusal is covered. |
| explain / inspect / annotate / init | yes, offline | Reasonable. `inspect` has the best exit-code coverage (0 **and** 1). Missing-file / malformed-YAML `toolError` paths untested across verify/inspect/hydrate. |
| mcp (command) | tools yes, command no | `newMCPCmd` + `rsmcp.Serve` stdio loop (`mcp.go:22-28`) untested. |

**The single most important CLI gap:** WARN-only → exit **2** is never asserted
end-to-end by any command. Collapsing WARN into PASS(0) or FAIL(1) is precisely
the agent-confusion the exit-code contract exists to prevent, and it would pass
every test in the repo today.

## Findings / verdict engine gaps

- **Multi-finding aggregation is untested offline.** `verdict.Combine` is unit-tested
  with hand-built inputs, but `validate.BuildResult` is never exercised with an
  analyzer set that emits *co-occurring findings of differing severity*. The real
  "one FAIL dominates a WARN" case (`validate_orphans`) and the WARN+WARN cases
  exist **only in the DSN-gated corpus** (`validate.go:143-150`).
- **The entire `capture.go` apply layer is 0% without a live PG.** Lock-mode
  reading (`strongestLock:184`), SQLSTATE capture (`recordResult:167`), and the
  CONCURRENTLY-outside-transaction branch (`applyOne:131`) — the signals every
  finding rests on — are unverified in a default `go test ./...`. There is no fake
  `pgx.Conn`, so none of this can be reached offline.
- **Corpus verdicts are unasserted offline.** `TestCorpusVerdicts` and
  `TestVersionConditionalBoundary` skip without the DSN
  (`corpus/harness/harness_test.go:133,307`).
- **Version-conditionality is tested for exactly one rule.** Only RS-LOCK-001
  (ADD COLUMN DEFAULT) has a PG-major divergence test. RS-INDEX-020 (REINDEX
  CONCURRENTLY, PG 12+) and RS-DATA-001 (validated-CHECK fast path, PG 12+) carry
  version-specific remediation text with **no divergence test**.
- **DB-free, load-bearing classifiers sit at 0% direct coverage:** `isTxControl`
  and `classifyIndexBuild` (`internal/validate/capture.go:206,218`) — trivially
  unit-testable, and `isTxControl` is **duplicated** in `internal/plan/plan.go:163`
  with no test guarding the two copies against divergence.
- **`predictMs` degenerate-basis fallbacks** (`internal/estimate/models.go:132-134,140-141`)
  — the "no usable basis width" paths — are never hit.

## The CI topology gap

The full suite (including `cmd` end-to-end validate/plan/verify) runs on **PG 16
only** (`ci.yml`, `matrix.pg: ["16"]`). The 10–17 matrix in `corpus.yml` runs
only `./corpus/... ./internal/profile/... ./internal/target/... ./test/pathology/...`.

So the corpus *finding rules* are checked across all majors, but the **CLI command
wiring that assembles and returns those verdicts is single-version.** A version
bug in the `validate`/`plan`/`verify` command layer (as opposed to the analyzer
layer) — e.g. the `DROP DATABASE ... WITH (FORCE)` PG13+ breakage that already bit
this repo once — would not be caught by the matrix as scoped.

## Input-parsing attack surface

The tool ingests three untrusted inputs; this is where fuzzing pays off most.

- **SQL splitter** `SplitStatementsIn` (`internal/validate/validate.go:251`) — a
  hand-written rune scanner (line/block comments, `'`/`"` quotes, `E'...'` escape
  strings, doubled quotes, `$tag$` dollar-quoting). Genuinely well unit-tested
  for the happy and escape cases, but **no tests for**: unterminated constructs
  (unclosed block comment / string / dollar-quote), Unicode / invalid UTF-8,
  nested or mismatched dollar tags, or pathological size.
- **YAML fixture** `fixture.Parse` (`internal/fixture/model.go:291`) and the
  custom `Fact[T].UnmarshalYAML` (`internal/fixture/fact.go:24`) — **no direct
  malformed-input test on `Parse` itself**; wrong scalar types decode to silent
  zero values; duplicate keys, alias/anchor bombs, and deeply nested `shape any`
  trees are unguarded and untested.
- **Postgres catalog** `parseAttnums` (`internal/profile/catalog.go:609`) — a
  hand-rolled numeric parser with **no test at all**. (Its `int16` accumulator can
  overflow in principle, but not from a real catalog: Postgres caps a relation at
  1600 columns. Worth a test to pin the contract, not an exploitable bug.)

### Top fuzz-target candidates (ranked by blast radius)

1. `SplitStatementsIn` — the single front door for all migration SQL. A mis-split
   turns one valid statement into a broken fragment that reaches `capture.Apply`
   and yields a spurious verdict.
2. `fixture.Parse` — the single door for every committed fixture; drives verdicts,
   hydration, and MCP output.
3. `Fact[T].UnmarshalYAML` — type confusion here silently yields a zero-value fact,
   feeding `INV-CONFIDENCE-CAPPING` a fabricated confidence.
4. `ParseVerified` — the digest-integrity gate a wrong PASS depends on.
5. `classifyRewrite` (`internal/findings/rslock.go:93`) — a missed
   `ADD COLUMN ... DEFAULT volatile()` is an unflagged production rewrite.

## MCP surface gaps

| Tool | Happy path | Error paths |
|------|-----------|-------------|
| describe_shape | ✔ (+ leak checks, payload budget) | empty/unreadable/tampered fixture untested |
| validate_migration | ✔ (capping, buckets, from-file) | **no test asserts `IsError`** for any bad input; empty-statement branch untested |
| explain_finding | ✔ best-covered (asserts parity with CLI catalog for every code) | empty/whitespace code untested |
| plan_against | ✔ but **DSN-gated** (does not run in normal CI) | highest-risk tool (touches a live DB, `INV-BLAST-RADIUS-ZERO`), weakest unconditional coverage |

The token budget is enforced in **characters** (chars ≈ tokens ÷ 4), strict `>`,
at 700/tool and 2400/session. The boundary itself has no fixture proving a
701-char tool actually trips the guard, and the 4096-char payload budget is only
checked against a 2-table fixture — a large real fixture could exceed it untested.

## Prioritized backlog

Ranked by (regression blast radius) ÷ (effort). Items marked **safe** are new
test files only — no production code changes.

| P | Item | Effort | Safe? |
|---|------|--------|-------|
| 1 | Direct unit tests for `internal/plan` classifiers (`Items`/`classify`/`planTable`/`addColumnName`/`addsBareColumn`) — 11%→covered, all pure | S | ✅ **landed** |
| 2 | `FuzzSplitStatements` with a seed corpus + no-blank / located-agreement / monotonic-line properties | S | ✅ **landed** |
| 3 | Unit tests for `isTxControl` + `classifyIndexBuild` | S | ✅ **landed** |
| 3b | De-duplicate `isTxControl` (verbatim in `internal/plan` and `internal/validate`) into `internal/sqlkind` so one guard covers both — a cross-package pin can't be a test because `plan`→`validate` already, so it must be a refactor | S | ✅ **landed** |
| 4 | End-to-end exit-code assertions: validate FAIL→1, **WARN-only→2**, verify drift→1 | M | ✅ **landed** |
| 5 | `pull` command tests (privacy redaction, source hashing, superuser refusal, toolError paths) | M | ✅ **landed** |
| 6 | `FuzzParseFixture` + malformed-YAML / wrong-type / duplicate-key table tests on `fixture.Parse` | M | ✅ **landed** |
| 7 | MCP error-path tests asserting `IsError` (empty/missing/malformed inputs) for each tool | M | ✅ **landed** |
| 8 | Offline multi-finding aggregation test through `BuildResult` (FAIL-dominates-WARN, WARN+WARN, failed-apply-floor + findings) via a multi-emitting fake analyzer | M | ✅ **landed** |
| 9 | `capture.go` apply/capture covered by a direct **integration** test against a live PG (a fake `pgx` would test a mock, not the pg_locks/SQLSTATE behavior) — 0% → 78-94% | L | ✅ **landed** |
| 10 | Widen the CLI e2e (validate/plan/verify/pull) onto the 10–17 matrix (`corpus.yml` now runs `./cmd/...`) | M | ✅ **landed** |
| 11 | `-race` CI job over `./internal/... ./cmd/...`; benchmarks for the `estimate` hot paths and the SQL splitter | M | ✅ **landed** |
| 12 | Version-**invariance** tests for RS-INDEX-020 and RS-DATA-001 (they carry a "PG 12+" caveat but do NOT diverge — the audit had assumed divergence) | M | ✅ **landed** |

**All twelve items are landed.** The whole backlog was implemented on this branch;
the sections above describe the gaps each one closed.

## Two real bugs the new tests uncovered

Closing an "only covered end-to-end" gap is worth it precisely because the direct
test sees what the end-to-end path walked past. Two shipped bugs surfaced:

> **1. `plan --against` misreports indexes on existing tables.** `planTable` split
> the target table on whitespace only, so `CREATE INDEX i ON t(col)` (no space
> before the `(`) parsed the table as `t(col)`. Against a live schema that name
> never matches, so a normal index on an **existing** table reported
> `missing-target` — the plan told the user their index targets a table that
> isn't there. Fixed by cutting the table token at the first `(`
> (`internal/plan/plan.go`), pinned in `TestPlanTable`.

> **2. The SQL splitter was O(n²) in time and memory.** The dollar-quote scanner
> did `string(runes[i:])` — a full copy of the *remaining input* — on every
> character inside a `$$…$$` body. A ~1000-statement migration took ~0.5s and
> allocated 117 MB. The first benchmark written for the splitter (item 11) made
> it obvious. Replaced with an allocation-free rune-prefix compare: ~0.7ms and
> ~0.8 MB, a ~700× / 150× improvement, with `FuzzSplitStatements` and the
> existing split tests confirming unchanged semantics.

A gap that hides a shipped bug is the strongest argument for closing it — and two
of these were performance/correctness bugs a green `go test` had happily carried.
