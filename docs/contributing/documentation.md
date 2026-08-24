---
title: Documentation
---

# Documentation

When changing CLI command behavior, adding or removing subcommands, or renaming flags, you must update every documentation file that references the affected command. CLI commands are documented across CLI reference pages, user and operator guides, ADRs, and inline Go help text. Missing even one location causes documentation drift that surfaces as review findings on later PRs.

## General discovery rule

Before considering a CLI change complete, run:

```bash
grep -rn '<command-name>' docs/ internal/cli/
```

The `internal/cli/` path covers inline `Short`/`Long` help text in Go source. Review every hit and update references that describe behavior you changed. This catches files not listed in the cross-reference below.

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
| Guides | `docs/guides/getting-started/operations.md`, `docs/guides/getting-started/repo-management.md`, `docs/guides/getting-started/getting-inference.md`, `docs/guides/dev/cli-internals.md` |
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
