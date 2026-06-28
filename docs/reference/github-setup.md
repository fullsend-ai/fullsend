# Setting up fullsend with pre-provisioned infrastructure

This guide walks through configuring fullsend in a GitHub organization or repository when the GCP infrastructure (token mint and inference WIF) has already been provisioned by a GCP administrator. No GCP credentials are required.

For the all-in-one setup that provisions both GCP and GitHub in a single command, see [Installing fullsend](installation.md).

## Prerequisites

- **GitHub access** — the required permission level depends on the setup mode:
  - **Per-org mode** (`fullsend github setup <org>`) requires **GitHub organization owner** access. The command creates org-level variables (`FULLSEND_MINT_URL`), creates the `.fullsend` config repo, and (unless `--skip-app-setup` is passed) creates or installs GitHub Apps — all of which require the `admin:org` OAuth scope.
  - **Per-repo mode** (`fullsend github setup <owner/repo>`) requires **repo admin** access only. It writes secrets and variables to the target repo and does not create or install GitHub Apps. Only the `repo` and `workflow` OAuth scopes are needed. However, an **org owner** must [pre-install the GitHub Apps](#default-fullsend-ai-app-set-installation-urls) on the org before agents can function — the token mint requires an app installation to generate tokens.
- **GitHub CLI** (`gh`) authenticated — the installer runs a preflight check and tells you which scopes are missing. When prompted, run the `gh auth refresh -s <scopes>` command it suggests.
- **fullsend CLI** — download the latest binary from [GitHub Releases](https://github.com/fullsend-ai/fullsend/releases)
- **From your Mint service provider admin** (currently GCP-managed; other providers planned):
  - Token mint URL (`--mint-url`) — the HTTPS endpoint of the deployed mint Cloud Function. If you are using the fullsend hosted mint, the URL is `https://fullsend-mint-gljhbkcloq-uc.a.run.app` (see [Hosted mint](../guides/infrastructure/mint-administration.md#hosted-mint)).
- **From your Inference provider admin** (currently GCP Agent Platform, formerly Vertex AI; other providers planned):
  - GCP project ID (`--inference-project`) — the project where Agent Platform is enabled (e.g., `my-gcp-project`)
  - WIF provider resource name (`--inference-wif-provider`) — the full resource path, e.g., `projects/123456789/locations/global/workloadIdentityPools/fullsend-inference/providers/github-oidc` (note: the leading number is the GCP **project number**, not the project ID string; your GCP admin can find it with `gcloud projects describe <project-id> --format='value(projectNumber)'`)
  - GCP region for inference (`--inference-region`, defaults to `global`)

> **Note:** You do NOT need GCP project access, `gcloud` authentication, or any IAM roles. All GCP infrastructure values are provided as flags.

## Roles and responsibilities

This guide covers the **GitHub Maintainer** role. The GCP-side work (token mint and inference WIF) is handled by a GCP administrator before you start. Here's where this guide fits in the overall workflow:

| Role | What they do | Covered in |
|------|-------------|------------|
| **GCP Admin (Mint)** | Deploy token mint, enroll orgs | [Mint service administration](../guides/infrastructure/mint-administration.md) |
| **GCP Admin (Inference)** | Provision WIF and Agent Platform access | [Installing fullsend — standalone commands](installation.md#standalone-commands) |
| **GitHub Maintainer (org)** | Configure GitHub org — Apps, config repo, enrollment | **This guide** |
| **GitHub Maintainer (repo)** | Configure a single repo — secrets, variables, shim workflow | **This guide** ([per-repo setup](#per-repo-setup)) |
| **Full Admin** | All of the above in one command | [Installing fullsend — all-in-one](installation.md#all-in-one-admin-install) |

The typical workflow: a GCP admin runs `mint deploy` + `mint enroll` + `inference provision`, then hands you the mint URL and WIF provider resource name. You run `github setup` with those values. For the full role breakdown with IAM roles, see [Installing fullsend — standalone commands](installation.md#standalone-commands).

The GitHub Apps must be installed on the target org before **agents can run**, but the timing relative to GCP setup is flexible:

- **Per-org mode** — `github setup <org>` handles app installation interactively (opens a browser for each role), so a single org owner can run it without pre-installing apps.
- **Per-org with `--skip-app-setup`** — an org owner must [pre-install the apps](#default-fullsend-ai-app-set-installation-urls) before running setup.
- **Per-repo mode** — an org owner must [pre-install the apps](#default-fullsend-ai-app-set-installation-urls) before a repo admin runs `github setup <owner/repo>`.

### Default `fullsend-ai` app set installation URLs

When using the default app set, an **org owner** installs each app from these URLs. The org owner approves which repositories the app can access during installation.

| Role | Installation URL |
|------|-----------------|
| fullsend | <https://github.com/apps/fullsend-ai-fullsend/installations/new> |
| triage | <https://github.com/apps/fullsend-ai-triage/installations/new> |
| coder | <https://github.com/apps/fullsend-ai-coder/installations/new> |
| review | <https://github.com/apps/fullsend-ai-review/installations/new> |
| retro | <https://github.com/apps/fullsend-ai-retro/installations/new> |
| prioritize | <https://github.com/apps/fullsend-ai-prioritize/installations/new> |

> **Tip:** To verify apps are installed, run `gh api /orgs/{org}/installations --jq '.installations[].app_slug'`.

## Per-org setup

Per-org mode creates a `.fullsend` config repository, deploys reusable workflows, configures secrets and variables, and enrolls repositories:

```bash
fullsend github setup acme-corp \
  --mint-url=<MINT_URL> \
  --inference-project=my-gcp-project \
  --inference-wif-provider=projects/123456789/locations/global/workloadIdentityPools/fullsend-inference/providers/github-oidc \
  --inference-region=global
```

Unless `--skip-app-setup` is passed, the command opens a browser for each agent role to create or install GitHub Apps (see [GitHub App setup](#github-app-setup) below), then prompts you to select repositories for enrollment.

### GitHub App setup

Per-org setup includes a GitHub App setup phase that runs automatically unless `--skip-app-setup` is passed. Understanding this phase is important for choosing the right workflow.

**What happens during app setup:**

1. For each agent role (e.g., `triage`, `coder`, `review`), the CLI checks whether a GitHub App matching the naming convention (`{app-set}-{role}`, e.g., `fullsend-ai-triage`) is already installed on the org.
2. If an app is already installed and its private key secret exists, the CLI reuses it.
3. If no matching app is found, the CLI checks whether the app exists globally (e.g., a public app owned by another org). If found, it opens a browser to install the existing app on your org.
4. If no app exists at all, the CLI runs the [manifest flow](https://docs.github.com/en/apps/creating-github-apps/setting-up-a-github-app/creating-a-github-app-from-a-manifest) — it opens a browser to `https://github.com/organizations/{org}/settings/apps/new` to create a new GitHub App.

**Permission requirements for app setup:**

- **Creating a new GitHub App** (manifest flow) requires **GitHub organization owner** access. Only org owners can create apps in an organization's developer settings.
- **Installing an existing app** (e.g., a public app like `fullsend-ai-triage`) on an org also requires org owner access, or the org must have an [app installation approval policy](https://docs.github.com/en/organizations/managing-programmatic-access-to-your-organization/limiting-oauth-app-and-github-app-access-requests) that allows members to request installations.

**The default `fullsend-ai` app set:**

The default `--app-set` value is `fullsend-ai`, which corresponds to public GitHub Apps owned by the `fullsend-ai` organization (e.g., `fullsend-ai-triage`, `fullsend-ai-coder`, `fullsend-ai-review`). When using this default, the CLI detects these existing public apps and installs them on your org rather than creating new ones. An org owner must approve the installation.

**When to use `--skip-app-setup`:**

Pass `--skip-app-setup` when the GitHub Apps are already installed on the org — for example, when an org owner has already installed the `fullsend-ai` apps or completed a previous `github setup` run that handled app creation. With this flag, the CLI skips all app-related steps (no browser windows, no manifest flow, no installation prompts) and proceeds directly to configuring the config repo, org variables, secrets, and enrollment.

```bash
# Org owner has already installed fullsend-ai apps on acme-corp
fullsend github setup acme-corp \
  --mint-url=<MINT_URL> \
  --inference-project=my-gcp-project \
  --inference-wif-provider=projects/123456789/locations/global/workloadIdentityPools/fullsend-inference/providers/github-oidc \
  --skip-app-setup
```

> **Note:** Even with `--skip-app-setup`, per-org setup still requires the `admin:org` OAuth scope because it creates org-level variables. Repo admins without org-level access should use [per-repo mode](#per-repo-setup) instead.

### Setup flags

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `--mint-url` | Yes | — | Token mint Cloud Function URL (HTTPS) |
| `--agents` | No | `fullsend,triage,coder,review,retro,prioritize` | Comma-separated agent roles to configure (per-repo omits `fullsend`) |
| `--inference-project` | Yes (optional on re-run) | — | GCP project ID where Agent Platform is enabled |
| `--inference-region` | No | `global` | GCP region for Agent Platform inference |
| `--inference-wif-provider` | Yes (optional on re-run) | — | Full WIF provider resource name (see [Prerequisites](#prerequisites) for format) |
| `--skip-app-setup` | No | `false` | Skip GitHub App creation/installation (use when apps are already installed; see [GitHub App setup](#github-app-setup)) |
| `--public` | No | `false` | Create public (unlisted) GitHub Apps (for multi-org sharing) |
| `--app-set` | No | `fullsend-ai` | App set name prefix for GitHub Apps |
| `--enroll-all` | No | `false` | Enroll all repositories without prompting (per-org only) |
| `--enroll-none` | No | `false` | Skip enrollment without prompting (per-org only) |
| `--vendor` | No | `false` | Vendor binary, reusable workflows, actions, and agent content (see [Vendored vs layered installs](#vendored-vs-layered-installs)) |
| `--fullsend-source` | No | | Fullsend source checkout for content and cross-compile (requires `--vendor`) |
| `--fullsend-binary` | No | | Path to a Linux fullsend binary when vendoring (skips auto-resolution) |
| `--direct` | No | `false` | Push scaffold files directly to the default branch instead of creating a PR (falls back to PR if branch protection blocks the push) |
| `--dry-run` | No | `false` | Preview changes without making them |

### Vendored vs layered installs

Same behavior as [admin install](installation.md#vendored-vs-layered-installs): layered (default) fetches upstream at runtime; `--vendor` installs binary plus workflow/action/agent content and runtime detects vendored installs via `action.yml` presence.

`fullsend admin analyze <org>` reports when stale vendored assets are present (analyze has no install flags).

## Per-repo setup

Per-repo mode bootstraps a single repository with a `.fullsend/` directory, shim workflow, and repo-level secrets:

```bash
fullsend github setup acme-corp/my-service \
  --mint-url=<MINT_URL> \
  --inference-project=my-gcp-project \
  --inference-wif-provider=projects/123456789/locations/global/workloadIdentityPools/fullsend-inference/providers/github-oidc
```

Per-repo mode differs from per-org in these ways:
- `--inference-project` and `--inference-wif-provider` are required on first setup; on re-runs, each is optional if the corresponding repo secret already exists
- Secrets and variables are written to the target repo (not a `.fullsend` config repo)
- The `fullsend` meta-role is excluded from the default agent set
- `--enroll-all` and `--enroll-none` flags are not available
- No config repo is created
- No GitHub App creation or installation — per-repo mode does not manage apps

### Repo admin without org access

If you are a repo admin but **not** an org owner, per-repo mode is your setup path. The CLI itself only needs `repo` and `workflow` scopes, but the agent pipeline requires the GitHub Apps to be installed on your org **before** agents can function. The token mint exchanges OIDC tokens for GitHub App installation tokens — if the app isn't installed, the mint returns an error and the agent workflow fails.

**Prerequisites for repo admins:**

1. An **org owner** must have [installed the GitHub Apps](#default-fullsend-ai-app-set-installation-urls) on the org (or on your specific repos)
2. A **GCP admin** has deployed the token mint and provisioned WIF infrastructure
3. You have received the mint URL, inference project, and WIF provider values

Once the apps are installed, run [per-repo setup](#per-repo-setup) for each repository you manage. This writes the shim workflow, config directory, repo variables, and repo secrets to the target repo. No org-level changes are made and no GitHub App interaction occurs.

After setup, agents activate once the GitHub Apps have access to the repo. If the apps are installed org-wide, no further action is needed. If installed on specific repos only, ask your org owner to add your repo to each app's installation.

## Merging enrollment PRs

After setup, each enrolled repository will have an open PR adding the agent workflow files. Review and merge these PRs to activate agents. See the "Merge enrollment PRs" section of the [installation guide](installation.md) for details.

## Day-2 operations

### Enrolling and unenrolling repositories

Add or remove repositories from agent enrollment after initial setup:

```bash
# Enable specific repos
fullsend github enroll acme-corp repo-a repo-b

# Enable all repos (excluding .fullsend)
fullsend github enroll acme-corp --all

# Disable specific repos
fullsend github unenroll acme-corp repo-a repo-b

# Disable all repos (--yolo skips the confirmation prompt)
fullsend github unenroll acme-corp --all --yolo
```

### Updating configuration values

Update individual secrets or variables without re-running full setup:

```bash
# Update a value for an org (writes to the .fullsend config repo)
fullsend github set acme-corp FULLSEND_GCP_REGION us-east5

# Update a value for a specific repo
fullsend github set acme-corp/my-service FULLSEND_GCP_PROJECT_ID new-gcp-project
```

| Key | Storage Type | Description | Example value |
|-----|-------------|-------------|---------------|
| `FULLSEND_GCP_REGION` | Repo variable | GCP region for Agent Platform inference | `global` |
| `FULLSEND_PER_REPO_INSTALL` | Repo variable | Set to `true` for per-repo installations (auto-set by installer) | `true` |
| `FULLSEND_GCP_PROJECT_ID` | Repo secret | GCP project ID where Agent Platform is enabled | `my-gcp-project` |
| `FULLSEND_GCP_WIF_PROVIDER` | Repo secret | Full WIF provider resource name for OIDC authentication | `projects/123456789/locations/global/...` |

For org targets, values are written to the `.fullsend` config repo. For `owner/repo` targets, values are written directly to that repo.

### Syncing workflow templates

Update the workflow files in the `.fullsend` config repo to match the current fullsend binary version:

```bash
fullsend github sync-scaffold acme-corp
```

Run this after upgrading the fullsend CLI to pick up workflow template changes.

### Checking status

Inspect the GitHub-side installation state:

```bash
fullsend github status acme-corp
```

Reports on: config repo presence, workflow files, org variables, inference secrets, and enrollment state. Does not check GCP resources.

## Uninstalling

Remove fullsend GitHub configuration from an organization:

```bash
fullsend github uninstall acme-corp
```

This removes the `.fullsend` config repo, org variables (`FULLSEND_MINT_URL`), and org secrets (`FULLSEND_DISPATCH_TOKEN`). It also lists any installed GitHub Apps and provides links for manual deletion. Add `--yolo` to skip the confirmation prompt.

> **Note:** `github uninstall` only removes GitHub-side configuration. GCP resources (mint, WIF, PEM secrets) are managed separately via `fullsend mint unenroll` and `fullsend inference deprovision` commands by the GCP administrator.

## Relationship to admin install

`fullsend admin install` performs all setup — GCP and GitHub — in a single command. The standalone subcommands decompose the same pipeline:

```
fullsend admin install <org>
  ├── mint deploy + mint enroll      → fullsend mint deploy + fullsend mint enroll <org>
  ├── inference provision             → fullsend inference provision <org>
  └── github setup                   → fullsend github setup <org> --mint-url=... --inference-wif-provider=...
```

Use `admin install` when one person has both GCP and GitHub access. Use the standalone commands when responsibilities are split across teams.

## See Also

- [Installing fullsend](installation.md) — End-user setup (inference + GitHub) and all-in-one admin install
- [Mint service administration](../guides/infrastructure/mint-administration.md) — Deploying and managing the token mint
- [Infrastructure Reference](../guides/infrastructure/infrastructure-reference.md) — Token mint, WIF, and secrets details
- [CLI Internals](../guides/dev/cli-internals.md) — Command structure and implementation details
