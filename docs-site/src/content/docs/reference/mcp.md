---
title: "rowshape mcp"
description: "Run rowshape as a Model Context Protocol server over stdio, exposing the shape and the verdict to an agent inside its own turn."
sidebar:
  order: 7
---

mcp starts a Model Context Protocol server over stdio, exposing rowshape's
four tools (describe_shape, validate_migration, explain_finding,
plan_against) to an agent. Point your MCP client at `rowshape mcp`.

## Usage

```sh
rowshape mcp
```

