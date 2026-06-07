# E2E GCP Setup

Operator guide for the dedicated GCP project and GitHub secrets used by CI e2e
tests. Follows a **verify-then-create** pattern: inspect existing state first,
create or update only what is missing or wrong.

The **long-term goal** is secretless e2e (no per-repo secrets/variables for GCP
or sessions). This guide covers infrastructure for the **current** workflow,
which still requires several secrets — see [ADR 0043](../../ADRs/0043-e2e-wif-shim-and-pr-authorization-gate.md)
for what is delivered vs. outstanding.

For architecture context see ADR 0043. For contributor CI behavior see
[E2E testing](../dev/e2e-testing.md).

## Canonical resource names

Provision and inspect resources using these names so they match **`e2e.yml`
workflow defaults** when repository secrets are unset:

| Resource | Canonical name |
|----------|----------------|
| GCP project ID | `it-gcp-konflux-e2e-fullsend` (workflow default when secret unset) |
| GCP project number | `208332380190` (workflow default when secret unset) |
| Service account ID | `fullsend-e2e` |
| Service account email | `fullsend-e2e@${PROJECT_ID}.iam.gserviceaccount.com` |
| WIF pool ID | `fullsend-e2e-pool` |
| WIF provider ID | `github-oidc` |
| WIF provider resource | `projects/208332380190/locations/global/workloadIdentityPools/fullsend-e2e-pool/providers/github-oidc` |

Override via `E2E_GCP_PROJECT_ID`, `E2E_GCP_SERVICE_ACCOUNT`, or
`E2E_GCP_WIF_PROVIDER` secrets only when pointing at different infrastructure.
When overriding the project, set all three secrets — the workflow does not
derive the project number or WIF provider path at runtime.

## Quick health check

Run these from a machine with `gcloud` and `gh` authenticated.

```bash
PROJECT_ID="it-gcp-konflux-e2e-fullsend"
PROJECT_NUMBER="$(gcloud projects describe "${PROJECT_ID}" --format='value(projectNumber)')"
SA_EMAIL="fullsend-e2e@${PROJECT_ID}.iam.gserviceaccount.com"
POOL_ID="fullsend-e2e-pool"
PROVIDER_ID="github-oidc"

gcloud projects describe "${PROJECT_ID}"
gcloud services list --enabled --project="${PROJECT_ID}" | grep -E 'iam|sts|aiplatform|secretmanager|cloudfunctions|run'
gcloud iam service-accounts describe "${SA_EMAIL}" --project="${PROJECT_ID}"
gcloud iam workload-identity-pools describe "${POOL_ID}" --location=global --project="${PROJECT_ID}"
gcloud iam workload-identity-pools providers describe "${PROVIDER_ID}" \
  --workload-identity-pool="${POOL_ID}" --location=global --project="${PROJECT_ID}"
gh secret list --repo fullsend-ai/fullsend | grep E2E_
gh label list --repo fullsend-ai/fullsend | grep ok-to-test
```

All commands should succeed and show expected values described in the sections
below.

## 1. Project and APIs

### Check

| Item | Command | Expected when OK |
|------|---------|----------------|
| Project exists | `gcloud projects describe PROJECT_ID` | `lifecycleState: ACTIVE` |
| Project number | `gcloud projects describe PROJECT_ID --format='value(projectNumber)'` | Numeric ID for WIF resource names |
| APIs enabled | `gcloud services list --enabled --project=PROJECT_ID` | Required APIs listed below |

Required APIs:

- `iam.googleapis.com`
- `iamcredentials.googleapis.com`
- `sts.googleapis.com`
- `cloudresourcemanager.googleapis.com`
- `aiplatform.googleapis.com`
- `secretmanager.googleapis.com`
- `cloudfunctions.googleapis.com`
- `run.googleapis.com`
- `cloudbuild.googleapis.com`
- `artifactregistry.googleapis.com`

### Create / enable if missing

```bash
gcloud projects create "${PROJECT_ID}" --name="Fullsend E2E"
gcloud services enable iam.googleapis.com iamcredentials.googleapis.com sts.googleapis.com \
  cloudresourcemanager.googleapis.com aiplatform.googleapis.com secretmanager.googleapis.com \
  cloudfunctions.googleapis.com run.googleapis.com cloudbuild.googleapis.com \
  artifactregistry.googleapis.com --project="${PROJECT_ID}"
```

## 2. E2E workflow service account

CI impersonates this account via WIF (`E2E_GCP_SERVICE_ACCOUNT`).

### Check

```bash
gcloud iam service-accounts describe "${SA_EMAIL}" --project="${PROJECT_ID}"

gcloud projects get-iam-policy "${PROJECT_ID}" \
  --flatten='bindings[].members' \
  --filter="bindings.members:serviceAccount:${SA_EMAIL}" \
  --format='table(bindings.role)'
```

### Roles

Grant only roles that are absent from the policy output.

**Required now (inference during admin install tests):**

| Role | Purpose |
|------|---------|
| `roles/aiplatform.user` | Vertex / Agent Platform API calls |

**Required later ([#817](https://github.com/fullsend-ai/fullsend/issues/817) mint/install tests):**

| Role | Purpose |
|------|---------|
| `roles/iam.workloadIdentityPoolAdmin` | WIF pool/provider provisioning |
| `roles/secretmanager.admin` | PEM secret storage |
| `roles/cloudfunctions.admin` | Mint Cloud Function deploy |
| `roles/run.admin` | Cloud Run (2nd gen functions) |
| `roles/iam.serviceAccountAdmin` | SA creation during install |
| `roles/iam.serviceAccountUser` | SA impersonation during install |
| `roles/resourcemanager.projectIamAdmin` | IAM bindings during install |

### Create if missing

```bash
gcloud iam service-accounts create fullsend-e2e \
  --project="${PROJECT_ID}" \
  --display-name="Fullsend E2E CI"

gcloud projects add-iam-policy-binding "${PROJECT_ID}" \
  --member="serviceAccount:${SA_EMAIL}" \
  --role="roles/aiplatform.user"
```

Repeat `add-iam-policy-binding` for each additional role when #817 tests land.

## 3. WIF pool and provider

E2E CI uses a **repo-scoped** provider (distinct from org mint/inference pools
created by `fullsend admin install`). See
[infrastructure-reference.md](infrastructure-reference.md).

### Check

```bash
PROJECT_NUMBER="$(gcloud projects describe "${PROJECT_ID}" --format='value(projectNumber)')"

gcloud iam workload-identity-pools describe "${POOL_ID}" \
  --location=global --project="${PROJECT_ID}"

gcloud iam workload-identity-pools providers describe "${PROVIDER_ID}" \
  --workload-identity-pool="${POOL_ID}" --location=global --project="${PROJECT_ID}"

gcloud iam service-accounts get-iam-policy "${SA_EMAIL}" --project="${PROJECT_ID}"
```

**Expected provider attribute condition:**

```
assertion.repository == 'fullsend-ai/fullsend' &&
assertion.job_workflow_ref.startsWith('fullsend-ai/fullsend/.github/workflows/e2e.yml@')
```

**Expected OIDC issuer:** `https://token.actions.githubusercontent.com`

**Expected IAM binding on the service account:** principal set for the WIF pool
with `roles/iam.workloadIdentityUser`.

Provider resource name (matches workflow default when secret is unset):

```
projects/208332380190/locations/global/workloadIdentityPools/fullsend-e2e-pool/providers/github-oidc
```

Look up `${PROJECT_NUMBER}` with `gcloud projects describe "${PROJECT_ID}" --format='value(projectNumber)'`
when provisioning or rotating infrastructure. The workflow hardcodes this value
for the canonical project because WIF auth cannot bootstrap it without prior
GCP credentials.

### Create if missing

```bash
gcloud iam workload-identity-pools create "${POOL_ID}" \
  --project="${PROJECT_ID}" \
  --location=global \
  --display-name="Fullsend E2E GitHub OIDC Pool"

gcloud iam workload-identity-pools providers create-oidc "${PROVIDER_ID}" \
  --project="${PROJECT_ID}" \
  --location=global \
  --workload-identity-pool="${POOL_ID}" \
  --display-name="GitHub OIDC for E2E" \
  --issuer-uri="https://token.actions.githubusercontent.com" \
  --attribute-mapping="google.subject=assertion.sub,attribute.repository=assertion.repository,attribute.job_workflow_ref=assertion.job_workflow_ref" \
  --attribute-condition="assertion.repository == 'fullsend-ai/fullsend' && assertion.job_workflow_ref.startsWith('fullsend-ai/fullsend/.github/workflows/e2e.yml@')"

gcloud iam service-accounts add-iam-policy-binding "${SA_EMAIL}" \
  --project="${PROJECT_ID}" \
  --role="roles/iam.workloadIdentityUser" \
  --member="principalSet://iam.googleapis.com/projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${POOL_ID}/attribute.repository/fullsend-ai/fullsend"
```

### Update if attribute condition is wrong

Do not delete an existing pool — update the provider:

```bash
gcloud iam workload-identity-pools providers update-oidc "${PROVIDER_ID}" \
  --project="${PROJECT_ID}" \
  --location=global \
  --workload-identity-pool="${POOL_ID}" \
  --attribute-condition="assertion.repository == 'fullsend-ai/fullsend' && assertion.job_workflow_ref.startsWith('fullsend-ai/fullsend/.github/workflows/e2e.yml@')"
```

## 4. GitHub repository secrets and labels

Secret **values** cannot be read back from GitHub. Verify names exist, then
confirm values match GCP inspect output when rotating.

### Check

```bash
gh secret list --repo fullsend-ai/fullsend | grep '^E2E_'
gh label list --repo fullsend-ai/fullsend | grep ok-to-test
```

### Secrets and variables

| Name | Required? | Value source |
|------|-----------|----------------|
| `E2E_GCP_WIF_PROVIDER` | No | Defaults to canonical provider resource above; set to override |
| `E2E_GCP_SERVICE_ACCOUNT` | No | Defaults to `${SA_EMAIL}`; set to override |
| `E2E_GCP_PROJECT_ID` | No | Defaults to `it-gcp-konflux-e2e-fullsend`; set to override |
| `E2E_GITHUB_SESSION` | **Yes** | Base64 Playwright session ([ADR 0010](../../ADRs/0010-stored-session-for-e2e-browser-auth.md)) |
| `E2E_GITHUB_PASSWORD` | **Yes** | Test account password |
| `E2E_GITHUB_TOTP_SECRET` | **Yes** | Test account TOTP secret ([ADR 0039](../../ADRs/0039-totp-automation-for-e2e-2fa.md)) |
| `E2E_MINT_URL` | **Yes** | Shared mint URL for install tests |

Full secretless e2e (eliminating the required rows) is follow-up work tracked
under [ADR 0043](../../ADRs/0043-e2e-wif-shim-and-pr-authorization-gate.md).

### Set or rotate

Optional — omit all three to use workflow defaults from [Canonical resource names](#canonical-resource-names):

```bash
# gh secret set E2E_GCP_WIF_PROVIDER --repo fullsend-ai/fullsend \
#   --body "projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${POOL_ID}/providers/${PROVIDER_ID}"
# gh secret set E2E_GCP_SERVICE_ACCOUNT --repo fullsend-ai/fullsend --body "${SA_EMAIL}"
# gh secret set E2E_GCP_PROJECT_ID --repo fullsend-ai/fullsend --body "${PROJECT_ID}"
```

### Create ok-to-test label if missing

```bash
gh label create ok-to-test --repo fullsend-ai/fullsend \
  --color fbca04 \
  --description "Allow e2e CI to run after maintainer review (must be re-applied after each push)"
```

## 5. End-to-end verification

1. Run **E2E Tests** via `workflow_dispatch` on `main`.
2. Confirm the **Authenticate to GCP** step succeeds.
3. Open a same-repo PR from a member account — **E2E Shim** should call the gate
   and run tests when authorized.

### Troubleshooting

| Symptom | Inspect |
|---------|---------|
| `google-github-actions/auth` permission denied | Provider attribute condition and SA `workloadIdentityUser` binding (section 3) |
| WIF provider not found | `E2E_GCP_WIF_PROVIDER` secret vs provider resource name |
| E2e skipped — secrets unavailable | Fork PR or missing `E2E_GITHUB_SESSION` ([e2e-testing.md](../dev/e2e-testing.md)) |
| Gate comment not posted | Caller job needs `pull-requests: write` in `e2e_shim.yml` |
| Tests skip after label added | Label must be applied **after** latest push; stale labels are removed |

## Comparison with install-time WIF

| Use case | Pool scope | Provisioned by |
|----------|------------|----------------|
| Token mint | Org allowlist | `fullsend mint deploy` |
| Inference (agents) | Org or repo | `fullsend inference provision` |
| E2E CI | `fullsend-ai/fullsend` repo + `e2e.yml@main` only | Manual (this guide) |
