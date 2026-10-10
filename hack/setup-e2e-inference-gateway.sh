#!/usr/bin/env bash
# setup-e2e-inference-gateway — provision the durable agentgateway test
# gateway used by the inference gateway behaviour tests (#8280, ADR 0137).
#
# Usage: hack/setup-e2e-inference-gateway.sh [--project <id>] [--region us-east5]
#                                           [--with-vertex] [--delete]
#   --project      GCP project (default: $E2E_GCP_PROJECT_ID; no other default)
#   --region       Cloud Run and Artifact Registry region (default: us-east5)
#   --with-vertex  also serve one real Vertex model (Tier B, runtime-pi-gateway)
#                  and grant the runtime service account roles/aiplatform.user.
#                  Pass it on every run once Tier B is in use: a run without it
#                  removes the Vertex model and the grant.
#   --delete       remove exactly the resources below, by name, then exit
#
# Idempotent: checks each resource and creates or updates only what is
# missing or different, then prints what it did. Re-running after a pool
# change (POOL_ORGS / POOL_REPOS_PER_ORG below) adds a config secret version
# and rolls a new Cloud Run revision. A second run with no changes is a no-op.
#
# Resources, all named fullsend-e2e-gateway*:
#   - Artifact Registry repository, holding the agentgateway release image
#     copied unchanged (digest verified against upstream)
#   - runtime service account
#   - Secret Manager secrets: the gateway config and a stub upstream key
#     (a fixed, non-secret value), each readable by the service account only
#   - Cloud Run service, unauthenticated at the platform level: the gateway
#     itself validates GitHub Actions OIDC tokens
#
# The script never modifies or deletes a resource with one of these names
# that it did not create (checked by label, or by description for the
# service account).
#
# Requires: gcloud (authenticated), crane, jq, curl.
# See docs/guides/dev/e2e-testing.md#inference-gateway-test-gateway for the
# IAM roles the operator needs.

set -euo pipefail

# --- Pool repositories allowed to use the gateway -------------------------
# Keep in sync with orgPool in internal/e2etest/testutil.go (plus the STAGE
# org halfsend) and DefaultPoolSize in pkg/behaviourtest/drivers/install/
# driver.go. A pool change is an edit here plus a re-run.
POOL_ORGS=(halfsend-01 halfsend-02 halfsend-03 halfsend-04 halfsend-05 halfsend-06 halfsend-07 halfsend-08 halfsend-09 halfsend-10 halfsend-11 halfsend-12 halfsend)
POOL_REPOS_PER_ORG=12

# A repository outside the pool. Only it may use the echo-denied model, so a
# behaviour test can assert the 403 for a pool repository.
DENIED_REPO="fullsend-ai/e2e-gateway-denied-sentinel"

# --- Fixed names and versions ---------------------------------------------
NAME="fullsend-e2e-gateway"
AUDIENCE="fullsend-e2e-gateway"
AGW_VERSION="v1.6.0"
UPSTREAM_IMAGE="cr.agentgateway.dev/agentgateway:${AGW_VERSION}"
CONFIG_SECRET="${NAME}-config"
KEY_SECRET="${NAME}-upstream-key"
STUB_UPSTREAM_KEY="e2e-stub-upstream-key"
CONFIG_MOUNT="/etc/agentgateway/config/config.yaml"
KEY_MOUNT="/etc/agentgateway/upstream/key"
GATEWAY_PORT=8080
ECHO_PORT=9090
GITHUB_ISSUER="https://token.actions.githubusercontent.com"
GITHUB_JWKS="${GITHUB_ISSUER}/.well-known/jwks"
MANAGED_LABEL_KEY="managed-by"
MANAGED_LABEL_VALUE="fullsend-e2e-gateway-setup"
SPEC_LABEL_KEY="fullsend-e2e-gateway-spec"
SA_DESCRIPTION="Managed by hack/setup-e2e-inference-gateway.sh"
# Tier B: one real model on Vertex, reached with the runtime service account.
VERTEX_MODEL_NAME="claude-haiku-4-5"
VERTEX_MODEL_ID="claude-haiku-4-5@20251001"

# --- Arguments --------------------------------------------------------------
usage() {
  sed -n '2,/^$/{s/^# \{0,1\}//;p}' "$0" >&2
}

PROJECT="${E2E_GCP_PROJECT_ID:-}"
REGION="us-east5"
WITH_VERTEX=false
DELETE=false
while [[ $# -gt 0 ]]; do
  case "$1" in
    --project|--region)
      if [[ $# -lt 2 ]]; then
        echo "Error: $1 needs a value." >&2
        exit 1
      fi
      if [[ "$1" == "--project" ]]; then PROJECT="$2"; else REGION="$2"; fi
      shift 2 ;;
    --project=*) PROJECT="${1#--project=}"; shift ;;
    --region=*) REGION="${1#--region=}"; shift ;;
    --with-vertex) WITH_VERTEX=true; shift ;;
    --delete) DELETE=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Error: unknown argument: $1" >&2; usage; exit 1 ;;
  esac
done

if [[ -z "${PROJECT}" ]]; then
  echo "Error: no project. Pass --project <id> or set E2E_GCP_PROJECT_ID." >&2
  exit 1
fi
if ! [[ "${PROJECT}" =~ ^[a-z][a-z0-9-]{4,28}[a-z0-9]$ ]]; then
  echo "Error: '${PROJECT}' is not a valid GCP project ID." >&2
  exit 1
fi
if ! [[ "${REGION}" =~ ^[a-z]+-[a-z]+[0-9]+$ ]]; then
  echo "Error: '${REGION}' is not a valid GCP region." >&2
  exit 1
fi
if [[ "${DELETE}" == "true" && "${WITH_VERTEX}" == "true" ]]; then
  echo "Error: --delete and --with-vertex cannot be combined." >&2
  exit 1
fi

SA_EMAIL="${NAME}@${PROJECT}.iam.gserviceaccount.com"
SA_MEMBER="serviceAccount:${SA_EMAIL}"
REGISTRY_HOST="${REGION}-docker.pkg.dev"
DEST_REPO="${REGISTRY_HOST}/${PROJECT}/${NAME}/agentgateway"
DEST_IMAGE="${DEST_REPO}:${AGW_VERSION}"

TMP=$(mktemp -d)
trap 'rm -rf "${TMP}"' EXIT

CHANGES=()
ERRORS=0

say() { echo "    $*"; }
ok() { say "OK: $*"; }
changed() {
  say "CHANGED: $*"
  CHANGES+=("$*")
}
die() {
  echo "    ERROR: $*" >&2
  exit 1
}

# gc runs gcloud against the target project, non-interactively.
gc() {
  gcloud --project="${PROJECT}" --quiet "$@"
}

# describe sets DESCRIBED to a resource's JSON and returns 0. It returns 1
# when the resource does not exist and exits on any other error, so a
# permission or network failure is never mistaken for "missing". It runs in
# the current shell (not inside $(...)) so that the exit stops the script.
DESCRIBED=""
describe() {
  DESCRIBED=""
  if gc "$@" --format=json >"${TMP}/describe.out" 2>"${TMP}/describe.err"; then
    DESCRIBED=$(cat "${TMP}/describe.out")
    return 0
  fi
  if grep -qiE 'NOT_FOUND|not found|does not exist|cannot find' "${TMP}/describe.err"; then
    return 1
  fi
  die "gcloud $*: $(cat "${TMP}/describe.err")"
}

# is_managed reports whether resource JSON carries the managed-by label at
# the given jq path (for example .labels or .metadata.labels).
is_managed() {
  local json="$1" path="$2"
  [[ "$(jq -r --arg k "${MANAGED_LABEL_KEY}" "${path}[\$k] // empty" <<<"${json}")" \
    == "${MANAGED_LABEL_VALUE}" ]]
}

# retry runs a command up to 5 times. IAM bindings for a just-created service
# account can fail until the account has propagated.
retry() {
  local attempt=1
  until "$@"; do
    if (( attempt >= 5 )); then
      return 1
    fi
    say "retrying in 5s (attempt ${attempt}/5 failed)..." >&2
    sleep 5
    attempt=$((attempt + 1))
  done
}

# has_binding reports whether the IAM policy JSON grants role to member.
has_binding() {
  local policy="$1" role="$2" member="$3"
  jq -e --arg r "${role}" --arg m "${member}" \
    '[.bindings[]? | select(.role == $r) | .members[]? | select(. == $m)] | length > 0' \
    <<<"${policy}" >/dev/null
}

# pool_allow_rule prints the exact-match authorization rule for every pool
# repository, indented by the given number of spaces.
pool_allow_rule() {
  local pad
  pad=$(printf '%*s' "$1" '')
  local org i sep=""
  echo "${pad}- allow: |-"
  echo "${pad}    jwt.repository in ["
  for org in "${POOL_ORGS[@]}"; do
    for ((i = 1; i <= POOL_REPOS_PER_ORG; i++)); do
      printf '%s%s      "%s/test-repo-%02d"' "${sep}" "${pad}" "${org}" "${i}"
      sep=$',\n'
    done
  done
  echo
  echo "${pad}    ]"
}

# render_config prints the agentgateway config. It must not contain "$":
# agentgateway expands $VARS in its config file.
render_config() {
  cat <<EOF
# Generated by hack/setup-e2e-inference-gateway.sh. Do not edit by hand:
# change the script and re-run it.
gateways:
  default:
    port: ${GATEWAY_PORT}
  # Loopback-only header-echo upstream, used by the echo models. It answers
  # with a chat completion whose content is the JSON of the request headers
  # it received, so a test can check which credential reached the upstream.
  echo:
    port: ${ECHO_PORT}
    bindAddress: 127.0.0.1
routes:
- name: echo-upstream
  gateways: echo
  policies:
    directResponse:
      status: 200
      headers:
        content-type: '"application/json"'
      bodyExpression: >-
        toJson({"id": "echo", "object": "chat.completion", "created": 0,
        "model": "echo", "choices": [{"index": 0, "finish_reason": "stop",
        "message": {"role": "assistant", "content": toJson(request.headers)}}],
        "usage": {"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0}})
llm:
  gateways: default
  policies:
    jwtAuth:
      mode: strict
      issuer: ${GITHUB_ISSUER}
      audiences:
      - ${AUDIENCE}
      jwks:
        url: ${GITHUB_JWKS}
      # Never forward the caller's token upstream (agentgateway's default).
      preserveToken: false
  models:
  - name: echo
    provider: openAI
    params:
      model: echo
      baseUrl: http://127.0.0.1:${ECHO_PORT}/v1
      apiKey:
        file: ${KEY_MOUNT}
    requestHeaders:
      remove:
      - x-api-key
    authorization:
      rules:
$(pool_allow_rule 6)
  - name: echo-denied
    provider: openAI
    params:
      model: echo
      baseUrl: http://127.0.0.1:${ECHO_PORT}/v1
      apiKey:
        file: ${KEY_MOUNT}
    requestHeaders:
      remove:
      - x-api-key
    authorization:
      rules:
      - allow: jwt.repository == "${DENIED_REPO}"
EOF
  if [[ "${WITH_VERTEX}" == "true" ]]; then
    cat <<EOF
  - name: ${VERTEX_MODEL_NAME}
    provider: vertex
    params:
      model: ${VERTEX_MODEL_ID}
      vertexProject: ${PROJECT}
      vertexRegion: ${REGION}
    requestHeaders:
      remove:
      - x-api-key
    authorization:
      rules:
$(pool_allow_rule 6)
EOF
  fi
}

# --- Prerequisites ----------------------------------------------------------
for tool in gcloud crane jq curl; do
  if ! command -v "${tool}" &>/dev/null; then
    echo "Error: ${tool} is required." >&2
    exit 1
  fi
done
if ! gcloud auth print-access-token >/dev/null 2>&1; then
  echo "Error: gcloud is not authenticated. Run 'gcloud auth login' first." >&2
  exit 1
fi

# =============================================================================
# --delete
# =============================================================================
if [[ "${DELETE}" == "true" ]]; then
  echo "==> Deleting the e2e inference gateway in project ${PROJECT} (${REGION})"
  echo

  # delete_managed deletes a resource only when it exists and carries the
  # managed-by label. Arguments: description, jq label path, describe args...
  # The delete command is read from DELETE_CMD.
  delete_managed() {
    local what="$1" path="$2" json
    shift 2
    if ! describe "$@"; then
      ok "${what} is already absent"
      return 0
    fi
    json="${DESCRIBED}"
    if ! is_managed "${json}" "${path}"; then
      echo "    ERROR: ${what} exists but was not created by this script; leaving it." >&2
      ERRORS=$((ERRORS + 1))
      return 0
    fi
    gc "${DELETE_CMD[@]}" >/dev/null
    changed "deleted ${what}"
  }

  echo "==> Cloud Run service"
  DELETE_CMD=(run services delete "${NAME}" --region="${REGION}")
  delete_managed "Cloud Run service ${NAME}" '.metadata.labels' \
    run services describe "${NAME}" --region="${REGION}"

  echo "==> Secrets"
  for secret in "${CONFIG_SECRET}" "${KEY_SECRET}"; do
    DELETE_CMD=(secrets delete "${secret}")
    delete_managed "secret ${secret}" '.labels' secrets describe "${secret}"
  done

  echo "==> Service account"
  if describe iam service-accounts describe "${SA_EMAIL}"; then
    if [[ "$(jq -r '.description // empty' <<<"${DESCRIBED}")" != "${SA_DESCRIPTION}" ]]; then
      echo "    ERROR: service account ${SA_EMAIL} was not created by this script; leaving it." >&2
      ERRORS=$((ERRORS + 1))
    else
      project_policy=$(gc projects get-iam-policy "${PROJECT}" --format=json)
      if has_binding "${project_policy}" roles/aiplatform.user "${SA_MEMBER}"; then
        gc projects remove-iam-policy-binding "${PROJECT}" \
          --member="${SA_MEMBER}" --role=roles/aiplatform.user >/dev/null
        changed "removed roles/aiplatform.user from ${SA_EMAIL}"
      fi
      gc iam service-accounts delete "${SA_EMAIL}" >/dev/null
      changed "deleted service account ${SA_EMAIL}"
    fi
  else
    ok "service account ${SA_EMAIL} is already absent"
  fi

  echo "==> Artifact Registry repository"
  DELETE_CMD=(artifacts repositories delete "${NAME}" --location="${REGION}")
  delete_managed "Artifact Registry repository ${NAME}" '.labels' \
    artifacts repositories describe "${NAME}" --location="${REGION}"

  echo
  if (( ERRORS > 0 )); then
    echo "==> Finished with ${ERRORS} error(s); see above." >&2
    exit 1
  fi
  if (( ${#CHANGES[@]} == 0 )); then
    echo "==> Nothing to delete."
  else
    echo "==> Deleted ${#CHANGES[@]} resource(s)."
  fi
  exit 0
fi

# =============================================================================
# Setup
# =============================================================================
echo "==> Setting up the e2e inference gateway in project ${PROJECT} (${REGION})"
echo "    Tier B Vertex model: ${WITH_VERTEX}"
echo

# --- 0. APIs ----------------------------------------------------------------
echo "==> Checking required APIs..."
required_apis=(run.googleapis.com secretmanager.googleapis.com artifactregistry.googleapis.com iam.googleapis.com)
if [[ "${WITH_VERTEX}" == "true" ]]; then
  required_apis+=(aiplatform.googleapis.com)
fi
enabled_apis=$(gc services list --enabled --format='value(config.name)')
missing_apis=()
for api in "${required_apis[@]}"; do
  if ! grep -qx "${api}" <<<"${enabled_apis}"; then
    missing_apis+=("${api}")
  fi
done
if (( ${#missing_apis[@]} > 0 )); then
  die "APIs not enabled: ${missing_apis[*]}. Enable them with:
      gcloud services enable ${missing_apis[*]} --project=${PROJECT}"
fi
ok "required APIs are enabled"
echo

# --- 1. Artifact Registry repository ---------------------------------------
echo "==> Artifact Registry repository ${NAME}..."
if describe artifacts repositories describe "${NAME}" --location="${REGION}"; then
  is_managed "${DESCRIBED}" '.labels' \
    || die "repository ${NAME} exists but was not created by this script."
  ok "repository ${NAME} exists"
else
  gc artifacts repositories create "${NAME}" --location="${REGION}" \
    --repository-format=docker \
    --description="agentgateway image for the e2e inference gateway" \
    --labels="${MANAGED_LABEL_KEY}=${MANAGED_LABEL_VALUE}" >/dev/null
  changed "created Artifact Registry repository ${NAME}"
fi
echo

# --- 2. agentgateway image ----------------------------------------------------
echo "==> agentgateway ${AGW_VERSION} image..."
# A private Docker config, so the operator's own config is left untouched.
export DOCKER_CONFIG="${TMP}/docker"
mkdir -p "${DOCKER_CONFIG}"
gcloud auth print-access-token \
  | crane auth login "${REGISTRY_HOST}" -u oauth2accesstoken --password-stdin >/dev/null 2>&1 \
  || die "crane could not log in to ${REGISTRY_HOST}."

upstream_digest=$(crane digest "${UPSTREAM_IMAGE}") \
  || die "could not read the digest of ${UPSTREAM_IMAGE}."
say "upstream ${UPSTREAM_IMAGE} is ${upstream_digest}"

if dest_digest=$(crane digest "${DEST_IMAGE}" 2>"${TMP}/crane.err"); then
  [[ "${dest_digest}" == "${upstream_digest}" ]] \
    || die "${DEST_IMAGE} is ${dest_digest}, not upstream ${upstream_digest}. Refusing to overwrite it; investigate before re-running."
  ok "${DEST_IMAGE} matches upstream"
else
  grep -qiE 'MANIFEST_UNKNOWN|NAME_UNKNOWN|not found' "${TMP}/crane.err" \
    || die "could not read ${DEST_IMAGE}: $(cat "${TMP}/crane.err")"
  crane copy "${UPSTREAM_IMAGE}" "${DEST_IMAGE}" >/dev/null
  dest_digest=$(crane digest "${DEST_IMAGE}")
  [[ "${dest_digest}" == "${upstream_digest}" ]] \
    || die "copied ${DEST_IMAGE} is ${dest_digest}, not upstream ${upstream_digest}."
  changed "copied ${UPSTREAM_IMAGE} to ${DEST_IMAGE} (${dest_digest})"
fi
IMAGE_REF="${DEST_REPO}@${dest_digest}"
echo

# --- 3. Runtime service account ---------------------------------------------
echo "==> Service account ${SA_EMAIL}..."
if describe iam service-accounts describe "${SA_EMAIL}"; then
  [[ "$(jq -r '.description // empty' <<<"${DESCRIBED}")" == "${SA_DESCRIPTION}" ]] \
    || die "service account ${SA_EMAIL} exists but was not created by this script."
  ok "service account exists"
else
  gc iam service-accounts create "${NAME}" \
    --display-name="e2e inference gateway runtime" \
    --description="${SA_DESCRIPTION}" >/dev/null
  changed "created service account ${SA_EMAIL}"
fi

project_policy=$(gc projects get-iam-policy "${PROJECT}" --format=json)
if [[ "${WITH_VERTEX}" == "true" ]]; then
  if has_binding "${project_policy}" roles/aiplatform.user "${SA_MEMBER}"; then
    ok "service account has roles/aiplatform.user"
  else
    retry gc projects add-iam-policy-binding "${PROJECT}" \
      --member="${SA_MEMBER}" --role=roles/aiplatform.user --condition=None >/dev/null \
      || die "could not grant roles/aiplatform.user to ${SA_EMAIL}."
    changed "granted roles/aiplatform.user to ${SA_EMAIL}"
  fi
elif has_binding "${project_policy}" roles/aiplatform.user "${SA_MEMBER}"; then
  gc projects remove-iam-policy-binding "${PROJECT}" \
    --member="${SA_MEMBER}" --role=roles/aiplatform.user >/dev/null
  changed "removed roles/aiplatform.user from ${SA_EMAIL} (no --with-vertex)"
else
  ok "service account has no project roles (no --with-vertex)"
fi
echo

# --- 4. Secrets ---------------------------------------------------------------
# ensure_secret makes the secret's latest version hold exactly the given
# file, adding a version only when the content differs, and grants the
# service account secretAccessor on it. Sets SECRET_VERSION.
ensure_secret() {
  local secret="$1" file="$2" version_name
  if describe secrets describe "${secret}"; then
    is_managed "${DESCRIBED}" '.labels' \
      || die "secret ${secret} exists but was not created by this script."
  else
    gc secrets create "${secret}" --replication-policy=automatic \
      --labels="${MANAGED_LABEL_KEY}=${MANAGED_LABEL_VALUE}" >/dev/null
    changed "created secret ${secret}"
  fi

  local up_to_date=false
  if gc secrets versions access latest --secret="${secret}" \
      >"${TMP}/current" 2>"${TMP}/access.err"; then
    if cmp -s "${TMP}/current" "${file}"; then
      up_to_date=true
    fi
  elif ! grep -qiE 'NOT_FOUND|not found' "${TMP}/access.err"; then
    die "could not read secret ${secret}: $(cat "${TMP}/access.err")"
  fi
  if [[ "${up_to_date}" == "true" ]]; then
    ok "secret ${secret} is up to date"
  else
    gc secrets versions add "${secret}" --data-file="${file}" >/dev/null
    changed "added a new version of secret ${secret}"
  fi
  version_name=$(gc secrets versions describe latest --secret="${secret}" --format='value(name)')
  SECRET_VERSION="${version_name##*/}"
  [[ "${SECRET_VERSION}" =~ ^[0-9]+$ ]] \
    || die "could not resolve the latest version of secret ${secret} (got '${version_name}')."
  say "secret ${secret} latest version: ${SECRET_VERSION}"

  local policy
  policy=$(gc secrets get-iam-policy "${secret}" --format=json)
  if has_binding "${policy}" roles/secretmanager.secretAccessor "${SA_MEMBER}"; then
    ok "service account can read secret ${secret}"
  else
    retry gc secrets add-iam-policy-binding "${secret}" \
      --member="${SA_MEMBER}" --role=roles/secretmanager.secretAccessor >/dev/null \
      || die "could not grant secretAccessor on ${secret} to ${SA_EMAIL}."
    changed "granted secretAccessor on ${secret} to ${SA_EMAIL}"
  fi
}

echo "==> Secrets..."
render_config > "${TMP}/config.yaml"
if grep -q '\$' "${TMP}/config.yaml"; then
  die "rendered config contains '\$', which agentgateway would expand."
fi
ensure_secret "${CONFIG_SECRET}" "${TMP}/config.yaml"
CONFIG_VERSION="${SECRET_VERSION}"

# The stub key is a fixed, non-secret value. It exists so the custody check
# can see the gateway's own key, never the caller's token, reach the upstream.
printf '%s' "${STUB_UPSTREAM_KEY}" > "${TMP}/upstream-key"
ensure_secret "${KEY_SECRET}" "${TMP}/upstream-key"
KEY_VERSION="${SECRET_VERSION}"
echo

# --- 5. Cloud Run service -----------------------------------------------------
echo "==> Cloud Run service ${NAME}..."
deploy_args=(
  run deploy "${NAME}"
  --region="${REGION}"
  --image="${IMAGE_REF}"
  --service-account="${SA_EMAIL}"
  # Without --no-invoker-iam-check, Cloud Run's front end rejects GitHub
  # bearer tokens with an HTML 401 before they reach the gateway.
  --allow-unauthenticated
  --no-invoker-iam-check
  --cpu=1
  --memory=512Mi
  --min-instances=0
  --max-instances=1
  --port="${GATEWAY_PORT}"
  --args="--file=${CONFIG_MOUNT}"
  --set-secrets="${CONFIG_MOUNT}=${CONFIG_SECRET}:${CONFIG_VERSION},${KEY_MOUNT}=${KEY_SECRET}:${KEY_VERSION}"
)
# The spec label records a hash of everything above, so an unchanged re-run
# skips the deploy and any change (image, config version, flags) rolls a new
# revision.
if command -v sha256sum &>/dev/null; then
  spec=$(printf '%s\n' "${deploy_args[@]}" | sha256sum | cut -c1-20)
else
  spec=$(printf '%s\n' "${deploy_args[@]}" | shasum -a 256 | cut -c1-20)
fi
deploy_args+=(--labels="${MANAGED_LABEL_KEY}=${MANAGED_LABEL_VALUE},${SPEC_LABEL_KEY}=${spec}")

if describe run services describe "${NAME}" --region="${REGION}"; then
  svc_json="${DESCRIBED}"
  is_managed "${svc_json}" '.metadata.labels' \
    || die "Cloud Run service ${NAME} exists but was not created by this script."
  current_spec=$(jq -r --arg k "${SPEC_LABEL_KEY}" '.metadata.labels[$k] // empty' <<<"${svc_json}")
  current_ready=$(jq -r '[.status.conditions[]? | select(.type == "Ready") | .status][0] // "Unknown"' <<<"${svc_json}")
  if [[ "${current_spec}" == "${spec}" && "${current_ready}" == "True" ]]; then
    ok "service is up to date (spec ${spec})"
  elif [[ "${current_spec}" == "${spec}" ]]; then
    gc "${deploy_args[@]}" >/dev/null
    changed "redeployed Cloud Run service ${NAME} (spec ${spec} was not Ready)"
  else
    gc "${deploy_args[@]}" >/dev/null
    changed "rolled a new revision of Cloud Run service ${NAME} (spec ${current_spec:-none} -> ${spec})"
  fi
else
  gc "${deploy_args[@]}" >/dev/null
  changed "created Cloud Run service ${NAME} (spec ${spec})"
fi
echo

# --- 6. Verification ----------------------------------------------------------
echo "==> Verifying..."
describe run services describe "${NAME}" --region="${REGION}" \
  || die "Cloud Run service ${NAME} not found after deploy."
svc_json="${DESCRIBED}"
URL=$(jq -r '.status.url // empty' <<<"${svc_json}")
[[ -n "${URL}" ]] || die "Cloud Run service ${NAME} has no URL."
ready=$(jq -r '[.status.conditions[]? | select(.type == "Ready") | .status][0] // "Unknown"' <<<"${svc_json}")

verify_failed=0
say "service Ready condition: ${ready}"
if [[ "${ready}" != "True" ]]; then
  echo "    FAIL: service is not Ready." >&2
  verify_failed=1
fi

# probe sends one request and expects a 401 from the gateway itself (not an
# HTML page from Cloud Run's front end). It prints the real status and body.
probe() {
  local label="$1"
  shift
  local status body
  status=$(curl -sS -m 30 -o "${TMP}/probe.body" -w '%{http_code}' "$@" "${URL}/v1/models") \
    || status="curl-failed"
  body=$(head -c 300 "${TMP}/probe.body" 2>/dev/null | tr '\n' ' ')
  say "${label}: GET /v1/models -> HTTP ${status}"
  say "  body: ${body}"
  if [[ "${status}" != "401" ]]; then
    echo "    FAIL: expected HTTP 401." >&2
    verify_failed=1
  elif grep -qi '<html' "${TMP}/probe.body"; then
    echo "    FAIL: 401 is an HTML page; the invoker IAM check is still on." >&2
    verify_failed=1
  fi
}
probe "no token"
probe "x-api-key only" -H "x-api-key: ${STUB_UPSTREAM_KEY}"
echo

# --- Summary ------------------------------------------------------------------
if (( ${#CHANGES[@]} == 0 )); then
  echo "==> No changes: everything was already in place."
else
  echo "==> Made ${#CHANGES[@]} change(s):"
  for c in "${CHANGES[@]}"; do
    echo "    - ${c}"
  done
fi
echo
echo "==> Gateway for the scenario's committed .fullsend/config.yaml:"
echo
echo "    inference:"
echo "      gateway:"
echo "        url: ${URL}"
echo "        audience: ${AUDIENCE}"
echo
echo "    A positive check needs a GitHub Actions OIDC token for a pool"
echo "    repository; the inference-gateway behaviour test covers it."

if (( verify_failed > 0 )); then
  echo >&2
  echo "==> Verification failed; see FAIL lines above." >&2
  exit 1
fi
