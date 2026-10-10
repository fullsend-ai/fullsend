#!/usr/bin/env bash
# setup-e2e-inference-gateway_test.sh — tests for
# setup-e2e-inference-gateway.sh against stubbed gcloud, crane and curl.
#
# Covers: a fresh run creates every resource; a second run is a no-op; a
# config change (--with-vertex) adds a config secret version, rolls a new
# revision and grants roles/aiplatform.user, and a run without it reverts
# both; --delete removes exactly the managed resources; resources the script
# did not create are never modified or deleted; an image digest mismatch and
# an HTML 401 from Cloud Run's front end fail the run.
#
# Run from the repo root:
#   bash hack/setup-e2e-inference-gateway_test.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SETUP="${SCRIPT_DIR}/setup-e2e-inference-gateway.sh"
FAILURES=0

pass() { echo "ok       $*"; }
fail() {
  echo "FAIL     $*" >&2
  FAILURES=$((FAILURES + 1))
}

SHIM_DIR=$(mktemp -d)
STATE=""
trap 'rm -rf "${SHIM_DIR}" "${STATE}"' EXIT

printf '#!/bin/sh\nexit 0\n' > "${SHIM_DIR}/sleep"

# --- gcloud: Artifact Registry, IAM, Secret Manager and Cloud Run state ----
cat > "${SHIM_DIR}/gcloud" <<'EOF'
#!/usr/bin/env bash
S="${STUB_STATE}"
echo "gcloud $*" >> "${S}/gcloud.log"
# Drop the global flags the script always passes.
while [[ "${1:-}" == --project=* || "${1:-}" == --quiet ]]; do shift; done
notfound() { echo "ERROR: NOT_FOUND: $1" >&2; exit 1; }
flag() { # flag NAME ARGS... -> value of --NAME=VALUE
  local n="$1" a; shift
  for a in "$@"; do [[ "${a}" == "--${n}="* ]] && { echo "${a#--"${n}"=}"; return; }; done
}
labels_json() { # "k=v,k=v" -> {"k":"v",...}
  jq -Rn --arg l "$1" '$l | split(",") | map(split("=") | {(.[0]): .[1]}) | add // {}'
}
policy_json() { # policy_json FILE -> bindings from "role member" lines
  jq -Rn '[inputs | split(" ") | {role: .[0], members: [.[1]]}] | {bindings: .}' < "$1"
}
mkdir -p "${S}/secrets"
case "$1 $2 $3" in
  "auth print-access-token "*) echo "stub-access-token" ;;
  "services list "*)
    for a in run secretmanager artifactregistry iam aiplatform; do
      [[ -f "${S}/api_disabled_${a}" ]] || echo "${a}.googleapis.com"
    done ;;
  "artifacts repositories describe")
    [[ -f "${S}/ar" ]] || notfound "repository"
    jq -n --argjson l "$(cat "${S}/ar")" '{labels: $l}' ;;
  "artifacts repositories create")
    labels_json "$(flag labels "$@")" > "${S}/ar" ;;
  "artifacts repositories delete") rm "${S}/ar" ;;
  "iam service-accounts describe")
    [[ -f "${S}/sa" ]] || notfound "Unknown service account"
    jq -n --arg d "$(cat "${S}/sa")" '{description: $d}' ;;
  "iam service-accounts create") flag description "$@" > "${S}/sa" ;;
  "iam service-accounts delete") rm "${S}/sa" ;;
  "projects get-iam-policy "*)
    touch "${S}/project_policy"; policy_json "${S}/project_policy" ;;
  "projects add-iam-policy-binding "*)
    echo "$(flag role "$@") $(flag member "$@")" >> "${S}/project_policy" ;;
  "projects remove-iam-policy-binding "*)
    grep -vx "$(flag role "$@") $(flag member "$@")" "${S}/project_policy" > "${S}/pp.tmp" || true
    mv "${S}/pp.tmp" "${S}/project_policy" ;;
  "secrets describe "*)
    [[ -f "${S}/deny_secrets" ]] && { echo "ERROR: PERMISSION_DENIED: secretmanager.secrets.get" >&2; exit 1; }
    [[ -f "${S}/secrets/$3.labels" ]] || notfound "Secret [$3] not found"
    jq -n --argjson l "$(cat "${S}/secrets/$3.labels")" '{labels: $l}' ;;
  "secrets create "*)
    labels_json "$(flag labels "$@")" > "${S}/secrets/$3.labels" ;;
  "secrets delete "*) rm -f "${S}/secrets/$3".* ;;
  "secrets get-iam-policy "*)
    touch "${S}/secrets/$3.policy"; policy_json "${S}/secrets/$3.policy" ;;
  "secrets add-iam-policy-binding "*)
    echo "$(flag role "$@") $(flag member "$@")" >> "${S}/secrets/$3.policy" ;;
  "secrets versions access")
    sec=$(flag secret "$@"); n=$(cat "${S}/secrets/${sec}.latest" 2>/dev/null) \
      || notfound "Secret [${sec}] not found or has no versions"
    cat "${S}/secrets/${sec}.v${n}" ;;
  "secrets versions add")
    n=$(( $(cat "${S}/secrets/$4.latest" 2>/dev/null || echo 0) + 1 ))
    cp "$(flag data-file "$@")" "${S}/secrets/$4.v${n}"
    echo "${n}" > "${S}/secrets/$4.latest" ;;
  "secrets versions describe")
    sec=$(flag secret "$@")
    echo "projects/123/secrets/${sec}/versions/$(cat "${S}/secrets/${sec}.latest")" ;;
  "run services describe")
    [[ -f "${S}/svc" ]] || { echo "ERROR: (gcloud.run.services.describe) Cannot find service [$4]" >&2; exit 1; }
    jq -n --argjson l "$(cat "${S}/svc")" --arg r "$(cat "${S}/svc_ready" 2>/dev/null || echo True)" \
      '{metadata: {labels: $l}, status: {url: "https://gw.example.test",
        conditions: [{type: "Ready", status: $r}]}}' ;;
  "run deploy "*)
    labels_json "$(flag labels "$@")" > "${S}/svc"
    printf '%s\n' "$@" > "${S}/deploy_args"
    echo deployed >> "${S}/revisions" ;;
  "run services delete") rm "${S}/svc" ;;
  *) echo "gcloud stub: unhandled: $*" >&2; exit 2 ;;
esac
EOF

# --- crane: upstream and Artifact Registry digests ---------------------------
cat > "${SHIM_DIR}/crane" <<'EOF'
#!/usr/bin/env bash
S="${STUB_STATE}"
echo "crane $*" >> "${S}/crane.log"
case "$1" in
  auth) cat > /dev/null ;;
  digest)
    case "$2" in
      cr.agentgateway.dev/*) echo "sha256:upstream" ;;
      *) [[ -f "${S}/image" ]] || { echo "MANIFEST_UNKNOWN: Requested entity was not found." >&2; exit 1; }
         cat "${S}/image" ;;
    esac ;;
  copy) cat "${S}/copy_digest" 2>/dev/null > "${S}/image" || echo "sha256:upstream" > "${S}/image" ;;
esac
EOF

# --- curl: the gateway's answer to unauthenticated probes -------------------
cat > "${SHIM_DIR}/curl" <<'EOF'
#!/usr/bin/env bash
S="${STUB_STATE}"
echo "curl $*" >> "${S}/curl.log"
out=""
while [[ $# -gt 0 ]]; do
  [[ "$1" == "-o" ]] && { out="$2"; shift; }
  shift
done
if [[ -f "${S}/html_401" ]]; then
  echo '<html><body>401 Unauthorized</body></html>' > "${out}"
else
  echo 'authentication failure: no bearer token found' > "${out}"
fi
printf '401'
EOF
chmod +x "${SHIM_DIR}"/*

fresh_state() {
  [[ -n "${STATE}" ]] && rm -rf "${STATE}"
  STATE=$(mktemp -d)
}

# run_setup ARGS... — runs the script against the stubs; output in ${STATE}/out.
run_setup() {
  local rc=0
  env -u E2E_GCP_PROJECT_ID PATH="${SHIM_DIR}:${PATH}" STUB_STATE="${STATE}" \
    bash "${SETUP}" "$@" > "${STATE}/out" 2>&1 || rc=$?
  return "${rc}"
}

expect_out() { # expect_out NAME PATTERN
  if grep -qE -- "$2" "${STATE}/out"; then pass "$1"; else
    fail "$1: output lacks /$2/"; sed 's/^/         /' "${STATE}/out" >&2
  fi
}

revisions() { wc -l < "${STATE}/revisions" 2>/dev/null | tr -d ' ' || echo 0; }

SA="serviceAccount:fullsend-e2e-gateway@test-project-123.iam.gserviceaccount.com"

# --- 1. fresh run creates everything ------------------------------------------
fresh_state
if run_setup --project test-project-123; then pass "fresh run succeeds"; else
  fail "fresh run failed"; cat "${STATE}/out" >&2; fi
expect_out "creates the repository" "created Artifact Registry repository fullsend-e2e-gateway"
expect_out "copies the image" "copied cr.agentgateway.dev/agentgateway:v1.6.0 to us-east5-docker.pkg.dev/test-project-123/fullsend-e2e-gateway/agentgateway:v1.6.0"
expect_out "creates the service account" "created service account"
expect_out "creates the config secret" "created secret fullsend-e2e-gateway-config"
expect_out "creates the stub key secret" "created secret fullsend-e2e-gateway-upstream-key"
expect_out "creates the service" "created Cloud Run service fullsend-e2e-gateway"
expect_out "prints the URL" "url: https://gw.example.test"
expect_out "prints the audience" "audience: fullsend-e2e-gateway"
expect_out "prints the 401 probe" "no token: GET /v1/models -> HTTP 401"
expect_out "prints the x-api-key probe" "x-api-key only: GET /v1/models -> HTTP 401"

CFG="${STATE}/secrets/fullsend-e2e-gateway-config.v1"
if [[ "$(cat "${STATE}/secrets/fullsend-e2e-gateway-upstream-key.v1")" == "e2e-stub-upstream-key" ]]; then
  pass "stub key holds the fixed value"; else fail "stub key value"; fi
if grep -q '"halfsend-01/test-repo-01"' "${CFG}" && grep -q '"halfsend/test-repo-12"' "${CFG}" \
    && [[ "$(grep -c '"halfsend[-0-9]*/test-repo-[0-9][0-9]"' "${CFG}")" == "156" ]]; then
  pass "config allows the 13 x 12 pool repositories"; else fail "pool allowlist"; fi
if grep -q 'mode: strict' "${CFG}" && grep -q 'preserveToken: false' "${CFG}" \
    && grep -q -- '- fullsend-e2e-gateway' "${CFG}"; then
  pass "config validates GitHub OIDC strictly with the fixed audience"; else fail "jwtAuth"; fi
if grep -q 'jwt.repository == "fullsend-ai/e2e-gateway-denied-sentinel"' "${CFG}"; then
  pass "echo-denied is authorised only outside the pool"; else fail "echo-denied rule"; fi
if ! grep -q 'provider: vertex' "${CFG}" && ! grep -q 'aiplatform' "${STATE}/project_policy"; then
  pass "no Vertex model or grant without --with-vertex"; else fail "unexpected Vertex"; fi
for s in fullsend-e2e-gateway-config fullsend-e2e-gateway-upstream-key; do
  if grep -qx "roles/secretmanager.secretAccessor ${SA}" "${STATE}/secrets/${s}.policy"; then
    pass "service account can read ${s}"; else fail "secretAccessor on ${s}"; fi
done
args="${STATE}/deploy_args"
for a in --allow-unauthenticated --no-invoker-iam-check --cpu=1 --memory=512Mi --max-instances=1 \
    "--image=us-east5-docker.pkg.dev/test-project-123/fullsend-e2e-gateway/agentgateway@sha256:upstream" \
    "--set-secrets=/etc/agentgateway/config/config.yaml=fullsend-e2e-gateway-config:1,/etc/agentgateway/upstream/key=fullsend-e2e-gateway-upstream-key:1"; do
  if grep -qx -- "${a}" "${args}"; then pass "deploys with ${a}"; else fail "deploy lacks ${a}"; fi
done

# --- 2. second run is a no-op -------------------------------------------------
cp "${STATE}/gcloud.log" "${STATE}/gcloud.log.1"
run_setup --project test-project-123 || fail "second run failed"
expect_out "second run reports no changes" "No changes: everything was already in place"
if [[ "$(revisions)" == "1" ]]; then pass "second run rolls no revision"; else fail "second run deployed"; fi
if ! diff <(grep -E ' (create|add|deploy|delete|copy) ' "${STATE}/gcloud.log.1") \
    <(grep -E ' (create|add|deploy|delete|copy) ' "${STATE}/gcloud.log") >/dev/null; then
  fail "second run mutated something"; else pass "second run mutates nothing"; fi
if [[ "$(grep -c '^crane copy' "${STATE}/crane.log")" == "1" ]]; then
  pass "second run copies no image"; else fail "image copied twice"; fi

# --- 3. config change: --with-vertex, then back --------------------------------
run_setup --project test-project-123 --with-vertex || fail "--with-vertex run failed"
expect_out "--with-vertex adds a config version" "added a new version of secret fullsend-e2e-gateway-config"
expect_out "--with-vertex grants aiplatform.user" "granted roles/aiplatform.user"
expect_out "--with-vertex rolls a revision" "rolled a new revision"
if grep -q 'provider: vertex' "${STATE}/secrets/fullsend-e2e-gateway-config.v2" \
    && grep -q 'vertexProject: test-project-123' "${STATE}/secrets/fullsend-e2e-gateway-config.v2"; then
  pass "--with-vertex adds the Vertex model"; else fail "Vertex model missing"; fi
if [[ "$(cat "${STATE}/secrets/fullsend-e2e-gateway-upstream-key.latest")" == "1" ]]; then
  pass "--with-vertex leaves the stub key alone"; else fail "stub key re-versioned"; fi
if grep -q 'fullsend-e2e-gateway-config:2' "${STATE}/deploy_args"; then
  pass "new revision mounts config version 2"; else fail "revision config version"; fi
run_setup --project test-project-123 --with-vertex || fail "repeat --with-vertex failed"
expect_out "repeat --with-vertex is a no-op" "No changes"
run_setup --project test-project-123 || fail "run without --with-vertex failed"
expect_out "dropping --with-vertex removes the grant" "removed roles/aiplatform.user"
if [[ "$(revisions)" == "3" ]]; then pass "dropping --with-vertex rolls a revision"; else
  fail "expected 3 revisions, got $(revisions)"; fi

# --- 4. a not-Ready service is redeployed -------------------------------------
echo False > "${STATE}/svc_ready"
run_setup --project test-project-123 || true
expect_out "not-Ready service is redeployed" "redeployed Cloud Run service"
rm "${STATE}/svc_ready"

# --- 5. --delete removes exactly the managed resources ------------------------
run_setup --project test-project-123 --delete || fail "--delete failed"
for f in ar sa svc secrets/fullsend-e2e-gateway-config.labels secrets/fullsend-e2e-gateway-upstream-key.labels; do
  if [[ ! -e "${STATE}/${f}" ]]; then pass "--delete removed ${f}"; else fail "--delete left ${f}"; fi
done
run_setup --project test-project-123 --delete || fail "second --delete failed"
expect_out "second --delete is a no-op" "Nothing to delete"

# --- 6. unmanaged resources are never touched ---------------------------------
fresh_state
mkdir -p "${STATE}/secrets"
echo '{}' > "${STATE}/secrets/fullsend-e2e-gateway-config.labels"
echo '{"team": "other"}' > "${STATE}/svc"
if run_setup --project test-project-123; then fail "setup modified an unmanaged secret"; else
  pass "setup refuses an unmanaged secret"; fi
if [[ ! -e "${STATE}/secrets/fullsend-e2e-gateway-config.latest" ]]; then
  pass "unmanaged secret got no new version"; else fail "unmanaged secret versioned"; fi
if run_setup --project test-project-123 --delete; then fail "--delete ignored unmanaged resources"; else
  pass "--delete fails on unmanaged resources"; fi
if [[ -e "${STATE}/secrets/fullsend-e2e-gateway-config.labels" && -e "${STATE}/svc" ]]; then
  pass "--delete leaves unmanaged resources"; else fail "--delete removed an unmanaged resource"; fi

# --- 7. failures ------------------------------------------------------------------
fresh_state
echo "sha256:tampered" > "${STATE}/copy_digest"
if run_setup --project test-project-123; then fail "digest mismatch accepted"; else
  expect_out "digest mismatch fails" "not upstream sha256:upstream"; fi

fresh_state
touch "${STATE}/html_401"
if run_setup --project test-project-123; then fail "HTML 401 accepted"; else
  expect_out "HTML 401 fails verification" "invoker IAM check is still on"; fi

fresh_state
touch "${STATE}/deny_secrets"
if run_setup --project test-project-123; then fail "describe error treated as missing"; else
  expect_out "describe error fails closed" "PERMISSION_DENIED"; fi
if ! grep -q 'secrets create' "${STATE}/gcloud.log"; then
  pass "describe error creates nothing"; else fail "describe error created a secret"; fi

fresh_state
if run_setup --project; then fail "--project without a value accepted"; else
  expect_out "--project needs a value" "--project needs a value"; fi

fresh_state
touch "${STATE}/api_disabled_run"
if run_setup --project test-project-123; then fail "disabled API accepted"; else
  expect_out "disabled API is reported" "APIs not enabled: run.googleapis.com"; fi

fresh_state
if run_setup; then fail "ran without a project"; else
  expect_out "project is required" "no project"; fi
if E2E_GCP_PROJECT_ID=test-project-123 env PATH="${SHIM_DIR}:${PATH}" STUB_STATE="${STATE}" \
    bash "${SETUP}" > "${STATE}/out" 2>&1; then
  pass "E2E_GCP_PROJECT_ID is the default project"; else fail "E2E_GCP_PROJECT_ID ignored"; fi

echo
if (( FAILURES > 0 )); then
  echo "${FAILURES} failure(s)" >&2
  exit 1
fi
echo "all tests passed"
