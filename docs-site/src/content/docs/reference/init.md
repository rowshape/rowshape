---
title: "rowshape init"
description: "Scaffold rowshape config in the current repo (offline detection only)"
sidebar:
  order: 5
---

init detects your database engine and migration runner from the repo
layout and writes a starter rowshape.toml. It makes no network or
database connection. Re-running it leaves an existing config untouched
unless you pass --force.

NOTE: rowshape.toml is currently a RECORD OF WHAT WAS DETECTED, not a
settings file. No rowshape command reads it yet — every option is passed as
a flag. Editing it will not change any behaviour.

--agent additionally wires this repo for coding agents: it registers
`rowshape mcp` in the MCP config of every detected client (.mcp.json for
Claude Code, .cursor/mcp.json, .vscode/mcp.json). It merges into existing
config and is safe to re-run.

## Usage

```sh
rowshape init [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--agent` | — | also wire this repo for coding agents (MCP client config) |
| `--force`, `-f` | — | regenerate an existing rowshape.toml |

