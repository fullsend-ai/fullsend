#!/usr/bin/env bash
#
# create-gcp-vm.sh — Create and provision a GitLab Runner VM on GCE.
#
# This script:
#   1. Auto-numbers the VM (fullsend-gitlab-runner-01, -02, ...)
#   2. Creates a GCE VM via gcloud compute instances create
#   3. Waits for SSH readiness, then installs packages via dnf
#   4. Registers a new runner (project-scoped or group-scoped) via the GitLab API
#   5. Copies setup files and runs setup.sh to configure the custom
#      executor, OpenShell gateway, and pre-pull images
#
# When done, the runner is online and accepting jobs tagged with RUNNER_TAG.
#
# Prerequisites (one-time GCP setup):
#   - Compute Engine API and Cloud IAP API enabled in the target project
#   - VPC network "gitlab-runners" (auto-subnets, or set GCP_SUBNET for custom-mode)
#   - Firewall rules:
#     - gitlab-runners-allow-iap (ingress TCP:22 from 35.235.240.0/20, tag: gitlab-runner)
#     - gitlab-runners-allow-egress (egress TCP:80,443, tag: gitlab-runner)
#   - IAM: operator needs roles/iap.tunnelResourceAccessor on the project
#
# Required environment variables:
#   GL_TOKEN     — GitLab personal access token (Owner role on the target
#                  group or project, scopes: create_runner + manage_runner + api)
#   PROJECT_ID   — GitLab project ID (mutually exclusive with GROUP_ID)
#   GROUP_ID     — GitLab group ID  (mutually exclusive with PROJECT_ID)
#                  Exactly one of PROJECT_ID or GROUP_ID must be set.
#                  GROUP_ID is recommended for platform-service deployments.
#   GITLAB_URL   — GitLab instance URL (e.g. https://gitlab.example.com)
#   GCP_PROJECT  — GCP project ID
#   RUNNER_IMAGE — image pre-pulled as warm cache (e.g. ghcr.io/org/runner:v1.2.3)
#
# Optional environment variables:
#   GCP_ZONE              — GCE zone (default: us-east1-b)
#   GCP_MACHINE_TYPE      — machine type (default: e2-standard-4)
#   GCP_NETWORK           — VPC network (default: gitlab-runners)
#   GCP_SUBNET            — VPC subnet (required for custom-mode VPCs; omit for auto-mode)
#   GCP_IMAGE_FAMILY      — GCE image family (default: fedora-cloud-43)
#   GCP_IMAGE_PROJECT     — GCE image project (default: fedora-cloud)
#   RUNNER_TAG            — runner tag for job matching (default: fullsend-gitlab-runner)
#   GITLAB_RUNNER_VERSION — gitlab-runner version to install (default: 19.2.1)
#   OPENSHELL_VERSION     — OpenShell version (default: from .github/scripts/openshell-version.sh)
#   GCP_USE_IAP           — use IAP tunneling for SSH (default: true). Set to
#                           false to create the VM with an external IP and SSH
#                           directly. Useful for initial provisioning; the
#                           external IP can be removed afterward.
#   RUNNER_ACCESS_LEVEL   — not_protected (default) or ref_protected. Protected
#                           runners only pick up jobs on protected branches and
#                           tags, so merge-request pipelines never match.
#
# Arguments:
#   [NUMBER]  — optional runner number (e.g. 01, 03). Auto-increments if omitted.
#
# Examples:
#   # Group-scoped runner (recommended):
#   GL_TOKEN=glpat-xxx GROUP_ID=12345 \
#     GITLAB_URL=https://gitlab.example.com \
#     GCP_PROJECT=my-gcp-project \
#     RUNNER_IMAGE=ghcr.io/org/runner:v1.2.3 ./create-gcp-vm.sh
#
#   # Project-scoped runner:
#   GL_TOKEN=glpat-xxx PROJECT_ID=12345 \
#     GITLAB_URL=https://gitlab.example.com \
#     GCP_PROJECT=my-gcp-project \
#     RUNNER_IMAGE=ghcr.io/org/runner:v1.2.3 ./create-gcp-vm.sh
#
#   # Explicit runner number:
#   GL_TOKEN=glpat-xxx GROUP_ID=12345 \
#     GITLAB_URL=https://gitlab.example.com \
#     GCP_PROJECT=my-gcp-project \
#     RUNNER_IMAGE=ghcr.io/org/runner:v1.2.3 ./create-gcp-vm.sh 01
#
set -euo pipefail

GITLAB_URL="${GITLAB_URL:-}"
GCP_PROJECT="${GCP_PROJECT:-}"
GCP_ZONE="${GCP_ZONE:-us-east1-b}"
GCP_MACHINE_TYPE="${GCP_MACHINE_TYPE:-e2-standard-4}"
GCP_NETWORK="${GCP_NETWORK:-gitlab-runners}"
GCP_SUBNET="${GCP_SUBNET:-}"
GCP_IMAGE_FAMILY="${GCP_IMAGE_FAMILY:-fedora-cloud-43}"
GCP_IMAGE_PROJECT="${GCP_IMAGE_PROJECT:-fedora-cloud}"
GCP_USE_IAP="${GCP_USE_IAP:-true}"
RUNNER_TAG="${RUNNER_TAG:-fullsend-gitlab-runner}"
RUNNER_IMAGE="${RUNNER_IMAGE:-}"
# ref_protected restricts the runner to jobs on protected branches and tags.
# Merge-request pipelines run on the (unprotected) source ref, so the default
# is not_protected; scoping comes from runner_type + locked/run_untagged settings.
RUNNER_ACCESS_LEVEL="${RUNNER_ACCESS_LEVEL:-not_protected}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Source the central gitlab-runner version pin (shared with setup.sh).
_runner_version_sh="${SCRIPT_DIR}/gitlab-runner-version.sh"
if [ -f "${_runner_version_sh}" ]; then
  # shellcheck source=gitlab-runner-version.sh
  source "${_runner_version_sh}"
fi
GITLAB_RUNNER_VERSION="${GITLAB_RUNNER_VERSION:-19.2.1}"
# Source the repo-wide OpenShell version pin (Renovate-tracked).
_openshell_version_sh="${SCRIPT_DIR}/../../.github/scripts/openshell-version.sh"
if [ -f "${_openshell_version_sh}" ]; then
  # shellcheck source=../../.github/scripts/openshell-version.sh
  source "${_openshell_version_sh}"
fi
OPENSHELL_VERSION="${OPENSHELL_VERSION:-0.0.116}"
PREFIX="fullsend-gitlab-runner"

# Validate GCP_USE_IAP early — it controls flag construction below, so an
# invalid value (e.g. "yes") must not silently skip --tunnel-through-iap.
if [[ "${GCP_USE_IAP}" != "true" && "${GCP_USE_IAP}" != "false" ]]; then
  echo "ERROR: GCP_USE_IAP must be true or false (got: ${GCP_USE_IAP})" >&2
  exit 1
fi

if [ "${GCP_USE_IAP}" = "false" ]; then
  echo "  WARN: GCP_USE_IAP=false — SSH will connect over the public internet." >&2
  echo "        Host-key verification uses accept-new (TOFU within this session)." >&2
  echo "        Secrets (REGISTRATION_TOKEN) are transmitted over this session." >&2
fi

# Common flags for gcloud compute ssh. IAP tunneling is enabled by default
# (no public IP); set GCP_USE_IAP=false to SSH directly via an external IP.
#
# Host-key handling differs by mode:
#   IAP (default):  StrictHostKeyChecking=no + /dev/null — the IAP relay has no
#                   stable host key, and the tunnel itself authenticates via IAM.
#   Direct IP:      StrictHostKeyChecking=accept-new + temp known-hosts file —
#                   the VM has a stable public IP, so we trust-on-first-use and
#                   reject key changes for all subsequent connections in this run.
GCE_SSH_FLAGS=(
  --ssh-flag="-o ConnectTimeout=10"
  --ssh-flag="-o LogLevel=ERROR"
)
if [ "${GCP_USE_IAP}" = "true" ]; then
  GCE_SSH_FLAGS=(
    --tunnel-through-iap
    --ssh-flag="-o StrictHostKeyChecking=no"
    --ssh-flag="-o UserKnownHostsFile=/dev/null"
    "${GCE_SSH_FLAGS[@]}"
  )
else
  _known_hosts=$(mktemp)
  GCE_SSH_FLAGS=(
    --ssh-flag="-o StrictHostKeyChecking=accept-new"
    --ssh-flag="-o UserKnownHostsFile=${_known_hosts}"
    "${GCE_SSH_FLAGS[@]}"
  )
fi

# shellcheck source=lib.sh
source "${SCRIPT_DIR}/lib.sh"

# Helper to run commands on the GCE VM via gcloud compute ssh.
gce_ssh() {
  gcloud compute ssh "${vm_name}" \
    --project="${GCP_PROJECT}" \
    --zone="${GCP_ZONE}" \
    "${GCE_SSH_FLAGS[@]}" \
    --command="$1"
}

# Retry a command with exponential backoff for IAP tunnel resilience.
# After long-running SSH sessions, IAP may rate-limit or exhaust its
# connection pool, causing subsequent connections to fail with
# "Connection timed out during banner exchange" or "4003: failed to
# connect to backend". Diagnostic output goes to stderr to avoid stdout
# contamination (see docs/contributing/shell-scripting.md).
with_backoff() {
  local max_attempts=5 attempt=1 delay=5 rc
  while true; do
    rc=0
    "$@" || rc=$?
    if [ "${rc}" -eq 0 ]; then return 0; fi
    if [ "${attempt}" -ge "${max_attempts}" ]; then
      echo "  ERROR: command failed after ${max_attempts} attempts (exit ${rc})" >&2
      return "${rc}"
    fi
    echo "  WARN: attempt ${attempt}/${max_attempts} failed (exit ${rc}), retrying in ${delay}s..." >&2
    sleep "${delay}"
    delay=$(( delay * 2 ))
    (( attempt++ ))
  done
}

# ----------------------------------------------------------------------
# Validate inputs
# ----------------------------------------------------------------------
usage() {
  echo "Usage: GL_TOKEN=glpat-xxx {GROUP_ID=<id>|PROJECT_ID=<id>} GCP_PROJECT=<project> $0 [NUMBER]"
  echo ""
  echo "Run '$0' with --help for details."
}

if [ "${1:-}" = "--help" ] || [ "${1:-}" = "-h" ]; then
  # Extract the comment block after the shebang until the first non-comment
  # line, stripping the leading "# " prefix.  This is immune to header edits
  # (no hardcoded line numbers).
  awk 'NR==1{next} /^[^#]/{exit} {sub(/^# ?/, ""); print}' "$0"
  exit 0
fi

if [ -z "${GL_TOKEN:-}" ]; then
  echo "ERROR: GL_TOKEN is required (GitLab personal access token)" >&2
  usage >&2
  exit 1
fi
if ! [[ "${GL_TOKEN}" =~ ^[A-Za-z0-9._-]+$ ]]; then
  echo "ERROR: GL_TOKEN contains invalid characters" >&2
  exit 1
fi

if ! validate_runner_scope; then
  usage >&2
  exit 1
fi

if [ -z "${GITLAB_URL}" ]; then
  echo "ERROR: GITLAB_URL is required (e.g. https://gitlab.example.com)" >&2
  usage >&2
  exit 1
fi
if ! [[ "${GITLAB_URL}" =~ ^https://[a-zA-Z0-9._-]+(:[0-9]+)?$ ]]; then
  echo "ERROR: GITLAB_URL must start with https:// (got: ${GITLAB_URL})" >&2
  exit 1
fi

if [ -z "${GCP_PROJECT}" ]; then
  echo "ERROR: GCP_PROJECT is required (GCP project ID)" >&2
  usage >&2
  exit 1
fi

if [ -z "${RUNNER_IMAGE}" ]; then
  echo "ERROR: RUNNER_IMAGE is required (e.g. ghcr.io/fullsend-ai/fullsend-runner:v1.2.3)" >&2
  usage >&2
  exit 1
fi

if [[ "${RUNNER_ACCESS_LEVEL}" != "not_protected" && "${RUNNER_ACCESS_LEVEL}" != "ref_protected" ]]; then
  echo "ERROR: RUNNER_ACCESS_LEVEL must be not_protected or ref_protected (got: ${RUNNER_ACCESS_LEVEL})" >&2
  exit 1
fi

# GCP_USE_IAP is validated early (before GCE_SSH_FLAGS construction).

# Preflight — every tool and file this run depends on. Without this, a missing
# executor script or an absent `timeout` is discovered only after the VM has
# booted and a runner has been registered, so the failure costs a rollback.
REPO_ROOT="$(cd "${SCRIPT_DIR}" && cd ../.. && pwd)"
_missing=0
for tool in gcloud python3 curl timeout sha256sum; do
  if ! command -v "${tool}" >/dev/null 2>&1; then
    echo "ERROR: required tool not found in PATH: ${tool}" >&2
    _missing=1
  fi
done
for _f in setup.sh create-gcp-vm.sh gitlab-runner-version.sh \
  executor/job_id.sh executor/prepare.sh executor/run.sh executor/cleanup.sh; do
  if [ ! -f "${SCRIPT_DIR}/${_f}" ]; then
    echo "ERROR: required file not found: ${SCRIPT_DIR}/${_f}" >&2
    _missing=1
  fi
done
for _f in install-openshell.sh openshell-version.sh; do
  if [ ! -f "${REPO_ROOT}/.github/scripts/${_f}" ]; then
    echo "ERROR: required file not found: ${REPO_ROOT}/.github/scripts/${_f}" >&2
    _missing=1
  fi
done
if [ "${_missing}" -ne 0 ]; then
  exit 1
fi

# ----------------------------------------------------------------------
# 1. Pick the VM number (explicit arg or auto-increment)
# ----------------------------------------------------------------------
if [ -n "${1:-}" ]; then
  if ! [[ "$1" =~ ^[0-9]+$ ]]; then
    echo "ERROR: NUMBER must be numeric (got: $1)" >&2
    exit 1
  fi
  next=$(printf "%02d" "$((10#$1))")
  vm_name="${PREFIX}-${next}"
else
  max=0
  while IFS= read -r name; do
    [ -z "${name}" ] && continue
    num="${name#"${PREFIX}"-}"
    if [[ "${num}" =~ ^[0-9]+$ ]] && [ "$((10#${num}))" -gt "$((10#${max}))" ]; then
      max="${num}"
    fi
  done < <(gcloud compute instances list \
    --project="${GCP_PROJECT}" \
    --filter="name~'^${PREFIX}-'" \
    --format="value(name)" 2>/dev/null || true)

  next=$(printf "%02d" $((10#${max} + 1)))
  vm_name="${PREFIX}-${next}"
fi

echo "==> Creating VM: ${vm_name} in ${GCP_PROJECT} (${GCP_ZONE})"

# ----------------------------------------------------------------------
# 2. Create the GCE VM
# ----------------------------------------------------------------------
if gcloud compute instances describe "${vm_name}" \
  --project="${GCP_PROJECT}" --zone="${GCP_ZONE}" >/dev/null 2>&1; then
  echo "ERROR: VM ${vm_name} already exists in ${GCP_PROJECT}/${GCP_ZONE} — delete it first or choose a different number" >&2
  exit 1
fi

if ! [[ "${vm_name}" =~ ^[a-z0-9-]+$ ]]; then
  echo "ERROR: vm_name contains invalid characters: ${vm_name}" >&2
  exit 1
fi

subnet_flag=()
if [ -n "${GCP_SUBNET}" ]; then
  subnet_flag=(--subnet="${GCP_SUBNET}")
fi

address_flag=()
if [ "${GCP_USE_IAP}" = "true" ]; then
  address_flag=(--no-address)
fi

gcloud compute instances create "${vm_name}" \
  --project="${GCP_PROJECT}" \
  --zone="${GCP_ZONE}" \
  --machine-type="${GCP_MACHINE_TYPE}" \
  --network="${GCP_NETWORK}" \
  "${subnet_flag[@]+"${subnet_flag[@]}"}" \
  --tags="gitlab-runner" \
  "${address_flag[@]+"${address_flag[@]}"}" \
  --image-family="${GCP_IMAGE_FAMILY}" \
  --image-project="${GCP_IMAGE_PROJECT}" \
  --boot-disk-size="20GB" \
  --boot-disk-type="pd-balanced" \
  --quiet
cleanup_vm() {
  echo "  NOTE: VM ${vm_name} was created — to clean up run:" >&2
  echo "    GCP_PROJECT=${GCP_PROJECT} GCP_ZONE=${GCP_ZONE} GL_TOKEN=\$GL_TOKEN GITLAB_URL=${GITLAB_URL} ./delete-gcp-vm.sh ${vm_name}" >&2
}
trap cleanup_vm ERR
# ERR does not fire on Ctrl-C; the boot and package-install waits below can
# take up to 20 minutes, so print the cleanup hint on interrupt as well.
trap 'cleanup_vm; exit 130' INT
trap 'cleanup_vm; exit 143' TERM

# ----------------------------------------------------------------------
# 3. Wait for the VM to boot, accept SSH, and install packages
# ----------------------------------------------------------------------
echo "==> Waiting for ${vm_name} to boot..."
for i in $(seq 1 60); do
  if gce_ssh "true" >/dev/null 2>&1; then
    echo "  OK: VM is up (${i}0s)"
    break
  fi
  if [ "${i}" -eq 60 ]; then
    echo "ERROR: VM did not become reachable after 10 minutes" >&2
    cleanup_vm
    exit 1
  fi
  sleep 10
done

# Install packages via dnf over SSH. Fedora GCE images ship google-guest-agent,
# not cloud-init, so #cloud-config user-data is silently ignored. Direct SSH
# dnf install is the reliable path on all Fedora GCE images.
echo "==> Installing packages via SSH (dnf)..."
if ! timeout 600 gce_ssh "sudo dnf install -y podman curl git python3 openssl" 2>&1; then
  echo "ERROR: dnf install failed or timed out — check the VM for dnf errors" >&2
  cleanup_vm
  exit 1
fi
echo "  OK: packages installed"

# ----------------------------------------------------------------------
# 4. Register a runner via the GitLab API
# ----------------------------------------------------------------------
echo "==> Registering runner with ${GITLAB_URL} (${RUNNER_SCOPE} ${SCOPE_ID})"

build_scope_args

# shellcheck disable=SC2154  # scope_args set by build_scope_args
runner_json=$(gl_curl -X POST \
  "${GITLAB_URL}/api/v4/user/runners" \
  "${scope_args[@]}" \
  --data-urlencode "tag_list=${RUNNER_TAG}" \
  --data-urlencode "description=${GCP_PROJECT}/${vm_name}" \
  --data-urlencode "run_untagged=false" \
  --data-urlencode "access_level=${RUNNER_ACCESS_LEVEL}" 2>&1) || {
  echo "ERROR: GitLab runner registration failed. Response: ${runner_json}" >&2
  cleanup_vm
  exit 1
}

if [ -z "${runner_json}" ]; then
  echo "ERROR: GitLab runner registration returned empty response" >&2
  cleanup_vm
  exit 1
fi

# Set up rollback before extracting fields — a malformed API response would
# orphan the runner if the trap weren't active yet.
runner_id=""
cleanup_runner() {
  if [ -z "${runner_id}" ]; then
    echo "ERROR: provisioning failed — runner may have been created but ID is unknown" >&2
    echo "  Check ${GITLAB_URL} for orphaned runners in ${RUNNER_SCOPE} ${SCOPE_ID}" >&2
  else
    echo "ERROR: provisioning failed — deregistering runner ${runner_id}" >&2
    if gl_curl -X DELETE "${GITLAB_URL}/api/v4/runners/${runner_id}" >/dev/null 2>&1; then
      echo "  OK: runner ${runner_id} deregistered" >&2
    else
      echo "  WARN: failed to deregister runner ${runner_id} — remove it manually at ${GITLAB_URL}" >&2
    fi
  fi
  echo "  NOTE: VM ${vm_name} was not cleaned up — run: GCP_PROJECT=${GCP_PROJECT} GCP_ZONE=${GCP_ZONE} GL_TOKEN=\$GL_TOKEN GITLAB_URL=${GITLAB_URL} ./delete-gcp-vm.sh ${vm_name}" >&2
}
trap cleanup_runner ERR
# ERR does not fire on Ctrl-C, and the window below spans a ~20-minute setup
# run — without this, an interrupt leaves the runner registered with nobody
# tracking it.
trap 'cleanup_runner; exit 130' INT
trap 'cleanup_runner; exit 143' TERM

REGISTRATION_TOKEN=$(echo "${runner_json}" | python3 -c "import sys,json; print(json.load(sys.stdin)['token'])" 2>&1) || {
  echo "ERROR: failed to parse registration token from API response (length: ${#runner_json})" >&2
  cleanup_runner
  exit 1
}
runner_id=$(echo "${runner_json}" | python3 -c "import sys,json; print(json.load(sys.stdin)['id'])" 2>&1) || {
  echo "ERROR: failed to parse runner ID from API response (length: ${#runner_json})" >&2
  cleanup_runner
  exit 1
}

echo "  OK: runner ID ${runner_id} created"

# ----------------------------------------------------------------------
# 5. Copy setup files to the VM
# ----------------------------------------------------------------------
echo "==> Copying setup files to ${vm_name}"

with_backoff gce_ssh "mkdir -p ~/gitlab-runner-vm"

# Stage files in a local temp directory for batch transfer.
_stage_dir=$(mktemp -d)
trap 'rm -rf "${_stage_dir}"; cleanup_runner' ERR
trap 'rm -rf "${_stage_dir}"; cleanup_runner; exit 130' INT
trap 'rm -rf "${_stage_dir}"; cleanup_runner; exit 143' TERM
cp "${SCRIPT_DIR}/setup.sh" "${_stage_dir}/"
cp "${SCRIPT_DIR}/create-gcp-vm.sh" "${_stage_dir}/"
cp "${SCRIPT_DIR}/gitlab-runner-version.sh" "${_stage_dir}/"
mkdir -p "${_stage_dir}/executor"
for file in job_id.sh prepare.sh run.sh cleanup.sh; do
  cp "${SCRIPT_DIR}/executor/${file}" "${_stage_dir}/executor/"
done
mkdir -p "${_stage_dir}/.github/scripts"
for file in install-openshell.sh openshell-version.sh; do
  cp "${REPO_ROOT}/.github/scripts/${file}" "${_stage_dir}/.github/scripts/"
done

# Transfer all files in a single SSH connection via tar to minimize IAP
# tunnel usage. This replaces multiple scp calls that each open a separate
# IAP tunnel, which triggers rate-limiting after long-running sessions.
copy_files_to_vm() {
  tar -C "${_stage_dir}" -cf - . \
    | gcloud compute ssh "${vm_name}" \
        --project="${GCP_PROJECT}" \
        --zone="${GCP_ZONE}" \
        "${GCE_SSH_FLAGS[@]}" \
        -- "tar -C ~/gitlab-runner-vm -xf -"
}
with_backoff copy_files_to_vm

rm -rf "${_stage_dir}"
trap cleanup_runner ERR
trap 'cleanup_runner; exit 130' INT
trap 'cleanup_runner; exit 143' TERM

with_backoff gce_ssh "chmod +x ~/gitlab-runner-vm/setup.sh ~/gitlab-runner-vm/create-gcp-vm.sh ~/gitlab-runner-vm/executor/*.sh ~/gitlab-runner-vm/.github/scripts/*.sh"

# Verify every copy against a locally computed manifest before running it.
# A dropped SSH channel can leave a truncated setup.sh that then executes an
# arbitrary prefix of provisioning.
echo "==> Verifying copied files"
verify_copied_files() {
  {
    (cd "${SCRIPT_DIR}" && sha256sum setup.sh create-gcp-vm.sh gitlab-runner-version.sh \
      executor/job_id.sh executor/prepare.sh executor/run.sh executor/cleanup.sh)
    (cd "${REPO_ROOT}/.github/scripts" \
      && sha256sum install-openshell.sh openshell-version.sh \
      | sed 's|  |  .github/scripts/|')
  } | gce_ssh "cd ~/gitlab-runner-vm && sha256sum -c --quiet -"
}
if ! verify_copied_files; then
  echo "ERROR: copied files failed checksum verification — transfer was truncated" >&2
  cleanup_runner
  exit 1
fi

echo "  OK: files copied"

# ----------------------------------------------------------------------
# 6. Run setup.sh on the VM
# ----------------------------------------------------------------------
echo "==> Running setup.sh on ${vm_name}"

# Write env vars to a file on the VM to avoid exposing secrets in the process list.
# Values are single-quoted to prevent interpretation of special characters.
for val in "${REGISTRATION_TOKEN}" "${GITLAB_URL}" "${RUNNER_TAG}" "${RUNNER_IMAGE}" "${OPENSHELL_VERSION}" "${GITLAB_RUNNER_VERSION}"; do
  if [[ "${val}" == *"'"* ]] || [[ "${val}" == *\\* ]] || [[ "${val}" =~ [[:cntrl:]] ]]; then
    echo "ERROR: environment variable values must not contain single quotes, backslashes, or control characters" >&2
    cleanup_runner
    exit 1
  fi
done
# One remote session: install the .env removal trap first, receive the env
# file on stdin, then run setup.sh. Doing this in one session means there is
# no window where the token-bearing file exists without a trap covering it.
# The signal handlers must terminate the shell (which then fires EXIT): a
# handler that merely returns would swallow the SIGHUP from a dropped SSH
# connection and let setup.sh keep running while the local side deregisters
# the runner. Bounded at 20 minutes (image pulls and binary downloads are the
# bottleneck). Wrapped in with_backoff for IAP tunnel resilience — setup.sh
# is idempotent (each function checks existing state before acting), so
# retrying the full invocation after a dropped SSH connection is safe.
# Note: each retry re-transmits REGISTRATION_TOKEN over a new SSH session.
# The remote EXIT trap (`rm -f .env`) cleans up the token file on disconnect,
# and umask 077 ensures it is never world-readable between attempts.
run_setup_on_vm() {
  {
    printf "REGISTRATION_TOKEN='%s'\n" "${REGISTRATION_TOKEN}"
    printf "GITLAB_URL='%s'\n" "${GITLAB_URL}"
    printf "RUNNER_TAG='%s'\n" "${RUNNER_TAG}"
    printf "RUNNER_IMAGE='%s'\n" "${RUNNER_IMAGE}"
    printf "OPENSHELL_VERSION='%s'\n" "${OPENSHELL_VERSION}"
    printf "GITLAB_RUNNER_VERSION='%s'\n" "${GITLAB_RUNNER_VERSION}"
  } | timeout 1200 gcloud compute ssh "${vm_name}" \
    --project="${GCP_PROJECT}" \
    --zone="${GCP_ZONE}" \
    "${GCE_SSH_FLAGS[@]}" \
    -- "trap 'rm -f ~/gitlab-runner-vm/.env' EXIT; trap 'exit 129' HUP; trap 'exit 130' INT; trap 'exit 143' TERM; umask 077 && cat > ~/gitlab-runner-vm/.env && set -a && . ~/gitlab-runner-vm/.env && set +a && bash ~/gitlab-runner-vm/setup.sh"
}
with_backoff run_setup_on_vm

# Setup succeeded — clear every rollback trap so a stray signal during the
# final output cannot deregister a healthy runner.
trap - ERR INT TERM
rm -f "${_known_hosts:-}"

echo ""
echo "Done. Runner ${vm_name} (ID ${runner_id}) is ready."
echo "  Tag:       ${RUNNER_TAG}"
echo "  Project:   ${GCP_PROJECT}"
echo "  Zone:      ${GCP_ZONE}"
if [ "${GCP_USE_IAP}" = "true" ]; then
  echo "  SSH:       gcloud compute ssh ${vm_name} --project=${GCP_PROJECT} --zone=${GCP_ZONE} --tunnel-through-iap"
else
  echo "  SSH:       gcloud compute ssh ${vm_name} --project=${GCP_PROJECT} --zone=${GCP_ZONE}"
  echo "  NOTE:      VM has an external IP. To remove it after provisioning:"
  echo "             gcloud compute instances delete-access-config ${vm_name} --project=${GCP_PROJECT} --zone=${GCP_ZONE}"
fi
