---
title: "rowshape annotate"
description: "Render a JSON verdict as GitHub PR annotations + a check summary"
sidebar:
  order: 2
---

annotate reads a JSON verdict (from `rowshape validate --json`) on a
file argument or stdin and renders it into GitHub's PR surface:
inline file/line annotations on stdout and a Markdown check summary to
$GITHUB_STEP_SUMMARY. It is used by the rowshape GitHub Action; the
annotations are derived from the Verdict struct, not a separate formatter.

## Usage

```sh
rowshape annotate [verdict.json] [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--summary` | — | write the Markdown check summary here (default $GITHUB_STEP_SUMMARY, else stdout) |

