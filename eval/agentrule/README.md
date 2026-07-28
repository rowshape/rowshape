# Agent-rule evaluation harness

The rowshape [agent rule](../../internal/agentrule/rule.md) is a product artifact,
iterated against real agent sessions the way a prompt is (PRD §8.3). "The bet is
the loop, not the tool" (PRD §15). So a rule change has to be **measured**, not
guessed — this harness is the measurement.

## What it measures

It scores an agent **session trace** — the ordered tool calls and actions an
agent took — against the behaviours the rule is supposed to produce:

| Behaviour | Rule clause |
| --- | --- |
| `describe_shape` before the first `write_sql` | "Before writing a migration, call `describe_shape`." |
| `validate_migration` before `open_pr` | "Before opening a PR, call `validate_migration`." |
| No hand-waved verdict (PR only on PASS) | "A WARN is not a pass." / "Never hand-wave a FAIL." |
| **Loop closed** | started non-PASS → reached PASS → opened the PR |

Per session it reports the three adherence booleans (and their mean); across a
set it reports **mean adherence** and **loop-closure rate**, grouped by rule
label — so two rule versions can be A/B-compared.

## Run it

```sh
go run ./eval/agentrule eval/agentrule/traces
```

```
By rule version:
  v2            sessions=2  adherence=100%  loop-closure=100%
  weakened      sessions=2  adherence=17%   loop-closure=0%
```

The committed traces illustrate the two ends of the scale: the `v2`-rule sessions
read the shape, validate before the PR, and rewrite a WARN/FAIL until PASS; the
`weakened`-rule sessions skip `describe_shape` and open a PR on a WARN.

> **What this does and does not measure.** `TestWeakenedRuleScoresLower` asserts
> that the adherent traces score higher than the non-adherent ones. Those traces
> are **hand-authored fixtures**, and this package never loads
> `internal/agentrule` — the `rule_version` field is parsed and then read by
> nothing. So the test is a unit test of `Score()`, not an A/B of the rule: it
> would pass unchanged if `rule.md` were emptied, deleted, or replaced with its
> own negation.
>
> That is worth stating plainly because the scorer being correct is genuinely
> useful, and because the gap is not in the code but in the input — the live
> runner described below does not exist in this repo yet. Until it does, nothing
> here supports a claim about the rule's *effect*.

## Where the traces come from

Scoring is deterministic and runs in CI. **Producing** a trace is not: a live
runner drives a coding agent (Claude Code, wired via `rowshape init --agent`)
against [`demo/repo`](../../demo/README.md) and records what it did into the trace
schema below. That step is non-deterministic and out of CI; this harness consumes
its output, so a rule change is evaluated on real sessions rather than intuition.

### Trace schema

```json
{
  "name": "add-not-null-email",
  "rule_label": "v2",
  "events": [
    {"type": "describe_shape", "tables": ["public.users"]},
    {"type": "write_sql", "path": "migrations/naive/001.sql"},
    {"type": "validate", "verdict": "WARN", "findings": ["RS-LOCK-001"]},
    {"type": "explain", "code": "RS-LOCK-001"},
    {"type": "write_sql", "path": "migrations/rewrite/003.sql"},
    {"type": "validate", "verdict": "PASS"},
    {"type": "open_pr"}
  ]
}
```

Event types: `describe_shape`, `write_sql`, `validate` (with `verdict` +
`findings`), `explain`, `open_pr`. Group sessions by `rule_label` to compare
versions.
