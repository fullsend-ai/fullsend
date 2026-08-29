#!/usr/bin/env bash
#
# create-vm.sh — Create and provision a GitLab Runner VM in one command.
#
# This script:
#   1. Auto-numbers the VM (fullsend-gitlab-runner-01, -02, ...)
#   2. Creates the VM on OpenShift Virtualization from vm.yaml
#   3. Waits for it to boot and accept SSH (~2 minutes)
#   4. Registers a new project runner via the GitLab API
#   5. Copies setup files and runs setup.sh to configure the custom
#      executor, OpenShell gateway, and pre-pull images
#
# When done, the runner is online and accepting jobs tagged with RUNNER_TAG.
#
# Required environment variables:
#   GL_TOKEN     — GitLab personal access token (Owner role on PROJECT_ID,
#                  scopes: create_runner + manage_runner + api)
#   PROJECT_ID   — GitLab project ID to register the runner against
#   GITLAB_URL   — GitLab instance URL (e.g. https://gitlab.example.com)
#   NAMESPACE    — OpenShift namespace for the VM
#   RUNNER_IMAGE — image pre-pulled as warm cache (e.g. ghcr.io/org/runner:v1.2.3)
#
# Optional environment variables:
#   RUNNER_TAG            — runner tag for job matching (default: fullsend-gitlab-runner)
#   GITLAB_RUNNER_VERSION — gitlab-runner version to install (default: 19.2.1)
#   VM_USER               — cloud-image login user (default: fedora; RHEL/CentOS
#                           Stream images use cloud-user)
#   RUNNER_ACCESS_LEVEL   — not_protected (default) or ref_protected. Protected
#                           runners only pick up jobs on protected branches and
#                           tags, so merge-request pipelines never match.
#
# Arguments:
#   [NUMBER]  — optional runner number (e.g. 01, 03). Auto-increments if omitted.
#
# Examples:
#   # Auto-numbers the VM:
#   GL_TOKEN=glpat-xxx PROJECT_ID=12345 \
#     GITLAB_URL=https://gitlab.example.com NAMESPACE=my-namespace \
#     RUNNER_IMAGE=ghcr.io/org/runner:v1.2.3 ./create-vm.sh
#
#   # Explicit runner number:
#   GL_TOKEN=glpat-xxx PROJECT_ID=12345 \
#     GITLAB_URL=https://gitlab.example.com NAMESPACE=my-namespace \
#     RUNNER_IMAGE=ghcr.io/org/runner:v1.2.3 ./create-vm.sh 01
#
set -euo pipefail

GITLAB_URL="${GITLAB_URL:-}"
NAMESPACE="${NAMESPACE:-}"
RUNNER_TAG="${RUNNER_TAG:-fullsend-gitlab-runner}"
RUNNER_IMAGE="${RUNNER_IMAGE:-}"
# Cloud-image login user. Fedora images use "fedora"; RHEL/CentOS Stream
# images use "cloud-user", so keep this overridable alongside vm.yaml.
VM_USER="${VM_USER:-fedora}"
# ref_protected restricts the runner to jobs on protected branches and tags.
# Merge-request pipelines run on the (unprotected) source ref, so the default
# is not_protected; scoping comes from runner_type=project_type + locked=true.
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
TEMPLATE="${SCRIPT_DIR}/vm.yaml"
PREFIX="fullsend-gitlab-runner"

# Wrap curl with GL_TOKEN passed via a temp config file to avoid
# exposing the token in /proc/<pid>/cmdline.
gl_curl() {
  local config old_umask rc
  old_umask=$(umask)
  umask 077
  config=$(mktemp)
  umask "${old_umask}"
  printf 'header = "PRIVATE-TOKEN: %s"\n' "${GL_TOKEN}" > "${config}"
  rc=0
  curl --max-time 30 --connect-timeout 10 -sf -K "${config}" "$@" || rc=$?
  rm -f "${config}"
  return "${rc}"
}

# ----------------------------------------------------------------------
# Validate inputs
# ----------------------------------------------------------------------
usage() {
  echo "Usage: GL_TOKEN=glpat-xxx PROJECT_ID=<id> $0 [NUMBER]"
  echo ""
  echo "Run '$0' with --help for details."
}

if [ "${1:-}" = "--help" ] || [ "${1:-}" = "-h" ]; then
  head -44 "$0" | tail -42 | sed 's/^# \?//'
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

if [ -z "${PROJECT_ID:-}" ]; then
  echo "ERROR: PROJECT_ID is required (GitLab project ID)" >&2
  usage >&2
  exit 1
fi

if [ -z "${GITLAB_URL}" ]; then
  echo "ERROR: GITLAB_URL is required (e.g. https://gitlab.example.com)" >&2
  exit 1
fi
if ! [[ "${GITLAB_URL}" =~ ^https://[a-zA-Z0-9._-]+(:[0-9]+)?$ ]]; then
  echo "ERROR: GITLAB_URL must start with https:// (got: ${GITLAB_URL})" >&2
  exit 1
fi

if [ -z "${NAMESPACE}" ]; then
  echo "ERROR: NAMESPACE is required (OpenShift namespace)" >&2
  exit 1
fi

if [ -z "${RUNNER_IMAGE}" ]; then
  echo "ERROR: RUNNER_IMAGE is required (e.g. ghcr.io/fullsend-ai/fullsend-runner:v1.2.3)" >&2
  exit 1
fi

if [ ! -f "${TEMPLATE}" ]; then
  echo "ERROR: VM template not found: ${TEMPLATE}" >&2
  exit 1
fi

if ! [[ "${VM_USER}" =~ ^[a-z_][a-z0-9_-]*$ ]]; then
  echo "ERROR: VM_USER must be a plain Unix user name (got: ${VM_USER})" >&2
  exit 1
fi
if [[ "${RUNNER_ACCESS_LEVEL}" != "not_protected" && "${RUNNER_ACCESS_LEVEL}" != "ref_protected" ]]; then
  echo "ERROR: RUNNER_ACCESS_LEVEL must be not_protected or ref_protected (got: ${RUNNER_ACCESS_LEVEL})" >&2
  exit 1
fi

# Preflight — every tool and file this run depends on. Without this, a missing
# executor script or an absent `timeout` is discovered only after the VM has
# booted and a runner has been registered, so the failure costs a rollback.
REPO_ROOT="$(cd "${SCRIPT_DIR}" && cd ../.. && pwd)"
_missing=0
for tool in oc virtctl python3 curl timeout sha256sum; do
  if ! command -v "${tool}" >/dev/null 2>&1; then
    echo "ERROR: required tool not found in PATH: ${tool}" >&2
    _missing=1
  fi
done
for _f in setup.sh create-vm.sh vm.yaml gitlab-runner-version.sh \
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
    num="${name#"${PREFIX}"-}"
    if [[ "${num}" =~ ^[0-9]+$ ]] && [ "$((10#${num}))" -gt "$((10#${max}))" ]; then
      max="${num}"
    fi
  done < <(oc -n "${NAMESPACE}" get vm --no-headers -o custom-columns=NAME:.metadata.name 2>/dev/null \
    | grep "^${PREFIX}-" || true)

  next=$(printf "%02d" $((10#${max} + 1)))
  vm_name="${PREFIX}-${next}"
fi

echo "==> Creating VM: ${vm_name} in ${NAMESPACE}"

# ----------------------------------------------------------------------
# 2. Apply the VM manifest
# ----------------------------------------------------------------------
if oc -n "${NAMESPACE}" get vm "${vm_name}" >/dev/null 2>&1; then
  echo "ERROR: VM ${vm_name} already exists in ${NAMESPACE} — delete it first or choose a different number" >&2
  exit 1
fi

if ! [[ "${vm_name}" =~ ^[a-z0-9-]+$ ]]; then
  echo "ERROR: vm_name contains invalid characters: ${vm_name}" >&2
  exit 1
fi

SSH_PUBLIC_KEY="${SSH_PUBLIC_KEY:-}"
if [ -z "${SSH_PUBLIC_KEY}" ]; then
  if [ -f "${HOME}/.ssh/id_rsa.pub" ]; then
    SSH_PUBLIC_KEY=$(cat "${HOME}/.ssh/id_rsa.pub")
  elif [ -f "${HOME}/.ssh/id_ed25519.pub" ]; then
    SSH_PUBLIC_KEY=$(cat "${HOME}/.ssh/id_ed25519.pub")
  else
    echo "ERROR: SSH_PUBLIC_KEY not set and no key found in ~/.ssh/" >&2
    exit 1
  fi
fi

if [[ "${SSH_PUBLIC_KEY}" == *$'\n'* ]]; then
  echo "ERROR: SSH_PUBLIC_KEY must not contain newlines" >&2
  exit 1
fi
if ! [[ "${SSH_PUBLIC_KEY}" =~ ^(ssh-|ecdsa-) ]]; then
  echo "ERROR: SSH_PUBLIC_KEY must contain key contents (e.g. ssh-rsa AAAA...), not a file path" >&2
  exit 1
fi

python3 -c "
import sys
template = sys.stdin.read()
print(template.replace('__VM_NAME__', sys.argv[1]).replace('__SSH_PUBLIC_KEY__', sys.argv[2]).replace('__VM_USER__', sys.argv[3]), end='')
" "${vm_name}" "${SSH_PUBLIC_KEY}" "${VM_USER}" < "${TEMPLATE}" \
  | oc create -n "${NAMESPACE}" -f -

cleanup_vm() {
  echo "  NOTE: VM ${vm_name} was created — to clean up run:" >&2
  echo "    NAMESPACE=${NAMESPACE} GL_TOKEN=\$GL_TOKEN GITLAB_URL=${GITLAB_URL} ./delete-vm.sh ${vm_name}" >&2
}
trap cleanup_vm ERR
# ERR does not fire on Ctrl-C; the boot and cloud-init waits below can take
# up to 20 minutes, so print the cleanup hint on interrupt as well.
trap 'cleanup_vm; exit 130' INT
trap 'cleanup_vm; exit 143' TERM

# ----------------------------------------------------------------------
# 3. Wait for the VM to boot and accept SSH
# ----------------------------------------------------------------------
echo "==> Waiting for ${vm_name} to boot..."
for i in $(seq 1 60); do
  if virtctl -n "${NAMESPACE}" ssh "${VM_USER}"@vm/"${vm_name}" \
    -t "-o StrictHostKeyChecking=no" -t "-o UserKnownHostsFile=/dev/null" -t "-o ConnectTimeout=5" \
    -c "true" >/dev/null 2>&1; then
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

# Wait for cloud-init to finish installing packages (podman, curl, git, python3).
# Bounded at 10 minutes to match the SSH readiness loop.
echo "==> Waiting for cloud-init to complete..."
if ! timeout 600 virtctl -n "${NAMESPACE}" ssh "${VM_USER}"@vm/"${vm_name}" \
  -t "-o StrictHostKeyChecking=no" -t "-o UserKnownHostsFile=/dev/null" \
  -c "cloud-init status --wait" 2>&1; then
  echo "ERROR: cloud-init failed or timed out — check cloud-init logs on the VM" >&2
  cleanup_vm
  exit 1
fi
echo "  OK: cloud-init complete"

# ----------------------------------------------------------------------
# 4. Register a runner via the GitLab API
# ----------------------------------------------------------------------
echo "==> Registering runner with ${GITLAB_URL} (project ${PROJECT_ID})"

runner_json=$(gl_curl -X POST \
  "${GITLAB_URL}/api/v4/user/runners" \
  --data-urlencode "runner_type=project_type" \
  --data-urlencode "project_id=${PROJECT_ID}" \
  --data-urlencode "tag_list=${RUNNER_TAG}" \
  --data-urlencode "description=${NAMESPACE}/${vm_name}" \
  --data-urlencode "run_untagged=false" \
  --data-urlencode "access_level=${RUNNER_ACCESS_LEVEL}" \
  --data-urlencode "locked=true" 2>&1) || {
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
    echo "  Check ${GITLAB_URL} for orphaned runners in project ${PROJECT_ID}" >&2
  else
    echo "ERROR: provisioning failed — deregistering runner ${runner_id}" >&2
    if gl_curl -X DELETE "${GITLAB_URL}/api/v4/runners/${runner_id}" >/dev/null 2>&1; then
      echo "  OK: runner ${runner_id} deregistered" >&2
    else
      echo "  WARN: failed to deregister runner ${runner_id} — remove it manually at ${GITLAB_URL}" >&2
    fi
  fi
  echo "  NOTE: VM ${vm_name} was not cleaned up — run: NAMESPACE=${NAMESPACE} GL_TOKEN=\$GL_TOKEN GITLAB_URL=${GITLAB_URL} ./delete-vm.sh ${vm_name}" >&2
}
trap cleanup_runner ERR
# ERR does not fire on Ctrl-C, and the window below spans a ~20-minute setup
# run — without this, an interrupt leaves the runner registered with nobody
# tracking it.
trap 'cleanup_runner; exit 130' INT
trap 'cleanup_runner; exit 143' TERM

REGISTRATION_TOKEN=$(echo "${runner_json}" | python3 -c "import sys,json; print(json.load(sys.stdin)['token'])")
runner_id=$(echo "${runner_json}" | python3 -c "import sys,json; print(json.load(sys.stdin)['id'])")

echo "  OK: runner ID ${runner_id} created"

# ----------------------------------------------------------------------
# 5. Copy setup files to the VM
# ----------------------------------------------------------------------
echo "==> Copying setup files to ${vm_name}"

virtctl -n "${NAMESPACE}" ssh "${VM_USER}"@vm/"${vm_name}" \
  -t "-o StrictHostKeyChecking=no" -t "-o UserKnownHostsFile=/dev/null" \
  -c "mkdir -p ~/gitlab-runner-vm/executor ~/gitlab-runner-vm/.github/scripts"

for file in setup.sh create-vm.sh vm.yaml gitlab-runner-version.sh; do
  virtctl -n "${NAMESPACE}" ssh "${VM_USER}"@vm/"${vm_name}" \
    -t "-o StrictHostKeyChecking=no" -t "-o UserKnownHostsFile=/dev/null" \
    -c "cat > ~/gitlab-runner-vm/${file}" < "${SCRIPT_DIR}/${file}"
done

for file in job_id.sh prepare.sh run.sh cleanup.sh; do
  virtctl -n "${NAMESPACE}" ssh "${VM_USER}"@vm/"${vm_name}" \
    -t "-o StrictHostKeyChecking=no" -t "-o UserKnownHostsFile=/dev/null" \
    -c "cat > ~/gitlab-runner-vm/executor/${file}" < "${SCRIPT_DIR}/executor/${file}"
done

# setup.sh's install_openshell() delegates to the repo's SHA-pinned installer.
for file in install-openshell.sh openshell-version.sh; do
  virtctl -n "${NAMESPACE}" ssh "${VM_USER}"@vm/"${vm_name}" \
    -t "-o StrictHostKeyChecking=no" -t "-o UserKnownHostsFile=/dev/null" \
    -c "cat > ~/gitlab-runner-vm/.github/scripts/${file}" < "${REPO_ROOT}/.github/scripts/${file}"
done

virtctl -n "${NAMESPACE}" ssh "${VM_USER}"@vm/"${vm_name}" \
  -t "-o StrictHostKeyChecking=no" -t "-o UserKnownHostsFile=/dev/null" \
  -c "chmod +x ~/gitlab-runner-vm/setup.sh ~/gitlab-runner-vm/create-vm.sh ~/gitlab-runner-vm/executor/*.sh ~/gitlab-runner-vm/.github/scripts/*.sh"

# `cat > file` exits 0 on a short write, so a dropped SSH channel can leave a
# truncated setup.sh that then executes an arbitrary prefix of provisioning.
# Verify every copy against a locally computed manifest before running it.
echo "==> Verifying copied files"
{
  (cd "${SCRIPT_DIR}" && sha256sum setup.sh create-vm.sh vm.yaml gitlab-runner-version.sh \
    executor/job_id.sh executor/prepare.sh executor/run.sh executor/cleanup.sh)
  (cd "${REPO_ROOT}/.github/scripts" \
    && sha256sum install-openshell.sh openshell-version.sh \
    | sed 's|  |  .github/scripts/|')
} | virtctl -n "${NAMESPACE}" ssh "${VM_USER}"@vm/"${vm_name}" \
  -t "-o StrictHostKeyChecking=no" -t "-o UserKnownHostsFile=/dev/null" \
  -c "cd ~/gitlab-runner-vm && sha256sum -c --quiet -" || {
  echo "ERROR: copied files failed checksum verification — transfer was truncated" >&2
  cleanup_runner
  exit 1
}

echo "  OK: files copied"

# ----------------------------------------------------------------------
# 6. Run setup.sh on the VM
# ----------------------------------------------------------------------
echo "==> Running setup.sh on ${vm_name}"

# Write env vars to a file on the VM to avoid exposing secrets in the process list.
# Values are single-quoted to prevent interpretation of special characters.
for val in "${REGISTRATION_TOKEN}" "${GITLAB_URL}" "${RUNNER_TAG}" "${RUNNER_IMAGE}" "${OPENSHELL_VERSION}" "${GITLAB_RUNNER_VERSION}"; do
  if [[ "${val}" == *"'"* ]]; then
    echo "ERROR: environment variable values must not contain single quotes" >&2
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
# bottleneck).
{
  printf "REGISTRATION_TOKEN='%s'\n" "${REGISTRATION_TOKEN}"
  printf "GITLAB_URL='%s'\n" "${GITLAB_URL}"
  printf "RUNNER_TAG='%s'\n" "${RUNNER_TAG}"
  printf "RUNNER_IMAGE='%s'\n" "${RUNNER_IMAGE}"
  printf "OPENSHELL_VERSION='%s'\n" "${OPENSHELL_VERSION}"
  printf "GITLAB_RUNNER_VERSION='%s'\n" "${GITLAB_RUNNER_VERSION}"
} | timeout 1200 virtctl -n "${NAMESPACE}" ssh "${VM_USER}"@vm/"${vm_name}" \
  -t "-o StrictHostKeyChecking=no" -t "-o UserKnownHostsFile=/dev/null" \
  -c "trap 'rm -f ~/gitlab-runner-vm/.env' EXIT; trap 'exit 129' HUP; trap 'exit 130' INT; trap 'exit 143' TERM; umask 077 && cat > ~/gitlab-runner-vm/.env && set -a && . ~/gitlab-runner-vm/.env && set +a && bash ~/gitlab-runner-vm/setup.sh"

# Setup succeeded — clear every rollback trap so a stray signal during the
# final output cannot deregister a healthy runner.
trap - ERR INT TERM

echo ""
echo "Done. Runner ${vm_name} (ID ${runner_id}) is ready."
echo "  Tag:       ${RUNNER_TAG}"
echo "  Namespace: ${NAMESPACE}"
echo "  SSH:       virtctl -n ${NAMESPACE} ssh ${VM_USER}@vm/${vm_name}"
