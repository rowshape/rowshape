# Rowshape — Decisions Log

This file is the durable record of cross-cutting decisions referenced by `prd.json`.
It is the source of truth for the canonical Go module path (P0-T1) and the
resolution status of the `open_decisions` in `prd.json`.

---

## D-001 — Canonical Go module path

**Decision:** `github.com/rowshape/rowshape`

- This is the fixed import root for the OSS CLI + MCP server binary.
- Every internal package imports under this path (e.g.
  `github.com/rowshape/rowshape/internal/fixture`,
  `github.com/rowshape/rowshape/internal/verdict`).
- The phase-5 cloud API (PRD §9) imports the CLI's `fixture` and `verdict`
  packages from this same path — there must be exactly ONE implementation of
  canonical form + digesting (INV-ONE-CANONICAL-FORM).
- `go.mod` MUST declare exactly this module path. (P0-T1 verification:
  `grep` module path in `go.mod` matches this file.)

Rationale: PRD §14 Phase 0, §7 (distribution / single static binary).

> Status note: the repository currently lives at `github.com/dj-pearson/rowshape`.
> The Go module path is decided independently of where the git remote points and
> will not change when the code moves under the `rowshape` GitHub org. Imports are
> stable from day one.

---

## D-002 — Namespace reservations (P0-T1)

These are external ops actions. Each must be confirmed by the owner
(Dan Pearson / Pearson Media LLC) and the confirmation captured here.

| Namespace | Target | Status | Confirmation |
|-----------|--------|--------|--------------|
| Domain | `rowshape.com` | ☐ pending | _capture registrar confirmation_ |
| GitHub org | `rowshape` | ☐ pending | _create org; move repos under it_ |
| npm package | `rowshape` | ☐ pending | _placeholder publish of the npx wrapper package name_ |
| GitHub repo | `rowshape/fixture-spec` | ☐ pending | _create public repo; RFC-0001 + README + LICENSE (P0-T2)_ |
| GitHub repo | `rowshape/homebrew-tap` | ☐ pending | _tap the goreleaser cask targets (P0-T4)_ |
| Go module path | `github.com/rowshape/rowshape` | ☑ decided | D-001 above |

Until the three external reservations are confirmed, **P0-T1 stays `blocked`**
in `prd.json` (loop rule: never fake-pass; never weaken acceptance criteria).
The Go module path — the only part that blocks downstream code — is settled, so
code tasks (P0-T3+) can proceed.

---

## D-003 — Open decisions (mirrors `prd.json.open_decisions`)

Tracked here so resolutions are recorded where code can cite them.

| ID | Question (short) | Leaning | Resolve by |
|----|------------------|---------|-----------|
| OQ-PGSS | Capture `pg_stat_statements` in v1 fixtures? | Capture, don't act (schema-additive) | phase-1 (P1-T1) |
| OQ-TARGET | Disposable target: testcontainers-go vs embedded/pg_tmp? | **RESOLVED — see D-005: the docker CLI is driven directly; testcontainers-go was not adopted.** (This row previously read "ship testcontainers-go default", contradicting D-005 and the code; corrected by the CR-loop audit, D-018.) | phase-1 (P1-T9) |
| OQ-ESCALATION-CEILING | Cost ceiling for auto-escalation? | Soft cap + WARN naming what was skipped | phase-1b (P1b-T4) |
| OQ-HLL-PRECISION | HLL precision 14 vs 16? | 14 (~1.6% err, 16KB) — measured only needs to beat estimated | phase-1b (P1b-T1) |
| OQ-PARTITIONS | Partitioned tables: parent-only or per-partition? | Parent declares `partitions: {count, strategy, skew}` | phase-1 (P1-T12) |
| OQ-BLOAT | Does `bloat_estimate` belong in a portable spec? | Unresolved; revisit at MySQL | phase-5 (P5-T4) |
| OQ-CORRELATION | Multi-column correlation block now or defer? | Defer to v2 | v2 (out of v1 scope) |

---

## D-005 — Disposable hydrate target (OQ-TARGET, P1-T9)

**Decision:** A single `internal/target.Target` interface abstracts the database
`hydrate` loads into, so the disposable mechanism can be swapped without touching
the synthesis engine. Three implementations ship:

- **`Ephemeral`** (default disposable target) — creates a throwaway database on a
  reachable Postgres server and drops it on teardown. Dependency-light: it needs
  only a libpq connection, no Docker daemon and no container SDK.
- **`Provided`** — hydrate loads into a user-supplied `--target` URL; teardown
  only closes connections, never drops the database.
- **`Container`** — a Docker-based disposable target (throwaway `postgres`
  container) for full OS-level isolation, invoked through the `docker` CLI.

**Why not testcontainers-go as the default (RFC §17.2 / OQ-TARGET):**
`testcontainers-go` pulls in the Docker client SDK and dozens of transitive
dependencies, which conflicts with **INV-SUPPLY-CHAIN** ("single static binary,
deps kept deliberately few"). The `Target` interface keeps testcontainers-go — or
`pg_tmp` / an embedded Postgres — a clean swap-in behind the same contract, so the
open question stays genuinely open while the shipped default stays dependency-light.
The ephemeral-database default provides the same observable behaviour the story
requires: a disposable Postgres is spun up and torn down after use.

---

## D-004 — Cloud traction gate (P5-T7)

Cloud (registry, audit, drift, attestation, billing) does NOT start until the
CLI shows organic pull-through (PRD §14 / §14.1 week-20 kill criterion).

**Gate criteria (must be written down and measurable):**

- [ ] GitHub stars ≥ a few hundred (organic, not solicited)
- [ ] ≥ 1 unsolicited issue or PR from someone not personally told about the project

A negative gate result is recorded as an explicit **stop / reassess** decision —
not silently ignored, not a reason to push harder on momentum alone. Cloud tasks
P5-T8..P5-T14 stay `blocked` until this gate is explicitly marked passed here.

---

## D-006 — Version-conditional extrapolation boundary is PG 11, not 11↔16 (P2-T5)

**Context:** P2-T5 acceptance criterion 4 asks for "a version-conditional case
[that] differs correctly between PG 11 and PG 16." Per RFC §9.1 the operation
whose cost is version-conditional is `ADD COLUMN ... DEFAULT`: PG **11+**
fast-paths a *non-volatile* default into the catalog (O(1), instant) instead of
rewriting the table; a *volatile* default rewrites on every version, and
`SET NOT NULL` full-scans on every version.

**Decision:** The catalog fast-path landed in PG 11, so PG 11 and PG 16 behave
**identically** for this operation — correctly, not by omission. The genuine
divergence is at the **10 → 11** boundary: PG 10 rewrites the whole table (a
heavy, user-visible bucket) while PG 11 and PG 16 are a catalog-only instant.

`internal/estimate` therefore asserts version-conditional divergence across the
**real** boundary (PG 10 vs 11 vs 16 in `TestVersionConditionalDivergence`),
including that PG 11 == PG 16. Fabricating a false 11-vs-16 difference — or
modeling the PG 12 `SET NOT NULL`/`CHECK` scan-skip, which the P2-T5 description
explicitly excludes ("SET NOT NULL still full-scans") — would contradict the
spec, so neither was done (loop rule: spec wins; never fake a pass).

The corpus PG-version matrix (P2-T13, `ROWSHAPE_PG_VERSION` 11–17) is where
version-conditioned corpus *runs* live; the model's version-conditionality is
proven here in the estimate unit tests named by P2-T5's verification.

## D-007 — Corpus version matrix extended to PG 10 to exercise the real boundary (P5-T3)

**Context:** P5-T3 asks for corpus cases that "cover version-divergent behaviors
… across PG 11-17" and cost models that "produce version-correct buckets on each
major." But D-006 established that within PG **11–17** the operations rowshape
models do **not** diverge: the non-volatile-`DEFAULT` catalog fast-path landed in
PG 11, so PG 11 == PG 17 for `ADD COLUMN … DEFAULT`; a volatile default rewrites
on every version; `SET NOT NULL` full-scans on every version; a bare (non-`CHECK`
-assisted) `SET NOT NULL` gains nothing from the PG 12 optimization. Fabricating a
false 11-vs-17 divergence would contradict the spec (loop rule: spec wins; never
fake a pass).

**Decision:** The genuine version boundary is **10 → 11**, so the version matrix
is extended down to PG **10** (`.github/workflows/corpus.yml` now runs 10–17). A
single new corpus case, `version-add-column-default`, carries the same migration
across that boundary: `ADD COLUMN … DEFAULT '<const>'` is a catalog-only instant
(PASS) on PG 11+ but a full-table `ACCESS EXCLUSIVE` rewrite (RS-LOCK WARN) on PG
10. This is the RFC §9.1 point made executable — the older databases most likely
to hold scary migrations are exactly where the model must not be confidently
wrong.

To express "right on one major, wrong on another" the corpus format gains an
optional per-major override: `expected.json` may carry a `version_verdicts` map
(keyed by the major the matrix drives, e.g. `"10"`) that overrides the default
verdict/findings for that major only. `Expected.ForMajor(major)` resolves it, and
`TestCorpusVerdicts` compares against the resolved expectation for the major under
test. Every other case keeps a single verdict that holds across the whole matrix.

The model's per-major verdict is also proven **offline** (no live server) in
`internal/findings/version_matrix_test.go`, which drives the case through
`BuildResult` for PG 10–17 and asserts WARN on 10, PASS on 11–17. Deepening the
model further (e.g. modeling the PG 12 `CHECK`-assisted `SET NOT NULL` scan-skip)
would require a new duration finding for a *different* migration shape (a
pre-existing validated `CHECK (col IS NOT NULL)`); it is deliberately left for a
follow-up rather than bolted onto this boundary case.

---

## D-008 — Release + deploy infrastructure gate (P0-T4, P0-T5, P4-T1, P4-T3)

All buildable work for the release pipeline, the GitHub Action, and the docs site
is **complete and locally verified**; the remaining steps are owner-only and
cannot be performed from the build environment. They gate a large set of stories
from flipping to `passes: true` (the loop rule: never fake-pass).

**One-time owner setup (order matters):**

1. Reserve the namespaces + create the repos in D-002 (org must exist first).
2. Add repository/organization **secrets** for the release + deploy workflows:
   - `NPM_TOKEN` — publish the npm wrapper (`.github/workflows/release.yml`).
   - `HOMEBREW_TAP_GITHUB_TOKEN` — push the cask to `rowshape/homebrew-tap`
     (without it the release SUCCEEDS but the tap silently does not update).
   - cosign keyless signing uses CI OIDC (`id-token: write` — already in the
     workflow; no secret, but requires the workflow to run under the org).
   - `CLOUDFLARE_API_TOKEN` + `CLOUDFLARE_ACCOUNT_ID` — deploy the docs site
     (`.github/workflows/docs-deploy.yml`); create the Cloudflare Pages project.
3. Push the first release tag: `git tag v0.1.0 && git push --tags` → the release
   workflow builds all 6 platform/arch archives, SBOM + cosign, the Homebrew cask,
   the ghcr image, and publishes npm.

**What unblocks once the org exists + a tag is published:**
`P0-T4`, `P0-T5` become verifiable; `P4-T1` (the Action fetches a released binary)
and the DB-backed e2e (`test/action`, `test/demo`) run for real; `P4-T3` docs
deploy once Cloudflare secrets exist. Until then these stay `blocked`.

---

## D-009 — Two PRD wording amendments pending owner sign-off (P4-T3, P4-T6, P4-T7)

Two acceptance criteria are, as literally written, **impossible against the
correct implementation**. They were NOT reinterpreted or relaxed unilaterally
(loop rule: never weaken an acceptance criterion to make a task pass); they are
recorded here for an owner decision, each with a recommendation. Deciding them is
what lets the affected docs/demo stories flip to `passes: true`.

| ID | Criterion (as written) | Why it can't hold | Recommendation |
|----|------------------------|-------------------|----------------|
| **§9 zero-JS** (P4-T3) | "ships zero client JS (Starlight default)" | Starlight emits 8.6–11.1 KiB/page of progressive-enhancement JS; there is no supported zero-JS mode. | Amend to "**no framework runtime; client JS under a per-page budget enforced in CI**" — already implemented (`check-build.mjs`, 32 KiB/page). |
| **§13 FAIL RS-LOCK-001** (P4-T6, P4-T7) | naive migration "→ FAIL RS-LOCK-001" | `RS-LOCK-001` is `WARN` **by design** (a rewrite is an availability problem, not data corruption; capping only lowers severity). FAIL is reserved for RS-DATA integrity findings. | Amend to "**rejected on RS-LOCK-001 (WARN gated to fail)**". The demo gates the WARN via `--warn-fail` + the agent rule, so the loop still rejects the naive migration. |

Corollary already handled in the demo (no decision needed, recorded for context):
the three-step rewrite enforces NOT NULL via a **validated `CHECK`**, not a final
`SET NOT NULL`, because rowshape correctly cannot certify `SET NOT NULL` on a
column absent from the fixture (it caps to WARN). This is the tool being more
honest than the PRD shorthand; the validated CHECK is the equivalent it can prove.

---

## D-010 — `range` carries no confidence, so findings resting on it declare `absent` (CR-T11)

**Status:** open — needs an owner/RFC decision. The code change is already made
and is safe either way; this records the question the change exposed.

`rsconstraint.checkConflict` (RS-CONSTRAINT-010) concludes from a column's
profiled **range** that a `CHECK` will fail on existing rows. It used to declare
`DependsOn: [<table>.rows]` — a fact it never reads. That is false provenance in
a signed document, and it borrowed the row count's confidence for a claim the row
count does not support.

Fixed to declare `<table>.<column>.range`. That path has no case in
`verdict.factConfidence`, so it resolves to `absent`.

**Why `absent` is the correct reading, not a gap to paper over:** `fixture.Range`
is `{Min, Max, Mean}` (RFC §6.1) with **no confidence field**. There is genuinely
nothing to report. `absent` ranks below every named level (RFC §7.4), so a finding
resting on a range can never license a PASS — which is right, since a range read
from `pg_stats` is not a proven bound. It does not weaken RS-CONSTRAINT-010
itself: that finding is severity `error` → wants FAIL, and capping leaves FAIL
untouched.

**The open question:** should `Range` carry a `confidence` (and `via`) like every
other fact in the format?

| Option | Consequence |
|---|---|
| **A. Leave it** (status quo) | Range-based findings always read `absent`. Correct and safe, but a range proven by a full scan is indistinguishable from one guessed by the planner. |
| **B. Add `confidence`/`via` to `Range`** | Additive, RFC §12-compatible. A scanned range could then certify, and `--exact` (see CR-T28) would have something to upgrade. Requires an RFC revision, a schema bump, an emitter change, and a `factConfidence` case. |

**Recommendation: B**, but as its own RFC change with the fixture-spec version
bump, not folded into a code-review remediation story. Every other fact in the
format carries its confidence; `Range` looks like an omission rather than a
decision, and option A permanently caps an entire finding class at WARN-or-worse
regardless of how well the data was measured.

---

## D-011 — Explicit `float64()` on the synthesis path is load-bearing (CR-T14)

**Status:** decided and implemented.

`INV-DETERMINISM` (RFC §10) promises that the same fixture, seed and engine
version produce **byte-identical** output on any platform. Three expressions on
the synthesis path had the form `x + y*z`:

| Site | Expression |
|---|---|
| `internal/hydrate/engine.go` `sampleHistogram` | `lo + r.float64()*span` |
| `internal/hydrate/fanout.go` `lerp` | `a + (b-a)*t` |
| `internal/hydrate/fanout.go` `bodyQuantile` | `2*p50 - p95` |

The Go spec permits an implementation to **fuse** a floating-point multiply and
add into one operation with a single rounding, "possibly across statements". The
gc compiler does exactly this on **arm64, ppc64 and s390x**, and does **not** on
amd64 (FMA3 is outside the GOAMD64=v1 baseline). So a developer on Apple Silicon
and amd64 CI would synthesize different data from the same fixture and seed.

**This was measured, not assumed.** Simulating a fusing backend with
`math.FMA(r, span, lo)` over 2,000,000 draws at realistic fixture magnitudes:

- **14.15%** of float results differ
- **0.0725%** of the `int64` values that actually reach the SQL differ — about
  **one row in 1,380** (~72 rows on a 100k-row table)

**Remedy, from the spec itself:** "An explicit floating-point type conversion
rounds to the precision of the target type, preventing fusion that would discard
that rounding." All three sites now wrap the product in `float64(...)`.

**Do not "simplify" these conversions away.** They look redundant and are not.
Each carries a comment saying so, and `TestFMAFusionWouldChangeSynthesizedValues`
fails if the hazard ever stops being real (i.e. it would tell you when the
conversions genuinely became unnecessary, rather than leaving them as cargo).

**Enforcement:** `TestHydrateOutputDigestIsStable` pins a golden SHA-256 of the
emitted SQL, and the `determinism-matrix` job in `ci.yml` runs it on
`ubuntu-latest` **and** `ubuntu-24.04-arm`. A cross-architecture promise cannot
be enforced by a single-architecture job.

**Not yet observed on real arm64 hardware.** The fix is derived from the language
spec and verified by FMA simulation plus an arm64 cross-compile; the arm64 CI job
is written but has not run here (no arm64 runner available in this environment,
and pushing is gated by D-008). First green run of `determinism-matrix` on
`ubuntu-24.04-arm` is what converts this from "correct by construction" to
"observed".

---

## D-012 — Two unreachable functions deleted, and the version gate centralized (CR-T21)

**Status:** decided and implemented.

Two functions had no production caller — only tests, which is how they looked
alive. Both were **deleted**, and the reasoning is recorded here because
unreachable code that is silently removed tends to be silently reinvented.

### `internal/profile.probeUniqueCount` (RFC §7.2 route 3) — deleted

Production proves uniqueness exclusively via `probeUniqueExistence` (route 2),
called from `columns.go` and `escalation.go`. Route 3 answers the same question
and costs strictly more: `EXISTS` short-circuits on the first duplicate, while
the count comparison scans the whole column. Both are exact, so `INV-UNIQUENESS`
is satisfied either way and route 2 is the better of two correct options.

The one thing route 3 offered that route 2 does not is the **number** of
duplicates. Nothing consumes that today. If a future finding wants "N duplicate
values block this unique index", route 3 is in git history and its shape is
described in the RFC — recovering it is cheaper than carrying an untested path.

### `internal/estimate.ForFixture` (+ `ErrNoVersion`) — deleted

`ForFixture` enforced RFC §9.1's refusal to extrapolate without
`meta.engine.version`. It was never called: `internal/findings.estimateFor` is
the path every analyzer actually uses, and it is the more evolved of the two —
it also handles the `tableKnown` refusal (the P2-T8 follow-up) and the
`--calibrate` two-point fit, neither of which `ForFixture` knows about.

Keeping a second, weaker implementation of the same rule in another package is
how the two drift.

### The gate now has ONE enforcement point

Deleting `ForFixture` alone would have left the real duplication in place: the
version check was written three times, as `if hasVersion { ... }` in `rslock`,
`rsindex` and `rsconstraint`. That is the shape the review flagged — the gate
enforced in N places, able to drift, and easy for a fourth analyzer to forget.

`hasVersion` is now a parameter of `estimateFor`, which returns `nil` when it is
false. The three wrappers are gone. `TestVersionGateHasOneEnforcementPoint`
drives all three analyzers against a version-less fixture and asserts none
attaches an estimate; it `Fatal`s rather than skips if an analyzer produces no
finding, so it cannot pass over nothing.

`hasVersion` is still computed per analyzer and still used for a *different*
purpose in `rslock.classifyRewrite` (the version-conditional rewrite decision at
the PG 11 boundary, D-006). That is not duplication of the estimate gate.

**Verified:** no emitted verdict changed — the full corpus is green against a
live PG, and `golangci-lint`'s `unused` check is clean.

---

## D-013 — PR-summary cells escape structure, not authored Markdown (CR-T26)

**Status:** decided. CR-T26's request to escape backticks was **investigated and
declined**, with the reasoning recorded here rather than the story closed by
silence (phase-cr definition of done).

CR-T26 asked for `cell()` to escape backticks and other inline Markdown in the
PR-summary table. Implementing it **broke an existing test**, and the test was
right.

**What `cell()` actually receives.** It is applied to exactly four values:
`Code`, `Severity`, `Estimate` and `Remediation`. The first three are
rowshape-controlled enums or codes. None of the four is free-form user text —
the finding `Title` and `Detail`, which do interpolate identifiers from the
user's migration, are **not** rendered through the summary table at all.

**Why escaping backticks is actively wrong.** The `Remediation` strings in the
finding catalog contain backticks **on purpose**, so commands render as inline
code in the PR summary — e.g. ``Run `rowshape pull --exact` to prove
uniqueness.`` Escaping them makes a reviewer see literal `\`` backslashes. That
is a regression in the surface P4-T2 makes the primary reviewer-facing output,
in exchange for protecting against nothing.

**What is escaped, and why that is the right line:** a literal `|` and newlines,
because those break the table *structurally* — a row splits or the table ends.
Inline constructs change *formatting*, and here the formatting they change is
formatting rowshape authored deliberately.

**Residual risk, accepted:** an unbalanced backtick reaching a cell through an
identifier interpolated into a remediation (CR-T5's partial-index query is the
one construction that does this) would open a code span. Postgres identifiers
require double-quoting to contain a backtick, so this needs a deliberately
pathological table name, and the consequence is a mis-rendered cell in a summary
— not a wrong verdict, and not an injection vector (GitHub sanitizes raw HTML,
as the original review noted).

`TestCellEscapesStructureButPreservesAuthoredMarkdown` pins **both** halves so
neither can be changed by accident, and `TestSummaryRendersRemediationCodeSpans`
guards the specific regression this nearly shipped.

---

## D-014 — Frozen-package readiness assessment (CR-T8/T9/T23/T24)

**Status:** awaiting owner sign-off. **No frozen code was changed to produce
this.** Each story's `blocked_reason` named a specific question; this records the
answers so the decision rests on evidence rather than judgement.

The four are blocked because `internal/verdict`, `internal/fixture` and
`internal/toolerror` are consumer-facing (npm wrapper, GitHub Action, MCP) and
DSSE-signable. The review found **no live bug** in any of them — these are
missing guards and provenance nits around a sound design.

| Story | Verified property | Risk | Recommendation |
|---|---|---|---|
| **CR-T23** exit code 3 duplicated | **No import cycle exists.** `verdict` ⊬ `toolerror` and `toolerror` ⊬ `verdict`. Both sites (`verdict.go:26`, `toolerror.go:68`) are **not JSON fields**, so the refactor cannot alter an emitted document. | Lowest | **Approve first.** New `internal/exitcode`; `toolerror` is deliberately a leaf. CR-T17 now pins exit 3 for all seven categories. |
| **CR-T9** fabricated `0 rows in 0ms` | **Render-only fix is sufficient** — no `Estimate` change needed. `rsindex.go:175` leaves basis fields zero; `render_human.go:45` prints them unconditionally. `estimateFor` floors `ms1` at 1 for row-based estimates, so **`BasisMs == 0` uniquely identifies the byte path** and cannot collide with a real measured zero. | Low | **Approve as render-only.** Explicitly decline any pointer-field variant. |
| **CR-T8** unvalidated severity strings | Analyzers emit exactly three constants; `wantFor`/`Cap` have **one call site each**. A guard is a **no-op for every current caller**. | Low | **Approve.** Additive-only. |
| **CR-T24** first-match FK tie-break | Confirmed first-match in the `orphan_fraction` branch. **No corpus fixture has two FKs on one column**, so the case is unreachable today — the change is a no-op for existing tests, and nothing would catch a regression. | Low, but in the capping engine | **Approve with a condition:** requires a new capping test constructing the two-FK shape, since the corpus cannot exercise it. |

**Why none were implemented:** "needs explicit owner sign-off" is the gate the
stories carry, and gathering evidence is what makes sign-off possible — not a
substitute for it. Each remains `passes: false`, `status: blocked`, with the
assessment recorded in a `readiness` field on the story.

---

## D-015 — What "the PG matrix is green" does and does not prove (CR-loop audit)

**Status:** recorded. No story's `passes` changed; this makes four already-passing
stories' claims precise instead of leaving them overstated.

Auditing the `verification` entries of passing stories found that **12 have no
mechanically-runnable verification at all**, and four of those rest on a claim
that has **never been observed**:

| Story | Claim |
|---|---|
| `P2-T8` | "corpus RS-LOCK cases green across PG matrix" |
| `P2-T11` | "corpus RS-INDEX cases green across matrix" |
| `P2-T13` | "CI matrix green across all seven majors" |
| `P5-T3` | "corpus matrix CI green across PG 11–17" |

The matrix lives in `corpus.yml`, which needs the GitHub org from `P0-T1`. **CI
has never run.** Every local verification in this repo's history has used a
single major — currently 18.1, which is not even inside the declared 11–17 range.

### What the audit then established

**1. Sweeping 11–17 is nearly vacuous for version-conditionality.** Exactly one
corpus case declares `version_verdicts`, and it overrides major **10**. Across
11–17 every case falls through to the same default, so seven runs assert one
thing seven times. They are not worthless — they prove no case's expectations are
malformed at any declared major — but they do not exercise a branch.

**2. The boundary that matters IS locally verifiable, and passes.** The
version-conditional finding derives from the fixture's declared
`meta.engine.version`, **not** from the server's actual behavior — the pipeline
validator sets `c.Fixture.Meta.Engine.Version = PGMajor()`. So the PG 10 vs 11+
boundary (D-006, D-007: `ADD COLUMN ... DEFAULT <const>` rewrites on 10, catalog-
only on 11+) can be tested against one real server:

```
ROWSHAPE_PG_VERSION=10 → WARN + RS-LOCK   (override branch)  PASS
ROWSHAPE_PG_VERSION=16 → PASS, no findings (default branch)  PASS
```

Both green against the same PG 18.1 cluster. **The version-conditional model is
verified across its only real boundary.**

**3. What remains genuinely unverified** is narrower than "the matrix": whether
real PG 11 through 17 *engines* behave as the models predict. That needs seven
real servers, i.e. CI, i.e. `P0-T1`. It is the same category as CR-T14's arm64
gap — correct by construction and locally corroborated, not observed on the
target.

### Why this is not a downgrade

None of the four stories is wrong; the code works and the model is right at the
boundary. The defect was that the record asserted an observation nobody had made,
which is the same class of problem as CR-T13 (a guard naming a guarantee it did
not check). Stating the limit costs nothing and stops the claim being read as
stronger than the evidence.

---

## D-016 — Two ordering violations, and the exposure they created (CR-loop audit)

**Status:** recorded and quantified. No story's `passes` changed — the work is
real. What is recorded is the risk the violated dependencies existed to prevent.

Auditing the `depends_on` graph (102 stories: no cycles, no dangling refs, no
backwards phase dependencies) found **two passing stories whose dependency does
not pass**, which the loop's own rule forbids: *"a story may only be selected
when every id in its `depends_on` has `passes: true`."*

| Passing story | Unmet dependency |
|---|---|
| `P0-T3` — Scaffold the Go CLI module **and fix the module path** | `P0-T1` — Reserve all namespaces (**blocked**) |
| `P2-T16` — Conformance suite + **published** JSON Schema | `P0-T2` — Publish the spec as its own public repo (**todo**) |

Neither is bookkeeping. Both dependencies encode an **external act** — reserving
a namespace, publishing a repo — that no amount of local work can satisfy, so in
each case code was written against an assumption the dependency exists to remove.

### The exposure, measured

`P0-T3` fixed the module path to `github.com/rowshape/rowshape` **before**
`P0-T1` reserved the name. As of this audit:

- **97 Go files** contain that import path
- **21** further occurrences in `.goreleaser.yaml` and `npm/package.json`
- **50 passing stories** transitively depend on `P0-T1`
- the GitHub org `rowshape` and the npm package `rowshape` are **still free but
  still unreserved** (re-verified this session: both 404)

`P0-T1`'s own `namespace_check` states the consequence exactly: *"If any of it
were taken this is not a blocked story, it is a redesign."* The ordering
violation is the mechanism by which that risk spread from one story to fifty.

`rowshape.com` is registered and delegated to Cloudflare, so the domain leg is
likely already held.

### Why nothing is being reverted

The work is correct and verified; unwinding it would destroy value and fix
nothing. The dependency was violated in the only direction that is recoverable —
**the name is still available**. The action that closes this is not a code
change: create the GitHub org and reserve the npm name, which costs minutes and
retires the exposure on all 50 stories at once.

`TestNoNewOrderingViolations` allow-lists exactly these two with reasons, fails
on any new one, and fails if an entry goes stale — so an exemption cannot
silently start covering a different violation later.

---

## D-017 — `spec_refs` audit: clean, and the citation convention that fooled the audit

**Status:** recorded. No defect found; this exists so the next audit does not
re-raise the same false alarm.

All **55 distinct PRD/RFC sections** cited across the 102 stories were checked
against `PRD-rowshape-v1.md` (580 lines) and
`RFC-0001-rowshape-fixture-spec.md` (599 lines). **Every citation resolves.**

Four initially read as dangling — `PRD §17.2`, `RFC §14.2`, `§14.4`, `§14.5` —
because the convention is `§<section>.<numbered item>` where the item is a
**numbered list entry, not a heading**. Both documents end with an "Open
questions" section (PRD §17, RFC §14) whose entries are cited by ordinal:

| Citation | Resolves to |
|---|---|
| `PRD §17.2` | open question 2 — disposable target: testcontainers-go vs pg_tmp |
| `RFC §14.2` | open question 2 — partitioned tables |
| `RFC §14.4` | open question 4 — HLL parameters |
| `RFC §14.5` | open question 5 — auto-escalation cost ceiling |

The PRD uses the convention itself: its §17.3 cites "RFC-0001 §14.5".

**No permanent check was added**, deliberately. A parser that handles headings,
numbered items and nested ordinals correctly is fragile, and this audit found
zero real defects — so the check would be pure maintenance cost against a
demonstrated-zero yield. The convention is documented here instead.

### Note on audit false-positive rate

Across six audit passes this session, first-pass automated checks raised **four
false alarms** — a vacuous `-run` pattern that was really a prefix match, an
"unenforced" invariant that had six tests not naming it, and these four
citations. Every one was caught by confirming before reporting. The checks that
survived into `tools/prdaudit` are the ones whose findings held up.

---

## D-018 — `open_decisions` audit: four settled in code, one decided by omission (CR-loop)

**Status:** recorded. Each conclusion is backed by a code reference, not by
reading the leaning and assuming it happened.

`prd.json.open_decisions` has no field in which a question can be marked
resolved — entries carry only `id`, `question`, `leaning`, `resolve_by`,
`source`. So a question settled by shipped code stays indistinguishable from one
nobody has touched. A `status_note` now records the actual state of each.

| Question | Verified state |
|---|---|
| `OQ-TARGET` | **Resolved (D-005).** The docker CLI is driven directly — 14 call sites in `internal/target/container.go`. testcontainers-go was **not** adopted. |
| `OQ-HLL-PRECISION` | **Resolved.** `const Precision = 14` in `internal/profile/hll/hll.go`. |
| `OQ-ESCALATION-CEILING` | **Resolved.** `DefaultMaxEscalationRows = 50_000_000` — the soft cap + WARN shape. |
| `OQ-PARTITIONS` | **Resolved.** Parent-only `Partitions *Partitions` in the fixture model. |
| `OQ-PGSS` | **Decided by omission — needs an explicit call.** |
| `OQ-BLOAT` | Genuinely open; defers to MySQL (`P5-T4`, blocked). |
| `OQ-CORRELATION` | Genuinely open; deferred to v2 by design. |

### Two findings worth acting on

**1. A three-way contradiction on `OQ-TARGET`.** `prd.json` said unresolved;
D-003's table said *"Unresolved; ship testcontainers-go default"*; **D-005 says
the opposite and the code agrees with D-005.** D-003's row has been corrected —
it claimed to *mirror* `open_decisions` and had drifted from the very file it
mirrors. (`internal/target/testcontainers.go` was also still listed as a `P1-T9`
artifact and does not exist; the artifact audit removed it.)

**2. `OQ-PGSS` was answered by nobody.** The recorded leaning is *"capture, don't
act"*, and `pg_stat_statements` appears **nowhere in the codebase** — so the
opposite shipped without a decision being made. Its `resolve_by` (phase-1,
`P1-T1`) is long past, and the question itself warned that *"retrofitting the
fixture schema is costly"* — which is now the situation, since the schema is
stable and published. **This needs an owner call:** accept "omit entirely"
deliberately, or implement the leaning and pay the retrofit.

That is the only question in the file where the default that shipped contradicts
the recorded intent.
