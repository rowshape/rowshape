---
title: "rowshape pull"
description: "Read a PostgreSQL database's structure and statistical shape through catalog views only, never your rows, and write a committable fixture."
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
| `--idle-timeout` | `10m0s` | cap how long the read transaction may sit idle, so a stalled client cannot hold back vacuum (negative disables it) |
| `--lock-timeout` | `5s` | cap how long a read waits for its lock, so pull never queues behind a pending ALTER TABLE (negative disables it) |
| `--max-escalation-rows` | `50000000` | skip uniqueness escalation on tables larger than this (0 = default, negative = no cap) |
| `--out`, `-o` | `rowshape.yaml` | output path for the fixture |
| `--privacy` | `standard` | privacy level: strict \| standard \| permissive |
| `--schema` | — | restrict to these schemas (default: all non-system) |
| `--statement-timeout` | `5m0s` | cap one profiling query against the source database (negative disables it; --exact defaults to disabled) |

