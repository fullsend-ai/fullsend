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
#   GL_TOKEN     — GitLab personal access token (needs Maintainer on PROJECT_ID)
#   PROJECT_ID   — GitLab project ID to register the runner against
#   GITLAB_URL   — GitLab instance URL (e.g. https://gitlab.example.com)
#   NAMESPACE    — OpenShift namespace for the VM
#   RUNNER_IMAGE — container image for jobs (e.g. ghcr.io/org/runner:v1.2.3)
#
# Optional environment variables:
#   RUNNER_TAG        — runner tag for job matching (default: fullsend-gitlab-runner)
#   OPENSHELL_VERSION — OpenShell version to install (default: 0.0.83)
#
# Arguments:
#   [NUMBER]  — optional runner number (e.g. 01, 03). Auto-increments if omitted.
#
# Examples:
#   # Auto-numbers the VM:
#   GL_TOKEN=glpat-xxx PROJECT_ID=266558 \
#     GITLAB_URL=https://gitlab.example.com NAMESPACE=my-namespace \
#     RUNNER_IMAGE=ghcr.io/org/runner:v1.2.3 ./create-vm.sh
#
#   # Explicit runner number:
#   GL_TOKEN=glpat-xxx PROJECT_ID=266558 \
#     GITLAB_URL=https://gitlab.example.com NAMESPACE=my-namespace \
#     RUNNER_IMAGE=ghcr.io/org/runner:v1.2.3 ./create-vm.sh 01
#
set -euo pipefail

GITLAB_URL="${GITLAB_URL:-}"
NAMESPACE="${NAMESPACE:-}"
RUNNER_TAG="${RUNNER_TAG:-fullsend-gitlab-runner}"
RUNNER_IMAGE="${RUNNER_IMAGE:-}"
OPENSHELL_VERSION="${OPENSHELL_VERSION:-0.0.83}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
TEMPLATE="${SCRIPT_DIR}/vm.yaml"
PREFIX="fullsend-gitlab-runner"

# Wrap curl with GL_TOKEN passed via a temp config file to avoid
# exposing the token in /proc/<pid>/cmdline.
gl_curl() {
  local config
  config=$(mktemp)
  printf 'header = "PRIVATE-TOKEN: %s"\n' "${GL_TOKEN}" > "${config}"
  chmod 600 "${config}"
  curl -sf -K "${config}" "$@"
  local rc=$?
  rm -f "${config}"
  return $rc
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
  head -39 "$0" | tail -37 | sed 's/^# \?//'
  exit 0
fi

if [ -z "${GL_TOKEN:-}" ]; then
  echo "ERROR: GL_TOKEN is required (GitLab personal access token)" >&2
  usage >&2
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

# ----------------------------------------------------------------------
# 1. Pick the VM number (explicit arg or auto-increment)
# ----------------------------------------------------------------------
if [ -n "${1:-}" ]; then
  if ! [[ "$1" =~ ^[0-9]+$ ]]; then
    echo "ERROR: NUMBER must be numeric (got: $1)" >&2
    exit 1
  fi
  next=$(printf "%02d" "$1")
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

python3 -c "
import sys
template = sys.stdin.read()
print(template.replace('__VM_NAME__', sys.argv[1]).replace('__SSH_PUBLIC_KEY__', sys.argv[2]), end='')
" "${vm_name}" "${SSH_PUBLIC_KEY}" < "${TEMPLATE}" \
  | oc apply -n "${NAMESPACE}" -f -

# ----------------------------------------------------------------------
# 3. Wait for the VM to boot and accept SSH
# ----------------------------------------------------------------------
echo "==> Waiting for ${vm_name} to boot..."
for i in $(seq 1 60); do
  if virtctl -n "${NAMESPACE}" ssh fedora@"${vm_name}" \
    -t "-o StrictHostKeyChecking=no" -t "-o UserKnownHostsFile=/dev/null" -t "-o ConnectTimeout=5" \
    -c "true" >/dev/null 2>&1; then
    echo "  OK: VM is up (${i}0s)"
    break
  fi
  if [ "${i}" -eq 60 ]; then
    echo "ERROR: VM did not become reachable after 10 minutes" >&2
    exit 1
  fi
  sleep 10
done

# ----------------------------------------------------------------------
# 4. Register a runner via the GitLab API
# ----------------------------------------------------------------------
echo "==> Registering runner with ${GITLAB_URL} (project ${PROJECT_ID})"

runner_json=$(gl_curl -X POST \
  "${GITLAB_URL}/api/v4/user/runners" \
  --data-urlencode "runner_type=project_type" \
  --data-urlencode "project_id=${PROJECT_ID}" \
  --data-urlencode "tag_list=${RUNNER_TAG}" \
  --data-urlencode "description=${vm_name}" \
  --data-urlencode "run_untagged=false" 2>&1) || {
  echo "ERROR: GitLab runner registration failed. Response: ${runner_json}" >&2
  exit 1
}

if [ -z "${runner_json}" ]; then
  echo "ERROR: GitLab runner registration returned empty response" >&2
  exit 1
fi

REGISTRATION_TOKEN=$(echo "${runner_json}" | python3 -c "import sys,json; print(json.load(sys.stdin)['token'])")
runner_id=$(echo "${runner_json}" | python3 -c "import sys,json; print(json.load(sys.stdin)['id'])")

echo "  OK: runner ID ${runner_id} created"

# ----------------------------------------------------------------------
# 5. Copy setup files to the VM
# ----------------------------------------------------------------------
echo "==> Copying setup files to ${vm_name}"

virtctl -n "${NAMESPACE}" ssh fedora@"${vm_name}" \
  -t "-o StrictHostKeyChecking=no" -t "-o UserKnownHostsFile=/dev/null" \
  -c "mkdir -p ~/gitlab-runner-vm/executor"

for file in setup.sh create-vm.sh vm.yaml; do
  virtctl -n "${NAMESPACE}" ssh fedora@"${vm_name}" \
    -t "-o StrictHostKeyChecking=no" -t "-o UserKnownHostsFile=/dev/null" \
    -c "cat > ~/gitlab-runner-vm/${file}" < "${SCRIPT_DIR}/${file}"
done

for file in prepare.sh run.sh cleanup.sh; do
  virtctl -n "${NAMESPACE}" ssh fedora@"${vm_name}" \
    -t "-o StrictHostKeyChecking=no" -t "-o UserKnownHostsFile=/dev/null" \
    -c "cat > ~/gitlab-runner-vm/executor/${file}" < "${SCRIPT_DIR}/executor/${file}"
done

virtctl -n "${NAMESPACE}" ssh fedora@"${vm_name}" \
  -t "-o StrictHostKeyChecking=no" -t "-o UserKnownHostsFile=/dev/null" \
  -c "chmod +x ~/gitlab-runner-vm/setup.sh ~/gitlab-runner-vm/create-vm.sh ~/gitlab-runner-vm/executor/*.sh"

echo "  OK: files copied"

# ----------------------------------------------------------------------
# 6. Run setup.sh on the VM
# ----------------------------------------------------------------------
echo "==> Running setup.sh on ${vm_name}"

# Write env vars to a file on the VM to avoid exposing secrets in the process list.
# Values are single-quoted to prevent interpretation of special characters.
for val in "${REGISTRATION_TOKEN}" "${GITLAB_URL}" "${RUNNER_TAG}" "${RUNNER_IMAGE}" "${OPENSHELL_VERSION}"; do
  if [[ "${val}" == *"'"* ]]; then
    echo "ERROR: environment variable values must not contain single quotes" >&2
    exit 1
  fi
done
{
  printf "REGISTRATION_TOKEN='%s'\n" "${REGISTRATION_TOKEN}"
  printf "GITLAB_URL='%s'\n" "${GITLAB_URL}"
  printf "RUNNER_TAG='%s'\n" "${RUNNER_TAG}"
  printf "RUNNER_IMAGE='%s'\n" "${RUNNER_IMAGE}"
  printf "OPENSHELL_VERSION='%s'\n" "${OPENSHELL_VERSION}"
} | virtctl -n "${NAMESPACE}" ssh fedora@"${vm_name}" \
  -t "-o StrictHostKeyChecking=no" -t "-o UserKnownHostsFile=/dev/null" \
  -c "cat > ~/gitlab-runner-vm/.env && chmod 600 ~/gitlab-runner-vm/.env"

# Run setup — capture exit code before .env cleanup. Roll back registration on failure.
if ! virtctl -n "${NAMESPACE}" ssh fedora@"${vm_name}" \
  -t "-o StrictHostKeyChecking=no" -t "-o UserKnownHostsFile=/dev/null" \
  -c "set -a && . ~/gitlab-runner-vm/.env && set +a && bash ~/gitlab-runner-vm/setup.sh; rc=\$?; rm -f ~/gitlab-runner-vm/.env; exit \$rc"; then
  echo "ERROR: setup.sh failed on ${vm_name} — rolling back runner registration" >&2
  gl_curl -X DELETE \
    "${GITLAB_URL}/api/v4/runners/${runner_id}" >/dev/null 2>&1 || true
  echo "  NOTE: VM ${vm_name} was not cleaned up — run: NAMESPACE=${NAMESPACE} ./delete-vm.sh ${vm_name}" >&2
  exit 1
fi

echo ""
echo "Done. Runner ${vm_name} (ID ${runner_id}) is ready."
echo "  Tag:       ${RUNNER_TAG}"
echo "  Namespace: ${NAMESPACE}"
echo "  SSH:       virtctl -n ${NAMESPACE} ssh fedora@${vm_name}"
