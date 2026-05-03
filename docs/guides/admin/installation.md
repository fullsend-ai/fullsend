# How to onboard a new organization

This guide walks through installing fullsend in a GitHub organization and enrolling your first repository.

The installer uses [Playwright browser automation](../../ADRs/0026-playwright-default-installer.md) by default to create GitHub Apps, upload logos, and generate a dispatch token — all in a single command. If you prefer to perform these steps manually, pass `--no-playwright` (see [Manual mode](#manual-mode-no-playwright)).

## Prerequisites

- **GitHub organization** with admin access
- **GitHub account credentials** — the Playwright installer prompts for your GitHub username and password (or reads `GITHUB_USERNAME` / `GITHUB_PASSWORD` env vars) to create an ephemeral browser session. The session is deleted automatically when the installer exits.
- **GitHub CLI** (`gh`) authenticated — no special scopes are needed upfront. The installer runs a preflight check and tells you exactly which scopes are missing before making any changes. When prompted, run the `gh auth refresh -s <scopes>` command it suggests.

  > **Note on scope breadth:** `gh auth` scopes apply to *every* organization your account belongs to — GitHub does not support per-org scoping for classic OAuth tokens. If that is a concern, create a [fine-grained personal access token](https://github.com/settings/tokens?type=beta) scoped to the target organization and export it as `GH_TOKEN` before running the installer.

- **fullsend CLI** — download the latest binary from [GitHub Releases](https://github.com/fullsend-ai/fullsend/releases)

  *Note*: If running from a local clone of the repository use `go run ./cmd/fullsend/main.go <command>`

- **Playwright browsers** — the installer installs Chromium automatically on first run. On headless systems without a display, use `--no-playwright` instead.
- **GCP project** with the [Vertex AI API](https://console.cloud.google.com/apis/library/aiplatform.googleapis.com) enabled

### OAuth scope reference

The table below lists every scope the installer may request and why. You are never asked for all of them at once — the preflight check requests only the scopes needed for the operation you are running.

| Scope | When needed | Why |
|-------|-------------|-----|
| `repo` | install, analyze | Read/write repository contents, manage repo-level secrets |
| `workflow` | install | Create and update GitHub Actions workflow files in `.github/workflows/` |
| `admin:org` | install, uninstall, analyze | Manage organization-level Actions secrets (the dispatch token) |
| `delete_repo` | uninstall | Delete the `.fullsend` config repository |

The default region is `global`. For a list of all available regions, see the [Vertex AI documentation](https://cloud.google.com/vertex-ai/generative-ai/docs/partner-models/use-claude#regions).

## 1. Set up GCP authentication

Fullsend supports two methods for authenticating to Vertex AI. **Workload Identity Federation (WIF) is recommended** — it eliminates long-lived credentials entirely.

### Option A: Workload Identity Federation (recommended)

WIF lets GitHub Actions exchange short-lived OIDC tokens for GCP access tokens. No service account keys are stored.

**1a. Create a service account**

```bash
export GCP_PROJECT="<gcp-project>"
export ORG_NAME="<org-name>"

gcloud iam service-accounts create fullsend-agent \
  --display-name="Fullsend agent inference" \
  --project="$GCP_PROJECT"

gcloud projects add-iam-policy-binding "$GCP_PROJECT" \
  --member="serviceAccount:fullsend-agent@$GCP_PROJECT.iam.gserviceaccount.com" \
  --role="roles/aiplatform.user" \
  --condition=None
```

**1b. Create a Workload Identity Pool and OIDC Provider**

```bash
gcloud iam workload-identity-pools create github-actions \
  --location=global \
  --display-name="GitHub Actions" \
  --project="$GCP_PROJECT"

gcloud iam workload-identity-pools providers create-oidc github \
  --location=global \
  --workload-identity-pool=github-actions \
  --issuer-uri="https://token.actions.githubusercontent.com" \
  --attribute-mapping="google.subject=assertion.sub,attribute.repository_owner=assertion.repository_owner,attribute.repository=assertion.repository" \
  --attribute-condition="assertion.repository_owner == '$ORG_NAME'" \
  --project="$GCP_PROJECT"
```

The `attribute-condition` restricts which GitHub Actions workflows can exchange OIDC tokens for GCP credentials.

- **Org-wide** (`repository_owner`): any repo in the org can authenticate. Simpler to maintain but means a compromised or misconfigured workflow in *any* repo could obtain Vertex AI credentials.
- **Repo-scoped** (`repository`): only the `.fullsend` repo can authenticate. Limits blast radius — recommended for orgs where not all repos are equally trusted.

For repo-scoped access, replace the `attribute-condition` above with:

```bash
--attribute-condition="assertion.repository == '$ORG_NAME/.fullsend'"
```

If you choose repo-scoped access, also update the `--member` in step 1c to match:

```bash
--member="principalSet://iam.googleapis.com/projects/$PROJECT_NUMBER/locations/global/workloadIdentityPools/github-actions/attribute.repository/$ORG_NAME/.fullsend"
```

**1c. Grant the service account impersonation permission**

```bash
export PROJECT_NUMBER=$(gcloud projects describe "$GCP_PROJECT" --format='value(projectNumber)')

gcloud iam service-accounts add-iam-policy-binding \
  "fullsend-agent@$GCP_PROJECT.iam.gserviceaccount.com" \
  --role="roles/iam.workloadIdentityUser" \
  --member="principalSet://iam.googleapis.com/projects/$PROJECT_NUMBER/locations/global/workloadIdentityPools/github-actions/attribute.repository_owner/$ORG_NAME" \
  --project="$GCP_PROJECT"
```

**1d. Note the WIF provider resource name**

```bash
export WIF_PROVIDER="projects/$PROJECT_NUMBER/locations/global/workloadIdentityPools/github-actions/providers/github"
export WIF_SA_EMAIL="fullsend-agent@$GCP_PROJECT.iam.gserviceaccount.com"
```

### Option B: Service account key (legacy)

Create a service account with the `Vertex AI User` role and download its key:

```bash
export GCP_PROJECT="<gcp-project>"
export ORG_NAME="<org-name>"

gcloud iam service-accounts create "$ORG_NAME" \
  --display-name="Fullsend for $ORG_NAME" \
  --project="$GCP_PROJECT"

gcloud projects add-iam-policy-binding "$GCP_PROJECT" \
  --member="serviceAccount:$ORG_NAME@$GCP_PROJECT.iam.gserviceaccount.com" \
  --role="roles/aiplatform.user" \
  --condition=None

gcloud iam service-accounts keys create sa-key.json \
  --iam-account="$ORG_NAME@$GCP_PROJECT.iam.gserviceaccount.com"
```

## 2. Run the installer

The installer creates GitHub Apps (one per agent role), uploads icons, installs each app on the organization, and creates a fine-grained PAT for workflow dispatch — all automatically via Playwright.

Before starting, the installer displays a briefing showing exactly what it will do and waits for confirmation. Pass `--yolo` to skip this prompt.

If the installer fails partway through, run `fullsend admin uninstall "$ORG_NAME"` to clean up before retrying. The uninstall preflight will prompt you to add the `delete_repo` scope if it is missing.

**With WIF (recommended):**

```bash
export REPO_NAME="<repo-name>"

fullsend admin install "$ORG_NAME" \
  --repo "$REPO_NAME" \
  --gcp-project "$GCP_PROJECT" \
  --gcp-region global \
  --gcp-wif-provider "$WIF_PROVIDER" \
  --gcp-wif-sa-email "$WIF_SA_EMAIL"
```

**With SA key (legacy):**

```bash
export REPO_NAME="<repo-name>"

fullsend admin install "$ORG_NAME" \
  --repo "$REPO_NAME" \
  --gcp-project "$GCP_PROJECT" \
  --gcp-region global \
  --gcp-credentials-file sa-key.json
rm sa-key.json
```

### Session reuse

By default the installer creates an ephemeral browser session that is deleted on exit. To reuse a session across multiple runs (useful when iterating on a partial install):

1. Export a session once:

   ```bash
   fullsend admin export-session
   ```

   This writes `~/.config/fullsend/session.json` by default (override with `--session-file <path>`).

2. Pass it to the installer:

   ```bash
   fullsend admin install "$ORG_NAME" \
     --session-file ~/.config/fullsend/session.json \
     --repo "$REPO_NAME" \
     --gcp-project "$GCP_PROJECT" \
     --gcp-region global \
     --gcp-wif-provider "$WIF_PROVIDER" \
     --gcp-wif-sa-email "$WIF_SA_EMAIL"
   ```

The installer does **not** delete a session file provided via `--session-file`. Delete it manually when no longer needed — it contains active GitHub credentials.

### Manual mode (`--no-playwright`)

Pass `--no-playwright` to skip browser automation entirely. The installer opens your system browser for each GitHub App and prompts you to complete each step manually, including PAT creation. When creating the PAT, grant **Actions: Read and write** permission scoped to the `.fullsend` repository — otherwise the verification step will fail with a 404.

### Installer flag reference

| Flag | Default | Description |
|------|---------|-------------|
| `--repo` | *(none)* | Repositories to enroll (repeatable) |
| `--agents` | `fullsend,triage,coder,review` | Comma-separated agent roles |
| `--gcp-project` | *(none)* | GCP project for Vertex AI |
| `--gcp-region` | *(none)* | GCP region (required with `--gcp-project`) |
| `--gcp-wif-provider` | *(none)* | WIF provider resource name |
| `--gcp-wif-sa-email` | *(none)* | Service account email for WIF |
| `--gcp-credentials-file` | *(none)* | Path to SA key JSON (mutually exclusive with WIF) |
| `--session-file` | *(none)* | Pre-existing Playwright session (skips login) |
| `--no-playwright` | `false` | Fall back to manual browser flow |
| `--skip-app-setup` | `false` | Skip GitHub App creation (reuse existing apps) |
| `--yolo` | `false` | Skip the briefing confirmation prompt |
| `--dry-run` | `false` | Preview changes without making them |
| `--vendor-fullsend-binary` | `false` | Cross-compile and upload the CLI binary into `.fullsend/bin/` |

### Migrating from SA key to WIF

If you already have fullsend installed with a service account key:

1. Create the WIF resources (steps 1a–1d in Option A above)
2. Re-run the installer with WIF flags (the installer updates secrets in-place):
   ```bash
   fullsend admin install "$ORG_NAME" \
     --repo "$REPO_NAME" \
     --skip-app-setup \
     --gcp-project "$GCP_PROJECT" \
     --gcp-region global \
     --gcp-wif-provider "$WIF_PROVIDER" \
     --gcp-wif-sa-email "$WIF_SA_EMAIL"
   ```
3. Verify a workflow run succeeds with WIF auth (check for "Authenticated using Workload Identity Federation" in the auth step output)
4. Delete the old SA key: `gcloud iam service-accounts keys delete <KEY_ID> --iam-account=...`
5. Remove the `FULLSEND_GCP_SA_KEY_JSON` secret from the `.fullsend` repo settings

**Note**: the `--repo` flag can be repeated to onboard multiple repositories.

## 3. Merge enrollment PRs

After install completes, the installer dispatches a workflow that creates an enrollment PR in each repo passed via `--repo`. These PRs add a shim workflow (`.github/workflows/fullsend.yaml`) that wires events to the agent pipeline.

Review and merge each enrollment PR to complete enrollment.

## 4. Test the pipeline

Once a repo is enrolled (enrollment PR merged):

1. Create an issue in the enrolled repo
2. The triage agent picks it up automatically — check the Actions tab in both the target repo and `.fullsend` for workflow run logs
