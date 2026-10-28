---
title: Agents & MCP
description: Put rowshape inside the agent's turn with init --agent and the MCP server.
sidebar:
  order: 1
---

An MCP server the agent never calls is decoration. The bet is the loop, not the
tool: the win is an agent that checks a migration against production
*shape* before it opens a PR, and rewrites it when the verdict says to — with no
human turn. Two things make that happen: the MCP tools, and getting rowshape into
the agent's context so it actually reaches for them.

## `rowshape init --agent`

One command wires a repo for coding agents. It makes no network or database
connection — it reads the repo layout and writes three things:

```sh
rowshape init --agent
```

1. **MCP client config.** It registers `rowshape mcp` in the MCP config of every
   detected client — `.mcp.json` (Claude Code), `.cursor/mcp.json`,
   `.vscode/mcp.json` — merging into existing config, safe to re-run:

   ```json
   {
     "mcpServers": {
       "rowshape": { "command": "rowshape", "args": ["mcp"] }
     }
   }
   ```

2. **A versioned agent rule.** It writes the rule into `AGENTS.md` (and
   `CLAUDE.md`) inside a managed block, leaving the rest of your file untouched.
   The rule is a product artifact, iterated like a prompt and shipped versioned so
   an improvement reaches repos that ran `init --agent` months ago. See
   [the exact text](./rule/).

3. **A pre-commit backstop.** In a git repo it installs a pre-commit hook so a
   migration can't be committed without a verdict — the belt to the rule's
   suspenders, for when an agent forgets to ask.

It is safe to re-run: existing config is merged, and the managed rule block is
replaced in place rather than duplicated.

## The loop it enables

With the tools wired in and the rule in context, the agent runs the loop on its
own — read the shape, validate before the PR, act on the verdict:

1. `describe_shape` for the tables it's about to touch — the shape decides the
   answer.
2. write the migration.
3. `validate_migration` — a verdict and compact finding codes.
4. on a WARN/FAIL, `explain_finding <CODE>` for the fix, rewrite, and validate
   again.

See the [MCP tools](./mcp/) for what each does, and the
[agent-loop demo](https://github.com/rowshape/rowshape/tree/main/demo) for the
whole thing closing without a human turn.

## The spec is the position

rowshape's fixture format is an open spec anyone can emit, not a private file
format. Anything that can read your database's statistics can produce one, and
anything that reads the format can check a migration against it. It lives in its
own repo:

- [The Rowshape Fixture Spec (RFC-0001)](https://github.com/rowshape/fixture-spec)
- [MCPFold](https://github.com/dj-pearson/MCPFold) — the thin-MCP discipline
  rowshape's server is built to; rowshape is its flagship consumer.
