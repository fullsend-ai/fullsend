---
sidebar_position: 5
---

# Repo Management

Manage per-repo fullsend installations at scale using a declarative
`repos.yaml` manifest. The `fullsend repos` command group provides bulk
install, status checking, drift detection, configuration sync, and
version upgrades across multiple repos and GitHub orgs.

**Target audience:** Platform administrators (SRE/DevOps) managing
fullsend across an organization. Individual repo owners should use
`fullsend github setup` for single-repo installation (see
[Configuring GitHub](configuring-github.md)).

## Prerequisites

- **fullsend CLI** installed (see [releases](https://github.com/fullsend-ai/fullsend/releases))
- **GitHub access** — admin or write access to the target repositories
- **`gh` CLI** authenticated with the required OAuth scopes (see [OAuth scope reference](../infrastructure/advanced-setup.md#oauth-scope-reference))
- **GCP prerequisites** (WIF mode only) — GCP WIF provisioning (`fullsend inference provision`) must be completed separately before running `repos install`. For self-managed mints, mint enrollment (`fullsend mint enroll`) is also required. The hosted community mint needs no enrollment — install the shared Apps and use the CLI defaults. See [Mint administration](../infrastructure/mint-administration.md) and [Advanced setup](../infrastructure/advanced-setup.md).

## Getting started

### Creating a manifest via migration

Migrate an org from per-org to per-repo install and generate a
`repos.yaml` manifest:

```bash
fullsend repos migrate <org> --project <gcp-project>
```

Migrate only specific repos:

```bash
fullsend repos migrate <org> --project <gcp-project> --repo api --repo web
```

Preview what would be migrated without making changes:

```bash
fullsend repos migrate <org> --project <gcp-project> --dry-run
```

The command discovers enrolled repos from the per-org config, provisions
WIF infrastructure per repo, installs per-repo (scaffold, variables,
secrets), unenrolls migrated repos from per-org config, and generates
a `repos.yaml` manifest.

### Creating a manifest from scratch

If you do not have an existing per-org installation to migrate from,
`repos install` can bootstrap a new manifest for you. Pass repo names
as positional arguments with `--forge`:

```bash
fullsend repos install acme/api acme/web --forge github --direct
```

This creates `repos.yaml` (or the path given by `-f`) with
`version: 1`, adds the specified repos, and runs the install. The
`--forge` flag is required when no manifest exists.

### Multi-forge manifests

Every repo entry in the manifest must declare its forge (`github` or
`gitlab`). Set `defaults.forge` to avoid repeating the forge on every
entry. Per-entry forge overrides are supported for mixed-forge manifests:

```yaml
version: 1
forge:
  github:
    mint_url: https://mint.example.com
    fullsend_ref: v2.5.0
  gitlab:
    url: https://gitlab.example.com
    fullsend_ref: v2.5.0
defaults:
  forge: github
repos:
  - acme/api-server            # inherits forge: github from defaults
  - acme/web-frontend
  - repo: gitlab-group/project # per-entry override
    forge: gitlab
```

All repos under the same owner must use the same forge. A GitHub org
and a GitLab group with the same name are different entities, and
mixing forges under one owner would route API calls incorrectly.

For GitLab repos, set the `GITLAB_TOKEN` environment variable or pass
`--gitlab-token` to `fullsend repos` subcommands. The `GITLAB_API_URL`
environment variable is kept as a fallback for callers without a
manifest.

### Credential modes

Each repo authenticates to the mint using one of three credential modes:

| Mode | Forges | Mechanism |
|------|--------|-----------|
| `wif` | GitHub, GitLab | OIDC token exchanged via GCP WIF provider; requires `FULLSEND_GCP_PROJECT_ID` + `FULLSEND_GCP_WIF_PROVIDER` secrets |
| `oidc` | GitHub only | OIDC token sent directly to a public mint; requires only `FULLSEND_MINT_URL` |
| `token` | GitLab only | Bot PAT stored as a CI/CD variable; no OIDC, no WIF |

Set `credential_mode` at the forge level or per-repo in the manifest:

```yaml
forge:
  github:
    credential_mode: oidc
    mint_url: https://mint.fullsend.sh
  gitlab:
    credential_mode: token
    url: https://gitlab.example.com
repos:
  - acme/api
  - repo: acme/special
    forge: github
    credential_mode: wif   # per-repo override
```

When omitted, GitHub defaults to `oidc` and GitLab defaults to `token`.
Both forges default to `wif` when the inference flags are provided.

The three inference flags (`--inference-project`, `--inference-project-number`,
`--inference-region`) are all-or-nothing: if any one is set, all three
are required.

See the [CLI reference](../../cli/repos.md) for all flags.

### Manifest paths and URLs

The `-f`/`--manifest` flag accepts either a local file path or an HTTPS
URL. Remote manifests are fetched with a 30-second timeout and a 1 MB
size limit. Most `repos` subcommands support this — see the
[CLI reference](../../cli/repos.md) for details.

```bash
fullsend repos status -f https://example.com/manifests/repos.yaml
```

### Concurrency

Most `repos` subcommands accept a `--concurrency` flag to control the
number of parallel API calls or operations. Defaults vary by command
(typically 4 for write operations, 8 for read-only operations). See the
[CLI reference](../../cli/repos.md) for per-command defaults and limits.

### Installing and converging repos

Install and converge repos defined in the manifest:

```bash
fullsend repos install -f repos.yaml
```

Install runs in three phases:

1. **Manifest add** — repos specified as positional arguments that are
   not already in the manifest are added (requires `--forge`).
2. **Provision** — repos not yet provisioned get scaffold files,
   variables, and secrets. Partially-installed repos are repaired
   automatically.
3. **Convergence** — repos already installed are checked for variable
   drift (synced automatically) and scaffold ref drift (upgraded
   automatically).

> **Prerequisite:** For repos using `wif` credential mode, GCP WIF
> provisioning (`fullsend inference provision`) must be completed
> before running install. For self-managed mints, also run
> `fullsend mint enroll`. The hosted community mint needs no
> enrollment. Repos using `oidc` or `token` mode do not require
> GCP infrastructure.

> **Note:** When your token does not have direct push access to a target
> repository, the install command creates a fork and submits the scaffold
> PR from the fork. To avoid fork-based delivery, ensure you have write
> (or admin) access to the target repositories before running install.

Preview what would change without making modifications:

```bash
fullsend repos install -f repos.yaml --dry-run
```

Glob patterns are supported:

```bash
fullsend repos install "acme/*" --direct --concurrency 8
```

Install a subset of agent roles (defaults to
`triage,coder,review,fix,retro,prioritize`):

```bash
fullsend repos install -f repos.yaml --roles triage,coder,review
```

## Day-2 operations

### Checking installation status

Compare the manifest against actual repo state:

```bash
fullsend repos status -f repos.yaml
```

Filter to specific repos:

```bash
fullsend repos status --repo acme/api --repo acme/web
```

The command reports per-repo status (installed, not installed, error) and
any configuration drift. Returns a non-zero exit code when drift or
errors exist, making it suitable for CI checks.

Use `--json` for machine-readable output:

```bash
fullsend repos status -f repos.yaml --json
```

### Detecting and reconciling configuration drift

Run `repos install` to detect and fix variable drift and scaffold ref
drift across all manifest repos:

```bash
fullsend repos install -f repos.yaml
```

Preview what would change without modifying anything:

```bash
fullsend repos install -f repos.yaml --dry-run
```

The convergence phase checks variables (`FULLSEND_MINT_URL`)
and scaffold workflow refs against the manifest.
Variables are synced automatically; ref updates are committed as PRs
(or direct pushes with `--direct`). Secrets are write-once at install
time and are not reconciled.

Use `repos status` for a read-only drift report (no changes applied):

```bash
fullsend repos status -f repos.yaml --json
```

### Adding repos

Add a new repo to the manifest and install it in one step:

```bash
fullsend repos install acme/new-api --forge github --direct
```

Add multiple repos:

```bash
fullsend repos install acme/new-api acme/new-web --forge github
```

Preview what would be added:

```bash
fullsend repos install acme/new-api --forge github --dry-run
```

Specify which agent roles to install (defaults to
`triage,coder,review,fix,retro,prioritize`):

```bash
fullsend repos install acme/new-api --forge github --roles triage,coder,review
```

Per-repo overrides can be specified with `--fullsend-ref`, `--mint-url`,
and `--allowed-remote-resources`. The `--inference-region` flag is
install-time only and is not stored in the manifest.

### Removing repos

Remove a repo from the manifest and tear down its installation:

```bash
fullsend repos uninstall acme/old-api
```

When targeting multiple repos (via globs or bulk lists), the command
prompts for confirmation:

```bash
fullsend repos uninstall "acme/*"
```

In non-interactive environments (CI, piped stdin), pass `--yes` to skip
the confirmation prompt:

```bash
fullsend repos uninstall "acme/*" --yes
```

Remove from manifest only (skip teardown — useful when the repo is
already deleted):

```bash
fullsend repos uninstall acme/old-api --manifest-only
```

Tear down without removing from manifest (temporary teardown):

```bash
fullsend repos uninstall acme/old-api --uninstall-only
```

### Rolling out a new fullsend version

To upgrade the scaffold workflow ref across all manifest repos:

1. Update `forge.github.fullsend_ref` (or `forge.gitlab.fullsend_ref` for
   GitLab repos) in `repos.yaml` to the new version.

2. Run install to converge:

   ```bash
   fullsend repos install -f repos.yaml
   ```

3. Review and merge the scaffold PRs in each repo.

Preview what would change without modifying repos:

```bash
fullsend repos install -f repos.yaml --dry-run
```

Push the upgrade directly to the default branch instead of creating a PR:

```bash
fullsend repos install -f repos.yaml --direct
```

Repos that are already SHA-pinned (`@<sha> # <ref>`) preserve their
pinning style during upgrades — the target ref is resolved to a commit
SHA and written as `@<sha> # <ref>`. Non-SHA-pinned repos keep their
string ref format (e.g., `@v2.3.0`). Downgrades are blocked unless
`--force` is set.

## Troubleshooting

### Unknown field errors in repos.yaml

The manifest parser strictly validates field names in all sections
(`defaults`, `forge`, `forge.github`, `forge.gitlab`, and top-level).
Unrecognized fields are rejected with an error naming the offending key:

```
parsing manifest YAML: line 8: field fullsend_ref not found in type repos.Defaults
```

Common causes:

- **Typos** — e.g., `mint_ulr` instead of `mint_url`.
- **Deprecated or unsupported fields** — fields that were never part of the
  manifest schema (such as the legacy `mint:` key) are rejected.
- **Wrong nesting level** — e.g., placing `fullsend_ref` under `defaults`
  instead of under `forge.github` or `forge.gitlab`.

To fix, correct the field name or remove the unrecognized entry and re-run
the command.

### Partial secret state (WIF mode)

When only one of the two required WIF repo secrets (`FULLSEND_GCP_PROJECT_ID`
or `FULLSEND_GCP_WIF_PROVIDER`) exists on a repo but not both, `repos
install` reports an error. This only applies to repos using `wif`
credential mode:

```
partial secret state: FULLSEND_GCP_PROJECT_ID exists but FULLSEND_GCP_WIF_PROVIDER is missing
```

This typically occurs when a previous install was interrupted or when
secrets were manually modified. To resolve, either:

- Delete the existing secret and re-run `repos install` to re-provision
  both secrets together.
- Manually create the missing secret with the correct value.

## Migrating from per-org mode to manifest management

Organizations migrating from per-org mode to per-repo manifest management
can use `repos migrate` — a single command that handles the full migration.

### Step 1: Migrate from per-org to per-repo

```bash
fullsend repos migrate <org> --project <gcp-project>
```

This discovers enrolled repos from the per-org config, provisions WIF
infrastructure, installs per-repo (scaffold, variables, secrets) with
config carried over from the org config, unenrolls migrated repos,
and writes `repos.yaml`.

Preview first with `--dry-run`:

```bash
fullsend repos migrate <org> --project <gcp-project> --dry-run
```

### Step 2: Verify per-repo installations

```bash
fullsend repos status -f repos.yaml
```

Confirm all repos show `installed` status with no drift.

### Step 3: Uninstall the per-org configuration

```bash
fullsend github uninstall "$ORG_NAME"
```

This removes the `.fullsend` config repo, org-level variables, and org
secrets. It also lists any installed GitHub Apps and provides links for
manual deletion.

> **Warning:** Do **not** delete the GitHub Apps listed by the uninstall
> command if you are migrating to per-repo mode. The agents still need
> these apps to function. The apps are shared between per-org and
> per-repo installations — only delete them if you are fully removing
> fullsend from the organization.

In non-interactive environments, pass `--yolo` to skip the confirmation
prompt:

```bash
fullsend github uninstall "$ORG_NAME" --yolo
```

> **Note:** `fullsend github unenroll` is only needed when keeping some
> repos on per-org mode while migrating others to per-repo. When
> migrating all repos, skip unenroll and go directly to uninstall.

## Tearing down

### Removing individual repos

Remove a repo from the manifest and tear down its fullsend installation:

```bash
fullsend repos uninstall acme/old-api
```

Tear down without modifying the manifest (temporary teardown):

```bash
fullsend repos uninstall acme/old-api --uninstall-only
```

When targeting multiple repos, a confirmation prompt appears. In
non-interactive environments (CI, piped stdin), pass `--yes`:

```bash
fullsend repos uninstall "acme/*" --yes
```

### Full teardown

To completely remove fullsend from all manifest repos and GCP
infrastructure, coordinate between roles:

| Step | Role | Command |
|------|------|---------|
| 1 | Platform Admin | `fullsend repos uninstall "org/*" --yes` (forge-side cleanup + manifest removal) |
| 2 | GCP Admin (Inference) | `fullsend inference deprovision <org>` (WIF cleanup) |
| 3 | Mint Admin | `fullsend mint unenroll <org>` |

Each `fullsend` command that prompts for confirmation accepts a skip
flag: `--yes` for `repos` commands, `--yolo` for `github` and `mint`
commands.

## See also

- [Operations](operations.md) — Day-2 per-repo administration and standalone commands
- [Per-Org Mode](org-mode.md) — Organization-mode installation (planned deprecation)
- [CLI Reference: fullsend repos](../../cli/repos.md) — Full flag and subcommand reference
- [Mint administration](../infrastructure/mint-administration.md) — Token mint deployment and management
