---
title: "rowshape validate"
description: "Validate a migration against production-shaped data; return a verdict"
sidebar:
  order: 10
---

validate hydrates a disposable Postgres from the fixture, applies the
migration set through your own runner, captures what happened (locks,
durations, rows, constraint violations, index builds), and returns a
verdict. Against a provided live branch (--target) the facts are ground
truth. validate never touches the fixture's source database.

## Usage

```sh
rowshape validate [rowshape.yaml] [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--calibrate` | — | hydrate at two scales and fit the cost curve, upgrading duration estimates to measured (slower) |
| `--ephemeral` | — | admin URL: create a disposable database, hydrate into it, then drop it |
| `--i-know-target-is-writable` | — | proceed with --target when the fixture records no source host to check it against |
| `--json` | — | emit the machine-readable verdict as JSON |
| `--max-rows` | `0` | cap hydrated rows per table (0 = no cap) |
| `--migrations`, `-m` | `migrations` | migration .sql file or directory |
| `--runner` | — | override runner detection (rawsql; alembic\|prisma\|drizzle are DETECTED but cannot be validated yet) |
| `--scale` | `1` | fraction of declared rows to hydrate |
| `--seed` | `0` | deterministic hydration seed |
| `--target` | — | validate against this live database URL (its data is ground truth) |
| `--warn-fail` | — | exit non-zero on a WARN-only verdict |

