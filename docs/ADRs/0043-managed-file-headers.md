---
title: "43. Add upstream source headers to managed scaffold files"
status: Accepted
relates_to:
  - agent-architecture
topics:
  - scaffold
  - installer
---

# 43. Add upstream source headers to managed scaffold files

Date: 2026-06-06

## Status

Accepted

## Context

When agents encounter scaffold-generated files in a `.fullsend` config repo
(workflow definitions, templates), they have no signal that these files are
managed by fullsend and should not be edited in place. Edits to deployed
copies are overwritten the next time the installer runs. The correct action
is to modify the upstream source in
`internal/scaffold/fullsend-repo/` and let the installer propagate changes.

See [#366](https://github.com/fullsend-ai/fullsend/issues/366) and
[#857](https://github.com/fullsend-ai/fullsend/issues/857).

## Decision

The installer prepends a managed-by header to scaffold files at install time.
The header identifies the file as fullsend-managed and links to its upstream
source in the fullsend repo. Headers are added only to files whose format
supports comments (YAML, shell); they are never added to user-configurable
files (`config.yaml`, `customized/` overrides, `AGENTS.md`) or files that
lack comment syntax (JSON, `.gitkeep`).

The upstream scaffold source files remain header-free so that agents editing
templates are not confused by self-referential headers.

## Consequences

- Agents encountering a managed file see where the upstream source lives and
  are directed to edit it there instead.
- Future scaffold files must follow this convention: managed infrastructure
  files get headers; user-owned files never do.
- The `ManagedHeader` function in `internal/scaffold/scaffold.go` is the
  single point of control for which files get headers and which do not.
- Header injection is invisible to the scaffold source tree, keeping the
  embedded templates clean for direct editing and testing.
