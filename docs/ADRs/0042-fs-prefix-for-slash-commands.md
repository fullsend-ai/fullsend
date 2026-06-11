---
title: "42. Use /fs- prefix for all slash commands"
status: Accepted
relates_to:
  - agent-architecture
topics:
  - slash-commands
  - namespace
  - ux
---

# 42. Use /fs- prefix for all slash commands

Date: 2026-05-26

## Status

Accepted

Supersedes the bare command examples in
[ADR 0002](0002-initial-fullsend-design.md).

## Context

Multiple AI-powered tools respond to slash commands in GitHub issue and
PR comments. GitHub Copilot uses `/fix` and `/review`; other bots use
similar bare verbs. When fullsend agents listen for the same bare
commands (e.g., `/triage`, `/implement`, `/review`), any of these tools
may intercept the command, causing unintended side effects or ambiguous
behavior.

The original design document ([ADR 0002](0002-initial-fullsend-design.md))
used bare commands as illustrative examples. As the ecosystem matured,
the collision risk became concrete — Copilot and other tools already
claim several of the same verbs.

Two separate issues (#1473, #1477) were filed proposing workarounds for
command collisions, both unaware that a namespacing decision had already
been made but was recorded only in the
[glossary](../glossary.md#slash-command). This ADR formalizes that
decision so it is discoverable alongside other architectural choices.

## Options

### A. Bare commands (`/triage`, `/implement`, `/review`)

- Pro: shorter to type, familiar syntax.
- Con: collides with Copilot and other tools that claim the same verbs.

### B. `/fs-` prefixed commands (`/fs-triage`, `/fs-code`, `/fs-review`)

- Pro: unique namespace; no known collision with existing tools.
- Pro: short enough to remain ergonomic.
- Con: users must learn the prefix.

### C. `/fullsend-` prefixed commands

- Pro: unambiguous namespace.
- Con: too verbose for frequent interactive use.

## Decision

All fullsend slash commands use the `/fs-` prefix
(e.g., `/fs-triage`, `/fs-code`, `/fs-review`). The entry point's slash
command parser must recognize only `/fs-`-prefixed commands and ignore
bare verbs.

## Consequences

- Fullsend commands will not collide with Copilot, other AI bots, or
  future GitHub-native slash commands.
- Users and documentation must use the `/fs-` prefix; bare commands are
  not recognized.
- The glossary already reflects this convention. ADR 0002's illustrative
  examples of bare commands (`/triage`, `/implement`, `/review`) are
  superseded by this decision.
- Agent prompt instructions, harness definitions, and dispatch
  configuration that reference slash commands must use the `/fs-` form.
