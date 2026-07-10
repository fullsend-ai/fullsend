# Operations

Day-2 administration for fullsend per-repo installations: configuration updates, workflow syncing, uninstall, and standalone commands for split-responsibility workflows. For per-org operations (enrollment, org-level status, org uninstall), see [Per-Org Mode](org-mode.md).

## Prerequisites

- **fullsend CLI** installed (see [Getting Started](../getting-started/))
- **GitHub access** — repository admin for the target repository
- **`gh` CLI** authenticated with the required OAuth scopes (see [OAuth scope reference](../infrastructure/advanced-setup.md#oauth-scope-reference))

## Updating configuration values

Update individual secrets or variables without re-running full setup:

```bash
fullsend github set "$OWNER/$REPO" FULLSEND_GCP_PROJECT_ID new-gcp-project
fullsend github set "$OWNER/$REPO" FULLSEND_GCP_REGION global
```

| Key | Storage Type | Description | Example value |
|-----|-------------|-------------|---------------|
| `FULLSEND_GCP_REGION` | Repo variable | GCP region for Agent Platform inference | `global` |
| `FULLSEND_PER_REPO_INSTALL` | Repo variable | Set to `true` for per-repo installations (auto-set by installer) | `true` |
| `FULLSEND_GCP_PROJECT_ID` | Repo secret | GCP project ID where Agent Platform is enabled | `my-gcp-project` |
| `FULLSEND_GCP_WIF_PROVIDER` | Repo secret | Full WIF provider resource name for OIDC authentication | `projects/123456789/locations/global/...` |

## Syncing workflow templates

After upgrading the fullsend CLI, re-run `github setup` to update the workflow file:

```bash
fullsend github setup "$OWNER/$REPO" \
  --inference-project "<GCP_PROJECT>" \
  --inference-wif-provider "<WIF_PROVIDER>"
```

This is idempotent — it updates the workflow file in place without changing other configuration.

## Uninstalling

### Per-repo teardown

To remove fullsend from a single repository:

1. Delete `.github/workflows/fullsend.yaml` and repo-level secrets/variables
2. Run `fullsend inference deprovision "$OWNER/$REPO"` to remove WIF access
3. Contact the fullsend team to unenroll the repo from the hosted mint

If you manage your own self-hosted mint, run `fullsend mint unenroll "$OWNER/$REPO"` instead of step 3. See the [standalone commands](#standalone-commands) table for details.

## Standalone commands

For organizations that separate GCP and GitHub responsibilities across teams, fullsend provides standalone commands that let each team run only the steps they own:

| Role | Command | What it does |
|------|---------|-------------|
| GCP Admin (Inference) | `fullsend inference provision <org\|owner/repo>` | Create WIF pool/provider and grant Agent Platform access (idempotent — safe to re-run for new orgs) |
| GCP Admin (Inference) | `fullsend inference deprovision <org\|owner/repo>` | Remove org or repo from WIF |
| GCP Admin (Inference) | `fullsend inference status <org\|owner/repo>` | Check WIF health, print config values |
| GitHub Maintainer | `fullsend github setup <org\|owner/repo>` | Configure GitHub org or repo (no GCP needed) |
| GitHub Maintainer | `fullsend github enroll <org> [repo...]` | Add repositories to agent enrollment |
| GitHub Maintainer | `fullsend github unenroll <org> [repo...]` | Remove repositories from agent enrollment |
| GitHub Maintainer | `fullsend github set <org\|owner/repo> <key> <value>` | Update a single config value (secret or variable) |
| GitHub Maintainer | `fullsend github status <org>` | Analyze GitHub-side installation state |
| GitHub Maintainer | `fullsend github sync-scaffold <org>` | Update workflow templates to current CLI version |
| GitHub Maintainer | `fullsend github uninstall <org>` | Remove GitHub configuration (org-level only) |
| GCP Admin (Mint) | `fullsend mint deploy` | Deploy the token mint Cloud Function |
| GCP Admin (Mint) | `fullsend mint add-role <role>` | Register a role PEM and app ID on the mint |
| GCP Admin (Mint) | `fullsend mint remove-role <role>` | Remove a role from the mint (deletes PEM secret by default) |
| GCP Admin (Mint) | `fullsend mint enroll <org\|owner/repo>` | Register an org or repo in the mint (does not grant Agent Platform access — use `inference provision`) |
| GCP Admin (Mint) | `fullsend mint unenroll <org\|owner/repo>` | Remove an org or repo from the mint |
| GCP Admin (Mint) | `fullsend mint status` | Inspect mint state and PEM health |

| Fleet Admin | `fullsend repos init <org\|owner/repo>` | Generate a `repos.yaml` manifest by discovering existing installations |
| Platform Admin | `fullsend repos install -f repos.yaml` | Bulk-install fullsend on repos from a declarative manifest (parallel discovery → sequential WIF → parallel scaffold) |
| Fleet Admin | `fullsend repos status` | Compare `repos.yaml` manifest against actual per-repo state (drift detection) |

| Developer | `fullsend agent add <url-or-path>` | Register an agent in config (URL auto-pinned to commit SHA) |
| Developer | `fullsend agent list` | List registered agents and their sources |
| Developer | `fullsend agent update <name> [sha]` | Re-pin a URL agent to a new commit SHA |
| Developer | `fullsend agent remove <name>` | Unregister an agent from config |
| Developer | `fullsend agent migrate-customizations` | Migrate `customized/` overlays to config-driven agents via PR |

The typical handoff: a GCP admin runs `mint deploy` + `mint enroll` + `inference provision`, then passes the mint URL and WIF provider resource name to a GitHub maintainer who runs `github setup --mint-url=... --inference-wif-provider=...`.

### Per-command IAM role breakdown

When using the split-responsibility workflow, each standalone command requires a subset of IAM roles. Use this table to request only what you need.

| IAM Role | `inference provision` | `inference deprovision` | `inference status` | `mint deploy` | `mint add-role` | `mint remove-role` | `mint enroll` | `mint unenroll` | `mint status` |
|----------|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|
| `roles/iam.workloadIdentityPoolAdmin` | x | x | | x | | | x | x | |
| `roles/resourcemanager.projectIamAdmin` | x | | | \* | | | \*\* | | |
| `roles/iam.serviceAccountAdmin` | | | | x | | | | | |
| `roles/secretmanager.admin` | | | | \* | \*\*\* | \*\*\*\* | | | |
| `roles/cloudfunctions.developer` | | | | x | | | | | |
| `roles/cloudfunctions.viewer` | | | | | x | x | x | x | x |
| `roles/run.admin` | | | | x | x | x | x | x | |
| `roles/iam.workloadIdentityPoolViewer` | | | x† | | | | | | |
| `roles/secretmanager.viewer` | | | | | § | | | | x |

\* `roles/resourcemanager.projectIamAdmin` and `roles/secretmanager.admin` are required for `mint deploy` only when using `--pem-dir` (first-time bootstrap). Standard deploys without `--pem-dir` do not need these roles.

\*\* `roles/resourcemanager.projectIamAdmin` is required for `mint enroll` only in per-repo mode (`mint enroll owner/repo`). Org-scoped enrollment does not grant IAM bindings — use `inference provision` separately.

\*\*\* `roles/secretmanager.admin` is required for `mint add-role` when uploading a new PEM (`--pem` or browser mode). When using `--use-existing-pem-secret`, only `roles/secretmanager.viewer` is required (see §).

\*\*\*\* `roles/secretmanager.admin` is required for `mint remove-role` unless `--keep-pem` is passed (default deletes the PEM secret).

§ `roles/secretmanager.viewer` is required for `mint add-role` when using `--use-existing-pem-secret` (checks that the PEM secret exists).

† All commands that call GCP APIs also require `resourcemanager.projects.get` (typically available via `roles/browser` or any project-level viewer role). This is only notable for `inference status` where it is not covered by the other listed roles.

Required GCP APIs also differ by command group:

```bash
# Inference commands (inference provision/deprovision/status):
gcloud services enable \
  iam.googleapis.com \
  cloudresourcemanager.googleapis.com \
  aiplatform.googleapis.com \
  --project="$GCP_PROJECT"

# Mint commands (mint deploy/enroll/unenroll/status):
gcloud services enable \
  iam.googleapis.com \
  cloudresourcemanager.googleapis.com \
  cloudfunctions.googleapis.com \
  run.googleapis.com \
  secretmanager.googleapis.com \
  iamcredentials.googleapis.com \
  --project="$GCP_PROJECT"
```

> **Note:** `iamcredentials.googleapis.com` is a runtime dependency — the deployed mint Cloud Function uses it for WIF token exchange, not the CLI itself. It must be enabled before `mint deploy`.

## Status notifications

Agent workflows post status comments on issues and PRs when they start and complete. This behavior is controlled by the `status_notifications` section in `config.yaml`:

```yaml
defaults:
  status_notifications:
    comment:
      start: enabled      # "enabled" (default) | "disabled"
      completion: enabled  # "enabled" (default) | "disabled"
```

When `status_notifications` is omitted, comments default to enabled.

The composite action accepts four optional inputs for status notifications:

| Input | Description |
|-------|-------------|
| `run-url` | URL of the CI/CD run shown in the status comment |
| `status-repo` | Repository (`owner/repo`) to post status comments on |
| `status-number` | Issue or PR number for status comments |
| `mint-url` | URL of the token mint service used to obtain fresh tokens for posting comments |

All reusable workflows pass these inputs automatically.

## See Also

- [Getting Started](../getting-started/) — Standard per-repo installation
- [Advanced setup](../infrastructure/advanced-setup.md) — Alternative installation paths, setup flags, custom app sets
- [Mint service administration](../infrastructure/mint-administration.md) — Deploying and managing the token mint
- [Infrastructure Reference](../infrastructure/infrastructure-reference.md) — Token mint, WIF, and secrets deployment details
- [CLI Internals](../dev/cli-internals.md) — Command structure and implementation details
