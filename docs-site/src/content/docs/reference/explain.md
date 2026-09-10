---
title: "rowshape explain"
description: "Look up a rowshape finding code and get its documentation and mandatory remediation: the same text a verdict carries, for a human or an agent."
sidebar:
  order: 3
---

explain returns structured documentation and the mandatory remediation
for a finding code (e.g. rowshape explain RS-LOCK-001). With no argument it
lists every code. The text is identical to the remediation the finding
carries — one source, no drift.

## Usage

```sh
rowshape explain [CODE] [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--json` | — | emit the explanation as JSON |

