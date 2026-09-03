# The rowshape agent-loop demo

This is the demo that is the launch (PRD §13): a coding agent is told to make a
schema change, writes a naive migration, **watches it get rejected**, rewrites it
into a safe online migration, and reaches **PASS** — with **no human turn** in
between. The loop closes because `rowshape` gives the agent the same
machine-readable verdict a human would get.

## The scenario

> "Add a NOT NULL `email` column to `users`."

`users` has 5,000,000 rows in production (see [`repo/rowshape.yaml`](repo/rowshape.yaml)
— a committed fixture of *statistics*, no rows). The agent works only against
that fixture; it never touches production.

## What happens, step by step

1. **The agent reads the shape.** Per the wired-in rule
   ([`repo/AGENTS.md`](repo/AGENTS.md)), it calls `describe_shape` for `users`
   before writing SQL and sees 5M rows.
2. **Naive migration → rejected.** It writes the obvious thing
   ([`repo/migrations/naive/001_add_email.sql`](repo/migrations/naive/001_add_email.sql)):
   `ADD COLUMN email text NOT NULL DEFAULT (gen_random_uuid()::text || …)`. The
   volatile default forces Postgres to rewrite all 5M rows under `ACCESS
   EXCLUSIVE` — a write outage. `validate_migration` returns **`RS-LOCK-001`**.
3. **The agent does not hand-wave it.** The rule says a WARN is not a pass and a
   lock finding must be resolved, so the agent calls `explain_finding RS-LOCK-001`
   and gets the expand/backfill/contract recipe.
4. **Rewrite → PASS.** It replaces the one statement with three online steps
   ([`repo/migrations/rewrite/`](repo/migrations/rewrite/)): add the column
   nullable, backfill it (with a `WHERE`, so it is not a whole-table write), then
   enforce NOT NULL via a `CHECK … NOT VALID` + `VALIDATE` split across a
   `COMMIT`. `validate_migration` returns **`PASS`**.
5. **No human turn.** Every step above was the agent reacting to a verdict, not a
   person. See [`transcript.md`](transcript.md).

## Run it yourself

```sh
# From the repo root, with a disposable Postgres reachable as $PG
#   e.g. docker run -e POSTGRES_PASSWORD=postgres -p 5432:5432 -d postgres:16
export PG='postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable'

# 1) Naive migration is rejected (WARN RS-LOCK-001; warn-as-fail exits non-zero)
rowshape validate demo/repo/rowshape.yaml \
  --migrations demo/repo/migrations/naive --ephemeral "$PG" --warn-fail   --statement-timeout 5m
# -> verdict WARN, finding RS-LOCK-001, exit 2 (or 1 with --warn-fail)

# 2) The three-step rewrite passes
rowshape validate demo/repo/rowshape.yaml \
  --migrations demo/repo/migrations/rewrite --ephemeral "$PG"   --statement-timeout 5m
# -> verdict PASS, exit 0
#
# The ceiling is raised from the 1m default deliberately: the rewrite's backfill
# is a bounded batch loop over 5,000,000 rows and takes about 90s, and the whole
# loop is ONE statement to the server. Cancelled at 1m it would floor to WARN
# (D-019) with no findings at all.
```

The GitHub Action (`uses: rowshape/rowshape@v1`) runs exactly this in CI, and
`test/demo/demo_test.go` is the scripted end-to-end run that asserts the reject
and the PASS.

## Two honest notes about the verdicts

These matter because the whole product is being trustworthy about them:

- **RS-LOCK-001 is a `WARN`, not a `FAIL`.** A rewrite is an *availability*
  problem (downtime), not data corruption, so rowshape rates it WARN — and the
  loop gates on it (`--warn-fail`, and the agent rule treats a WARN as
  must-resolve). `FAIL` is reserved for findings that prove existing data
  contradicts the change (RS-DATA null-on-NOT-NULL, orphans, unproven
  uniqueness). The PRD's shorthand "FAIL RS-LOCK-001" means *the loop rejects it*,
  which is what you see.
- **The rewrite enforces NOT NULL with a validated `CHECK`, not a final
  `SET NOT NULL`.** rowshape reasons from the fixture, which was pulled *before*
  `email` existed, so it cannot prove the backfill left zero NULLs and correctly
  caps a bare `SET NOT NULL` to WARN ("not confirmed safe"). A validated `CHECK
  (email IS NOT NULL)` is the equivalent guarantee it *can* certify: `VALIDATE`
  scans the real rows. Reaching PASS honestly is the point — a tool that
  certifies an unproven change is worse than no tool (PRD §15).
