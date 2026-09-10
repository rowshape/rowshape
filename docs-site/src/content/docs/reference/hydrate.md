---
title: "rowshape hydrate"
description: "Rebuild a disposable PostgreSQL from a rowshape fixture: rows whose shape matches production, with obviously-fake content, reproducible from a seed."
sidebar:
  order: 4
---

hydrate synthesizes rows whose SHAPE matches production — row counts,
null fractions, cardinality, fan-out — with obviously-fake content. The
same fixture, seed, and engine version produce identical output on any
platform. In phase 1 it emits INSERT SQL; --seed makes it reproducible.

## Usage

```sh
rowshape hydrate [rowshape.yaml] [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--ephemeral` | — | admin URL: create a disposable database, hydrate into it, then drop it |
| `--max-rows` | `0` | cap synthesized rows per table (0 = no cap) |
| `--out`, `-o` | `-` | output path for the SQL ('-' for stdout) |
| `--scale` | `1` | fraction of declared rows to synthesize |
| `--seed` | `0` | deterministic seed |
| `--target` | — | hydrate into this database URL instead of emitting SQL |

