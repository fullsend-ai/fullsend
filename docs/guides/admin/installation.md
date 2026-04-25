# How to onboard a new organization

This guide walks through installing fullsend in a GitHub organization and enrolling your first repository.

## Prerequisites

- **GitHub organization** with admin access
- **GitHub CLI** (`gh`) authenticated:

  ```bash
  gh auth refresh -s admin:org,repo,workflow
  ```

- **fullsend CLI** — download the latest binary from [GitHub Releases](https://github.com/fullsend-ai/fullsend/releases)

  *Note*: If running from a local clone of the repository use `go run ./cmd/fullsend/main.go <command>`

- **GCP project** with the [Vertex AI API](https://console.cloud.google.com/apis/library/aiplatform.googleapis.com) enabled

Available regions for Claude on Vertex AI include `us-east5`, `europe-west1`, and `asia-southeast1`. Check the [Vertex AI documentation](https://cloud.google.com/vertex-ai/generative-ai/docs/partner-models/use-claude#regions) for the latest list.

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

The installer is interactive. It will open multiple browser windows to create and install a GitHub App for each agent role. Follow the prompts in each window to complete the app setup.

Near the end, the installer opens a browser to create a fine-grained personal access token (dispatch token). When creating it, make sure to grant **Actions: Read and write** permission scoped to the `.fullsend` repository — otherwise the verification step will fail with a 404.

If the installer fails partway through, run `fullsend admin uninstall "$ORG_NAME"` to clean up before retrying. You will need to
refresh the permissions to add `delete_repo`: `gh auth refresh -s delete_repo`.

**With WIF (recommended):**

```bash
export REPO_NAME="<repo-name>"

fullsend admin install "$ORG_NAME" \
  --repo "$REPO_NAME" \
  --gcp-project "$GCP_PROJECT" \
  --gcp-region us-east5 \
  --gcp-wif-provider "$WIF_PROVIDER" \
  --gcp-wif-sa-email "$WIF_SA_EMAIL"
```

**With SA key (legacy):**

```bash
export REPO_NAME="<repo-name>"

fullsend admin install "$ORG_NAME" \
  --repo "$REPO_NAME" \
  --gcp-project "$GCP_PROJECT" \
  --gcp-region us-east5 \
  --gcp-credentials-file sa-key.json
rm sa-key.json
```

### Migrating from SA key to WIF

If you already have fullsend installed with a service account key:

1. Create the WIF resources (steps 1a–1d in Option A above)
2. Re-run the installer with WIF flags (the installer updates secrets in-place):
   ```bash
   fullsend admin install "$ORG_NAME" \
     --repo "$REPO_NAME" \
     --skip-app-setup \
     --gcp-project "$GCP_PROJECT" \
     --gcp-region us-east5 \
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
