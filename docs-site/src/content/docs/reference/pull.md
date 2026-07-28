---
title: "rowshape pull"
description: "Read a database's shape (read-only) and emit rowshape.yaml"
sidebar:
  order: 9
---

pull reads a database's structure and statistical shape through catalog
views only — never SELECT * on user tables — and writes a committable,
value-free rowshape.yaml. It requires a read-only role and refuses to run
as a superuser without --i-know.

The connection may be given as a URL argument or through the standard
libpq environment variables (PGHOST, PGPORT, PGUSER, PGDATABASE, ...).

## Usage

```sh
rowshape pull [connection-url] [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--exact` | — | full streaming pass: exact null counts and measured (HLL) distinct for every column (minutes to hours) |
| `--i-know` | — | override the refusal to run as a superuser |
| `--max-escalation-rows` | `50000000` | skip uniqueness escalation on tables larger than this (0 = default, negative = no cap) |
| `--out`, `-o` | `rowshape.yaml` | output path for the fixture |
| `--privacy` | `standard` | privacy level: strict \| standard \| permissive |
| `--schema` | — | restrict to these schemas (default: all non-system) |
| `--statement-timeout` | `0s` | server-side cap on any single query (0 = the mode default: 10m in fast mode, none with --exact) |

