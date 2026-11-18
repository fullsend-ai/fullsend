---
title: "74. Repos command consolidation"
status: Accepted
relates_to:
  - agent-infrastructure
topics:
  - installation
  - per-repo
  - repos
  - cli
---

# 74. Repos command consolidation

Date: 2026-07-31

## Status

Accepted

Supersedes the subcommand design in [ADR 0057](0057-repos-management.md).

## Context

ADR 0057 defined seven `fullsend repos` subcommands: `init`, `install`,
`status`, `add`, `remove`, `diff`, `sync`, and `upgrade`. In practice
operators rarely ran individual subcommands in isolation — a typical
day-2 session involved running `diff` then `sync` then `upgrade` in
sequence, and adding a repo required `add` followed by `install`.
The separation created unnecessary cognitive load and scripting
complexity without providing meaningful safety benefits.

## Decision

Consolidate the seven subcommands into four, plus a utility command:

| Command | Purpose | Absorbs |
|---------|---------|---------|
| `repos migrate` | Migrate an org from per-org to per-repo install | Replaced `repos init` (PR #5816) |
| `repos install` | Converge repos to desired state: manifest add, provision, sync drift, upgrade refs | `repos add`, `repos diff`, `repos sync`, `repos upgrade` |
| `repos status` | Read-only comparison of manifest vs actual state | _(unchanged)_ |
| `repos uninstall` | Tear down fullsend from repos and/or remove from manifest | `repos remove` |
| `repos set-default` | Set or remove a platform-level default in repos.yaml | _(new)_ |

> **Note:** `repos set-default` was later replaced by `repos set <path> <value>`
> (and `repos set --unset <path>`), which edits any schema field in
> repos.yaml. See [the `repos` CLI reference](../cli/repos.md#repos-set).

**`repos install`** becomes an idempotent convergence operator with three
phases: manifest add (for new repos passed as positional args), parallel
provision, and convergence (variable sync + ref upgrade). `--dry-run`
replaces `repos diff`. `--force` allows scaffold ref downgrades.

**`repos uninstall`** defaults to teardown + manifest removal. The old
split behaviors are preserved via `--manifest-only` (manifest removal
without teardown, replacing `repos remove`) and `--uninstall-only`
(teardown without manifest removal).

## Consequences

- **Simpler mental model** — four commands cover all lifecycle
  operations; operators no longer need to sequence multiple subcommands.
- **Atomic convergence** — drift detection and remediation happen in a
  single `repos install` invocation, reducing partial-apply risk.
- **Migration path** — the absorbed commands map directly to flag
  combinations on the new commands; no functionality is lost.

## References

- [ADR 0057](0057-repos-management.md) — original repos management design
- PR #5807 — implementation
