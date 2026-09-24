---
title: Documentation
---

# Documentation

When adding, removing, renaming, or changing the behavior of CLI commands,
flags, configuration variables, or environment variables, you must update
every documentation file that references the affected feature. CLI commands
are documented across CLI reference pages, user and operator guides, ADRs,
and inline Go help text. Missing even one location causes documentation drift
that surfaces as review findings on later PRs.

Problem-document conventions (options with trade-offs, open questions,
organization-agnostic core, bidirectional backlinks) live in
[Problem Documents](problem-docs.md).

**Per-org installation mode is deprecated**
([ADR 0044](../ADRs/0044-deprecate-per-org-installation-mode.md)). Do not add
or extend org-mode-specific content. When reviewing a PR that touches it, flag
it as deprecated rather than treating the org/repo-mode distinction as active
architecture. Per-repo is the sole supported installation model.

## General discovery rule

Before considering a CLI, configuration, or environment-variable change
complete, search for the affected command or setting:

```bash
grep -rn '<command-or-setting-name>' docs/ internal/ .github/
```

The `internal/cli/` path covers inline `Short`/`Long` help text in Go source,
and `internal/config/` defines `config.yaml` fields. Review every hit and
update references that describe behavior you changed. For new commands or
settings with no literal match, also inspect tables and lists of comparable
commands, flags, or variables. This catches files not listed in the
cross-reference below.

## Docs site (VitePress)

When adding a new page under `docs/`, check `docs/.vitepress/config.ts`.
Sections using `getMarkdownFiles()` are auto-discovered, including nested
directories (which become nested sidebar groups). All other sections need a
manual `{ text, link }` entry. Also add the new folder's prefix to
`search.options.scopes` in the same file so the folder's pages are reachable
when search scope pills are active.

When adding a new skill under `skills/`, check the user-facing guides for
cross-references: `docs/guides/user/customizing-with-skills.md`,
`docs/guides/user/bring-your-own-agent.md`, and `docs/guides/README.md`.
Add a brief pointer if the new skill fills a gap.

When writing skills, documentation, or guides that list fullsend agents,
skills, harness configs, or sub-agent rosters, discover them at runtime from
`fullsend-ai/agents` rather than hardcoding tables. See the
`author-fullsend-augmentations` skill.

## Configuration and environment variables

For a `.fullsend/config.yaml` field, update
`docs/reference/config-reference.md`, the canonical user-facing reference —
every supported field must appear there with its purpose, valid values, and
default. If its layered resolution or defaults change, also check
`docs/guides/infrastructure/layered-config-reference.md`. For an environment
variable or repository variable, check the repository-variable table in
`docs/guides/getting-started/operations.md` and the documentation for the
feature that consumes it.

When adding, removing, or modifying fields in `event_payload`
(`buildEventPayload` in `internal/harnessdispatch/project.go`) or
normalized-event structures (`internal/normevent/`), update the projection
table in `docs/normative/normalized-event/v1/README.md` and the resolution
description in [Harness Field Reference](harness-fields.md).

## ADR annotations

When a CLI change makes an ADR's decision obsolete, write a **new ADR** that supersedes the old one rather than editing the original decision text. Minor annotations (status updates, "see also" links) are welcome — see [ADR conventions](adrs.md) for the full rules.

## Cross-reference by command group

Each row lists the documentation touchpoints for a major CLI command group. The **Go source** column is where inline `Short`/`Long` help text lives.

### `agent`

| Category | Files |
|----------|-------|
| CLI reference | `docs/cli/agent.md` |
| Guides | `docs/guides/getting-started/operations.md`, `docs/guides/user/bring-your-own-agent.md`, `docs/guides/user/customizing-agents.md`, `docs/guides/user/customizing-with-skills.md`, `docs/guides/dev/cli-internals.md` |
| ADRs | `docs/ADRs/0058-agent-registration.md` |
| Go source | `internal/cli/agent.go` |

### `admin foreign`

The `admin` command group's `install`/`uninstall`/`analyze`/`enable`/`disable` subcommands are deprecated per-org installation tooling ([ADR-0044](../ADRs/0044-deprecate-per-org-installation-mode.md)). The actively supported subcommand is `admin foreign` (cross-org mint-authorization allow-list).

| Category | Files |
|----------|-------|
| CLI reference | _(no dedicated page)_ |
| Guides | `docs/guides/infrastructure/mint-administration.md`, `docs/guides/dev/cli-internals.md`, `docs/guides/dev/e2e-testing.md` |
| ADRs | `docs/ADRs/0060-cross-org-mint-authorization-via-org-variables.md`, `docs/ADRs/0083-repo-level-foreign-allow-list.md` |
| Go source | `internal/cli/admin.go` |

### `github`

| Category | Files |
|----------|-------|
| CLI reference | `docs/cli/github.md` |
| Guides | `docs/guides/getting-started/configuring-github.md`, `docs/guides/getting-started/operations.md`, `docs/guides/dev/cli-internals.md`, `docs/guides/dev/e2e-testing.md` |
| ADRs | `docs/ADRs/0057-repos-management.md` |
| Go source | `internal/cli/github.go` |

### `inference`

| Category | Files |
|----------|-------|
| CLI reference | `docs/cli/inference.md` |
| Guides | `docs/guides/getting-started/getting-inference.md`, `docs/guides/getting-started/operations.md`, `docs/guides/dev/cli-internals.md` |
| Go source | `internal/cli/inference.go` |

### `mint`

| Category | Files |
|----------|-------|
| CLI reference | `docs/cli/mint.md` |
| Guides | `docs/guides/getting-started/operations.md`, `docs/guides/getting-started/org-mode.md`, `docs/guides/infrastructure/mint-administration.md`, `docs/guides/infrastructure/infrastructure-reference.md`, `docs/guides/infrastructure/advanced-setup.md`, `docs/guides/infrastructure/standalone-mint.md`, `docs/guides/dev/cli-internals.md` |
| ADRs | `docs/ADRs/0059-public-mint-mode-with-wildcard-allowlists.md`, `docs/ADRs/0060-cross-org-mint-authorization-via-org-variables.md`, `docs/ADRs/0073-named-mint-privilege-levels.md`, `docs/ADRs/0077-mint-repos-scope-hardening.md`, `docs/ADRs/0078-simplified-mint-authorization-policy.md`, `docs/ADRs/0082-workflow-host-allow-list.md` |
| Go source | `internal/cli/mint.go`, `internal/cli/mint_setup.go`, `internal/cli/mint_delete.go`, `internal/cli/minttoken.go` |

### `repos`

| Category | Files |
|----------|-------|
| CLI reference | `docs/cli/repos.md` |
| Guides | `docs/guides/getting-started/operations.md`, `docs/guides/getting-started/repo-management.md`, `docs/guides/getting-started/getting-inference.md`, `docs/guides/getting-started/configuring-gitlab.md`, `docs/guides/dev/cli-internals.md` |
| ADRs | `docs/ADRs/0057-repos-management.md`, `docs/ADRs/0074-repos-command-consolidation.md` |
| Go source | `internal/cli/repos.go`, `internal/cli/repos_gitlab.go` |

### `run`

| Category | Files |
|----------|-------|
| CLI reference | `docs/cli/run.md` |
| Guides | `docs/guides/user/running-agents-locally.md`, `docs/guides/user/building-custom-agents.md`, `docs/guides/dev/cli-internals.md` |
| ADRs | `docs/ADRs/0036-agent-execution-sandbox.md` |
| Contributing | `docs/contributing/sandbox-topology.md` |
| Go source | `internal/cli/run.go` |

### `issues`

| Category | Files |
|----------|-------|
| CLI reference | _(no dedicated page)_ |
| Guides | `docs/guides/user/issues-commands.md`, `docs/guides/user/jira-integration.md`, `docs/guides/infrastructure/layered-config-reference.md`, `docs/guides/dev/cli-internals.md` |
| Go source | `internal/cli/issues.go` |

## Minor commands

The commands below have lighter documentation footprints. Apply the general `grep` rule when changing them:

`dispatch`, `scan`, `lock`, `poll`, `fetch-skill`, `post-review`, `post-comment`, `reconcile-status`

The most comprehensive single reference for all commands (including minor ones) is `docs/guides/dev/cli-internals.md`, which documents the full command tree with flags.
