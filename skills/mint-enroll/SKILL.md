---
name: mint-enroll
description: >
  SRE runbook for enrolling new GitHub orgs or repos into the fullsend token
  mint service. Use when onboarding a new org, adding a per-repo WIF provider,
  copying shared app PEM secrets, or updating mint Cloud Function env vars.
allowed-tools: Bash
---

# Mint Service Enrollment

Enroll a new GitHub org or per-repo into the fullsend token mint. The mint is
a stateless GCP Cloud Function that exchanges GitHub OIDC JWTs for scoped
GitHub App installation tokens.

Follow these steps in order. Do not skip steps.

**Always ask the operator for the GCP project ID.** Do not infer it from
`gcloud config get-value project` — the local config may point at the
wrong project.

## Setup

Collect deployment-specific values. Never hardcode project names, IDs, or
URLs — resolve them from the deployed resources.

The operator needs these IAM roles on the GCP project: Secret Manager Admin,
Workload Identity Pool Admin, Cloud Functions Developer, Cloud Run Admin.

```bash
set -o pipefail
GCP_PROJECT="<your-gcp-project-id>"
MINT_FUNCTION="fullsend-mint"
MINT_REGION="us-central1"
WIF_POOL="fullsend-pool"
SOURCE_ORG="fullsend-ai"
SA_EMAIL="fullsend-mint@${GCP_PROJECT}.iam.gserviceaccount.com"

GCP_PROJECT_NUMBER=$(gcloud projects describe "${GCP_PROJECT}" --format="value(projectNumber)") \
  || { echo "ERROR: failed to resolve project number for ${GCP_PROJECT}" >&2; exit 1; }
MINT_URL=$(gcloud functions describe "${MINT_FUNCTION}" \
  --project="${GCP_PROJECT}" --region="${MINT_REGION}" --gen2 \
  --format="value(serviceConfig.uri)") \
  || { echo "ERROR: failed to resolve mint URL" >&2; exit 1; }
MINT_SERVICE_FULL=$(gcloud functions describe "${MINT_FUNCTION}" \
  --project="${GCP_PROJECT}" --region="${MINT_REGION}" --gen2 \
  --format="value(serviceConfig.service)") \
  || { echo "ERROR: failed to resolve mint service" >&2; exit 1; }
MINT_SERVICE=$(basename "${MINT_SERVICE_FULL}")
WIF_POOL_RESOURCE=$(gcloud iam workload-identity-pools describe "${WIF_POOL}" \
  --project="${GCP_PROJECT}" --location=global \
  --format="value(name)") \
  || { echo "ERROR: WIF pool '${WIF_POOL}' not found in ${GCP_PROJECT}" >&2; exit 1; }
WIF_POOL_LOCATION=$(echo "${WIF_POOL_RESOURCE}" | sed 's|.*/locations/\([^/]*\)/.*|\1|')
if [ -z "${WIF_POOL_LOCATION}" ]; then
  echo "ERROR: could not resolve WIF pool location from ${WIF_POOL_RESOURCE}" >&2; exit 1
fi
```

## Constraints

- **NEVER use `--set-env-vars`** — always `--update-env-vars` (set replaces ALL env vars).
- **NEVER remove entries** — only add/merge. Removal is a separate deliberate operation (see Rollback).
- **No concurrent enrollment** — two operators reading and writing env vars simultaneously will race. Coordinate enrollment operations serially.
- **Always check before create** — read current state, show diff, then apply.
- **Dual audiences on per-repo WIF providers** — always include both `fullsend-mint` AND the full IAM URL. Exception: orgs using external inference (e.g., agentshed) only need `fullsend-mint`.
- **PEM secret naming** — `fullsend-{org}--{role}-app-pem` (double-dash separator).
- **WIF provider ID** — `gh-{owner}-{repo}` (lowercase, max 32 chars, no trailing hyphen). Non-alphanumeric characters are replaced with hyphens. Long names are truncated at 32 chars with trailing hyphens stripped. Consecutive hyphens are NOT collapsed (matches Go code) — verify the generated ID is unique.
- **Use `gcloud run services update` for env var changes** — `gcloud functions deploy --update-env-vars` triggers a full rebuild. Update the underlying Cloud Run service (`MINT_SERVICE`) directly to apply env vars without rebuilding.
- **Use `^|^` delimiter for `--update-env-vars`** — ROLE_APP_IDS is JSON containing commas, which conflicts with gcloud's default comma separator.

## Shared App Model

The fullsend-ai org maintains public GitHub Apps shared across orgs.

| Role | App Slug | Notes |
|------|----------|-------|
| fullsend | fullsend-ai-fullsend | Dispatch/admin. Per-org only — excluded from per-repo installs. |
| triage | fullsend-ai-triage | |
| coder | fullsend-ai-coder | `fix` role shares this app and PEM but has distinct token permissions (no `checks:read`). No separate enrollment needed. |
| review | fullsend-ai-review | |
| retro | fullsend-ai-retro | |
| prioritize | fullsend-ai-prioritize | |

PEM keys are tied to the app, not the org. Enrolling a new org copies PEMs
from a source org (e.g., `fullsend-ai`).

Apps must be installed on the target org before the mint can produce tokens.
An org admin installs via `https://github.com/apps/{slug}/installations/new`
or by running `fullsend admin install`.

### 1. Triage

Determine enrollment type and set variables. Choose one — per-org or per-repo:

```bash
# Per-org (set ORG only, leave REPO unset)
ORG="<github-org>"
unset REPO
ROLES="fullsend,triage,coder,review,retro,prioritize"

# Per-repo (set both ORG and REPO)
ORG="<github-org>"
REPO="<repo-name>"
ROLES="triage,coder,review,retro,prioritize"
```

Validate and normalize inputs:

```bash
ORG=$(echo "${ORG}" | tr '[:upper:]' '[:lower:]')
if [ -n "${REPO}" ]; then
  REPO=$(echo "${REPO}" | tr '[:upper:]' '[:lower:]')
fi

# GitHub org names: alphanumeric and hyphens only, no consecutive hyphens,
# no leading/trailing hyphens, max 39 chars (matches Go githubOrgPattern)
if [ -z "${ORG}" ] || [[ "${ORG}" =~ $'\n' ]] \
  || ! [[ "${ORG}" =~ ^[a-z0-9]([a-z0-9-]{0,37}[a-z0-9])?$ ]] \
  || [[ "${ORG}" == *--* ]]; then
  echo "ERROR: ORG must be a valid GitHub org name (alphanumeric/hyphens, no consecutive hyphens, max 39 chars)" >&2; exit 1
fi
if [ -n "${REPO}" ]; then
  # GitHub repo names: alphanumeric, hyphens, dots, underscores; no .git suffix
  if [[ "${REPO}" =~ $'\n' ]] \
    || ! [[ "${REPO}" =~ ^[a-z0-9._][a-z0-9._-]{0,99}$ ]] \
    || [[ "${REPO}" == "." ]] || [[ "${REPO}" == ".." ]] \
    || [[ "${REPO}" == *..* ]] \
    || [[ "${REPO}" == *.git ]]; then
    echo "ERROR: REPO must be a valid GitHub repo name" >&2; exit 1
  fi
  if [ "${REPO}" = ".fullsend" ]; then
    echo "ERROR: .fullsend repos use the shared per-org provider — enroll the org instead" >&2; exit 1
  fi
fi

# Validate ROLES against allowlist
VALID_ROLES="fullsend,triage,coder,review,retro,prioritize"
for ROLE in $(echo "${ROLES}" | tr ',' ' '); do
  if ! echo ",${VALID_ROLES}," | grep -Fq ",${ROLE},"; then
    echo "ERROR: role '${ROLE}' is not in allowlist: ${VALID_ROLES}" >&2; exit 1
  fi
done
```

Pre-flight check for per-repo WIF provider ID collision (before any
mutating steps):

```bash
if [ -n "${REPO}" ]; then
  PROVIDER_ID="gh-${ORG}-${REPO}"
  PROVIDER_ID=$(echo "${PROVIDER_ID}" | tr '[:upper:]' '[:lower:]' | tr -c 'a-z0-9-' '-' | cut -c1-32 | sed 's/-*$//')
  echo "Computed WIF provider ID: ${PROVIDER_ID} (${#PROVIDER_ID} chars)"
  PREFLIGHT_COND=$(gcloud iam workload-identity-pools providers describe "${PROVIDER_ID}" \
    --project="${GCP_PROJECT}" \
    --workload-identity-pool="${WIF_POOL}" \
    --location="${WIF_POOL_LOCATION}" \
    --format="value(attributeCondition)" 2>/dev/null) || true
  if [ -n "${PREFLIGHT_COND}" ] && [ "${PREFLIGHT_COND}" != "assertion.repository == '${ORG}/${REPO}'" ]; then
    echo "ERROR: Provider ID collision — ${PROVIDER_ID} belongs to a different repo:" >&2
    echo "  Existing: ${PREFLIGHT_COND}" >&2
    echo "Aborting before any mutations." >&2
    exit 1
  fi
fi
```

### 2. Verify prerequisites

Confirm correct GCP project and active credentials. If `gcloud config
get-value project` does not match `GCP_PROJECT`, the commands still
target the right project via explicit `--project` flags — but run
`gcloud config set project "${GCP_PROJECT}"` to avoid confusion.

```bash
gcloud config get-value project
gcloud auth list --filter=status:ACTIVE --format="value(account)"
```

### 3. Audit current state

Run ALL of these before making any changes.

```bash
# ALLOWED_ORGS
gcloud run services describe "${MINT_SERVICE}" \
  --project="${GCP_PROJECT}" --region="${MINT_REGION}" \
  --format="value(spec.template.spec.containers[0].env.filter(name=ALLOWED_ORGS).value)"

# ROLE_APP_IDS (JSON)
gcloud run services describe "${MINT_SERVICE}" \
  --project="${GCP_PROJECT}" --region="${MINT_REGION}" \
  --format="value(spec.template.spec.containers[0].env.filter(name=ROLE_APP_IDS).value)"

# PER_REPO_WIF_REPOS (per-repo only)
gcloud run services describe "${MINT_SERVICE}" \
  --project="${GCP_PROJECT}" --region="${MINT_REGION}" \
  --format="value(spec.template.spec.containers[0].env.filter(name=PER_REPO_WIF_REPOS).value)"

# ALLOWED_ROLES
gcloud run services describe "${MINT_SERVICE}" \
  --project="${GCP_PROJECT}" --region="${MINT_REGION}" \
  --format="value(spec.template.spec.containers[0].env.filter(name=ALLOWED_ROLES).value)"

# ALLOWED_WORKFLOW_FILES (must not be empty)
gcloud run services describe "${MINT_SERVICE}" \
  --project="${GCP_PROJECT}" --region="${MINT_REGION}" \
  --format="value(spec.template.spec.containers[0].env.filter(name=ALLOWED_WORKFLOW_FILES).value)"

# PEM secrets for the org
gcloud secrets list --project="${GCP_PROJECT}" \
  --filter="name:fullsend-${ORG}--" \
  --format="table(name,createTime)"

# WIF providers
gcloud iam workload-identity-pools providers list \
  --project="${GCP_PROJECT}" \
  --workload-identity-pool="${WIF_POOL}" \
  --location="${WIF_POOL_LOCATION}" \
  --format="table(name.basename(),state,disabled)"
```

### 4. Copy PEM secrets

For shared apps, copy the PEM from the source org. Pipes directly to
avoid holding PEM material in shell variables.

```bash
set -o pipefail
for ROLE in $(echo "${ROLES}" | tr ',' ' '); do
  SECRET_ID="fullsend-${ORG}--${ROLE}-app-pem"

  if gcloud secrets describe "${SECRET_ID}" --project="${GCP_PROJECT}" 2>/dev/null; then
    VERSION_COUNT=$(gcloud secrets versions list "${SECRET_ID}" \
      --project="${GCP_PROJECT}" --filter="state=ENABLED" \
      --format="value(name)" 2>/dev/null | wc -l | tr -d ' ')
    if [ "${VERSION_COUNT}" -gt 0 ]; then
      echo "SKIP: ${SECRET_ID} already exists with ${VERSION_COUNT} active version(s)"
      continue
    fi
    echo "WARN: ${SECRET_ID} exists but has no active versions — re-adding"
  fi

  SOURCE_SECRET="fullsend-${SOURCE_ORG}--${ROLE}-app-pem"

  if ! gcloud secrets describe "${SOURCE_SECRET}" --project="${GCP_PROJECT}" >/dev/null 2>&1; then
    echo "ERROR: source secret ${SOURCE_SECRET} not found" >&2
    exit 1
  fi

  if ! gcloud secrets describe "${SECRET_ID}" --project="${GCP_PROJECT}" 2>/dev/null; then
    gcloud secrets create "${SECRET_ID}" \
      --project="${GCP_PROJECT}" --replication-policy=automatic \
      || { echo "ERROR: failed to create secret ${SECRET_ID}" >&2; exit 1; }
  fi

  SOURCE_VERSION_STATE=$(gcloud secrets versions describe latest \
    --secret="${SOURCE_SECRET}" --project="${GCP_PROJECT}" \
    --format="value(state)" 2>/dev/null)
  if [ "${SOURCE_VERSION_STATE}" != "ENABLED" ]; then
    echo "ERROR: source secret ${SOURCE_SECRET} latest version is not ENABLED (got: ${SOURCE_VERSION_STATE:-empty})" >&2
    exit 1
  fi

  gcloud secrets versions access latest \
    --secret="${SOURCE_SECRET}" --project="${GCP_PROJECT}" \
    | gcloud secrets versions add "${SECRET_ID}" \
    --project="${GCP_PROJECT}" --data-file=- \
    || { echo "ERROR: failed to copy PEM from ${SOURCE_SECRET} to ${SECRET_ID}" >&2; exit 1; }

  gcloud secrets add-iam-policy-binding "${SECRET_ID}" \
    --project="${GCP_PROJECT}" \
    --member="serviceAccount:${SA_EMAIL}" \
    --role="roles/secretmanager.secretAccessor" \
    || { echo "ERROR: failed to bind IAM policy on ${SECRET_ID}" >&2; exit 1; }

  echo "DONE: ${SECRET_ID}"
done

# fix role reuses coder app — create fix PEM as a copy of coder PEM
if echo ",${ROLES}," | grep -Fq ",coder,"; then
  FIX_SECRET="fullsend-${ORG}--fix-app-pem"
  CODER_SECRET="fullsend-${ORG}--coder-app-pem"
  if gcloud secrets describe "${FIX_SECRET}" --project="${GCP_PROJECT}" 2>/dev/null; then
    FIX_VERSION_COUNT=$(gcloud secrets versions list "${FIX_SECRET}" \
      --project="${GCP_PROJECT}" --filter="state=ENABLED" \
      --format="value(name)" 2>/dev/null | wc -l | tr -d ' ')
    if [ "${FIX_VERSION_COUNT}" -gt 0 ]; then
      echo "SKIP: ${FIX_SECRET} already exists"
    else
      echo "WARN: ${FIX_SECRET} exists but has no active versions — re-adding from coder"
      gcloud secrets versions access latest \
        --secret="${CODER_SECRET}" --project="${GCP_PROJECT}" \
        | gcloud secrets versions add "${FIX_SECRET}" \
        --project="${GCP_PROJECT}" --data-file=- \
        || { echo "ERROR: failed to copy PEM to ${FIX_SECRET}" >&2; exit 1; }
    fi
  else
    gcloud secrets create "${FIX_SECRET}" \
      --project="${GCP_PROJECT}" --replication-policy=automatic \
      || { echo "ERROR: failed to create secret ${FIX_SECRET}" >&2; exit 1; }
    gcloud secrets versions access latest \
      --secret="${CODER_SECRET}" --project="${GCP_PROJECT}" \
      | gcloud secrets versions add "${FIX_SECRET}" \
      --project="${GCP_PROJECT}" --data-file=- \
      || { echo "ERROR: failed to copy PEM to ${FIX_SECRET}" >&2; exit 1; }
  fi
  gcloud secrets add-iam-policy-binding "${FIX_SECRET}" \
    --project="${GCP_PROJECT}" \
    --member="serviceAccount:${SA_EMAIL}" \
    --role="roles/secretmanager.secretAccessor" \
    || { echo "ERROR: failed to bind IAM policy on ${FIX_SECRET}" >&2; exit 1; }
  echo "DONE: ${FIX_SECRET} (fix role, copied from coder)"
fi
```

### 5. Update mint env vars

Read current values, merge new entries, apply via `gcloud run services update`.

```bash
set -o pipefail
CURRENT_ALLOWED_ORGS=$(gcloud run services describe "${MINT_SERVICE}" \
  --project="${GCP_PROJECT}" --region="${MINT_REGION}" \
  --format="value(spec.template.spec.containers[0].env.filter(name=ALLOWED_ORGS).value)") \
  || { echo "ERROR: failed to read ALLOWED_ORGS" >&2; exit 1; }

CURRENT_ROLE_APP_IDS=$(gcloud run services describe "${MINT_SERVICE}" \
  --project="${GCP_PROJECT}" --region="${MINT_REGION}" \
  --format="value(spec.template.spec.containers[0].env.filter(name=ROLE_APP_IDS).value)") \
  || { echo "ERROR: failed to read ROLE_APP_IDS" >&2; exit 1; }

# Check if org already in ALLOWED_ORGS
if echo ",${CURRENT_ALLOWED_ORGS}," | grep -Fq ",${ORG},"; then
  echo "SKIP: ${ORG} already in ALLOWED_ORGS"
  NEW_ALLOWED_ORGS="${CURRENT_ALLOWED_ORGS}"
elif [ -z "${CURRENT_ALLOWED_ORGS}" ]; then
  NEW_ALLOWED_ORGS="${ORG}"
else
  NEW_ALLOWED_ORGS="${CURRENT_ALLOWED_ORGS},${ORG}"
fi

# Merge new entries into ROLE_APP_IDS (pin lookup to SOURCE_ORG)
NEW_ROLE_APP_IDS=$(echo "${CURRENT_ROLE_APP_IDS}" | \
  ORG="${ORG}" ROLES="${ROLES}" SOURCE_ORG="${SOURCE_ORG}" python3 -c "
import json, sys, os
raw = sys.stdin.read().strip()
try:
    data = json.loads(raw) if raw else {}
except json.JSONDecodeError as e:
    print(f'FATAL: ROLE_APP_IDS is not valid JSON: {e}', file=sys.stderr)
    sys.exit(1)
org = os.environ['ORG'].lower()
roles_csv = os.environ['ROLES']
source_org = os.environ['SOURCE_ORG'].lower()
for role in (r.strip() for r in roles_csv.split(',')):
    key = f'{org}/{role}'
    source_key = f'{source_org}/{role}'
    if key in data:
        print(f'SKIP: {key} already exists', file=sys.stderr)
    elif source_key in data:
        data[key] = data[source_key]
        print(f'ADDING: {key} = {data[source_key]}', file=sys.stderr)
    else:
        print(f'ERROR: no app ID found for {source_key}', file=sys.stderr)
        sys.exit(1)
# fix role reuses coder app ID — auto-derive if coder was enrolled
fix_key = f'{org}/fix'
coder_key = f'{org}/coder'
if fix_key not in data and coder_key in data:
    data[fix_key] = data[coder_key]
    print(f'ADDING: {fix_key} = {data[coder_key]} (derived from coder)', file=sys.stderr)
print(json.dumps(data, sort_keys=True))
") || { echo "ERROR: failed to merge ROLE_APP_IDS — see above" >&2; exit 1; }

# Validate merged JSON
echo "${NEW_ROLE_APP_IDS}" | python3 -c "import json,sys; json.loads(sys.stdin.read())" \
  || { echo "ERROR: merged ROLE_APP_IDS is not valid JSON — aborting" >&2; exit 1; }

# Derive ALLOWED_ROLES
NEW_ALLOWED_ROLES=$(echo "${NEW_ROLE_APP_IDS}" | python3 -c "
import json, sys
data = json.loads(sys.stdin.read())
roles = sorted(set(k.split('/', 1)[1] for k in data if '/' in k))
print(','.join(roles))
") || { echo "ERROR: failed to derive ALLOWED_ROLES" >&2; exit 1; }
```

For per-repo, also add to PER_REPO_WIF_REPOS:

```bash
if [ -n "${REPO}" ]; then
  CURRENT_PER_REPO=$(gcloud run services describe "${MINT_SERVICE}" \
    --project="${GCP_PROJECT}" --region="${MINT_REGION}" \
    --format="value(spec.template.spec.containers[0].env.filter(name=PER_REPO_WIF_REPOS).value)") \
    || { echo "ERROR: failed to read PER_REPO_WIF_REPOS" >&2; exit 1; }

  REPO_FULL="${ORG}/${REPO}"
  if echo ",${CURRENT_PER_REPO}," | grep -Fq ",${REPO_FULL},"; then
    echo "SKIP: ${REPO_FULL} already in PER_REPO_WIF_REPOS"
    NEW_PER_REPO="${CURRENT_PER_REPO}"
  elif [ -z "${CURRENT_PER_REPO}" ]; then
    NEW_PER_REPO="${REPO_FULL}"
  else
    NEW_PER_REPO="${CURRENT_PER_REPO},${REPO_FULL}"
  fi
fi
```

Show diff before applying:

```bash
CURRENT_ALLOWED_ROLES=$(gcloud run services describe "${MINT_SERVICE}" \
  --project="${GCP_PROJECT}" --region="${MINT_REGION}" \
  --format="value(spec.template.spec.containers[0].env.filter(name=ALLOWED_ROLES).value)" 2>/dev/null)

echo "=== ALLOWED_ORGS ==="
echo "  OLD: ${CURRENT_ALLOWED_ORGS}"
echo "  NEW: ${NEW_ALLOWED_ORGS}"
echo "=== ROLE_APP_IDS ==="
echo "  OLD: ${CURRENT_ROLE_APP_IDS}"
echo "  NEW: ${NEW_ROLE_APP_IDS}"
echo "=== ALLOWED_ROLES ==="
echo "  OLD: ${CURRENT_ALLOWED_ROLES}"
echo "  NEW: ${NEW_ALLOWED_ROLES}"
if [ -n "${REPO}" ]; then
  echo "=== PER_REPO_WIF_REPOS ==="
  echo "  OLD: ${CURRENT_PER_REPO}"
  echo "  NEW: ${NEW_PER_REPO}"
fi
```

Abort if any computed value is empty — this prevents wiping existing
configuration:

```bash
if [ -z "${NEW_ALLOWED_ORGS}" ]; then
  echo "ERROR: NEW_ALLOWED_ORGS is empty — aborting to prevent config wipe" >&2
  exit 1
fi
if [ -z "${NEW_ROLE_APP_IDS}" ] || [ "${NEW_ROLE_APP_IDS}" = "{}" ]; then
  echo "ERROR: NEW_ROLE_APP_IDS is empty or has no entries — aborting" >&2
  exit 1
fi
if [ -z "${NEW_ALLOWED_ROLES}" ]; then
  echo "ERROR: NEW_ALLOWED_ROLES is empty — aborting to prevent config wipe" >&2
  exit 1
fi
if [ -n "${REPO}" ] && [ -z "${NEW_PER_REPO}" ]; then
  echo "ERROR: NEW_PER_REPO is empty — aborting to prevent config wipe" >&2
  exit 1
fi
```

Apply using `gcloud run services update` with `^|^` delimiter to avoid
comma conflicts with JSON in ROLE_APP_IDS:

```bash
if [ -z "${REPO}" ]; then
  # Per-org
  gcloud run services update "${MINT_SERVICE}" \
    --project="${GCP_PROJECT}" --region="${MINT_REGION}" \
    --update-env-vars="^|^ALLOWED_ORGS=${NEW_ALLOWED_ORGS}|ROLE_APP_IDS=${NEW_ROLE_APP_IDS}|ALLOWED_ROLES=${NEW_ALLOWED_ROLES}" \
    || { echo "ERROR: failed to update mint env vars" >&2; exit 1; }
else
  # Per-repo (also include PER_REPO_WIF_REPOS)
  gcloud run services update "${MINT_SERVICE}" \
    --project="${GCP_PROJECT}" --region="${MINT_REGION}" \
    --update-env-vars="^|^ALLOWED_ORGS=${NEW_ALLOWED_ORGS}|ROLE_APP_IDS=${NEW_ROLE_APP_IDS}|ALLOWED_ROLES=${NEW_ALLOWED_ROLES}|PER_REPO_WIF_REPOS=${NEW_PER_REPO}" \
    || { echo "ERROR: failed to update mint env vars" >&2; exit 1; }
fi
```

### 6. Configure WIF provider

Per-repo repos get a dedicated provider. Per-org repos use the shared
`github-oidc` provider — its `attributeCondition` must include the new org.

```bash
set -o pipefail
if [ -z "${REPO}" ]; then
  # Per-org: update the shared provider's attribute condition to include the new org
  DEFAULT_PROVIDER="github-oidc"
  EXISTING_CONDITION=$(gcloud iam workload-identity-pools providers describe "${DEFAULT_PROVIDER}" \
      --project="${GCP_PROJECT}" \
      --workload-identity-pool="${WIF_POOL}" \
      --location="${WIF_POOL_LOCATION}" \
      --format="value(attributeCondition)") \
      || { echo "ERROR: shared provider '${DEFAULT_PROVIDER}' not found — run 'mint deploy' first" >&2; exit 1; }

  if echo "${EXISTING_CONDITION}" | grep -qF "'${ORG}'"; then
    echo "SKIP: ${ORG} already in shared provider condition"
  else
    # Parse existing orgs, add new one, rebuild condition (uses python3 for portability)
    NEW_CONDITION=$(echo "${EXISTING_CONDITION}" | ORG="${ORG}" python3 -c "
import re, sys, os
cond = sys.stdin.read().strip()
orgs = set(re.findall(r\"'([a-z0-9][a-z0-9._-]*?)'\", cond))
orgs.add(os.environ['ORG'])
orgs = sorted(orgs)
if len(orgs) == 1:
    print(f\"assertion.repository_owner == '{orgs[0]}'\")
else:
    quoted = ', '.join(f\"'{o}'\" for o in orgs)
    print(f'assertion.repository_owner in [{quoted}]')
") || { echo "ERROR: failed to rebuild WIF condition" >&2; exit 1; }
    echo "=== WIF Condition ==="
    echo "  OLD: ${EXISTING_CONDITION}"
    echo "  NEW: ${NEW_CONDITION}"
    FULL_IAM_AUDIENCE="https://iam.googleapis.com/projects/${GCP_PROJECT_NUMBER}/locations/${WIF_POOL_LOCATION}/workloadIdentityPools/${WIF_POOL}/providers/${DEFAULT_PROVIDER}"
    gcloud iam workload-identity-pools providers update-oidc "${DEFAULT_PROVIDER}" \
      --project="${GCP_PROJECT}" \
      --workload-identity-pool="${WIF_POOL}" \
      --location="${WIF_POOL_LOCATION}" \
      --attribute-condition="${NEW_CONDITION}" \
      --allowed-audiences="fullsend-mint,${FULL_IAM_AUDIENCE}" \
      || { echo "ERROR: failed to update shared WIF provider" >&2; exit 1; }
  fi
else
  PROVIDER_ID="gh-${ORG}-${REPO}"
  # Lowercase, replace non-alphanumeric with hyphens, truncate, strip trailing hyphens
  PROVIDER_ID=$(echo "${PROVIDER_ID}" | tr '[:upper:]' '[:lower:]' | tr -c 'a-z0-9-' '-' | cut -c1-32 | sed 's/-*$//')
  FULL_IAM_AUDIENCE="https://iam.googleapis.com/projects/${GCP_PROJECT_NUMBER}/locations/${WIF_POOL_LOCATION}/workloadIdentityPools/${WIF_POOL}/providers/${PROVIDER_ID}"

  echo "Provider ID: ${PROVIDER_ID} (${#PROVIDER_ID} chars)"

  EXPECTED_CONDITION="assertion.repository == '${ORG}/${REPO}'"
  DESCRIBE_ERR=$(mktemp)
  EXISTING_CONDITION=$(gcloud iam workload-identity-pools providers describe "${PROVIDER_ID}" \
      --project="${GCP_PROJECT}" \
      --workload-identity-pool="${WIF_POOL}" \
      --location="${WIF_POOL_LOCATION}" \
      --format="value(attributeCondition)" 2>"${DESCRIBE_ERR}") || true
  if [ -s "${DESCRIBE_ERR}" ] && ! grep -q "NOT_FOUND" "${DESCRIBE_ERR}"; then
    echo "ERROR: failed to describe provider ${PROVIDER_ID}:" >&2
    cat "${DESCRIBE_ERR}" >&2
    rm -f "${DESCRIBE_ERR}"
    exit 1
  fi
  rm -f "${DESCRIBE_ERR}"

  if [ -n "${EXISTING_CONDITION}" ]; then
    if [ "${EXISTING_CONDITION}" != "${EXPECTED_CONDITION}" ]; then
      echo "ERROR: Provider ID collision — ${PROVIDER_ID} belongs to a different repo:" >&2
      echo "  Existing: ${EXISTING_CONDITION}" >&2
      echo "  Expected: ${EXPECTED_CONDITION}" >&2
      exit 1
    fi
    echo "Provider exists for this repo — updating"
    gcloud iam workload-identity-pools providers update-oidc "${PROVIDER_ID}" \
      --project="${GCP_PROJECT}" \
      --workload-identity-pool="${WIF_POOL}" \
      --location="${WIF_POOL_LOCATION}" \
      --attribute-mapping="google.subject=assertion.sub,attribute.repository=assertion.repository,attribute.repository_owner=assertion.repository_owner,attribute.actor=assertion.actor" \
      --attribute-condition="assertion.repository == '${ORG}/${REPO}'" \
      --allowed-audiences="fullsend-mint,${FULL_IAM_AUDIENCE}" \
      || { echo "ERROR: failed to update WIF provider ${PROVIDER_ID}" >&2; exit 1; }
  else
    gcloud iam workload-identity-pools providers create-oidc "${PROVIDER_ID}" \
      --project="${GCP_PROJECT}" \
      --workload-identity-pool="${WIF_POOL}" \
      --location="${WIF_POOL_LOCATION}" \
      --issuer-uri="https://token.actions.githubusercontent.com" \
      --attribute-mapping="google.subject=assertion.sub,attribute.repository=assertion.repository,attribute.repository_owner=assertion.repository_owner,attribute.actor=assertion.actor" \
      --attribute-condition="assertion.repository == '${ORG}/${REPO}'" \
      --allowed-audiences="fullsend-mint,${FULL_IAM_AUDIENCE}" \
      || { echo "ERROR: failed to create WIF provider ${PROVIDER_ID}" >&2; exit 1; }
  fi
fi
```

### 7. Handoff to repo admin

The mint SRE does not configure target repos. Inform the repo admin that
mint-side enrollment is complete and provide these details:

- **Mint URL:** `${MINT_URL}` (resolved in Setup)
- `.github/workflows/fullsend.yaml` shim workflow in the target repo
- GitHub Actions org variable: `FULLSEND_MINT_URL` (set by `fullsend admin install` or manually)

For per-repo enrollments, also provide:
- **WIF Provider ID:** the computed provider ID from Step 6 (needed for
  the `google-github-actions/auth` step in the shim workflow)

For per-org, the `repo-maintenance` workflow in `.fullsend` handles shim
deployment. For per-repo, the admin runs `fullsend admin install --repo`
or configures manually.

Verify that all required GitHub Apps are installed on the target org
before the admin triggers a workflow — the mint cannot produce tokens
for apps that are not installed (see Shared App Model above).

### 8. Verify

Confirm mint infrastructure is ready:

```bash
set -o pipefail
VERIFY_FAILED=0

# ALLOWED_WORKFLOW_FILES must not be empty (mint rejects all requests if unset).
# This is set during mint deploy, not during enrollment — if empty, re-run mint deploy.
ALLOWED_WF=$(gcloud run services describe "${MINT_SERVICE}" \
  --project="${GCP_PROJECT}" --region="${MINT_REGION}" \
  --format="value(spec.template.spec.containers[0].env.filter(name=ALLOWED_WORKFLOW_FILES).value)")
if [ -n "${ALLOWED_WF}" ]; then
  echo "OK: ALLOWED_WORKFLOW_FILES = ${ALLOWED_WF}"
else
  echo "FAIL: ALLOWED_WORKFLOW_FILES is empty — mint will reject ALL token requests (set during 'mint deploy', not enrollment)"
  VERIFY_FAILED=1
fi

# Org in ALLOWED_ORGS
gcloud run services describe "${MINT_SERVICE}" \
  --project="${GCP_PROJECT}" --region="${MINT_REGION}" \
  --format="value(spec.template.spec.containers[0].env.filter(name=ALLOWED_ORGS).value)" \
  | tr ',' '\n' | grep -Fxq "${ORG}" \
  && echo "OK: ${ORG} in ALLOWED_ORGS" || { echo "FAIL: ${ORG} not in ALLOWED_ORGS"; VERIFY_FAILED=1; }

# Org roles in ROLE_APP_IDS
gcloud run services describe "${MINT_SERVICE}" \
  --project="${GCP_PROJECT}" --region="${MINT_REGION}" \
  --format="value(spec.template.spec.containers[0].env.filter(name=ROLE_APP_IDS).value)" \
  | ORG="${ORG}" ROLES="${ROLES}" python3 -c "
import json, sys, os
data = json.loads(sys.stdin.read())
org = os.environ['ORG']
roles = os.environ['ROLES'].split(',')
ok = True
for role in roles:
    key = f'{org}/{role.strip()}'
    if key in data:
        print(f'OK: {key} = {data[key]}')
    else:
        print(f'FAIL: {key} not found')
        ok = False
# fix role should also be present if coder is enrolled, with matching app ID
fix_key = f'{org}/fix'
coder_key = f'{org}/coder'
if fix_key in data and coder_key in data:
    if data[fix_key] == data[coder_key]:
        print(f'OK: {fix_key} = {data[fix_key]} (matches coder)')
    else:
        print(f'FAIL: {fix_key} = {data[fix_key]} but coder = {data[coder_key]} — must match')
        ok = False
elif fix_key in data:
    print(f'OK: {fix_key} = {data[fix_key]}')
elif coder_key in data:
    print(f'FAIL: {fix_key} not found (should be derived from coder)')
    ok = False
sys.exit(0 if ok else 1)
" || VERIFY_FAILED=1

# ALLOWED_ROLES contains all expected roles
CURRENT_ALLOWED_ROLES=$(gcloud run services describe "${MINT_SERVICE}" \
  --project="${GCP_PROJECT}" --region="${MINT_REGION}" \
  --format="value(spec.template.spec.containers[0].env.filter(name=ALLOWED_ROLES).value)" 2>/dev/null)
for ROLE in $(echo "${ROLES}" | tr ',' ' '); do
  if echo ",${CURRENT_ALLOWED_ROLES}," | grep -Fq ",${ROLE},"; then
    echo "OK: ${ROLE} in ALLOWED_ROLES"
  else
    echo "FAIL: ${ROLE} not in ALLOWED_ROLES"; VERIFY_FAILED=1
  fi
done
if echo ",${ROLES}," | grep -Fq ",coder,"; then
  if echo ",${CURRENT_ALLOWED_ROLES}," | grep -Fq ",fix,"; then
    echo "OK: fix in ALLOWED_ROLES (derived from coder)"
  else
    echo "FAIL: fix not in ALLOWED_ROLES (should be derived from coder)"; VERIFY_FAILED=1
  fi
fi

# PEM secrets accessible (metadata check — does not read PEM content)
for ROLE in $(echo "${ROLES}" | tr ',' ' '); do
  SECRET_ID="fullsend-${ORG}--${ROLE}-app-pem"
  gcloud secrets versions describe latest \
    --secret="${SECRET_ID}" \
    --project="${GCP_PROJECT}" --format="value(state)" 2>/dev/null \
    | grep -q "ENABLED" \
    && echo "OK: ${ROLE} secret" || { echo "FAIL: ${ROLE} secret"; VERIFY_FAILED=1; }
  gcloud secrets get-iam-policy "${SECRET_ID}" --project="${GCP_PROJECT}" \
    --format="value(bindings.members)" 2>/dev/null \
    | grep -q "serviceAccount:${SA_EMAIL}" \
    && echo "OK: ${ROLE} IAM binding" || { echo "FAIL: ${ROLE} IAM binding"; VERIFY_FAILED=1; }
done
# fix role PEM (if coder was enrolled)
if echo ",${ROLES}," | grep -Fq ",coder,"; then
  FIX_SECRET="fullsend-${ORG}--fix-app-pem"
  gcloud secrets versions describe latest \
    --secret="${FIX_SECRET}" \
    --project="${GCP_PROJECT}" --format="value(state)" 2>/dev/null \
    | grep -q "ENABLED" \
    && echo "OK: fix secret" || { echo "FAIL: fix secret"; VERIFY_FAILED=1; }
  gcloud secrets get-iam-policy "${FIX_SECRET}" --project="${GCP_PROJECT}" \
    --format="value(bindings.members)" 2>/dev/null \
    | grep -q "serviceAccount:${SA_EMAIL}" \
    && echo "OK: fix IAM binding" || { echo "FAIL: fix IAM binding"; VERIFY_FAILED=1; }
fi

# WIF provider verification
if [ -z "${REPO}" ]; then
  # Per-org: verify shared provider condition includes the org
  DEFAULT_PROVIDER="github-oidc"
  SHARED_CONDITION=$(gcloud iam workload-identity-pools providers describe "${DEFAULT_PROVIDER}" \
    --project="${GCP_PROJECT}" \
    --workload-identity-pool="${WIF_POOL}" \
    --location="${WIF_POOL_LOCATION}" \
    --format="value(attributeCondition)" 2>/dev/null) \
    || { echo "FAIL: shared WIF provider ${DEFAULT_PROVIDER} not found"; VERIFY_FAILED=1; }
  if [ -n "${SHARED_CONDITION}" ]; then
    if echo "${SHARED_CONDITION}" | grep -qF "'${ORG}'"; then
      echo "OK: ${ORG} in shared WIF provider condition"
    else
      echo "FAIL: ${ORG} not in shared WIF provider condition"; VERIFY_FAILED=1
      echo "  Condition: ${SHARED_CONDITION}"
    fi
    # Verify audiences on shared provider
    SHARED_AUDIENCES=$(gcloud iam workload-identity-pools providers describe "${DEFAULT_PROVIDER}" \
      --project="${GCP_PROJECT}" \
      --workload-identity-pool="${WIF_POOL}" \
      --location="${WIF_POOL_LOCATION}" \
      --format="value(oidc.allowedAudiences)" 2>/dev/null)
    SHARED_IAM_AUDIENCE="https://iam.googleapis.com/projects/${GCP_PROJECT_NUMBER}/locations/${WIF_POOL_LOCATION}/workloadIdentityPools/${WIF_POOL}/providers/${DEFAULT_PROVIDER}"
    if echo "${SHARED_AUDIENCES}" | grep -qF "fullsend-mint"; then
      echo "OK: shared provider has fullsend-mint audience"
    else
      echo "FAIL: shared provider missing fullsend-mint audience"; VERIFY_FAILED=1
    fi
    if echo "${SHARED_AUDIENCES}" | grep -qF "${SHARED_IAM_AUDIENCE}"; then
      echo "OK: shared provider has IAM audience URL"
    else
      echo "WARN: shared provider missing IAM audience URL"
    fi
  fi
fi

# Per-repo: verify repo in PER_REPO_WIF_REPOS and WIF provider
if [ -n "${REPO}" ]; then
  gcloud run services describe "${MINT_SERVICE}" \
    --project="${GCP_PROJECT}" --region="${MINT_REGION}" \
    --format="value(spec.template.spec.containers[0].env.filter(name=PER_REPO_WIF_REPOS).value)" \
    | tr ',' '\n' | grep -Fxq "${ORG}/${REPO}" \
    && echo "OK: ${ORG}/${REPO} in PER_REPO_WIF_REPOS" \
    || { echo "FAIL: ${ORG}/${REPO} not in PER_REPO_WIF_REPOS"; VERIFY_FAILED=1; }

  PROVIDER_ID=$(echo "gh-${ORG}-${REPO}" | tr '[:upper:]' '[:lower:]' | tr -c 'a-z0-9-' '-' | cut -c1-32 | sed 's/-*$//')
  PROVIDER_JSON=$(gcloud iam workload-identity-pools providers describe "${PROVIDER_ID}" \
    --project="${GCP_PROJECT}" \
    --workload-identity-pool="${WIF_POOL}" \
    --location="${WIF_POOL_LOCATION}" \
    --format="json(oidc.allowedAudiences,attributeCondition,state)" 2>/dev/null) \
    || { echo "FAIL: WIF provider ${PROVIDER_ID} not found"; VERIFY_FAILED=1; }
  if [ -n "${PROVIDER_JSON}" ]; then
    FULL_IAM_AUDIENCE="https://iam.googleapis.com/projects/${GCP_PROJECT_NUMBER}/locations/${WIF_POOL_LOCATION}/workloadIdentityPools/${WIF_POOL}/providers/${PROVIDER_ID}"
    echo "${PROVIDER_JSON}" | \
      EXPECTED_COND="assertion.repository == '${ORG}/${REPO}'" \
      EXPECTED_IAM_AUD="${FULL_IAM_AUDIENCE}" \
      python3 -c "
import json, sys, os
data = json.loads(sys.stdin.read())
state = data.get('state', '')
cond = data.get('attributeCondition', '')
auds = data.get('oidc', {}).get('allowedAudiences', [])
expected_cond = os.environ['EXPECTED_COND']
expected_iam_aud = os.environ['EXPECTED_IAM_AUD']
ok = True
if state == 'ACTIVE':
    print(f'OK: provider state = {state}')
else:
    print(f'FAIL: provider state = {state} (expected ACTIVE)')
    ok = False
if cond == expected_cond:
    print(f'OK: attributeCondition matches expected')
else:
    print(f'FAIL: attributeCondition mismatch')
    print(f'  Expected: {expected_cond}')
    print(f'  Actual:   {cond}')
    ok = False
if 'fullsend-mint' in auds:
    print('OK: audience fullsend-mint present')
else:
    print('FAIL: audience fullsend-mint missing')
    ok = False
if expected_iam_aud in auds:
    print('OK: IAM audience URL present')
else:
    print(f'WARN: IAM audience URL missing — expected {expected_iam_aud}')
sys.exit(0 if ok else 1)
" || VERIFY_FAILED=1
  fi
fi

exit "${VERIFY_FAILED}"
```

After the repo admin triggers a workflow, check mint logs:

```bash
gcloud functions logs read "${MINT_FUNCTION}" \
  --project="${GCP_PROJECT}" --region="${MINT_REGION}" --gen2 --limit=50 \
  --format="table(timecreated,severity,textPayload)" \
  | grep -i "${ORG}"
```

## Rollback

Rollback is a **separate deliberate operation** — do not execute without
explicit approval from the mint service owner. This section is reference
only; no commands are provided to prevent accidental execution.

Rollback order (reverse of enrollment):

1. **WIF provider** — for per-repo, disable then delete the dedicated
   provider after confirming no workflows depend on it (deletion is
   irreversible). For per-org, re-read the shared `github-oidc`
   provider's `attributeCondition`, remove the org from the list,
   rebuild the condition, and update with `update-oidc`. Verify other
   orgs in the condition are preserved.
2. **PER_REPO_WIF_REPOS** (per-repo only) — remove `{org}/{repo}` from
   the comma-separated list via `gcloud run services update`.
3. **Mint env vars** — re-read current values, remove `{org}/*` entries
   (including `{org}/fix`) from ROLE_APP_IDS, remove `{org}` from
   ALLOWED_ORGS, re-derive ALLOWED_ROLES, apply with `--update-env-vars`.
   Verify no other org shares the same app IDs before removing.
4. **PEM secrets** — disable the latest version (reversible) rather than
   deleting the secret. Include `fullsend-{org}--fix-app-pem` when
   rolling back coder. Only delete after a bake period confirms no
   workflows need the secret.
