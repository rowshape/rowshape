---
title: "rowshape verify"
description: "Check a live database against the schema you intended, read-only, and report the drift between what is deployed and what is committed."
sidebar:
  order: 11
---

verify reads a live target's schema (read-only) and compares it to the
schema a fixture declares: tables, columns, nullability, and constraints.
It writes nothing. It exits 0 when reality matches intent, 1 on drift.

## Usage

```sh
rowshape verify [rowshape.yaml] [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--against` | — | live database URL to verify (read-only) |

