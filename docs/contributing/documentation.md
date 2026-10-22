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

## Configuration and environment variables

For a `.fullsend/config.yaml` field, update
`docs/reference/config-reference.md`, the canonical reference for every field.
If its layered resolution or defaults change, also check
`docs/guides/infrastructure/layered-config-reference.md`. For an environment
variable or repository variable, check the repository-variable table in
`docs/guides/getting-started/operations.md` and the documentation for the
feature that consumes it.

## ADR annotations

When a CLI change makes an ADR's decision obsolete, write a **new ADR** that supersedes the old one rather than editing the original decision text. Minor annotations (status updates, "see also" links) are welcome — see [ADR conventions](adrs.md) for the full rules.

## Heading anchors

Heading anchors on the docs site follow GitHub's slug rules (for example `## 4. Next step` is `#4-next-step`), so a link that lychee (`make lint-md-links`) accepts also works on the site.

## Line wrapping

Wrap prose in Markdown files at about 80 columns. Git and GitHub compare
Markdown line by line. When a paragraph sits on one long line, editing a single
word marks the whole paragraph as changed, and reviewers have to hunt for the
real edit. When lines are wrapped, a small wording fix changes only the lines
it touches.

- Break lines at word boundaries, close to 80 columns. The limit is a guide,
  not an exact count, and no linter enforces it, so reviewers check it.
- Indent the continuation lines of a list item to line up with the item's
  text, so they stay part of the item.
- Wrapping does not change the rendered page. Markdown joins consecutive lines
  of a paragraph into one.

Leave these as they are, because wrapping them breaks rendering or meaning:

- Table rows. Each row must stay on one line.
- Fenced code blocks. Keep their content exactly as it should be copied or
  run.
- Headings.
- Long URLs and link targets that do not fit within the limit by themselves.

Do not rewrap existing documents wholesale. That produces the same large diffs
this rule is meant to prevent. Write new content wrapped, and rewrap an
existing paragraph only when you are already editing it.

## CLI reference pages are manually maintained

CLI reference pages under `docs/cli/` are manually maintained and will not
auto-update. When modifying CLI commands, flag help text, defaults, or behavior
in `internal/cli/`, update the corresponding reference page in `docs/cli/` as
well as relevant tables and descriptions in `docs/guides/`.

## Adding new documentation pages

When adding a new doc under `docs/`, check `docs/.vitepress/config.ts` sidebar
configuration. Sections using `getMarkdownFiles()` are auto-discovered,
including nested directories (which become nested sidebar groups). All other
sections require a manual `{ text, link }` entry. Also add the new folder's
prefix to `search.options.scopes` in the same file so the folder's pages are
reachable when search scope pills are active.

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

The `admin` command group's `uninstall`/`analyze`/`enable`/`disable` subcommands were removed with per-org installation ([ADR-0044](../ADRs/0044-deprecate-per-org-installation-mode.md)); `admin install` is deprecated but still works as repository-only (`<owner/repo>`) installation (it still creates every role's GitHub App in one run). The other actively supported subcommand is `admin foreign` (cross-org mint-authorization allow-list).

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
| Guides | `docs/guides/getting-started/configuring-github.md`, `docs/guides/getting-started/migrating-from-github-setup.md`, `docs/guides/getting-started/operations.md`, `docs/guides/dev/cli-internals.md`, `docs/guides/dev/e2e-testing.md` |
| ADRs | `docs/ADRs/0057-repos-management.md` |
| Go source | `internal/cli/github.go` |

### `inference`

| Category | Files |
|----------|-------|
| CLI reference | `docs/cli/inference.md` |
| Guides | `docs/guides/getting-started/getting-inference.md`, `docs/guides/user/inference-gateway.md`, `docs/guides/getting-started/operations.md`, `docs/guides/dev/cli-internals.md` |
| Go source | `internal/cli/inference.go` |

### `mint`

| Category | Files |
|----------|-------|
| CLI reference | `docs/cli/mint.md` |
| Guides | `docs/guides/getting-started/operations.md`, `docs/guides/infrastructure/mint-administration.md`, `docs/guides/infrastructure/infrastructure-reference.md`, `docs/guides/infrastructure/advanced-setup.md`, `docs/guides/infrastructure/standalone-mint.md`, `docs/guides/dev/cli-internals.md` |
| ADRs | `docs/ADRs/0059-public-mint-mode-with-wildcard-allowlists.md`, `docs/ADRs/0060-cross-org-mint-authorization-via-org-variables.md`, `docs/ADRs/0073-named-mint-privilege-levels.md`, `docs/ADRs/0077-mint-repos-scope-hardening.md`, `docs/ADRs/0078-simplified-mint-authorization-policy.md`, `docs/ADRs/0082-workflow-host-allow-list.md` |
| Go source | `internal/cli/mint.go`, `internal/cli/mint_setup.go`, `internal/cli/mint_delete.go`, `internal/cli/minttoken.go` |

### `repos`

| Category | Files |
|----------|-------|
| CLI reference | `docs/cli/repos.md` |
| Guides | `docs/guides/getting-started/operations.md`, `docs/guides/getting-started/repo-management.md`, `docs/guides/getting-started/getting-inference.md`, `docs/guides/getting-started/configuring-gitlab.md`, `docs/guides/getting-started/migrating-from-github-setup.md`, `docs/guides/dev/cli-internals.md`, `docs/guides/infrastructure/layered-config-reference.md` |
| ADRs | `docs/ADRs/0057-repos-management.md`, `docs/ADRs/0074-repos-command-consolidation.md`, `docs/ADRs/0122-declarative-repo-configuration.md` |
| Go source | `internal/cli/repos.go`, `internal/cli/repos_gitlab.go`, `internal/cli/repos_gitlab_service_accounts.go` |

### `run`

| Category | Files |
|----------|-------|
| CLI reference | `docs/cli/run.md` |
| Guides | `docs/guides/user/running-agents-locally.md`, `docs/guides/user/building-custom-agents.md`, `docs/guides/dev/cli-internals.md` |
| ADRs | `docs/ADRs/0036-agent-execution-sandbox.md`, `docs/ADRs/0136-runner-secrets-through-one-stored-secret.md` |
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

`dispatch`, `scan`, `lock`, `poll`, `fetch-skill`, `fetch-review-threads`, `post-review`, `post-comment`, `reconcile-status`, `resolve-mr-source`, `check-protected-branch`

The most comprehensive single reference for all commands (including minor ones) is `docs/guides/dev/cli-internals.md`, which documents the full command tree with flags.
