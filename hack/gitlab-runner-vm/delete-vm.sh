#!/usr/bin/env bash
#
# delete-vm.sh — Delete a GitLab Runner VM and deregister it from GitLab.
#
# This script:
#   1. Reads the runner token from the VM's config.toml
#   2. Deregisters the runner from GitLab via the API
#   3. Deletes the VirtualMachine from OpenShift (and its DataVolume)
#
# Required environment variables:
#   GL_TOKEN    — GitLab personal access token
#   GITLAB_URL  — GitLab instance URL (e.g. https://gitlab.example.com)
#   NAMESPACE   — OpenShift namespace
#
# Optional environment variables:
#   RUNNER_TAG  — runner tag used for registration (default: fullsend-gitlab-runner)
#
# Usage:
#   GL_TOKEN=glpat-xxx GITLAB_URL=https://gitlab.example.com NAMESPACE=my-ns \
#     ./delete-vm.sh fullsend-gitlab-runner-01
#
#   # List existing runner VMs:
#   NAMESPACE=my-ns ./delete-vm.sh --list
#
set -euo pipefail

GITLAB_URL="${GITLAB_URL:-}"
NAMESPACE="${NAMESPACE:-}"
RUNNER_TAG="${RUNNER_TAG:-fullsend-gitlab-runner}"
PREFIX="fullsend-gitlab-runner"

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

usage() {
  echo "Usage: GL_TOKEN=glpat-xxx $0 <vm-name> [vm-name ...]"
  echo "       $0 --list"
}

if [ "${1:-}" = "--help" ] || [ "${1:-}" = "-h" ]; then
  head -24 "$0" | tail -22 | sed 's/^# \?//'
  exit 0
fi

if [ "${1:-}" = "--list" ]; then
  if [ -z "${NAMESPACE}" ]; then
    echo "ERROR: NAMESPACE is required for --list" >&2
    exit 1
  fi
  echo "Runner VMs in ${NAMESPACE}:"
  oc -n "${NAMESPACE}" get vm --no-headers -o custom-columns=NAME:.metadata.name,STATUS:.status.printableStatus \
    | grep "^${PREFIX}" || echo "  (none)"
  exit 0
fi

if [ $# -eq 0 ]; then
  echo "ERROR: specify at least one VM name to delete" >&2
  usage >&2
  exit 1
fi

if [ -z "${GL_TOKEN:-}" ]; then
  echo "ERROR: GL_TOKEN is required (GitLab personal access token)" >&2
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

for vm_name in "$@"; do
  echo "==> Deleting ${vm_name}"

  # ------------------------------------------------------------------
  # 1. Find the runner ID from the VM's config.toml
  # ------------------------------------------------------------------
  runner_id=""

  # Look up the runner by description via the GitLab API (paginated).
  # Uses /runners (user-scoped) instead of /runners/all (admin-only).
  encoded_tag=$(python3 -c "import urllib.parse, sys; print(urllib.parse.quote(sys.argv[1]))" "${RUNNER_TAG}")
  page=1
  while [ -z "${runner_id}" ]; do
    page_json=$(gl_curl \
      "${GITLAB_URL}/api/v4/runners?per_page=100&page=${page}&tag_list=${encoded_tag}" 2>/dev/null) || break
    runner_id=$(echo "${page_json}" | python3 -c "
import sys, json
vm = sys.argv[1]
runners = json.load(sys.stdin)
for r in runners:
    if r.get('description') == vm:
        print(r['id'])
        break
" "${vm_name}" 2>/dev/null) || true
    [ -n "${runner_id}" ] && break
    count=$(echo "${page_json}" | python3 -c "import sys,json; print(len(json.load(sys.stdin)))" 2>/dev/null) || break
    [ "${count}" -lt 100 ] && break
    page=$((page + 1))
  done

  # ------------------------------------------------------------------
  # 2. Deregister from GitLab
  # ------------------------------------------------------------------
  if [ -n "${runner_id}" ]; then
    if gl_curl -X DELETE \
      "${GITLAB_URL}/api/v4/runners/${runner_id}" >/dev/null 2>&1; then
      echo "  OK: deregistered runner ID ${runner_id}"
    else
      echo "  WARN: failed to deregister runner ID ${runner_id}"
    fi
  else
    echo "  WARN: could not determine runner ID — skipping deregistration"
  fi

  # ------------------------------------------------------------------
  # 3. Delete the VM
  # ------------------------------------------------------------------
  if oc -n "${NAMESPACE}" delete vm "${vm_name}" --wait=false; then
    echo "  OK: VM ${vm_name} deletion initiated"
  else
    echo "  WARN: failed to delete VM ${vm_name} (may not exist)"
  fi

  # DataVolume shares the VM name — delete it too.
  oc -n "${NAMESPACE}" delete dv "${vm_name}" --wait=false 2>/dev/null || true

  echo ""
done

echo "Done."
