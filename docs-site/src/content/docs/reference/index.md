---
title: "CLI reference"
description: "Every rowshape command and every flag it accepts, generated from the binary itself so the reference cannot drift from what the CLI does."
sidebar:
  order: 1
---

This reference is generated from the command tree, so it lists exactly the
commands and flags this version of `rowshape` accepts. If a flag is missing
here, it does not exist.

## Commands

| Command | What it does |
| --- | --- |
| [`rowshape annotate`](./annotate/) | Render a JSON verdict as GitHub PR annotations + a check summary |
| [`rowshape explain`](./explain/) | Explain a finding code (docs + remediation), agent-readable |
| [`rowshape hydrate`](./hydrate/) | Reconstruct a deterministic disposable database from rowshape.yaml |
| [`rowshape init`](./init/) | Scaffold rowshape config in the current repo (offline detection only) |
| [`rowshape inspect`](./inspect/) | Audit a committed fixture |
| [`rowshape mcp`](./mcp/) | Run rowshape as an MCP server (stdio) for agents |
| [`rowshape plan`](./plan/) | Dry-run diff of a migration against a live target (read-only, applies nothing) |
| [`rowshape pull`](./pull/) | Read a database's shape (read-only) and emit rowshape.yaml |
| [`rowshape validate`](./validate/) | Validate a migration against production-shaped data; return a verdict |
| [`rowshape verify`](./verify/) | Read-only check that a live target matches the intended schema (drift) |

## Global flags

These work on every command.

| Flag | Default | Description |
| --- | --- | --- |
| `--log-level` | `info` | log verbosity on stderr: debug \| info \| warn \| error |

## Exit codes

Every command maps its outcome onto one contract:

| Code | Meaning |
| --- | --- |
| `0` | PASS — the check ran and found nothing blocking |
| `1` | FAIL — the check ran and the migration is unsafe |
| `2` | WARN-only — findings worth reading, not blocking |
| `3` | Tool error — the check could NOT run |

`3` is distinct on purpose: "the tool could not produce a verdict" must never
be mistaken for "the migration is unsafe". A tool error carries
`"error": "tool_error"` and a category rather than a verdict field.
