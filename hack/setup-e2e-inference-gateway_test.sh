#!/usr/bin/env bash
# setup-e2e-inference-gateway_test.sh — tests for
# setup-e2e-inference-gateway.sh against stubbed gcloud, skopeo and curl.
#
# Covers: a fresh run creates every resource; a second run is a no-op; a
# gateway deployed by hand with the same names is adopted without changes;
# --dry-run changes nothing; a config change adds a secret version and rolls
# one revision; spec drift and a not-Ready service redeploy; a run without a
# Vertex flag never removes the Vertex models; --delete removes exactly the
# named resources; an image digest mismatch, an HTML 401 from Cloud Run's
# front end and an unreadable resource fail the run.
#
# Run from the repo root:
#   bash hack/setup-e2e-inference-gateway_test.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SETUP="${SCRIPT_DIR}/setup-e2e-inference-gateway.sh"
FAILURES=0
PROJECT="test-project-123"

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
policy_json() { # policy_json FILE -> bindings from "role member" lines
  jq -Rn '[inputs | split(" ") | {role: .[0], members: [.[1]]}] | {bindings: .}' < "$1"
}
# volume NAME SECRET FILE -> a Cloud Run secret volume
volume() {
  jq -n --arg n "$1" --arg s "$2" --arg f "$3" \
    '{name: $n, secret: {secretName: $s, items: [{key: "latest", path: $f}]}}'
}
mkdir -p "${S}/secrets"
case "$1 $2 $3" in
  "auth print-access-token "*) echo "stub-access-token" ;;
  "services list "*)
    for a in run secretmanager artifactregistry iam aiplatform; do
      [[ -f "${S}/api_disabled_${a}" ]] || echo "${a}.googleapis.com"
    done ;;
  "artifacts repositories describe") [[ -f "${S}/ar" ]] || notfound "repository"; echo '{}' ;;
  "artifacts repositories create") touch "${S}/ar" ;;
  "artifacts repositories delete") rm "${S}/ar" ;;
  "artifacts docker images")
    [[ -f "${S}/image" ]] || notfound "Requested entity was not found"
    jq -n --arg d "$(cat "${S}/image")" '{image_summary: {digest: $d}}' ;;
  "iam service-accounts describe") [[ -f "${S}/sa" ]] || notfound "Unknown service account"; echo '{}' ;;
  "iam service-accounts create") touch "${S}/sa" ;;
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
    [[ -f "${S}/secrets/$3.exists" ]] || notfound "Secret [$3] not found"
    echo '{}' ;;
  "secrets create "*)
    touch "${S}/secrets/$3.exists"
    cp "$(flag data-file "$@")" "${S}/secrets/$3.v1"
    echo 1 > "${S}/secrets/$3.latest" ;;
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
  "run services describe")
    [[ -f "${S}/svc.json" ]] || { echo "ERROR: (gcloud.run.services.describe) Cannot find service [$4]" >&2; exit 1; }
    jq --arg r "$(cat "${S}/svc_ready" 2>/dev/null || echo True)" \
      '.status = {url: "https://gw.example.test", latestReadyRevisionName: "rev",
        conditions: [{type: "Ready", status: $r}]}' "${S}/svc.json" ;;
  "run deploy "*)
    # Build the service the way Cloud Run would from the deploy flags.
    secrets=$(flag set-secrets "$@"); args=$(flag args "$@")
    cfg="${secrets%%,*}"; key="${secrets#*,}"
    jq -n --arg img "$(flag image "$@")" --arg sa "$(flag service-account "$@")" \
      --arg args "${args}" --argjson port "$(flag port "$@")" \
      --arg cpu "$(flag cpu "$@")" --arg mem "$(flag memory "$@")" \
      --arg max "$(flag max-instances "$@")" --arg label "$(flag labels "$@")" \
      --argjson v1 "$(volume cfg-1 "$(cut -d= -f2 <<<"${cfg}" | cut -d: -f1)" config.yaml)" \
      --argjson v2 "$(volume key-1 "$(cut -d= -f2 <<<"${key}" | cut -d: -f1)" key)" \
      --arg cfgdir "$(dirname "${cfg%%=*}")" --arg keydir "$(dirname "${key%%=*}")" \
      --arg noiam "$(printf '%s\n' "$@" | grep -qx -- --no-invoker-iam-check && echo true || echo false)" '
      {metadata: {labels: ($label | split("=") | {(.[0]): .[1]}),
                  annotations: {"run.googleapis.com/invoker-iam-disabled": $noiam}},
       spec: {template: {
         metadata: {annotations: {"autoscaling.knative.dev/maxScale": $max}},
         spec: {serviceAccountName: $sa, volumes: [$v1, $v2],
           containers: [{image: $img, args: ($args | split(",")),
             ports: [{containerPort: $port}],
             resources: {limits: {cpu: $cpu, memory: $mem}},
             volumeMounts: [{mountPath: $cfgdir, name: "cfg-1"},
                            {mountPath: $keydir, name: "key-1"}]}]}}}}' > "${S}/svc.json"
    echo deploy >> "${S}/revisions" ;;
  "run services update")
    # Like Cloud Run: each --update-secrets adds a new volume and leaves the
    # old one unmounted.
    n=$(wc -l < "${S}/revisions" | tr -d ' ')
    jq --argjson v "$(volume "cfg-u${n}" fullsend-e2e-gateway-config config.yaml)" '
      .spec.template.spec.volumes += [$v]
      | .spec.template.spec.containers[0].volumeMounts |= map(
          if .mountPath == "/etc/agw-config" then .name = $v.name else . end)' \
      "${S}/svc.json" > "${S}/svc.tmp" && mv "${S}/svc.tmp" "${S}/svc.json"
    echo update >> "${S}/revisions" ;;
  "run services delete") rm "${S}/svc.json" ;;
  *) echo "gcloud stub: unhandled: $*" >&2; exit 2 ;;
esac
EOF

# --- skopeo: the upstream index and the copy ----------------------------------
cat > "${SHIM_DIR}/skopeo" <<'EOF'
#!/usr/bin/env bash
S="${STUB_STATE}"
echo "skopeo $*" >> "${S}/skopeo.log"
case "$1" in
  login) cat > /dev/null ;;
  inspect) printf 'upstream-index' ;;
  copy) cat "${S}/copy_digest" 2>/dev/null > "${S}/image" \
          || printf 'sha256:%s\n' "$(printf 'upstream-index' | shasum -a 256 | cut -d' ' -f1)" > "${S}/image" ;;
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
  printf '401 text/html; charset=UTF-8'
else
  echo 'authentication failure: no bearer token found' > "${out}"
  printf '401 text/plain'
fi
EOF
chmod +x "${SHIM_DIR}"/*

UPSTREAM_DIGEST="sha256:$(printf 'upstream-index' | shasum -a 256 | cut -d' ' -f1)"
SA="serviceAccount:fullsend-e2e-gateway@${PROJECT}.iam.gserviceaccount.com"
CFG_SECRET="fullsend-e2e-gateway-config"
KEY_SECRET="fullsend-e2e-gateway-stub-upstream-key"

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

revisions() { if [[ -f "${STATE}/revisions" ]]; then wc -l < "${STATE}/revisions" | tr -d ' '; else echo 0; fi; }

# mutations prints every mutating gcloud or skopeo call made so far.
mutations() {
  cat "${STATE}/gcloud.log" "${STATE}/skopeo.log" 2>/dev/null \
    | grep -E ' (create|add|add-iam-policy-binding|remove-iam-policy-binding|deploy|update|delete|copy) ' || true
}

expect_no_mutations() { # expect_no_mutations NAME BEFORE_COUNT
  local now
  now=$(mutations | wc -l | tr -d ' ')
  if [[ "${now}" == "$2" ]]; then pass "$1"; else
    fail "$1: $((now - $2)) mutating call(s)"; mutations | tail -n "$((now - $2))" >&2
  fi
}

# --- 1. fresh run creates everything ------------------------------------------
fresh_state
if run_setup --project "${PROJECT}" --without-vertex; then pass "fresh run succeeds"; else
  fail "fresh run failed"; cat "${STATE}/out" >&2; fi
expect_out "creates the repository" "created Artifact Registry repository fullsend-e2e-gateway"
expect_out "copies the image" "copied ghcr.io/agentgateway/agentgateway:v1.6.0 to us-east5-docker.pkg.dev/${PROJECT}/fullsend-e2e-gateway/agentgateway:v1.6.0"
expect_out "creates the service account" "created service account"
expect_out "creates the config secret" "created secret ${CFG_SECRET}"
expect_out "creates the stub key secret" "created secret ${KEY_SECRET}"
expect_out "creates the service" "created Cloud Run service fullsend-e2e-gateway"
expect_out "prints the URL variable" "E2E_INFERENCE_GATEWAY_URL=https://gw.example.test"
expect_out "prints the audience variable" "E2E_INFERENCE_GATEWAY_AUDIENCE=fullsend-e2e-gateway"
expect_out "prints the no-token probe" "no token: GET /v1/models -> HTTP 401 text/plain"
expect_out "prints the x-api-key probe" "x-api-key only: GET /v1/models -> HTTP 401"

CFG="${STATE}/secrets/${CFG_SECRET}.v1"
if [[ "$(cat "${STATE}/secrets/${KEY_SECRET}.v1")" == "e2e-stub-upstream-key" ]]; then
  pass "stub key holds the fixed value"; else fail "stub key value"; fi
if [[ "$(grep -o '"halfsend[-0-9]*/test-repo-[0-9][0-9]"' "${CFG}" | sort -u | wc -l | tr -d ' ')" == "156" ]] \
    && grep -q '"halfsend/test-repo-12"' "${CFG}"; then
  pass "config allows the 13 x 12 pool repositories"; else fail "pool allowlist"; fi
if grep -q 'mode: strict' "${CFG}" && grep -q 'audiences: \[fullsend-e2e-gateway\]' "${CFG}" \
    && ! grep -q 'preserveToken: true' "${CFG}"; then
  pass "config validates GitHub OIDC strictly with the fixed audience"; else fail "jwtAuth"; fi
if grep -q "allow: 'jwt.repository == \"fullsend-e2e-gateway-outside/not-a-pool-repo\"'" "${CFG}"; then
  pass "echo-denied is authorised only outside the pool"; else fail "echo-denied rule"; fi
if [[ "$(grep -c 'requestHeaders: { remove: \[x-api-key\] }' "${CFG}")" == "2" ]]; then
  pass "every model strips x-api-key"; else fail "requestHeaders.remove"; fi
if ! grep -q 'provider: vertex' "${CFG}" && ! grep -q 'aiplatform' "${STATE}/project_policy"; then
  pass "no Vertex model or grant without --with-vertex"; else fail "unexpected Vertex"; fi
for s in "${CFG_SECRET}" "${KEY_SECRET}"; do
  if grep -qx "roles/secretmanager.secretAccessor ${SA}" "${STATE}/secrets/${s}.policy"; then
    pass "service account can read ${s}"; else fail "secretAccessor on ${s}"; fi
done
args=$(grep '^gcloud .* run deploy ' "${STATE}/gcloud.log" | tr ' ' '\n')
for a in --allow-unauthenticated --no-invoker-iam-check --cpu=1 --memory=512Mi --max-instances=1 \
    --port=8080 --args=-f,/etc/agw-config/config.yaml --labels=purpose=fullsend-e2e-gateway \
    "--image=us-east5-docker.pkg.dev/${PROJECT}/fullsend-e2e-gateway/agentgateway:v1.6.0" \
    "--set-secrets=/etc/agw-config/config.yaml=${CFG_SECRET}:latest,/etc/agw-upstream/key=${KEY_SECRET}:latest"; do
  if grep -qx -- "${a}" <<<"${args}"; then pass "deploys with ${a}"; else fail "deploy lacks ${a}"; fi
done

# --- 2. second run is a no-op -------------------------------------------------
before=$(mutations | wc -l | tr -d ' ')
run_setup --project "${PROJECT}" --without-vertex || fail "second run failed"
expect_out "second run reports no changes" "No changes: everything was already in place"
expect_no_mutations "second run mutates nothing" "${before}"
if [[ "$(revisions)" == "1" ]]; then pass "second run rolls no revision"; else fail "second run deployed"; fi

# --- 3. config change: --with-vertex adds a version and rolls one revision ------
before=$(mutations | wc -l | tr -d ' ')
run_setup --project "${PROJECT}" --with-vertex --dry-run && rc=0 || rc=$?
if [[ "${rc}" == "3" ]]; then pass "--dry-run with pending changes exits 3"; else fail "--dry-run exit ${rc}"; fi
expect_out "--dry-run lists the config change" "WOULD: added a new version of secret ${CFG_SECRET}"
expect_no_mutations "--dry-run mutates nothing" "${before}"

run_setup --project "${PROJECT}" --with-vertex || fail "--with-vertex run failed"
expect_out "--with-vertex adds a config version" "added a new version of secret ${CFG_SECRET}"
expect_out "--with-vertex grants aiplatform.user" "granted roles/aiplatform.user"
expect_out "--with-vertex rolls a revision" "rolled a new revision"
CFG2="${STATE}/secrets/${CFG_SECRET}.v2"
if grep -q 'name: claude-haiku-5-5' "${CFG2}" && grep -q 'name: gemini-3.8-flash' "${CFG2}" \
    && [[ "$(grep -c "vertexProject: ${PROJECT}, vertexRegion: global" "${CFG2}")" == "2" ]]; then
  pass "--with-vertex adds both Vertex models"; else fail "Vertex models"; fi
if [[ "$(cat "${STATE}/secrets/${KEY_SECRET}.latest")" == "1" ]]; then
  pass "--with-vertex leaves the stub key alone"; else fail "stub key re-versioned"; fi
if [[ "$(revisions)" == "2" ]] && grep -q '^gcloud .* run services update ' "${STATE}/gcloud.log"; then
  pass "a config change rolls one revision with services update"; else fail "revision after config change"; fi

before=$(mutations | wc -l | tr -d ' ')
run_setup --project "${PROJECT}" --with-vertex || fail "repeat --with-vertex failed"
expect_out "repeat --with-vertex is a no-op" "No changes"
expect_no_mutations "an orphaned config volume is not drift" "${before}"

# --- 4. no Vertex flag never removes the Vertex tier ---------------------------
before=$(mutations | wc -l | tr -d ' ')
if run_setup --project "${PROJECT}"; then fail "run without a Vertex flag removed Vertex"; else
  expect_out "run without a Vertex flag refuses" "Pass --with-vertex to keep them"; fi
expect_no_mutations "refused run mutates nothing" "${before}"
run_setup --project "${PROJECT}" --without-vertex || fail "--without-vertex failed"
expect_out "--without-vertex removes the grant" "removed roles/aiplatform.user"
if ! grep -q 'provider: vertex' "${STATE}/secrets/${CFG_SECRET}.v3"; then
  pass "--without-vertex removes the Vertex models"; else fail "Vertex models left"; fi

# --- 5. drift and not-Ready redeploy -------------------------------------------
jq '.spec.template.metadata.annotations["autoscaling.knative.dev/maxScale"] = "5"' \
  "${STATE}/svc.json" > "${STATE}/svc.tmp" && mv "${STATE}/svc.tmp" "${STATE}/svc.json"
run_setup --project "${PROJECT}" --without-vertex || fail "drift run failed"
expect_out "drift redeploys and names the field" "redeployed Cloud Run service fullsend-e2e-gateway \\(differed in: max instances\\)"
echo False > "${STATE}/svc_ready"
run_setup --project "${PROJECT}" --without-vertex || true
expect_out "not-Ready service is redeployed" "redeployed Cloud Run service fullsend-e2e-gateway \\(was not Ready\\)"
rm "${STATE}/svc_ready"

# --- 6. --delete removes exactly the named resources ---------------------------
run_setup --project "${PROJECT}" --delete --yes || fail "--delete failed"
for f in ar sa svc.json "secrets/${CFG_SECRET}.exists" "secrets/${KEY_SECRET}.exists"; do
  if [[ ! -e "${STATE}/${f}" ]]; then pass "--delete removed ${f}"; else fail "--delete left ${f}"; fi
done
run_setup --project "${PROJECT}" --delete --yes || fail "second --delete failed"
expect_out "second --delete is a no-op" "Nothing to delete"

# --- 7. adopt a gateway deployed by hand with the same names --------------------
# Shaped like a gateway built from the operator guide: no labels except on the
# service, three secret volumes of which two are mounted.
fresh_state
mkdir -p "${STATE}/secrets"
touch "${STATE}/ar" "${STATE}/sa"
echo "${UPSTREAM_DIGEST}" > "${STATE}/image"
echo "roles/aiplatform.user ${SA}" > "${STATE}/project_policy"
PATH="${SHIM_DIR}:${PATH}" bash "${SETUP}" --project "${PROJECT}" --with-vertex --print-config \
  > "${STATE}/secrets/${CFG_SECRET}.v9"
printf 'e2e-stub-upstream-key' > "${STATE}/secrets/${KEY_SECRET}.v1"
echo 9 > "${STATE}/secrets/${CFG_SECRET}.latest"
echo 1 > "${STATE}/secrets/${KEY_SECRET}.latest"
for s in "${CFG_SECRET}" "${KEY_SECRET}"; do
  touch "${STATE}/secrets/${s}.exists"
  echo "roles/secretmanager.secretAccessor ${SA}" > "${STATE}/secrets/${s}.policy"
done
jq -n --arg img "us-east5-docker.pkg.dev/${PROJECT}/fullsend-e2e-gateway/agentgateway:v1.6.0" \
  --arg sa "${SA#serviceAccount:}" --arg cfg "${CFG_SECRET}" --arg key "${KEY_SECRET}" '
  def vol($n; $s; $f): {name: $n, secret: {secretName: $s, items: [{key: "latest", path: $f}]}};
  {metadata: {labels: {purpose: "fullsend-e2e-gateway", "cloud.googleapis.com/location": "us-east5"},
              annotations: {"run.googleapis.com/invoker-iam-disabled": "true", "run.googleapis.com/ingress": "all"}},
   spec: {template: {
     metadata: {annotations: {"autoscaling.knative.dev/maxScale": "1"}},
     spec: {serviceAccountName: $sa, containerConcurrency: 80,
       volumes: [vol("key-a"; $key; "key"), vol("cfg-old"; $cfg; "config.yaml"), vol("cfg-new"; $cfg; "config.yaml")],
       containers: [{image: $img, args: ["-f", "/etc/agw-config/config.yaml"],
         ports: [{containerPort: 8080, name: "http1"}],
         resources: {limits: {cpu: "1", memory: "512Mi"}},
         volumeMounts: [{mountPath: "/etc/agw-config", name: "cfg-new"},
                        {mountPath: "/etc/agw-upstream", name: "key-a"}]}]}}}}' > "${STATE}/svc.json"
run_setup --project "${PROJECT}" --with-vertex --dry-run || fail "adopt --dry-run reported changes"
expect_out "adopt --dry-run reports no changes" "No changes: everything was already in place"
run_setup --project "${PROJECT}" --with-vertex || fail "adopt run failed"
expect_out "adopt run reports no changes" "No changes: everything was already in place"
expect_no_mutations "adopt run mutates nothing" 0
if run_setup --project "${PROJECT}"; then fail "adopt without a Vertex flag accepted"; else
  expect_out "adopt without a Vertex flag refuses" "serves Vertex models"; fi

# --- 8. failures ------------------------------------------------------------------
fresh_state
echo "sha256:tampered" > "${STATE}/copy_digest"
if run_setup --project "${PROJECT}" --without-vertex; then fail "digest mismatch accepted"; else
  expect_out "digest mismatch fails" "not upstream ${UPSTREAM_DIGEST}"; fi

fresh_state
echo "sha256:tampered" > "${STATE}/image"
if run_setup --project "${PROJECT}" --without-vertex; then fail "existing tampered image accepted"; else
  expect_out "existing tampered image is refused" "Refusing to overwrite it"; fi

fresh_state
touch "${STATE}/html_401"
if run_setup --project "${PROJECT}" --without-vertex; then fail "HTML 401 accepted"; else
  expect_out "HTML 401 fails verification" "invoker IAM check is still on"; fi

fresh_state
touch "${STATE}/deny_secrets"
if run_setup --project "${PROJECT}" --without-vertex; then fail "describe error treated as missing"; else
  expect_out "describe error fails closed" "PERMISSION_DENIED"; fi
if ! grep -q 'secrets create' "${STATE}/gcloud.log"; then
  pass "describe error creates nothing"; else fail "describe error created a secret"; fi

fresh_state
if run_setup --project; then fail "--project without a value accepted"; else
  expect_out "--project needs a value" "--project needs a value"; fi

fresh_state
touch "${STATE}/api_disabled_run"
if run_setup --project "${PROJECT}" --without-vertex; then fail "disabled API accepted"; else
  expect_out "disabled API is reported" "APIs not enabled: run.googleapis.com"; fi

fresh_state
if run_setup --delete --with-vertex --project "${PROJECT}"; then fail "--delete with --with-vertex accepted"; else
  expect_out "--delete rejects other modes" "cannot be combined"; fi

fresh_state
if run_setup --without-vertex; then fail "ran without a project"; else
  expect_out "project is required" "no project"; fi
if E2E_GCP_PROJECT_ID="${PROJECT}" env PATH="${SHIM_DIR}:${PATH}" STUB_STATE="${STATE}" \
    bash "${SETUP}" --without-vertex > "${STATE}/out" 2>&1; then
  pass "E2E_GCP_PROJECT_ID is the default project"; else fail "E2E_GCP_PROJECT_ID ignored"; fi

echo
if (( FAILURES > 0 )); then
  echo "${FAILURES} failure(s)" >&2
  exit 1
fi
echo "all tests passed"
