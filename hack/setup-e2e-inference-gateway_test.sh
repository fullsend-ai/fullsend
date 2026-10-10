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
# The upstream multi-arch index, and the linux/amd64 image Cloud Run runs.
AMD64_DIGEST="sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
printf '{"manifests":[{"digest":"%s","platform":{"os":"linux","architecture":"amd64"}}]}' \
  "${AMD64_DIGEST}" > "${SHIM_DIR}/upstream.json"

# --- gcloud: Artifact Registry, IAM, Secret Manager and Cloud Run state ----
cat > "${SHIM_DIR}/gcloud" <<'EOF'
#!/usr/bin/env bash
S="${STUB_STATE}"
echo "gcloud $*" >> "${S}/gcloud.log"
stub_sha256() { if command -v sha256sum >/dev/null; then sha256sum | cut -d' ' -f1; else shasum -a 256 | cut -d' ' -f1; fi; }
# Drop the global flags the script always passes.
while [[ "${1:-}" == --project=* || "${1:-}" == --quiet ]]; do shift; done
notfound() { echo "ERROR: NOT_FOUND: $1" >&2; exit 1; }
flag() { # flag NAME ARGS... -> value of --NAME=VALUE
  local n="$1" a; shift
  for a in "$@"; do [[ "${a}" == "--${n}="* ]] && { echo "${a#--"${n}"=}"; return; }; done
}
policy_json() { # policy_json FILE -> bindings from "role member [conditional]" lines
  jq -Rn '[inputs | split(" ") | {role: .[0], members: [.[1]]}
    + (if .[2] then {condition: {expression: "false"}} else {} end)] | {bindings: .}' < "$1"
}
tick() { # advance the stub clock; print the new time
  local n
  n=$(( $(cat "${S}/clock" 2>/dev/null || echo 1000) + 10 ))
  echo "${n}" > "${S}/clock"
  jq -rn --argjson n "${n}" '$n | todate'
}
# volume NAME SECRET FILE -> a Cloud Run secret volume
volume() {
  jq -n --arg n "$1" --arg s "$2" --arg f "$3" \
    '{name: $n, secret: {secretName: $s, items: [{key: "latest", path: $f}]}}'
}
labels_of() { # labels_of FILE -> {"k": "v"} from a "k=v" line, or {}
  jq -Rn '[inputs | select(length > 0) | split("=") | {(.[0]): .[1]}] | add // {}' < "$1"
}
UPSTREAM="$(dirname "$0")/upstream.json"
AMD64="sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
mkdir -p "${S}/secrets"
case "$1 $2 $3" in
  "auth print-access-token "*) echo "stub-access-token" ;;
  "services list "*)
    for a in run secretmanager artifactregistry iam aiplatform; do
      [[ -f "${S}/api_disabled_${a}" ]] || echo "${a}.googleapis.com"
    done ;;
  "artifacts repositories describe")
    [[ -f "${S}/ar" ]] || notfound "repository"
    jq -n --argjson l "$(labels_of "${S}/ar")" '{labels: $l}' ;;
  "artifacts repositories create") flag labels "$@" > "${S}/ar" ;;
  "artifacts repositories delete") rm "${S}/ar" ;;
  "iam service-accounts describe")
    [[ -f "${S}/sa" ]] || notfound "Unknown service account"
    jq -Rn '{description: (input? // null)}' < "${S}/sa" ;;
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
    [[ -f "${S}/secrets/$3.exists" ]] || notfound "Secret [$3] not found"
    jq -n --argjson l "$(labels_of "${S}/secrets/$3.exists")" '{labels: $l}' ;;
  "secrets create "*)
    flag labels "$@" > "${S}/secrets/$3.exists"
    cp "$(flag data-file "$@")" "${S}/secrets/$3.v1"
    tick > "${S}/secrets/$3.v1.time"
    echo 1 > "${S}/secrets/$3.latest" ;;
  "secrets delete "*) rm -f "${S}/secrets/$3".* ;;
  "secrets get-iam-policy "*)
    touch "${S}/secrets/$3.policy"; policy_json "${S}/secrets/$3.policy" ;;
  "secrets add-iam-policy-binding "*)
    echo "$(flag role "$@") $(flag member "$@")" >> "${S}/secrets/$3.policy" ;;
  "secrets versions access")
    [[ -f "${S}/deny_access" ]] && { echo "ERROR: PERMISSION_DENIED: secretmanager.versions.access" >&2; exit 1; }
    sec=$(flag secret "$@"); n=$(cat "${S}/secrets/${sec}.latest" 2>/dev/null) \
      || notfound "Secret [${sec}] not found or has no versions"
    cat "${S}/secrets/${sec}.v${n}" ;;
  "secrets versions describe")
    sec=$(flag secret "$@"); n=$(cat "${S}/secrets/${sec}.latest" 2>/dev/null) \
      || notfound "Secret [${sec}] has no versions"
    jq -n --arg t "$(cat "${S}/secrets/${sec}.v${n}.time")" '{createTime: $t}' ;;
  "secrets versions add")
    n=$(( $(cat "${S}/secrets/$4.latest" 2>/dev/null || echo 0) + 1 ))
    cp "$(flag data-file "$@")" "${S}/secrets/$4.v${n}"
    tick > "${S}/secrets/$4.v${n}.time"
    echo "${n}" > "${S}/secrets/$4.latest" ;;
  "run services list")
    # A run/region default narrows the listing to that region.
    if [[ -n "${CLOUDSDK_RUN_REGION:-}" ]]; then echo '[]'; exit 0; fi
    # Like gcloud: an error-only verbosity hides the warning unless the call
    # pins --verbosity=warning.
    if [[ -f "${S}/list_partial" ]] && { [[ "${CLOUDSDK_CORE_VERBOSITY:-}" != error ]] \
        || printf '%s\n' "$@" | grep -qx -- --verbosity=warning; }; then
      echo "WARNING: The following regions were unreachable: europe-west1" >&2
    fi
    { [[ -f "${S}/svc.json" ]] && jq '.metadata.labels["cloud.googleapis.com/location"] = "us-east5"' "${S}/svc.json"
      [[ -f "${S}/svc_elsewhere" ]] && jq -n '{metadata: {labels: {"cloud.googleapis.com/location": "europe-west1"}}}'
      true; } | jq -s . ;;
  "run services describe")
    [[ -f "${S}/svc.json" ]] || { echo "ERROR: (gcloud.run.services.describe) Cannot find service [$4]" >&2; exit 1; }
    jq --arg r "$(cat "${S}/svc_ready" 2>/dev/null || echo True)" \
      '.status = {url: "https://gw.example.test", latestReadyRevisionName: "rev",
        conditions: [{type: "Ready", status: $r}]}' "${S}/svc.json" ;;
  "run revisions describe")
    jq -n --arg t "$(cat "${S}/revision_time")" --arg i "$(cat "${S}/revision_image")" \
      '{metadata: {creationTimestamp: $t}, status: {imageDigest: $i}}' ;;
  "run services update-traffic")
    jq '.spec.traffic = [{latestRevision: true, percent: 100}]' "${S}/svc.json" > "${S}/svc.tmp" \
      && mv "${S}/svc.tmp" "${S}/svc.json"
    echo traffic >> "${S}/traffic" ;;
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
       spec: {traffic: [{latestRevision: true, percent: 100}], template: {
         metadata: {annotations: {"autoscaling.knative.dev/maxScale": $max}},
         spec: {serviceAccountName: $sa, volumes: [$v1, $v2],
           containers: [{image: $img, args: ($args | split(",")),
             ports: [{containerPort: $port}],
             resources: {limits: {cpu: $cpu, memory: $mem}},
             volumeMounts: [{mountPath: $cfgdir, name: "cfg-1"},
                            {mountPath: $keydir, name: "key-1"}]}]}}}}' > "${S}/svc.json"
    tick > "${S}/revision_time"
    img=$(flag image "$@"); base="${img%@*}"; [[ "${base}" == "${img}" ]] && base="${img%:*}"
    echo "${base}@${AMD64}" > "${S}/revision_image"
    echo deploy >> "${S}/revisions" ;;
  "run services update")
    # Like gcloud 588: the mount keeps its volume, a new revision rolls, and
    # the template image is pinned to the running revision's image digest
    # (the linux/amd64 image) when no new image is given.
    jq --arg d "$(cat "${S}/revision_image")" '.spec.template.spec.containers[0].image = $d' \
      "${S}/svc.json" > "${S}/svc.tmp" && mv "${S}/svc.tmp" "${S}/svc.json"
    tick > "${S}/revision_time"
    [[ -f "${S}/bad_running" ]] || { img=$(jq -r '.spec.template.spec.containers[0].image' "${S}/svc.json"); \
      echo "${img%@*}@${AMD64}" > "${S}/revision_image"; }
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
stub_sha256() { if command -v sha256sum >/dev/null; then sha256sum | cut -d' ' -f1; else shasum -a 256 | cut -d' ' -f1; fi; }
ref="${*: -1}"
UPSTREAM="$(dirname "$0")/upstream.json"
case "$1" in
  login) cat > /dev/null ;;
  inspect)
    case "${ref}" in
      docker://ghcr.io/*)
        [[ -f "${S}/upstream_down" ]] && { echo "FATAL: connection refused" >&2; exit 1; }
        cat "${UPSTREAM}"; [[ -f "${S}/trailing_newline" ]] && printf '\n'; true ;;
      *) [[ -f "${S}/image_raw" ]] || { echo "FATAL: manifest unknown" >&2; exit 1; }
         cat "${S}/image_raw"; [[ -f "${S}/trailing_newline" ]] && printf '\n'; true ;;
    esac ;;
  copy) cat "${S}/copy_raw" 2>/dev/null > "${S}/image_raw" || cat "${UPSTREAM}" > "${S}/image_raw" ;;
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
if [[ -f "${S}/curl_down" ]]; then
  echo "curl: (6) Could not resolve host" >&2
  exit 6
elif [[ -f "${S}/json_401" ]]; then
  echo '{"error":"unauthorized"}' > "${out}"
  printf '401 application/json'
elif [[ -f "${S}/html_401" ]]; then
  echo '<html><body>401 Unauthorized</body></html>' > "${out}"
  printf '401 text/html; charset=UTF-8'
else
  echo 'authentication failure: no bearer token found' > "${out}"
  printf '401 text/plain'
fi
EOF
chmod +x "${SHIM_DIR}"/*

stub_sha256() { if command -v sha256sum >/dev/null; then sha256sum | cut -d' ' -f1; else shasum -a 256 | cut -d' ' -f1; fi; }
UPSTREAM_DIGEST="sha256:$(stub_sha256 < "${SHIM_DIR}/upstream.json")"
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
    | grep -E ' (create|add|add-iam-policy-binding|remove-iam-policy-binding|deploy|update|update-traffic|delete|copy) ' || true
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
    "--image=us-east5-docker.pkg.dev/${PROJECT}/fullsend-e2e-gateway/agentgateway@${UPSTREAM_DIGEST}" \
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
if jq -e '.spec.template.spec.containers[0].image | test("@sha256:")' "${STATE}/svc.json" >/dev/null; then
  pass "an image pinned by digest after services update is not drift"; else fail "stub did not pin the image"; fi
expect_no_mutations "a re-run after services update mutates nothing" "${before}"

# --- 4. no Vertex flag never removes the Vertex models -------------------------
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
run_setup --project "${PROJECT}" --without-vertex --dry-run && rc=0 || rc=$?
if [[ "${rc}" == "3" ]]; then pass "--dry-run on a not-Ready service exits 3 (pending change)"; else fail "not-Ready --dry-run exit ${rc}"; fi
rm "${STATE}/svc_ready"

# A config version added by an earlier run that stopped before the rollout.
cp "${STATE}/secrets/${CFG_SECRET}.v3" "${STATE}/secrets/${CFG_SECRET}.v4"
later=$(( $(cat "${STATE}/clock") + 10 ))
jq -rn --argjson n "${later}" '$n | todate' > "${STATE}/secrets/${CFG_SECRET}.v4.time"
echo "${later}" > "${STATE}/clock"
echo 4 > "${STATE}/secrets/${CFG_SECRET}.latest"
revs=$(revisions)
run_setup --project "${PROJECT}" --without-vertex || fail "interrupted rollout run failed"
expect_out "an unfinished rollout is completed" "rolled a new revision of Cloud Run service fullsend-e2e-gateway for the latest secret versions"
if [[ "$(revisions)" == "$((revs + 1))" && "$(cat "${STATE}/secrets/${CFG_SECRET}.latest")" == "4" ]]; then
  pass "completing the rollout adds no secret version"; else fail "interrupted rollout"; fi
run_setup --project "${PROJECT}" --without-vertex || fail "run after completed rollout failed"
expect_out "after the rollout a re-run is a no-op" "No changes"

# Traffic pinned to an older revision.
jq '.spec.traffic = [{revisionName: "old", percent: 100}]' "${STATE}/svc.json" > "${STATE}/svc.tmp" \
  && mv "${STATE}/svc.tmp" "${STATE}/svc.json"
run_setup --project "${PROJECT}" --without-vertex || fail "pinned traffic run failed"
expect_out "pinned traffic is sent to the latest revision" "sent all traffic of Cloud Run service fullsend-e2e-gateway to the latest revision"
run_setup --project "${PROJECT}" --without-vertex || fail "run after traffic fix failed"
expect_out "after the traffic fix a re-run is a no-op" "No changes"

# An older revision still reachable through a 0% tag.
jq '.spec.traffic += [{revisionName: "old", percent: 0, tag: "old"}]' "${STATE}/svc.json" > "${STATE}/svc.tmp" \
  && mv "${STATE}/svc.tmp" "${STATE}/svc.json"
run_setup --project "${PROJECT}" --without-vertex || fail "tagged revision run failed"
expect_out "a tagged older revision is cleared" "to the latest revision and cleared tags"
if grep -q -- 'update-traffic .*--clear-tags' "${STATE}/gcloud.log"; then pass "update-traffic clears tags"; else fail "no --clear-tags"; fi

# A conditional grant is not the script's grant.
echo "roles/aiplatform.user ${SA} conditional" >> "${STATE}/project_policy"
run_setup --project "${PROJECT}" --without-vertex || fail "conditional grant run failed"
expect_out "a conditional grant does not count as the Vertex grant" "No changes"

# A secret whose policy holds only a conditional binding gets an
# unconditional one, added with --condition=None.
echo "roles/secretmanager.secretAccessor ${SA} conditional" > "${STATE}/secrets/${KEY_SECRET}.policy"
run_setup --project "${PROJECT}" --without-vertex || fail "conditional secret binding run failed"
if grep -q -- "secrets add-iam-policy-binding ${KEY_SECRET} .*--condition=None" "${STATE}/gcloud.log"; then
  pass "a conditional-only secret policy gets an unconditional binding"; else fail "secret binding lacks --condition=None"; fi

# A secret version created later in the same second as the serving revision.
echo 2027-01-01T00:00:03.100Z > "${STATE}/revision_time"
echo 2027-01-01T00:00:03.900Z > "${STATE}/secrets/${CFG_SECRET}.v4.time"
run_setup --project "${PROJECT}" --without-vertex || fail "same-second run failed"
expect_out "a same-second newer secret version is rolled out" "rolled a new revision of Cloud Run service fullsend-e2e-gateway for the latest secret versions"

# An unparseable timestamp stops the run instead of skipping the rollout.
echo 2099-01-01T00:00:00Z > "${STATE}/revision_time"
echo not-a-time > "${STATE}/secrets/${CFG_SECRET}.v4.time"
before=$(mutations | wc -l | tr -d ' ')
if run_setup --project "${PROJECT}" --without-vertex; then fail "unparseable timestamp accepted"; else
  expect_out "an unparseable timestamp fails" "could not compare timestamps"; fi
expect_no_mutations "an unparseable timestamp mutates nothing" "${before}"
echo 2026-12-31T00:00:00Z > "${STATE}/secrets/${CFG_SECRET}.v4.time"

# --- 6. --delete removes exactly the named resources ---------------------------
touch "${STATE}/list_partial"
export CLOUDSDK_CORE_VERBOSITY=error
before=$(mutations | wc -l | tr -d ' ')
if run_setup --project "${PROJECT}" --delete --yes; then fail "--delete trusted a partial listing"; else
  expect_out "--delete refuses a partial service listing" "listing may be incomplete"; fi
expect_no_mutations "partial-listing --delete deletes nothing" "${before}"
rm "${STATE}/list_partial"
unset CLOUDSDK_CORE_VERBOSITY
touch "${STATE}/svc_elsewhere"
export CLOUDSDK_RUN_REGION=us-east5
before=$(mutations | wc -l | tr -d ' ')
if run_setup --project "${PROJECT}" --delete --yes; then fail "--delete ignored a service in another region"; else
  expect_out "--delete refuses while the service runs in another region" "runs in europe-west1, not us-east5"; fi
expect_no_mutations "region-mismatch --delete deletes nothing" "${before}"
rm "${STATE}/svc_elsewhere"
unset CLOUDSDK_RUN_REGION
jq '.metadata.labels = {}' "${STATE}/svc.json" > "${STATE}/svc.tmp" && cp "${STATE}/svc.json" "${STATE}/svc.bak" \
  && mv "${STATE}/svc.tmp" "${STATE}/svc.json"
if run_setup --project "${PROJECT}" --delete --yes; then fail "--delete removed an unlabelled service"; else
  expect_out "--delete refuses an unlabelled service" "unmarked: Cloud Run service fullsend-e2e-gateway"; fi
expect_no_mutations "unlabelled-service --delete deletes nothing" "${before}"
mv "${STATE}/svc.bak" "${STATE}/svc.json"

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
cat "${SHIM_DIR}/upstream.json" > "${STATE}/image_raw"
echo "us-east5-docker.pkg.dev/${PROJECT}/fullsend-e2e-gateway/agentgateway@${AMD64_DIGEST}" > "${STATE}/revision_image"
echo "roles/aiplatform.user ${SA}" > "${STATE}/project_policy"
PATH="${SHIM_DIR}:${PATH}" bash "${SETUP}" --project "${PROJECT}" --with-vertex --print-config \
  > "${STATE}/secrets/${CFG_SECRET}.v9"
printf 'e2e-stub-upstream-key' > "${STATE}/secrets/${KEY_SECRET}.v1"
echo 9 > "${STATE}/secrets/${CFG_SECRET}.latest"
echo 1 > "${STATE}/secrets/${KEY_SECRET}.latest"
# Real timestamp shapes: fractional seconds, secret versions before the revision.
echo 2026-01-02T03:04:01.797859Z > "${STATE}/secrets/${CFG_SECRET}.v9.time"
echo 2026-01-02T02:50:38.762068Z > "${STATE}/secrets/${KEY_SECRET}.v1.time"
echo 2026-01-02T03:04:03.154420Z > "${STATE}/revision_time"
for s in "${CFG_SECRET}" "${KEY_SECRET}"; do
  : > "${STATE}/secrets/${s}.exists"
  echo "roles/secretmanager.secretAccessor ${SA}" > "${STATE}/secrets/${s}.policy"
done
jq -n --arg img "us-east5-docker.pkg.dev/${PROJECT}/fullsend-e2e-gateway/agentgateway:v1.6.0" \
  --arg sa "${SA#serviceAccount:}" --arg cfg "${CFG_SECRET}" --arg key "${KEY_SECRET}" '
  def vol($n; $s; $f): {name: $n, secret: {secretName: $s, items: [{key: "latest", path: $f}]}};
  {metadata: {labels: {purpose: "fullsend-e2e-gateway", "cloud.googleapis.com/location": "us-east5"},
              annotations: {"run.googleapis.com/invoker-iam-disabled": "true", "run.googleapis.com/ingress": "all"}},
   spec: {traffic: [{latestRevision: true, percent: 100}], template: {
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
touch "${STATE}/trailing_newline"
run_setup --project "${PROJECT}" --with-vertex || fail "trailing-newline manifest run failed"
expect_out "a manifest ending in a newline still matches upstream" "matches upstream"
rm "${STATE}/trailing_newline"
if run_setup --project "${PROJECT}"; then fail "adopt without a Vertex flag accepted"; else
  expect_out "adopt without a Vertex flag refuses" "serves Vertex models"; fi
# The Vertex guard fails closed when the config cannot be read.
: > "${STATE}/project_policy"
touch "${STATE}/deny_access"
if run_setup --project "${PROJECT}"; then fail "unreadable config treated as no Vertex"; else
  expect_out "unreadable config fails closed" "could not read secret ${CFG_SECRET}"; fi
expect_no_mutations "unreadable config mutates nothing" 0
rm "${STATE}/deny_access"
echo "roles/aiplatform.user ${SA}" > "${STATE}/project_policy"

# A revision running an image other than the verified one is redeployed.
echo "us-east5-docker.pkg.dev/${PROJECT}/fullsend-e2e-gateway/agentgateway@sha256:other" > "${STATE}/revision_image"
cp "${STATE}/svc.json" "${STATE}/svc.adopted"
cp "${STATE}/revision_image" "${STATE}/revision_image.adopted"
run_setup --project "${PROJECT}" --with-vertex --dry-run && rc=0 || rc=$?
if [[ "${rc}" == "3" ]]; then pass "an unverified running image is a pending change"; else fail "unverified image dry-run exit ${rc}"; fi
expect_out "an unverified running image is reported" "runs an unverified image"
expect_no_mutations "unverified-image --dry-run mutates nothing" 0

# A config rollout on the adopted (tag-deployed) gateway, then an unchanged
# re-run: the pinned linux/amd64 image is not drift.
cp "${STATE}/svc.adopted" "${STATE}/svc.json"
echo "us-east5-docker.pkg.dev/${PROJECT}/fullsend-e2e-gateway/agentgateway@${AMD64_DIGEST}" > "${STATE}/revision_image"
echo 2099-01-01T00:00:00Z > "${STATE}/secrets/${CFG_SECRET}.v9.time"
run_setup --project "${PROJECT}" --with-vertex || fail "adopted rollout failed"
expect_out "the adopted gateway rolls for a newer secret" "rolled a new revision"
echo 2099-01-01T00:00:05Z > "${STATE}/revision_time"  # the new revision is newer
before=$(mutations | wc -l | tr -d ' ')
run_setup --project "${PROJECT}" --with-vertex || fail "re-run after adopted rollout failed"
expect_out "a re-run after the adopted rollout is a no-op" "No changes"
expect_no_mutations "a pinned linux/amd64 image is not drift" "${before}"
echo 2026-01-02T03:04:01.797859Z > "${STATE}/secrets/${CFG_SECRET}.v9.time"

# --delete of the adopted gateway: unmarked resources stop it before anything goes.
before=$(mutations | wc -l | tr -d ' ')
echo "us-east5-docker.pkg.dev/${PROJECT}/fullsend-e2e-gateway/agentgateway@${AMD64_DIGEST}" > "${STATE}/revision_image"
if run_setup --project "${PROJECT}" --delete --yes; then fail "--delete removed unmarked adopted resources"; else
  expect_out "--delete lists the unmarked resources" "unmarked: secret ${CFG_SECRET}"; fi
expect_no_mutations "--delete of an adopted gateway deletes nothing without opt-in" "${before}"
run_setup --project "${PROJECT}" --delete --yes --include-unlabelled || fail "--include-unlabelled delete failed"
for f in ar sa svc.json "secrets/${CFG_SECRET}.exists" "secrets/${KEY_SECRET}.exists"; do
  if [[ ! -e "${STATE}/${f}" ]]; then pass "--include-unlabelled removed ${f}"; else fail "--include-unlabelled left ${f}"; fi
done
if ! grep -q aiplatform "${STATE}/project_policy"; then pass "--include-unlabelled removed the Vertex grant"; else fail "Vertex grant left"; fi

# Unmarked resources with the reserved names, service absent: never deleted.
fresh_state
mkdir -p "${STATE}/secrets"
touch "${STATE}/ar" "${STATE}/sa"
: > "${STATE}/secrets/${CFG_SECRET}.exists"
if run_setup --project "${PROJECT}" --delete --yes; then fail "--delete removed unmarked resources"; else
  expect_out "--delete refuses unmarked resources when the service is absent" "nothing deleted"; fi
if [[ -e "${STATE}/ar" && -e "${STATE}/sa" && -e "${STATE}/secrets/${CFG_SECRET}.exists" ]]; then
  pass "unmarked resources are left in place"; else fail "an unmarked resource was deleted"; fi

# --- 8. failures ------------------------------------------------------------------
fresh_state
printf 'tampered' > "${STATE}/copy_raw"
if run_setup --project "${PROJECT}" --without-vertex; then fail "digest mismatch accepted"; else
  expect_out "digest mismatch fails" "not upstream ${UPSTREAM_DIGEST}"; fi

fresh_state
printf 'tampered' > "${STATE}/image_raw"
if run_setup --project "${PROJECT}" --without-vertex; then fail "existing tampered image accepted"; else
  expect_out "existing tampered image is refused" "Refusing to overwrite it"; fi

fresh_state
run_setup --project "${PROJECT}" --without-vertex --dry-run && rc=0 || rc=$?
if [[ "${rc}" == "3" ]]; then pass "--dry-run on an empty project exits 3"; else fail "empty --dry-run exit ${rc}"; fi
expect_out "--dry-run on an empty project plans the service" "WOULD: created Cloud Run service"
expect_no_mutations "--dry-run on an empty project mutates nothing" 0

fresh_state
touch "${STATE}/upstream_down"
if run_setup --project "${PROJECT}" --without-vertex; then fail "unreadable upstream accepted"; else
  expect_out "unreadable upstream fails" "could not read ghcr.io/agentgateway/agentgateway:v1.6.0"; fi

fresh_state
run_setup --project "${PROJECT}" --without-vertex || fail "setup before curl failure failed"
touch "${STATE}/curl_down"
if run_setup --project "${PROJECT}" --without-vertex; then fail "unreachable gateway accepted"; else
  expect_out "an unreachable gateway is reported, not a crash" "no token: GET /v1/models -> HTTP curl-failed"
  expect_out "an unreachable gateway still prints the summary" "Verification failed"; fi

fresh_state
touch "${STATE}/json_401"
if run_setup --project "${PROJECT}" --without-vertex; then fail "non-agentgateway 401 accepted"; else
  expect_out "a 401 that is not agentgateway's fails verification" "not agentgateway's text/plain"; fi

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
if bash "${SETUP}" --help 2>/dev/null | grep -q '^Usage: hack/setup-e2e-inference-gateway.sh'; then
  pass "--help prints usage to stdout"; else fail "--help output"; fi

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
