---
title: "rowshape inspect"
description: "Audit a committed fixture"
sidebar:
  order: 6
---

inspect --leaks enumerates every field in a fixture derived from row
values — numeric/temporal ranges, histogram bounds, value sets and
frequencies, verbatim CHECK expressions, and free-text max length — with
its source column and the privacy level at which it appears (RFC §8.3).

Pass --fail-on-leak to exit non-zero when any value-derived field is
present. Run it against a strict fixture in CI to assert full redaction.

## Usage

```sh
rowshape inspect [rowshape.yaml] [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--fail-on-leak` | — | exit non-zero if any value-derived field is present (for CI) |
| `--leaks` | — | enumerate every value-derived field in the fixture |
| `--size` | — | report the fixture's size against the committable budget (RFC §3.3) |

