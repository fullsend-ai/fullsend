#!/usr/bin/env bash
# setup-e2e-inference-gateway — provision the durable agentgateway test
# gateway used by the inference gateway behaviour tests (#8280, ADR 0137).
#
# Usage: hack/setup-e2e-inference-gateway.sh [--project <id>] [--region us-east5]
#            [--with-vertex | --without-vertex] [--dry-run | --print-config]
#        hack/setup-e2e-inference-gateway.sh [--project <id>] [--region us-east5]
#            --delete [--yes] [--include-unlabelled]
#   --project         GCP project (default: $E2E_GCP_PROJECT_ID; no other default)
#   --region          Cloud Run and Artifact Registry region (default: us-east5)
#   --with-vertex     also serve the real Vertex models (for runtime-pi-gateway)
#                     and grant the runtime service account roles/aiplatform.user
#   --without-vertex  remove the Vertex models and the grant. Without either
#                     flag, a run against a gateway that serves Vertex models
#                     fails rather than removing them.
#   --dry-run         read only: print what would change, exit 3 if anything would
#   --print-config    print the generated gateway config and exit (no gcloud calls)
#   --delete          remove exactly the resources below, by name, then exit
#   --yes             with --delete, skip the confirmation prompt
#   --include-unlabelled
#                     with --delete, also delete resources with these names that
#                     lack the purpose=fullsend-e2e-gateway marker (for example
#                     a gateway deployed by hand and adopted). Without it, any
#                     unmarked resource stops the delete before anything goes.
#
# Optional gateway API keys (the api-key mode, ADR 0138), each given only as
# its hash, sha256:<64 hex>, never in plaintext:
#   ECHO_KEY_HASH     a key for the echo model only (E2E_INFERENCE_GATEWAY_TEST_KEY)
#   REAL_KEY_HASH     a time-boxed key for claude-haiku-5-5 only, for one local
#                     run; leave it unset once that run is done
# With a key, this TEST gateway runs both modes on one gateway: its OIDC
# check becomes permissive (a non-JWT bearer goes on to the key check),
# each model also admits its key's purpose, and the post-deploy probe
# expects a 401 for a wrong key instead of for no token. That is not a
# recommended setup: a gateway that only serves the oidc mode keeps strict
# jwtAuth. Permissive is safe here only because every model's
# authorization rules admit nothing but a pool repository's token or a
# configured key, so an anonymous request is refused per model.
#
# Idempotent: checks each resource and creates or updates only what is
# missing or different, then prints what it did. Re-running after a pool
# change (POOL_ORGS / POOL_REPOS below) adds a config secret version and rolls
# a new Cloud Run revision. A second run with no changes is a no-op.
#
# Resources, all named fullsend-e2e-gateway*:
#   - Artifact Registry repository, holding the agentgateway release image
#     copied unchanged (digest verified against upstream on every run)
#   - runtime service account
#   - Secret Manager secrets: the gateway config and a stub upstream key
#     (a fixed, non-secret value), each readable by the service account
#   - Cloud Run service, unauthenticated at the platform level: the gateway
#     itself validates GitHub Actions OIDC tokens
#
# These names are reserved for this script: it adopts resources that already
# carry them (for example a gateway deployed by hand from the operator guide)
# and never reads, changes or deletes any other resource. Resources it creates
# carry the marker purpose=fullsend-e2e-gateway (a label, or the description
# of the service account); --delete removes unmarked ones only on request.
#
# Requires: gcloud (authenticated), skopeo, jq, curl.
# See docs/guides/dev/e2e-testing.md#inference-gateway-test-gateway for the
# IAM roles the operator needs.

set -euo pipefail

# --- Pool repositories allowed to use the gateway -------------------------
# Keep in sync with orgPool in internal/e2etest/testutil.go (plus the STAGE
# org halfsend) and DefaultPoolSize in pkg/behaviourtest/drivers/install/
# driver.go. A pool change is an edit here plus a re-run.
POOL_ORGS=(halfsend-01 halfsend-02 halfsend-03 halfsend-04 halfsend-05 halfsend-06 halfsend-07 halfsend-08 halfsend-09 halfsend-10 halfsend-11 halfsend-12 halfsend)
POOL_REPOS=(test-repo-01 test-repo-02 test-repo-03 test-repo-04 test-repo-05 test-repo-06 test-repo-07 test-repo-08 test-repo-09 test-repo-10 test-repo-11 test-repo-12)

# A repository outside the pool. Only it may use the echo-denied model, so a
# behaviour test can assert the 403 for a pool repository.
DENIED_REPO="fullsend-e2e-gateway-outside/not-a-pool-repo"

# Real models on Vertex, reached with the runtime service account.
VERTEX_MODELS=(claude-haiku-5-5 gemini-3.8-flash)
VERTEX_REGION="global"

# --- Fixed names and versions ---------------------------------------------
NAME="fullsend-e2e-gateway"
AUDIENCE="fullsend-e2e-gateway"
AGW_VERSION="v1.6.0"
UPSTREAM_IMAGE="ghcr.io/agentgateway/agentgateway:${AGW_VERSION}"
CONFIG_SECRET="${NAME}-config"
KEY_SECRET="${NAME}-stub-upstream-key"
STUB_UPSTREAM_KEY="e2e-stub-upstream-key"
CONFIG_DIR="/etc/agw-config"
KEY_DIR="/etc/agw-upstream"
GATEWAY_PORT=8080
ECHO_PORT=9000
GITHUB_ISSUER="https://token.actions.githubusercontent.com"
LABEL="purpose=${NAME}"

# --- Arguments --------------------------------------------------------------
usage() {
  sed -n '2,/^$/{s/^# \{0,1\}//;p;}' "$0"
}

PROJECT="${E2E_GCP_PROJECT_ID:-}"
REGION="us-east5"
VERTEX=""
DELETE=false
YES=false
INCLUDE_UNLABELLED=false
DRY_RUN=false
PRINT_CONFIG=false
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
    --with-vertex) VERTEX=with; shift ;;
    --without-vertex) VERTEX=without; shift ;;
    --dry-run) DRY_RUN=true; shift ;;
    --print-config) PRINT_CONFIG=true; shift ;;
    --delete) DELETE=true; shift ;;
    --yes) YES=true; shift ;;
    --include-unlabelled) INCLUDE_UNLABELLED=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Error: unknown argument: $1" >&2; usage >&2; exit 1 ;;
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
if [[ "${DELETE}" == "true" && ( -n "${VERTEX}" || "${DRY_RUN}" == "true" || "${PRINT_CONFIG}" == "true" ) ]]; then
  echo "Error: --delete cannot be combined with other modes." >&2
  exit 1
fi
if [[ ( "${YES}" == "true" || "${INCLUDE_UNLABELLED}" == "true" ) && "${DELETE}" != "true" ]]; then
  echo "Error: --yes and --include-unlabelled only apply to --delete." >&2
  exit 1
fi
if [[ "${DRY_RUN}" == "true" && "${PRINT_CONFIG}" == "true" ]]; then
  echo "Error: --dry-run and --print-config cannot be combined." >&2
  exit 1
fi

SA_EMAIL="${NAME}@${PROJECT}.iam.gserviceaccount.com"
SA_MEMBER="serviceAccount:${SA_EMAIL}"
REGISTRY_HOST="${REGION}-docker.pkg.dev"
DEST_IMAGE="${REGISTRY_HOST}/${PROJECT}/${NAME}/agentgateway:${AGW_VERSION}"

# --- Gateway config -----------------------------------------------------------
# pool_cel prints the exact-match authorization rule for every pool repository.
pool_cel() {
  local org repo sep=""
  printf 'jwt.repository in ['
  for org in "${POOL_ORGS[@]}"; do
    for repo in "${POOL_REPOS[@]}"; do
      printf '%s"%s/%s"' "${sep}" "${org}" "${repo}"
      sep=", "
    done
  done
  printf ']'
}

# model prints one llm model. Arguments: name, provider, params, CEL rule,
# and an optional second CEL rule (an API key's purpose).
model() {
  cat <<EOF
  - name: $1
    provider: $2
    params: $3
    requestHeaders: { remove: [x-api-key] }
    authorization:
      rules:
      - allow: '$4'
EOF
  if [[ -n "${5:-}" ]]; then
    printf "      - allow: '%s'\n" "$5"
  fi
}

ECHO_KEY_HASH="${ECHO_KEY_HASH:-}"
REAL_KEY_HASH="${REAL_KEY_HASH:-}"
for key_hash in "${ECHO_KEY_HASH}" "${REAL_KEY_HASH}"; do
  if [[ -n "${key_hash}" && ! "${key_hash}" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    echo "Error: ECHO_KEY_HASH and REAL_KEY_HASH must be sha256:<64 lowercase hex>, never a plaintext key." >&2
    exit 1
  fi
done
ECHO_KEY_CEL='apiKey.purpose == "e2e-echo-test"'
REAL_KEY_CEL='apiKey.purpose == "e2e-real-run"'
HAS_KEYS=false
JWT_MODE=strict
if [[ -n "${ECHO_KEY_HASH}" || -n "${REAL_KEY_HASH}" ]]; then
  HAS_KEYS=true
  JWT_MODE=permissive
fi

# api_key_policy prints the llm apiKey policy for the configured key hashes.
# jwtAuth runs first and strips a valid JWT, so a key never reaches JWT
# validation and a JWT never reaches the key check. agentgateway v1.6.0
# rejects a key budget without config.database, so keys carry none.
api_key_policy() {
  [[ "${HAS_KEYS}" == "true" ]] || return 0
  printf '    apiKey:\n      mode: optional\n      keys:\n'
  if [[ -n "${ECHO_KEY_HASH}" ]]; then
    printf '      - keyHash: "%s"\n        metadata: { name: e2e-echo-test, purpose: e2e-echo-test }\n        allowedModels: [echo]\n' "${ECHO_KEY_HASH}"
  fi
  if [[ -n "${REAL_KEY_HASH}" ]]; then
    printf '      - keyHash: "%s"\n        metadata: { name: e2e-real-run, purpose: e2e-real-run }\n        allowedModels: [claude-haiku-5-5]\n' "${REAL_KEY_HASH}"
  fi
}

# render_config prints the agentgateway config. It must not contain "$":
# agentgateway expands $VARS in its config file.
#
# The echo listener is a header-echo upstream inside the same container (Cloud
# Run only routes to port 8080). Its answer reports whether the gateway's own
# stub key, rather than the caller's credential, reached the upstream.
render_config() {
  local pool m
  pool=$(pool_cel)
  local echo_provider="{ custom: { formats: [ { type: completions } ] } }"
  local echo_params="{ baseUrl: http://127.0.0.1:${ECHO_PORT}/v1, apiKey: { file: ${KEY_DIR}/key } }"
  cat <<EOF
# fullsend-e2e-gateway (fullsend#8285): agentgateway ${AGW_VERSION}, llm mode, GitHub Actions OIDC
# $(( ${#POOL_ORGS[@]} * ${#POOL_REPOS[@]} )) allowed repositories (${#POOL_ORGS[@]} orgs x ${#POOL_REPOS[@]} repos)
binds:
- port: ${ECHO_PORT}
  listeners:
  - name: echo
    hostname: "*"
    routes:
    - name: echo
      policies:
        directResponse:
          status: 200
          headers:
            content-type: '"application/json"'
          bodyExpression: >-
            toJson({"id":"echo","object":"chat.completion","created":0,"model":"echo",
            "choices":[{"index":0,"message":{"role":"assistant","content": toJson({
            "authorization": !("authorization" in request.headers) ? "absent" : (request.headers["authorization"] == "Bearer ${STUB_UPSTREAM_KEY}" ? "STUB_KEY" : "OTHER len=" + string(size(request.headers["authorization"]))),
            "x_api_key_present": "x-api-key" in request.headers,
            "header_names": request.headers.map(k, k)})},"finish_reason":"stop"}],
            "usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}})
gateways:
  default: { port: ${GATEWAY_PORT}, bindAddress: 0.0.0.0 }
llm:
  gateways: [default]
  discovery: disabled
  policies:
    jwtAuth:
      mode: ${JWT_MODE}
      providers:
      - issuer: ${GITHUB_ISSUER}
        audiences: [${AUDIENCE}]
        jwks: { url: ${GITHUB_ISSUER}/.well-known/jwks }
EOF
  api_key_policy
  echo "  models:"
  if [[ "${VERTEX}" == "with" ]]; then
    for m in "${VERTEX_MODELS[@]}"; do
      local extra=""
      if [[ "${m}" == "claude-haiku-5-5" && -n "${REAL_KEY_HASH}" ]]; then
        extra="${REAL_KEY_CEL}"
      fi
      model "${m}" vertex "{ vertexProject: ${PROJECT}, vertexRegion: ${VERTEX_REGION} }" "${pool}" "${extra}"
    done
  fi
  local echo_extra=""
  if [[ -n "${ECHO_KEY_HASH}" ]]; then
    echo_extra="${ECHO_KEY_CEL}"
  fi
  model echo "${echo_provider}" "${echo_params}" "${pool}" "${echo_extra}"
  model echo-denied "${echo_provider}" "${echo_params}" "jwt.repository == \"${DENIED_REPO}\""
}

if [[ "${PRINT_CONFIG}" == "true" ]]; then
  render_config
  exit 0
fi

TMP=$(mktemp -d)
trap 'rm -rf "${TMP}"' EXIT

CHANGES=()

say() { echo "    $*"; }
ok() { say "OK: $*"; }
die() {
  echo "    ERROR: $*" >&2
  exit 1
}

# change records a change and runs its command, or only prints it with
# --dry-run. Arguments: description, command...
change() {
  local what="$1"
  shift
  CHANGES+=("${what}")
  if [[ "${DRY_RUN}" == "true" ]]; then
    say "WOULD: ${what}"
    return 0
  fi
  "$@" >/dev/null
  say "CHANGED: ${what}"
}

# gc runs gcloud against the target project, non-interactively.
gc() {
  gcloud --project="${PROJECT}" --quiet "$@"
}

sha256() {
  if command -v sha256sum &>/dev/null; then
    sha256sum | cut -d' ' -f1
  else
    shasum -a 256 | cut -d' ' -f1
  fi
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

# has_binding reports whether the IAM policy JSON grants role to member
# without a condition. Conditional bindings are not the script's, and may not
# be in effect, so they never count.
has_binding() {
  local policy="$1" role="$2" member="$3"
  jq -e --arg r "${role}" --arg m "${member}" \
    '[.bindings[]? | select(.role == $r and .condition == null) | .members[]? | select(. == $m)] | length > 0' \
    <<<"${policy}" >/dev/null
}

# --- Prerequisites ----------------------------------------------------------
for tool in gcloud skopeo jq curl; do
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
  if [[ "${YES}" != "true" ]]; then
    read -rp "    Type the project ID to confirm: " confirm </dev/tty
    [[ "${confirm}" == "${PROJECT}" ]] || die "confirmation did not match; nothing deleted."
  fi
  echo

  # The secrets and the service account are global. Never delete them while a
  # service of this name runs in another region (a mistyped --region).
  # A listing that skipped unreachable regions is not proof, so any warning
  # stops the delete. The operator's run/region default is cleared so that it
  # cannot narrow the listing to one region, and the verbosity is pinned so
  # that a quieter setting cannot hide the warning.
  CLOUDSDK_RUN_REGION="" gc run services list --verbosity=warning \
    --filter="metadata.name=${NAME}" --format=json \
    >"${TMP}/services.json" 2>"${TMP}/services.err" \
    || die "could not list Cloud Run services: $(cat "${TMP}/services.err")"
  if grep -qiE 'warning|unreachable|unavailable' "${TMP}/services.err"; then
    die "the Cloud Run service listing may be incomplete; nothing deleted: $(cat "${TMP}/services.err")"
  fi
  elsewhere=$(jq -r --arg r "${REGION}" \
    '[.[] | .metadata.labels["cloud.googleapis.com/location"] | select(. != $r)] | join(", ")' \
    "${TMP}/services.json") || die "could not read the Cloud Run service listing."
  [[ -z "${elsewhere}" ]] \
    || die "Cloud Run service ${NAME} runs in ${elsewhere}, not ${REGION}; pass that --region. Nothing deleted."

  # First pass: find the resources and check each for the marker the script
  # sets on creation, before anything is deleted. present_* record what exists.
  unmarked=()
  marked() { # marked DESCRIPTION JQ_PATH EXPECTED -> records an unmarked resource
    [[ "$(jq -r "$2 // empty" <<<"${DESCRIBED}")" == "$3" ]] || unmarked+=("$1")
  }
  present_svc=false present_sa=false present_ar=false
  present_secrets=()
  if describe run services describe "${NAME}" --region="${REGION}"; then
    present_svc=true
    marked "Cloud Run service ${NAME}" '.metadata.labels.purpose' "${NAME}"
  fi
  for secret in "${CONFIG_SECRET}" "${KEY_SECRET}"; do
    if describe secrets describe "${secret}"; then
      present_secrets+=("${secret}")
      marked "secret ${secret}" '.labels.purpose' "${NAME}"
    fi
  done
  if describe iam service-accounts describe "${SA_EMAIL}"; then
    present_sa=true
    # Service accounts take no labels; the script marks the description.
    marked "service account ${SA_EMAIL}" '.description' "${LABEL}"
  fi
  if describe artifacts repositories describe "${NAME}" --location="${REGION}"; then
    present_ar=true
    marked "Artifact Registry repository ${NAME}" '.labels.purpose' "${NAME}"
  fi
  if (( ${#unmarked[@]} > 0 )) && [[ "${INCLUDE_UNLABELLED}" != "true" ]]; then
    printf '    unmarked: %s\n' "${unmarked[@]}" >&2
    die "these resources lack the marker ${LABEL}, so this script may not have created them; nothing deleted. Pass --include-unlabelled to delete them anyway (for example a gateway deployed by hand)."
  fi

  echo "==> Cloud Run service ${NAME}"
  if [[ "${present_svc}" == "true" ]]; then
    change "deleted Cloud Run service ${NAME}" gc run services delete "${NAME}" --region="${REGION}"
  else
    ok "Cloud Run service ${NAME} is already absent"
  fi

  echo "==> Secrets"
  for secret in "${CONFIG_SECRET}" "${KEY_SECRET}"; do
    if [[ " ${present_secrets[*]} " == *" ${secret} "* ]]; then
      change "deleted secret ${secret}" gc secrets delete "${secret}"
    else
      ok "secret ${secret} is already absent"
    fi
  done

  echo "==> Service account ${SA_EMAIL}"
  if [[ "${present_sa}" == "true" ]]; then
    project_policy=$(gc projects get-iam-policy "${PROJECT}" --format=json)
    if has_binding "${project_policy}" roles/aiplatform.user "${SA_MEMBER}"; then
      change "removed roles/aiplatform.user from ${SA_EMAIL}" \
        gc projects remove-iam-policy-binding "${PROJECT}" \
          --member="${SA_MEMBER}" --role=roles/aiplatform.user --condition=None
    fi
    change "deleted service account ${SA_EMAIL}" gc iam service-accounts delete "${SA_EMAIL}"
  else
    ok "service account is already absent"
  fi

  echo "==> Artifact Registry repository ${NAME}"
  if [[ "${present_ar}" == "true" ]]; then
    change "deleted Artifact Registry repository ${NAME}" \
      gc artifacts repositories delete "${NAME}" --location="${REGION}"
  else
    ok "repository is already absent"
  fi

  echo
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
if [[ "${DRY_RUN}" == "true" ]]; then
  echo "==> Dry run (read only) for the e2e inference gateway in project ${PROJECT} (${REGION})"
else
  echo "==> Setting up the e2e inference gateway in project ${PROJECT} (${REGION})"
fi
echo

# --- 0. APIs ----------------------------------------------------------------
echo "==> Checking required APIs..."
required_apis=(run.googleapis.com secretmanager.googleapis.com artifactregistry.googleapis.com iam.googleapis.com)
if [[ "${VERTEX}" == "with" ]]; then
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

# --- 1. Vertex models -----------------------------------------------------------
# Without --with-vertex or --without-vertex, follow what the gateway already
# serves only when it serves no Vertex models, so a forgotten flag never
# removes them from the durable gateway.
project_policy=$(gc projects get-iam-policy "${PROJECT}" --format=json)
has_vertex_grant=false
if has_binding "${project_policy}" roles/aiplatform.user "${SA_MEMBER}"; then
  has_vertex_grant=true
fi
if [[ -z "${VERTEX}" ]]; then
  current_config=""
  if describe secrets describe "${CONFIG_SECRET}"; then
    if gc secrets versions access latest --secret="${CONFIG_SECRET}" \
        >"${TMP}/current" 2>"${TMP}/access.err"; then
      current_config=$(cat "${TMP}/current")
    elif ! grep -qiE 'NOT_FOUND|not found' "${TMP}/access.err"; then
      die "could not read secret ${CONFIG_SECRET}: $(cat "${TMP}/access.err")"
    fi
  fi
  if [[ "${has_vertex_grant}" == "true" ]] || grep -q 'provider: vertex' <<<"${current_config}"; then
    die "this gateway serves Vertex models. Pass --with-vertex to keep them, or --without-vertex to remove them."
  fi
  VERTEX=without
fi

# --- 2. Artifact Registry repository ---------------------------------------
echo "==> Artifact Registry repository ${NAME}..."
if describe artifacts repositories describe "${NAME}" --location="${REGION}"; then
  ok "repository ${NAME} exists"
else
  change "created Artifact Registry repository ${NAME}" \
    gc artifacts repositories create "${NAME}" --location="${REGION}" \
      --repository-format=docker --labels="${LABEL}" \
      --description="agentgateway release image for the fullsend e2e inference gateway"
fi
echo

# --- 3. agentgateway image ----------------------------------------------------
# Both digests are of the raw multi-arch index, read with skopeo the same way.
# A private auth file keeps the operator's own registry config untouched.
echo "==> agentgateway ${AGW_VERSION} image..."
skopeo inspect --raw "docker://${UPSTREAM_IMAGE}" >"${TMP}/upstream.manifest" \
  || die "could not read ${UPSTREAM_IMAGE}."
upstream_digest="sha256:$(sha256 < "${TMP}/upstream.manifest")"
# Cloud Run resolves the index to its linux/amd64 image for each revision.
upstream_amd64=$(jq -r '[.manifests[]? | select(.platform.os == "linux" and .platform.architecture == "amd64") | .digest][0] // empty' \
  "${TMP}/upstream.manifest") || die "could not parse the ${UPSTREAM_IMAGE} index."
[[ -n "${upstream_amd64}" ]] || die "${UPSTREAM_IMAGE} has no linux/amd64 image."
say "upstream ${UPSTREAM_IMAGE} is ${upstream_digest}"
gcloud auth print-access-token \
  | skopeo login --authfile "${TMP}/auth.json" -u oauth2accesstoken --password-stdin "${REGISTRY_HOST}" >/dev/null \
  || die "skopeo could not log in to ${REGISTRY_HOST}."

# dest_digest prints the digest of the copy in Artifact Registry, or nothing
# when it does not exist yet. Any other error stops the script.
dest_digest() {
  # Hash the file, not a variable: command substitution drops trailing newlines.
  if skopeo inspect --raw --authfile "${TMP}/auth.json" "docker://${DEST_IMAGE}" \
      >"${TMP}/dest.manifest" 2>"${TMP}/skopeo.err"; then
    printf 'sha256:%s\n' "$(sha256 < "${TMP}/dest.manifest")"
  elif ! grep -qiE 'manifest unknown|name unknown|not found' "${TMP}/skopeo.err"; then
    die "could not read ${DEST_IMAGE}: $(cat "${TMP}/skopeo.err")"
  fi
}

copy_image() {
  skopeo copy --all --preserve-digests --quiet --authfile "${TMP}/auth.json" \
    "docker://${UPSTREAM_IMAGE}" "docker://${DEST_IMAGE}" \
    || die "skopeo could not copy ${UPSTREAM_IMAGE} to ${DEST_IMAGE}."
  local copied
  copied=$(dest_digest)
  [[ "${copied}" == "${upstream_digest}" ]] \
    || die "copied ${DEST_IMAGE} is ${copied:-missing}, not upstream ${upstream_digest}."
}

current_digest=$(dest_digest)
if [[ -n "${current_digest}" ]]; then
  [[ "${current_digest}" == "${upstream_digest}" ]] \
    || die "${DEST_IMAGE} is ${current_digest}, not upstream ${upstream_digest}. Refusing to overwrite it; investigate before re-running."
  ok "${DEST_IMAGE} matches upstream"
else
  change "copied ${UPSTREAM_IMAGE} to ${DEST_IMAGE}" copy_image
fi
# Deploy by the verified digest, so a retag between the check and the deploy
# cannot change what runs. A service template naming the tag, the index or its
# linux/amd64 image (which gcloud may pin on a later update) also counts as
# current: the running image is checked separately (see serving_image_ok).
DEST_IMAGE_BY_DIGEST="${DEST_IMAGE%:*}@${upstream_digest}"
echo

# --- 4. Runtime service account ---------------------------------------------
echo "==> Service account ${SA_EMAIL}..."
if describe iam service-accounts describe "${SA_EMAIL}"; then
  ok "service account exists"
else
  change "created service account ${SA_EMAIL}" \
    gc iam service-accounts create "${NAME}" \
      --display-name="fullsend e2e inference gateway runtime" --description="${LABEL}"
fi

if [[ "${VERTEX}" == "with" ]]; then
  if [[ "${has_vertex_grant}" == "true" ]]; then
    ok "service account has roles/aiplatform.user"
  else
    change "granted roles/aiplatform.user to ${SA_EMAIL}" \
      retry gc projects add-iam-policy-binding "${PROJECT}" \
        --member="${SA_MEMBER}" --role=roles/aiplatform.user --condition=None
  fi
elif [[ "${has_vertex_grant}" == "true" ]]; then
  change "removed roles/aiplatform.user from ${SA_EMAIL}" \
    gc projects remove-iam-policy-binding "${PROJECT}" \
      --member="${SA_MEMBER}" --role=roles/aiplatform.user --condition=None
else
  ok "service account has no Vertex grant (--without-vertex)"
fi
echo

# --- 5. Secrets ---------------------------------------------------------------
# ensure_secret makes the secret's latest version hold exactly the given file,
# adding a version only when the content differs, and grants the service
# account secretAccessor on it. Sets SECRET_CHANGED.
ensure_secret() {
  local secret="$1" file="$2"
  SECRET_CHANGED=false
  if describe secrets describe "${secret}"; then
    if gc secrets versions access latest --secret="${secret}" \
        >"${TMP}/current" 2>"${TMP}/access.err"; then
      if cmp -s "${TMP}/current" "${file}"; then
        ok "secret ${secret} is up to date"
      else
        SECRET_CHANGED=true
        change "added a new version of secret ${secret}" \
          gc secrets versions add "${secret}" --data-file="${file}"
      fi
    elif grep -qiE 'NOT_FOUND|not found' "${TMP}/access.err"; then
      SECRET_CHANGED=true
      change "added the first version of secret ${secret}" \
        gc secrets versions add "${secret}" --data-file="${file}"
    else
      die "could not read secret ${secret}: $(cat "${TMP}/access.err")"
    fi
  else
    SECRET_CHANGED=true
    change "created secret ${secret}" \
      gc secrets create "${secret}" --replication-policy=automatic \
        --labels="${LABEL}" --data-file="${file}"
  fi

  local policy="{}"
  if describe secrets get-iam-policy "${secret}"; then
    policy="${DESCRIBED}"
  fi
  if has_binding "${policy}" roles/secretmanager.secretAccessor "${SA_MEMBER}"; then
    ok "service account can read secret ${secret}"
  else
    change "granted secretAccessor on ${secret} to ${SA_EMAIL}" \
      retry gc secrets add-iam-policy-binding "${secret}" \
        --member="${SA_MEMBER}" --role=roles/secretmanager.secretAccessor --condition=None
  fi
}

echo "==> Secrets..."
render_config > "${TMP}/config.yaml"
if grep -q '\$' "${TMP}/config.yaml"; then
  die "rendered config contains '\$', which agentgateway would expand."
fi
say "rendered config sha256 $(sha256 < "${TMP}/config.yaml")"
ensure_secret "${CONFIG_SECRET}" "${TMP}/config.yaml"
CONFIG_CHANGED="${SECRET_CHANGED}"

# The stub key is a fixed, non-secret value. It exists so the custody check
# can see the gateway's own key, never the caller's token, reach the upstream.
printf '%s' "${STUB_UPSTREAM_KEY}" > "${TMP}/upstream-key"
ensure_secret "${KEY_SECRET}" "${TMP}/upstream-key"
KEY_CHANGED="${SECRET_CHANGED}"
echo

# --- 6. Cloud Run service -----------------------------------------------------
echo "==> Cloud Run service ${NAME}..."
deploy_args=(
  run deploy "${NAME}"
  --region="${REGION}"
  --image="${DEST_IMAGE_BY_DIGEST}"
  --service-account="${SA_EMAIL}"
  --port="${GATEWAY_PORT}"
  # Without --no-invoker-iam-check, Cloud Run's front end rejects GitHub
  # bearer tokens with an HTML 401 before they reach the gateway.
  --allow-unauthenticated
  --no-invoker-iam-check
  --cpu=1
  --memory=512Mi
  --max-instances=1
  "--args=-f,${CONFIG_DIR}/config.yaml"
  "--set-secrets=${CONFIG_DIR}/config.yaml=${CONFIG_SECRET}:latest,${KEY_DIR}/key=${KEY_SECRET}:latest"
  --labels="${LABEL}"
)

# spec_drift prints the deploy settings the service JSON does not match, comma
# separated, or nothing. Secret mounts are resolved mountPath -> volume ->
# secret, because each --update-secrets run leaves an unmounted volume behind.
spec_drift() {
  jq -r --arg img "${DEST_IMAGE}" --arg imgd "${DEST_IMAGE_BY_DIGEST}" \
    --arg imga "${DEST_IMAGE%:*}@${upstream_amd64}" --arg sa "${SA_EMAIL}" \
    --arg cfgdir "${CONFIG_DIR}" --arg keydir "${KEY_DIR}" \
    --arg cfg "${CONFIG_SECRET}" --arg key "${KEY_SECRET}" \
    --argjson port "${GATEWAY_PORT}" --arg name "${NAME}" '
    .spec.template as $t | ($t.spec.containers[0] // {}) as $c |
    def mounted($dir; $secret; $file):
      ([$c.volumeMounts[]? | select(.mountPath == $dir) | .name][0]) as $v |
      ([$t.spec.volumes[]? | select(.name == $v) | .secret][0] // {}) as $s |
      $s.secretName == $secret and $s.items == [{key: "latest", path: $file}];
    [
      (if ([$img, $imgd, $imga] | index($c.image)) == null then "image" else empty end),
      (if $t.spec.serviceAccountName != $sa then "service account" else empty end),
      (if $c.args != ["-f", ($cfgdir + "/config.yaml")] then "args" else empty end),
      (if [$c.ports[]?.containerPort] != [$port] then "port" else empty end),
      (if $c.resources.limits.cpu != "1" then "cpu" else empty end),
      (if $c.resources.limits.memory != "512Mi" then "memory" else empty end),
      (if $t.metadata.annotations["autoscaling.knative.dev/maxScale"] != "1" then "max instances" else empty end),
      (if .metadata.annotations["run.googleapis.com/invoker-iam-disabled"] != "true" then "invoker IAM check" else empty end),
      (if .metadata.labels.purpose != $name then "label" else empty end),
      (if mounted($cfgdir; $cfg; "config.yaml") | not then "config mount" else empty end),
      (if mounted($keydir; $key; "key") | not then "stub key mount" else empty end)
    ] | join(", ")'
}

ready_of() {
  jq -r '[.status.conditions[]? | select(.type == "Ready") | .status][0] // "Unknown"'
}

# created_after reports whether RFC 3339 timestamp $1 is later than $2, to the
# nanosecond. It exits on a timestamp it cannot parse.
created_after() {
  local result
  result=$(jq -rn --arg a "$1" --arg b "$2" '
    def norm: (capture("^(?<s>[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2})(\\.(?<f>[0-9]+))?Z$")
        // error("unparseable timestamp"))
      | .s + "." + (((.f // "") + "000000000")[0:9]);
    ($a | norm) > ($b | norm)') || die "could not compare timestamps '$1' and '$2'."
  [[ "${result}" == "true" ]]
}

# secrets_newer_than_revision reports whether either secret's latest version
# was created after the given revision. Cloud Run serves a mounted :latest
# secret's newest version as soon as it is added, but a running agentgateway
# only sees it if its file watch fires on the secret volume, which is not
# verified. Rolling a revision restarts the gateway on the new config for
# certain. Comparing times, not what this run changed, also finishes a rollout
# that an earlier run started but did not complete. Every lookup exits on
# failure, because this runs as an if condition, where set -e does not apply.
secrets_newer_than_revision() {
  local revision="$1" rev_time secret ver_time
  [[ -n "${revision}" ]] || return 0
  describe run revisions describe "${revision}" --region="${REGION}" \
    || die "revision ${revision} of ${NAME} not found."
  rev_time=$(jq -r '.metadata.creationTimestamp // empty' <<<"${DESCRIBED}")
  [[ -n "${rev_time}" ]] || die "revision ${revision} has no creation time."
  for secret in "${CONFIG_SECRET}" "${KEY_SECRET}"; do
    describe secrets versions describe latest --secret="${secret}" \
      || die "secret ${secret} has no versions."
    ver_time=$(jq -r '.createTime // empty' <<<"${DESCRIBED}")
    [[ -n "${ver_time}" ]] || die "the latest version of secret ${secret} has no create time."
    if created_after "${ver_time}" "${rev_time}"; then
      return 0
    fi
  done
  return 1
}

# serving_image_ok reports whether the given revision runs the verified image:
# the index digest, or its linux/amd64 image, which Cloud Run resolves a tag or
# an index to.
serving_image_ok() {
  local revision="$1" running
  describe run revisions describe "${revision}" --region="${REGION}" \
    || die "revision ${revision} of ${NAME} not found."
  running=$(jq -r '.status.imageDigest // empty' <<<"${DESCRIBED}")
  [[ "${running}" == "${DEST_IMAGE%:*}@${upstream_digest}" || "${running}" == "${DEST_IMAGE%:*}@${upstream_amd64}" ]]
}

# traffic_to_latest reports whether all traffic goes to the latest revision
# and no revision is reachable through a tag URL.
traffic_to_latest() {
  jq -e '[.spec.traffic[]?]
    | length == 1 and .[0].latestRevision == true and .[0].percent == 100 and (.[0].tag // "") == ""' >/dev/null
}

if describe run services describe "${NAME}" --region="${REGION}"; then
  svc_json="${DESCRIBED}"
  drift=$(spec_drift <<<"${svc_json}")
  ready=$(ready_of <<<"${svc_json}")
  serving=$(jq -r '.status.latestReadyRevisionName // empty' <<<"${svc_json}")
  if [[ -n "${drift}" ]]; then
    change "redeployed Cloud Run service ${NAME} (differed in: ${drift})" gc "${deploy_args[@]}"
  elif [[ "${ready}" != "True" ]]; then
    change "redeployed Cloud Run service ${NAME} (was not Ready)" gc "${deploy_args[@]}"
  elif ! serving_image_ok "${serving}"; then
    change "redeployed Cloud Run service ${NAME} (revision ${serving} runs an unverified image)" gc "${deploy_args[@]}"
  elif [[ "${CONFIG_CHANGED}" == "true" || "${KEY_CHANGED}" == "true" ]] \
      || secrets_newer_than_revision "${serving}"; then
    change "rolled a new revision of Cloud Run service ${NAME} for the latest secret versions" \
      gc run services update "${NAME}" --region="${REGION}" \
        "--update-secrets=${CONFIG_DIR}/config.yaml=${CONFIG_SECRET}:latest,${KEY_DIR}/key=${KEY_SECRET}:latest"
  else
    ok "service is up to date"
  fi
  # A deploy or update keeps an existing traffic split and revision tags, so
  # an older revision would stay reachable with an older config.
  if traffic_to_latest <<<"${svc_json}"; then
    ok "all traffic goes to the latest revision, with no tags"
  else
    change "sent all traffic of Cloud Run service ${NAME} to the latest revision and cleared tags" \
      gc run services update-traffic "${NAME}" --region="${REGION}" --to-latest --clear-tags
  fi
else
  change "created Cloud Run service ${NAME}" gc "${deploy_args[@]}"
fi
echo

# --- 7. Verification ----------------------------------------------------------
verify_failed=0
URL=""
echo "==> Verifying..."
if describe run services describe "${NAME}" --region="${REGION}"; then
  URL=$(jq -r '.status.url // empty' <<<"${DESCRIBED}")
  ready=$(ready_of <<<"${DESCRIBED}")
  say "service Ready condition: ${ready}, revision $(jq -r '.status.latestReadyRevisionName // "none"' <<<"${DESCRIBED}")"
  if [[ "${ready}" != "True" ]]; then
    echo "    FAIL: service is not Ready." >&2
    verify_failed=1
  fi
elif [[ "${DRY_RUN}" == "true" ]]; then
  say "service does not exist yet; nothing to probe"
else
  die "Cloud Run service ${NAME} not found after deploy."
fi

# probe sends one request and expects a 401 from the gateway itself (not an
# HTML page from Cloud Run's front end) whose text/plain body starts with
# the given prefix. It prints the real status and body.
probe() {
  local label="$1" prefix="$2"
  shift 2
  local status ctype body
  : > "${TMP}/probe.body"
  status=$(curl -sS -m 30 -o "${TMP}/probe.body" -w '%{http_code} %{content_type}' "$@" "${URL}/v1/models") \
    || status="curl-failed"
  ctype="${status#* }"
  status="${status%% *}"
  body=$(head -c 300 "${TMP}/probe.body" | tr '\n' ' ') || body=""
  say "${label}: GET /v1/models -> HTTP ${status} ${ctype}"
  say "  body: ${body}"
  if [[ "${status}" != "401" ]]; then
    echo "    FAIL: expected HTTP 401." >&2
    verify_failed=1
  elif [[ "${ctype}" == text/html* ]] || grep -qi '<html' "${TMP}/probe.body"; then
    echo "    FAIL: 401 is an HTML page; the invoker IAM check is still on." >&2
    verify_failed=1
  elif [[ "${ctype}" != text/plain* ]] || ! grep -q "^${prefix}" "${TMP}/probe.body"; then
    echo "    FAIL: 401 is not agentgateway's text/plain authentication failure." >&2
    verify_failed=1
  fi
}
if [[ -n "${URL}" && "${HAS_KEYS}" == "true" ]]; then
  # With keys, no credential passes authentication (and is refused per
  # model), so the probe that must fail is a bearer that is neither a
  # valid JWT nor a configured key.
  probe "wrong key" 'api key authentication failure: ' -H "authorization: Bearer not-a-configured-key"
elif [[ -n "${URL}" ]]; then
  probe "no token" 'authentication failure: '
  probe "x-api-key only" 'authentication failure: ' -H "x-api-key: ${STUB_UPSTREAM_KEY}"
  probe "non-JWT bearer" 'authentication failure: ' -H "authorization: Bearer not-a-jwt"
fi
echo

# --- Summary ------------------------------------------------------------------
if (( ${#CHANGES[@]} == 0 )); then
  echo "==> No changes: everything was already in place."
elif [[ "${DRY_RUN}" == "true" ]]; then
  echo "==> Dry run: ${#CHANGES[@]} change(s) would be made:"
  printf '    - %s\n' "${CHANGES[@]}"
else
  echo "==> Made ${#CHANGES[@]} change(s):"
  printf '    - %s\n' "${CHANGES[@]}"
fi
echo
if [[ -n "${URL}" ]]; then
  echo "==> Behaviour test settings (set the URL as a variable in the dev and stage"
  echo "    environments; it embeds the project number, so never commit it):"
  echo
  echo "    E2E_INFERENCE_GATEWAY_URL=${URL}"
  echo "    E2E_INFERENCE_GATEWAY_AUDIENCE=${AUDIENCE}"
  echo
  echo "    A positive check needs a GitHub Actions OIDC token for a pool"
  echo "    repository; the inference-gateway behaviour test covers it."
fi

# In a dry run, pending changes come first: a failed check may be exactly what
# they would fix.
if [[ "${DRY_RUN}" == "true" && ${#CHANGES[@]} -gt 0 ]]; then
  exit 3
fi
if (( verify_failed > 0 )); then
  echo >&2
  echo "==> Verification failed; see FAIL lines above." >&2
  exit 1
fi
