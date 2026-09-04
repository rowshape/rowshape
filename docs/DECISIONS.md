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

> Status note (resolved): the repository now lives at `github.com/rowshape/rowshape`,
> which is exactly what the module path declared from day one. It moved
> `dj-pearson` -> `Pearson-Media` -> `rowshape`, and not one import, goreleaser
> target, npm constant or docs link changed across either hop. Both earlier
> locations redirect. This is what deciding the module path independently of the
> git remote bought: the move cost nothing.

---

## D-002 — Namespace reservations (P0-T1)

These are external ops actions. Each must be confirmed by the owner
(Dan Pearson / Pearson Media LLC) and the confirmation captured here.

| Namespace | Target | Status | Confirmation |
|-----------|--------|--------|--------------|
| Domain | `rowshape.com` | ☑ done | Cloudflare zone in the owner's account; `active` custom domain on rowshape-docs, serving 200 |
| GitHub org | `rowshape` | ☑ done | Created; `rowshape/rowshape` public, old locations redirect |
| npm package | `rowshape` | ☑ done | `rowshape@0.0.0` published, owner pearsonmedia (see D-028) |
| GitHub repo | `rowshape/fixture-spec` | ☑ done | Public, RFC-0001 at format version `1`, MIT (P0-T2) |
| GitHub repo | `rowshape/homebrew-tap` | ☑ done | Public, `main` initialized so goreleaser has somewhere to push |
| Go module path | `github.com/rowshape/rowshape` | ☑ decided | D-001 above |

**All three external reservations are now confirmed, and P0-T1 is `done`.** The
domain was the last and the one that had been *inferred* rather than checked: the
early evidence (NOERROR, no A record, Cloudflare nameservers) was consistent with
registered-but-not-deployed, which is not the same as confirmed. It is now a zone
in the owner's account, an `active` custom domain on the `rowshape-docs` Pages
project, serving 200 on every documented route.

The loop rule held the whole way: the story stayed `blocked` while two of three,
then one of three, were outstanding — a story is not done because most of it is.
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

**Status: RESOLVED by D-020 (PR-T7).** `Range` now carries a confidence and
`factConfidence` resolves `.range`. The reasoning below — that `absent` "can never
license a PASS" and so the omission was safe either way — turned out to be wrong
in a way this entry could not see: it protects the verdict of a finding that is
never MADE. A sampled range made `checkConflict` emit nothing at all, and the
verdict was PASS. See D-020 for the reproduction and the two-part fix.

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

## D-019 — `validate` caps a migration statement at 60s, and a cancellation is evidence (PR-T15)

Reproducing a hazard faithfully means reproducing how long it takes. Once
PR-T3 made the disposable database enforce the foreign keys production has,
the corpus's own `cascade_delete_fanout` case stopped being a paper hazard and
became one: its `DELETE` matches 244,223 parent rows, each cascading to a
10,000,000-row child table with **no index on the referencing column** (the
fixture records none, because production has none). Postgres seq-scans 10M rows
per deleted parent. The 190-second suite became a ten-minute hard timeout, and
`validate` had no way to stop.

The hazard is not the bug — reproducing it is the product. The bug is that
**`validate` had to SURVIVE an outage in order to REPORT one.**

**Decision.** `Apply` sets a session `statement_timeout`, default **60 seconds**,
exposed as `--statement-timeout` (`0` disables it, deliberately opt-in).

Sixty seconds is chosen against what the number is *used for*, not against how
long a real migration takes. Hydrated data is a fraction of production and
`validate` runs inside an agent's turn or a CI job; a statement still running
after a minute on that data is already decisively in the slow/outage buckets,
and running it to completion buys no information the ceiling has not given.

**A cancellation is a third outcome, not a flavour of failure.** This is the
load-bearing part:

| outcome | what happened | verdict floor |
| --- | --- | --- |
| completed | nothing objected | whatever the analyzers say |
| **rejected** | the data or the schema said no (SQLSTATE) | FAIL |
| **cancelled** | nothing said anything; it did not finish | WARN |

`Capture.TimedOut` is therefore kept separate from `Capture.Success`, and the
cancelled statement's SQLSTATE is cleared rather than carried — leaving 57014 on
it would report an outage as though a constraint had been violated.

WARN, not FAIL, because FAIL asserts a defect nobody observed. WARN, not PASS,
because a statement that never completed has not been shown to be safe — the
same rule INV-CONFIDENCE-CAPPING states for weak facts, applied to an absent
observation. A duration of "at least the ceiling" is precisely what the `outage`
bucket means (INV-DURATIONS-BUCKETS).

`cascade_delete_fanout` consequently still reports **WARN / RS-PERF-001**, in 98
seconds instead of never: the expected verdict was preserved, not relaxed.

## D-020 — D-010 resolved: `range` carries a confidence, and absence is not proof (PR-T7)

D-010 recorded an open RFC-level question: `fixture.Range` had no confidence
field, so a finding resting on the profiled extremes resolved to `absent` in the
capping engine. The reasoning was that `absent` ranks below every named level and
therefore "can never license a PASS", which made the omission look safe.

**It was not safe, and the deep dive reproduced why.** The failure runs the
OPPOSITE way to the one capping guards:

- Capping caps findings that **exist**. Give it a weak fact and it downgrades the
  verdict the finding argued for.
- A sampled range makes a finding **fail to exist**. `pull` recorded
  `customer_id` as `max: 59,773` from a `TABLESAMPLE` against a true maximum of
  60,000; `ALTER TABLE ADD CONSTRAINT CHECK (customer_id <= 59900)` therefore
  looked satisfiable, `checkConflict` emitted nothing, and the verdict was
  **PASS with exit 0** — while the source database answered `check constraint
  "orders_cust_max" of relation "orders" is violated by some row`.

A missing finding is a PASS nothing downstream can reach. `absent` protected the
verdict of a finding that was never made.

**Decision, in two halves — both needed.**

1. `Range` gains `confidence` (RFC §6.2): `exact` when min/max were read over the
   whole column, `estimated` when sampled. `verdict.factConfidence` now resolves
   `<table>.<col>.range` instead of returning `absent`. Small tables are already
   read whole by `sampleClause`, so most fixtures get **exact** extremes for
   free — the ones that do not are exactly the large tables where the
   understatement bites.
2. `RS-CONSTRAINT-010` reports the ABSENCE of a conflict when it rests on sampled
   extremes: a WARN naming `pull --exact`. Half 1 alone would not have closed it,
   because there is no finding for capping to act on.

**No "far enough outside the range" escape.** A sample gives no bound on how far
past its extremes the real data lies, so any threshold would be a guess dressed as
one — the class of reasoning INV-CONFIDENCE-CAPPING exists to forbid. A sampled
minimum can only be too high and a sampled maximum only too low, so every
non-violating comparison against a sampled range is inconclusive. A fixture that
wants a positive answer can have one for the cost of `pull --exact`.

A fixture written before the field carries no confidence and is read as `absent`,
not as `estimated` — the weakest reading for a fact whose provenance is unknown,
and it deliberately does NOT trigger the new warning wholesale.

## D-021 — The size budget was wrong, not the emitter (PR-T12)

RFC §3.3 promised **"under 100KB for a 200-table schema."** A real
`rowshape pull` of a 200-table schema is **246,129 bytes** — 2.4× over.

The claim survived because the only thing guarding it was
`TestEmitSizeUnder100KB`, which built its 200-table fixture **by hand** with
facts deliberately "at estimated (bare)". It came in at 101,424 bytes against a
102,400 limit — **976 bytes of headroom, under 1%** — and passed. That is the
phase-dd pattern inside the test suite: a hand-authored fixture is
self-consistent in ways a real one is not, so the guard defended a number no
user would ever see. The margin was also so thin that any addition to the
emitted vocabulary — several of which this phase makes — would have turned it red
for reasons unrelated to the change.

**Which to move?** Measured where the bytes actually go, over a real 200-table
pull:

| field | share |
| --- | --- |
| `range` | 16.1% |
| `distinct` | 14.4% |
| `unique` | 13.1% |
| inline `confidence` | 11.5% |
| constraints | 5.2% |
| indexes | 4.5% |
| `length` | 3.7% |
| `format` | 2.5% |
| `histogram` | 0% (none in this schema) |

**No single field dominates and there is no fat.** Every one of the top four is
load-bearing — `unique` and its confidence are the whole §7.2 story, `range` and
`distinct` drive the findings. Slimming any of them costs information the format
exists to carry, to defend a number that was never measured in the first place.

**Decision: correct the claim.** §3.3 now says **under 320KB for a 200-table
schema (~1.2KB/table)**, which is what the emitter costs with room for the
vocabulary to grow. The principle it serves is untouched — 320KB of YAML is still
a file a reviewer reads in a diff — and it is now a number derived from the
artifact rather than asserted at it.

**CONFIRMED by the owner.** The three `prd.json` statements that still asserted
the old number — the phase-1 `ships` line, `P1-T6`'s acceptance criterion, and the
`M1` milestone definition — were corrected too. They belonged to PASSED work, so
leaving them would have meant a task marked green against a criterion the code
does not meet, which is exactly the rot `D-017`/`D-018` were written to catch.
Each now carries the corrected figure and points here.

**And make the guard real.** `TestEmitSizeAgainstBudget` parses
`testdata/real-pull.yaml`, the output of an actual `pull` against a 20-table
schema shaped like §3.3's own description (a heavy tail of lookup tables plus a
few wide ones), and asserts the **per-table** cost projected to 200 tables. Twenty
tables so the testdata stays small; per-table because that is what drifts when the
vocabulary grows. It also asserts a FLOOR, so the budget cannot be quietly
satisfied by the emitter losing facts.

`pull` now warns when what it just wrote is over budget and names what reduces it
(`--schema`, or splitting the schema), and `rowshape inspect --size` reports any
fixture against the budget.

## D-022 — Supported PostgreSQL majors, and how the matrix moves (PR-T14)

**Policy: rowshape supports every PostgreSQL major from 10 through the current
release, and the corpus matrix runs all of them.**

The matrix is not thoroughness for its own sake. Its whole purpose (D-006/D-007,
`corpus.yml`'s own header) is that a rule right on one major can be wrong on
another, and the catalog reads already branch on `serverMajor` at 12 and 15. A
new major is exactly where those branches break — so the newest release anyone
deploys is the worst one to have never run against.

**PostgreSQL 18 is added to `corpus.yml`.** It was previously absent while being
the current release: the matrix stopped at 17.

**One 18-specific defect was found and fixed without an 18 server**, from the
release's catalog changes rather than from a test run. PG 18 introduced
**VIRTUAL** generated columns, which carry `attgenerated = 'v'`. Nothing matched
`'v'`, so such a column fell through to the ordinary-column branch and its
generation expression was recorded as a plain `DEFAULT` — which the target then
emitted on an ordinary column, so an `UPDATE` production rejects would have
succeeded there. A wrong verdict produced by a catalog value the code did not
know existed.

Virtual columns are now recorded as `generated: virtual` and deliberately **not
reproduced**: the syntax exists only on 18+, so emitting it would make a fixture
from an 18 source unhydratable on the 10–17 targets rowshape supports. The column
is created ordinary and REPORTED, the same honest degradation a stored column
with a withheld expression already gets.

**When the next major ships**, add it to the matrix in the same commit that
claims support, and expect the first run to surface something — that is the job.
Behaviour that genuinely differs is captured as a per-major expected-verdict
override with a comment naming the change, never by weakening the assertion.

**Verification of 18 itself is delegated to CI, and that is a real gap until it
runs.** This environment's network policy blocks `apt.postgresql.org`, so no 18
server could be installed here and the corpus has NOT been observed green on 18
locally. The matrix entry is what will observe it.

## D-023 — RS-APPLY: a seventh finding namespace, and why it is not one of the six (PR-T9)

INV-VERDICT-STABLE names six finding classes: `RS-LOCK`, `RS-DATA`,
`RS-CONSTRAINT`, `RS-INDEX`, `RS-PERF`, `RS-REVERSE`. **This adds a seventh,
`RS-APPLY`, and that is a deliberate extension of the public verdict contract.**

**The gap.** A migration that failed to apply was floored to FAIL and produced NO
finding. `--json` returned:

```json
{"verdict": "FAIL", "findings": null}
```

No code, no location, no remediation. The engine's own message — the only thing
that says what went wrong — went to **stderr only**, which is nowhere in the
machine-readable contract.

That breaks the wedge directly rather than cosmetically. `rowshape mcp`'s
`validate_migration` tool and the GitHub Action both render the Verdict struct
**and nothing else**, so an agent was told FAIL and handed nothing to act on —
which is exactly the hand-waving the P4-T8 agent-rule harness scores against.
INV-VERDICT-STABLE also states that `remediation` is mandatory on every error, and
this path had none. And it is the most common real failure there is: a migration
with a typo.

**Why a new namespace rather than reusing one.** The six existing classes all
describe a HAZARD found in a migration that RAN — a lock it takes, data it will
break on, an index it rebuilds. This one says the migration **did not run**.
Folding it into `RS-DATA` would claim the data rejected a statement that may never
have parsed; folding it into any other would be worse. The honest reading is that
the six classify hazards and this classifies their absence.

The extension is additive: existing codes keep their meaning, and consumers were
always going to meet codes they had not seen. What INV-VERDICT-STABLE guarantees
is that codes are **permanent** and **namespaced**, not that the namespace list is
closed.

**CONFIRMED by the owner.** `INV-VERDICT-STABLE` in `prd.json` now names seven
namespaces and carries a `namespace_note` recording why the list grew, so the
invariant no longer contradicts the code it governs.

Landing it exposed how many places had their own copy of the list: the invariant,
the `Finding` doc comment in `internal/verdict`, the registry, and
`corpus/harness`'s `KnownCodes`. The one that was missed failed as `unknown
finding code "RS-APPLY"` on a corpus case that was perfectly correct. `KnownCodes`
is now DERIVED from the registry rather than hand-written — a list that must be
updated in lockstep with another list is a list that will drift — which leaves the
registry as the single source and the two prose statements as deliberate,
reviewable restatements of it.

The family also has a corpus case now (`rsapply-statement-rejected`, a `42P01`
undefined table), so the newest namespace is not a documented hole in the
credibility asset. It has no `resolve_contains` case deliberately: its remediation
names no command to re-run, because nothing resolves a typo but fixing it.

**Shape.** `RS-APPLY-001` carries the SQLSTATE and the engine's message in
`evidence`, the file and line from the capture in `location` (so the Action's
annotation step points at the offending line), and remediation that tells the
reader how to interpret the SQLSTATE class — 23 means production-shaped DATA
refused it, 42 means the statement does not match the schema.

It declares **no `depends_on`**: it rests on what the database DID, not on a
fixture fact. Declaring one would be false provenance in a DSSE-signed document —
the mistake `RS-CONSTRAINT-010` made when it borrowed the row count's confidence.

It lives in `internal/findings` as an ANALYZER rather than inside `validate`,
because analyzers already receive the Capture and because `internal/findings`
imports `internal/validate` — building it in `validate` would need the reverse and
close an import cycle.

**A cancelled statement is NOT an apply failure** (D-019): nothing rejected it, so
reporting one would claim the database refused something it never got to judge.
Pinned by a test.

**RS-APPLY-001 is a FLOOR, not an addition.** When a specific analyzer already
explains the same failure — `RS-DATA-001` saying the column contains NULLs, in the
column's own terms, with remediation about the NULLs — the generic finding is
dropped. Adding "read the SQLSTATE" alongside it is a second entry for one event
that dilutes the actionable advice. Five corpus cases regressed exactly this way
the moment the generic finding existed, which is how the rule was found.

The suppression cannot live in an analyzer: analyzers run independently and none
can see what the others produced. It happens where the whole finding set is known,
in `BuildResult`. Only ERROR findings suppress it — a warning about a migration is
not an explanation of why it was rejected, so a capture that failed with nothing
but warnings still gets the generic account.

Separately, `findings` now marshals as `[]` rather than `null` when empty. A JSON
contract that yields null for "none" makes every reader write the same nil guard,
and some of them forget.

---

## D-024 — RS-INDEX-002 belongs to the index build; RS-LOCK-002 is retired

Two branches independently gave `RS-INDEX-002` different meanings, and the merge
had to pick one. `main`'s meaning wins — **ADD PRIMARY KEY or UNIQUE builds an
index under ACCESS EXCLUSIVE** — because it was already the published contract
and because it covers strictly more: both `PRIMARY KEY` and `UNIQUE`, in both the
bare and the `ADD CONSTRAINT <name>` spellings.

The other branch's DROP INDEX finding is renumbered **`RS-INDEX-003`**. Finding
codes are permanent (INV-VERDICT-STABLE), so this is a one-time reconciliation of
two codes that were never both released, not a renumbering of a shipped code.

**`RS-LOCK-002` is retired**, not renumbered. It reported the same hazard as
`RS-INDEX-002` for `ADD PRIMARY KEY` alone, so keeping both would have filed two
findings for one hazard on one statement. The hazard is an index build, which is
where `RS-INDEX` says it lives.

Its analyzer (`addPrimaryKeyFinding`) was already dead code — nothing dispatched
it — which is how the duplication survived unnoticed.

---

## D-025 — A resolve command is only for a finding whose facts do not certify

`ResolveCommand` is the "here is how to turn this WARN into a PASS" string. It now
returns empty when the declared dependencies already certify.

The capping path only ever called it after a downgrade, so the guard is a no-op
there. The MCP surface asks for one on **every** finding, and without the guard an
`RS-INDEX-002` resting on an exact row count told the agent to `pull --exact` to
raise a fact that was already exact — a full production re-pull that would change
nothing, delivered to the surface the agent loop is built on.

Being wrong here is worse than being silent: the loop's whole premise is that an
agent can act on what rowshape says.

---

## D-026 — RS-PERF-010 must recognize the batching it prescribes

`RS-PERF-010`'s remediation says to loop over a bounded key range. It then flagged
the batched form exactly as loudly as the unbatched one, because it sized every
qualified UPDATE/DELETE against the table.

A WARN that survives the remediation is not a warning, it is a dead end — the
failure class phase-cr5 exists to close. It teaches a human to ignore the rule,
and it leaves an agent with no move that reaches PASS, so it either gives up or
hand-waves the verdict.

The rule now recognizes a window bounded on **both** sides of one column
(`BETWEEN`, or a `>`/`>=` and a `<`/`<=` on the same column):

- **Literal bounds** are decided on the window, not the table. A window at or
  above the threshold is still reported — batching is not a magic word, it is a
  small window.
- **One-sided bounds are not batching.** `WHERE id >= 1000` is the whole table
  minus a prefix, and reading it as batched would fail open on the exact statement
  this rule exists to catch. An `OR` anywhere disqualifies the clause for the same
  reason.
- **Placeholder or expression bounds** (`$1`, `:lo`, `lo + batch`) are silent. The
  window size is a run-time choice that is not in the SQL, so the alternative
  claim — "this may rewrite the whole table" — is simply false for a batch loop.
  rowshape cannot verify the caller keeps the window small; no static check can.

---

## D-027 — rowshape cannot validate a per-batch COMMIT

Found while making the launch-gate demo follow its own remediation.

`validate` applies a migration set inside one transaction. A `COMMIT` between
batches — the half of batching that actually releases locks — is therefore
rejected outright, as SQLSTATE `2D000` (invalid transaction termination), whether
it appears in a `DO` block or in a procedure reached by `CALL`. A top-level
`COMMIT` between statements works, but no plain-SQL construct loops across one.

So the committing form of the very pattern `RS-PERF-010` prescribes is not
something rowshape can certify today. The demo bounds each batch and says plainly
that the commit belongs in a task run outside the migration; it does not pretend
the wrapped form is equivalent.

A second consequence: a `DO` block is **one statement** to the server, so every
batch it runs is charged to the same `--statement-timeout`. The demo's 5,000,000
row backfill takes about 90s, which the 1m default cancels — flooring the verdict
to WARN with no findings (D-019) and reading as the demo breaking when nothing
about the migration is wrong.

---

## D-028 — Reserving `rowshape` on npm needed three properties in one token

Creating the npm **organization** `rowshape` reserved the **scope** `@rowshape`. It
did **not** reserve the unscoped name `rowshape` — which is what `npx rowshape`,
the install page, the wrapper's own naming test and PRD §7 all depend on. Those
are separate namespaces, and only the second one matters to this project.

Claiming it took three tokens, because a publishing credential for a **new,
unscoped** package needs all of:

| | reach the unscoped name | bypass 2FA | outcome |
|---|---|---|---|
| granular, `@rowshape` | no | yes | `403 Forbidden` on `PUT /rowshape` |
| granular, all packages | yes | no | `EOTP` — asks for a code |
| granular, all + bypass | yes | yes | published |

A granular token cannot create a package outside its scope, and cannot be scoped
to a package that does not exist yet — so "all packages" is the only granular
setting that can claim a *new* unscoped name. And a token without `bypass_2fa`
fails in CI with `EOTP`, asking for a code no one is there to type; that is not a
permissions error and does not read like one.

The token now in `NPM_TOKEN` satisfies both, and **expires 2026-09-11**. A release
after that date publishes the GitHub release, the cask and the image, then fails
only at the npm step — a half-published version. Classic automation tokens do not
expire; every granular token does.

**`--tag placeholder` did not prevent a `latest` tag.** npm gives a package's
first publish `latest` regardless, and `latest` cannot be removed. See MERGE-T7:
`install.js` now refuses a download it can predict will 404, rather than making
the request and reporting the failure.

---

## D-029 — `pull` was broken on PostgreSQL 10, and the matrix is what found it

`pg_index.indnkeyatts` arrived with INCLUDE — covering indexes — in **PostgreSQL
11**. The catalog read used it unconditionally, so on PG 10 every one of `pull`,
`plan`, `verify` and the structure read itself failed outright:

```
ERROR: column ix.indnkeyatts does not exist (SQLSTATE 42703)
```

against a README and D-022 that both claim support from 10. The tool did not
work at all on the oldest major it advertises.

It survived because **the version matrix had never run** (PR-T14, blocked on an
environment that could not install the servers). The first CI run after the org
move caught it on PG 10 and PG 11 within two minutes. This is precisely the
hazard D-006/D-007 name — a catalog read that is right on one major and absent on
another — and it is the argument for the matrix stated better than the matrix's
own header states it.

The boundary now lives in `indexKeyCountColumn(major)`, a named function with an
assertion on both sides, rather than an inline conditional. Before 11 there is no
INCLUDE, so every indexed column is a key column and `indnatts` is exactly the
number `indnkeyatts` would have returned.

**An unknown version (major 0) resolves to the modern column, deliberately.**
Every supported major from 11 has it, an unknown version is far likelier to be
new than to be 10, and the failure is a loud 42703 at the first catalog read.
Guessing the old column would instead record an index's INCLUDE payload as part
of its key — silently widening a UNIQUE index's key, which can certify a
migration that violates the uniqueness production actually enforces. Loud and
wrong-version beats quiet and wrong-answer.

**Separately, on PG 11:** two test SEEDS declare a STORED generated column, which
is PG 12+. That is a fixture the older server cannot express, not a reader
defect, so those seeds skip below 12 (`requireMajor`). The distinction matters: a
version-gated READ gets a real assertion on both sides of its boundary; only a
schema the server cannot state at all is skipped.
