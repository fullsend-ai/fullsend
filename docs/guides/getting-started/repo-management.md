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
- **GCP prerequisites** — GCP WIF provisioning (`fullsend inference provision`) must be completed separately before running `repos install`. For self-managed mints, mint enrollment (`fullsend mint enroll`) is also required. The hosted community mint needs no enrollment — install the shared Apps and use the CLI defaults. When multiple repos share the same GCP project, existing inference secrets are reused automatically. See [Mint administration](../infrastructure/mint-administration.md) and [Advanced setup](../infrastructure/advanced-setup.md).

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
a `repos.yaml` manifest. If a manifest already exists, newly migrated
repos are merged into it rather than overwriting it.

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

Each platform (`github`, `gitlab`) is a top-level key containing its
infrastructure config and repos list. Repos are grouped under their
platform — no per-entry forge selector is needed:

```yaml
version: 1
github:
  mint_url: https://mint.example.com
  fullsend_ref: v2.5.0
  repos:
    - name: acme/api-server
    - name: acme/web-frontend

gitlab:
  url: https://gitlab.example.com
  fullsend_ref: v2.5.0
  repos:
    - name: gitlab-group/project
```

GitHub repos use a token mint for authentication. The
`github.mint_mode` field controls the default mint URL:
`public` (default) uses `https://mint.fullsend.sh` when no explicit
`mint_url` is set; `private` requires an explicit `mint_url`. Both
`mint_mode` and `mint_url` can be overridden per-repo.

For GitLab repos, set the `GITLAB_TOKEN` environment variable or pass
`--gitlab-token` to `fullsend repos` subcommands. When no manifest URL
is set, the base URL falls back through `FULLSEND_GITLAB_URL` →
`GITLAB_API_URL` → `CI_SERVER_URL`, defaulting to `gitlab.com` when
none are set.

Per-repo fields inherit from the platform-level default when omitted.
To explicitly stop a field from inheriting, set it to the literal value
`none`. For example, `fullsend_ref: none` prevents the repo from
inheriting the platform-level `fullsend_ref`:

```yaml
github:
  fullsend_ref: v2.5.0
  repos:
    - name: acme/api
    - name: acme/legacy
      fullsend_ref: none  # does not inherit v2.5.0
```

The `none` sentinel works for string fields (`fullsend_ref`,
`mint_url`, `mint_mode`). List fields like `allowed_remote_resources`
are managed at the `defaults` level and cannot be cleared per-repo.

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

Install runs in two phases:

1. **Manifest add** — repos specified as positional arguments that are
   not already in the manifest are added (requires `--forge`).
2. **Convergence** — every repo flows through a single probe → diff →
   apply pipeline. New repos are fully provisioned (scaffold files,
   variables, secrets). Already-installed repos are checked for
   component drift (workflow, thin callers, variables, secrets) and
   scaffold ref drift. Missing or drifted components are repaired
   automatically; ref updates are committed as PRs (or direct pushes
   with `--direct`).

> **Prerequisite:** GCP WIF provisioning (`fullsend inference provision`)
> must be completed before running install. For self-managed mints,
> also run `fullsend mint enroll`. The hosted community mint needs no
> enrollment.

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

Run `repos install` to detect and fix component drift (workflow, thin
callers, variables, secrets) and scaffold ref drift across all manifest
repos:

```bash
fullsend repos install -f repos.yaml
```

Preview what would change without modifying anything:

```bash
fullsend repos install -f repos.yaml --dry-run
```

The convergence phase checks all components (workflow, thin callers,
variables, secrets) and scaffold workflow refs against the manifest.
Missing or drifted components are repaired automatically; ref updates
are committed as PRs (or direct pushes with `--direct`).

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

1. Update `github.fullsend_ref` (or `gitlab.fullsend_ref` for
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
string ref format (e.g., `@v2.3.0`).

Downgrades are blocked unless `--force` is set. When both the current
and target refs are semver tags, the guard uses version comparison. When
either ref is a SHA (or a mix of SHA and tag), the guard resolves both
to commit SHAs and uses git ancestry checking via the forge API to
detect downgrades. If the ancestry check fails (e.g., API error), the
upgrade proceeds rather than blocking — graceful degradation.

## Troubleshooting

### Unknown field errors in repos.yaml

The manifest parser strictly validates field names in all sections
(`defaults`, `github`, `gitlab`, and top-level).
Unrecognized fields are rejected with an error naming the offending key:

```
parsing manifest YAML: line 8: field fullsend_ref not found in type repos.Defaults
```

Common causes:

- **Typos** — e.g., `mint_ulr` instead of `mint_url`.
- **Deprecated or unsupported fields** — fields that were never part of the
  manifest schema (such as the legacy `mint:` key) are rejected.
- **Wrong nesting level** — e.g., placing `fullsend_ref` under `defaults`
  instead of under `github` or `gitlab`.

To fix, correct the field name or remove the unrecognized entry and re-run
the command.

### Partial secret state

When only one of the two required inference secrets (`FULLSEND_GCP_PROJECT_ID`
or `FULLSEND_GCP_WIF_PROVIDER`) exists on a repo but not both, `repos
install` reports an error:

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
and writes `repos.yaml`. If a manifest already exists, new entries are
merged in rather than overwriting it.

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
| 3 | GCP Admin (Mint) | `fullsend mint unenroll <org>` (self-hosted mints only; not needed for the hosted community mint) |

Each `fullsend` command that prompts for confirmation accepts a skip
flag: `--yes` for `repos` commands, `--yolo` for `github` and `mint`
commands.

## See also

- [Operations](operations.md) — Day-2 per-repo administration and standalone commands
- [Per-Org Mode](org-mode.md) — Organization-mode installation (planned deprecation)
- [CLI Reference: fullsend repos](../../cli/repos.md) — Full flag and subcommand reference
- [Mint administration](../infrastructure/mint-administration.md) — Token mint deployment and management
