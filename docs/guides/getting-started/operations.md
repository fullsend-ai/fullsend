# Operations

Day-2 administration for fullsend per-repo installations: configuration updates, workflow syncing, uninstall, and standalone commands for split-responsibility workflows. For per-org operations (enrollment, org-level status, org uninstall), see [Per-Org Mode](org-mode.md).

## Prerequisites

- **fullsend CLI** installed (see [Getting Started](../getting-started/))
- **GitHub access** — repository admin for the target repository
- **`gh` CLI** authenticated with the required OAuth scopes (see [OAuth scope reference](../infrastructure/advanced-setup.md#oauth-scope-reference))

## Updating configuration values

### GitHub

Update individual secrets or variables without re-running full setup:

```bash
fullsend github set "$OWNER/$REPO" FULLSEND_GCP_PROJECT_ID new-gcp-project
fullsend github set "$OWNER/$REPO" FULLSEND_GCP_REGION global
```

| Key | Storage Type | Description | Example value |
|-----|-------------|-------------|---------------|
| `FULLSEND_GCP_REGION` | Repo variable | GCP region for Agent Platform inference | `global` |
| `FULLSEND_PER_REPO_INSTALL` | Repo variable | Set to `true` for per-repo installations (auto-set by installer) | `true` |
| `FULLSEND_REVIEW_CLIENT_ID` | Repo variable | OAuth client ID of the review agent's GitHub App (best-effort, auto-set by installer) | `Iv23li1nIorNLIQy6NWK` |
| `FULLSEND_GCP_PROJECT_ID` | Repo secret | GCP project ID where Agent Platform is enabled | `my-gcp-project` |
| `FULLSEND_GCP_WIF_PROVIDER` | Repo secret | Full WIF provider resource name for OIDC authentication | `projects/123456789/locations/global/...` |

### GitLab

For GitLab repos, re-run `repos install` with updated values to converge configuration:

```bash
fullsend repos install -f repos.yaml "$OWNER/$REPO" \
  --inference-project "<GCP_PROJECT>"
```

| Key | Storage Type | Description | Example value |
|-----|-------------|-------------|---------------|
| `FULLSEND_GCP_REGION` | CI/CD variable | GCP region for Agent Platform inference | `us-central1` |
| `FULLSEND_GCP_PROJECT_ID` | CI/CD secret | GCP project ID for inference | `my-gcp-project` |
| `FULLSEND_GCP_WIF_PROVIDER` | CI/CD secret | WIF provider resource name for inference | `projects/123456789/locations/global/...` |

## Syncing workflow templates

After upgrading the fullsend CLI, re-run `github setup` to update the workflow file for a single repo:

```bash
fullsend github setup "$OWNER/$REPO" \
  --inference-project "<GCP_PROJECT>" \
  --inference-wif-provider "<WIF_PROVIDER>"
```

For manifest-managed installations (including GitLab repos), use `repos install` to converge all repos (including workflow ref upgrades):

```bash
fullsend repos install -f repos.yaml
```

This is idempotent — it provisions new repos, repairs missing or drifted components (workflow, thin callers, variables, secrets), and upgrades workflow refs. Variables with manifest-specified values (e.g. mint URL) are checked for value drift; secrets and one-time-set variables are checked for presence only.

## Uninstalling

### Per-repo teardown

To remove fullsend from a single repository:

**GitHub repos:**

1. Delete `.github/workflows/fullsend.yaml`, `.github/workflows/prioritize.yml`, and repo-level secrets/variables
2. Run `fullsend inference deprovision "$OWNER/$REPO"` to remove WIF access
3. Remove the `FULLSEND_MINT_URL` repository variable (if set) — no separate unenrollment is needed for the hosted community mint

**GitLab repos:**

1. Delete `.gitlab/ci/fullsend-*.yml`, `.gitlab-ci.yml` (if fullsend-managed), and `.fullsend/config.yaml`
2. Delete all CI/CD variables prefixed with `FULLSEND_`
3. Revoke the `fullsend-bot` project access token (Settings → Access Tokens)
4. Delete fullsend pipeline schedules (`fullsend slash poll` and `fullsend event poll`)

If you manage your own self-hosted mint, run `fullsend mint unenroll "$OWNER/$REPO"` to remove the repo from the mint's allowlist. See the [standalone commands](#standalone-commands) table for details.

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
| GCP Admin (Mint) | `fullsend mint delete` | Tear down mint infrastructure (inverse of deploy) |
| GCP Admin (Mint) | `fullsend mint add-role <role>` | Register a role PEM and app ID on the mint |
| GCP Admin (Mint) | `fullsend mint remove-role <role>` | Remove a role from the mint (deletes PEM secret by default) |
| GCP Admin (Mint) | `fullsend mint enroll <org\|owner/repo>` | Register an org or repo in the mint (does not grant Agent Platform access — use `inference provision`) |
| GCP Admin (Mint) | `fullsend mint unenroll <org\|owner/repo>` | Remove an org or repo from the mint |
| GCP Admin (Mint) | `fullsend mint status` | Inspect mint state and PEM health |

| Fleet Admin | `fullsend repos migrate <org> --project <gcp-project>` | Migrate an org from per-org to per-repo install, generating a `repos.yaml` manifest |
| Platform Admin | `fullsend repos install [repos...]` | Converge repos to desired state: provision new, repair component drift (workflow, thin callers, variables, secrets), upgrade refs |
| Platform Admin | `fullsend repos uninstall <repos...>` | Tear down fullsend from repos and remove from manifest |
| Fleet Admin | `fullsend repos status` | Compare manifest against actual per-repo state: detect missing or drifted components and ref drift |
| Fleet Admin | `fullsend repos set-default <key> <value>` | Set or remove a platform-level default in the manifest |

| Developer | `fullsend agent add <url-or-path>` | Register an agent in config (URL auto-pinned to commit SHA) |
| Developer | `fullsend agent list` | List registered agents and their sources |
| Developer | `fullsend agent update <name> [sha]` | Re-pin a URL agent to a new commit SHA |
| Developer | `fullsend agent remove <name>` | Unregister an agent from config |

The typical handoff for self-managed mints: a GCP admin runs `mint deploy` + `mint enroll` + `inference provision`, then passes the mint URL and WIF provider resource name to a GitHub maintainer who runs `github setup --mint-url=... --inference-wif-provider=...`. For the hosted community mint, enrollment is automatic — install the shared Apps and use the CLI defaults.

### Per-command IAM role breakdown

When using the split-responsibility workflow, each standalone command requires a subset of IAM roles. Use this table to request only what you need.

| IAM Role | `inference provision` | `inference deprovision` | `inference status` | `mint deploy` | `mint delete` | `mint add-role` | `mint remove-role` | `mint enroll` | `mint unenroll` | `mint status` |
|----------|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|
| `roles/iam.workloadIdentityPoolAdmin` | x | x | | x | x | | | x | x | |
| `roles/resourcemanager.projectIamAdmin` | x | | | \* | | | | | | |
| `roles/iam.serviceAccountAdmin` | | | | x | x | | | | | |
| `roles/secretmanager.admin` | | | | \* | x | \*\* | \*\*\* | | | |
| `roles/cloudfunctions.developer` | | | | x | x | | | | | |
| `roles/cloudfunctions.viewer` | | | | | | x | x | x | x | x |
| `roles/run.admin` | | | | x | | x | x | x | x | |
| `roles/iam.workloadIdentityPoolViewer` | | | x† | | | | | | | |
| `roles/secretmanager.viewer` | | | | | | § | | | | x |

\* `roles/resourcemanager.projectIamAdmin` and `roles/secretmanager.admin` are required for `mint deploy` only when using `--pem-dir` (first-time bootstrap). Standard deploys without `--pem-dir` do not need these roles.

\*\* `roles/secretmanager.admin` is required for `mint add-role` when uploading a new PEM (`--pem` or browser mode). When using `--use-existing-pem-secret`, only `roles/secretmanager.viewer` is required (see §).

\*\*\* `roles/secretmanager.admin` is required for `mint remove-role` unless `--keep-pem` is passed (default deletes the PEM secret).

§ `roles/secretmanager.viewer` is required for `mint add-role` when using `--use-existing-pem-secret` (checks that the PEM secret exists).

† All commands that call GCP APIs also require `resourcemanager.projects.get` (typically available via `roles/browser` or any project-level viewer role). This is only notable for `inference status` where it is not covered by the other listed roles.

Enrollment (org- or repo-scoped) does not grant IAM bindings — Vertex AI access is provisioned separately via `inference provision`.

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

See [Status Notifications](../user/customizing-agents.md#status-notifications) for configuring start and completion comments.

The composite action accepts four optional inputs for status notifications:

| Input | Description |
|-------|-------------|
| `run-url` | URL of the CI/CD run shown in the status comment |
| `status-repo` | Repository (`owner/repo`) to post status comments on |
| `status-number` | Issue or PR number for status comments |
| `mint-url` | URL of the token mint service used to obtain fresh tokens for posting comments |

All reusable workflows pass these inputs automatically.

### GitLab CI

On GitLab CI, the agent reads status notification context from standard CI/CD environment variables:

| Variable | Description |
|----------|-------------|
| `GITLAB_TOKEN` | **Required.** Project or group access token with API scope. |
| `CI_SERVER_URL` | GitLab instance URL (set automatically by GitLab CI). Fallback when `FULLSEND_GITLAB_URL` and `GITLAB_API_URL` are unset. |
| `CI_COMMIT_SHA` | Commit SHA shown in the status comment. |
| `CI_MERGE_REQUEST_SOURCE_BRANCH_SHA` | Preferred over `CI_COMMIT_SHA` in merge request pipelines. |
| `CI_PIPELINE_ID` | Used as the run ID for status comment markers. |
| `CI_MERGE_REQUEST_IID` | When set, status comments target the merge request notes API instead of issues. |
| `FULLSEND_GITLAB_URL` | Override for `GITLAB_API_URL` and `CI_SERVER_URL` (e.g., for self-hosted instances). |
| `FULLSEND_NOTE_TARGET` | Set to `merge_requests` to force MR note targeting when `CI_MERGE_REQUEST_IID` is unavailable (e.g., child pipelines, scheduled jobs). |

`GITLAB_TOKEN` should be configured as a CI/CD variable with the **Masked** and **Protected** flags enabled in your GitLab project or group settings. Unlike GitHub (where tokens are minted at runtime and masked via `::add-mask::`), GitLab uses pre-provisioned tokens and relies on the runner-level masking configuration.

## See Also

- [Getting Started](../getting-started/) — Standard per-repo installation
- [Advanced setup](../infrastructure/advanced-setup.md) — Alternative installation paths, setup flags, custom app sets
- [Mint service administration](../infrastructure/mint-administration.md) — Deploying and managing the token mint
- [Infrastructure Reference](../infrastructure/infrastructure-reference.md) — Token mint, WIF, and secrets deployment details
- [CLI Internals](../dev/cli-internals.md) — Command structure and implementation details
