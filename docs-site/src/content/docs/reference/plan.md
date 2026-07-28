---
title: "rowshape plan"
description: "Dry-run diff of a migration against a live target (read-only, applies nothing)"
sidebar:
  order: 8
---

plan reads a live target's current schema (read-only) and reports what
each migration statement would change against it — a dry run that applies
nothing. Point --against at the target and -m at a .sql file or directory.

## Usage

```sh
rowshape plan [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--against` | — | live database URL to plan against (read-only) |
| `--migrations`, `-m` | `migrations` | migration .sql file or directory |

