#!/usr/bin/env bash
#
# delete-openshift-vm.sh — Delete a GitLab Runner VM from OpenShift and deregister it from GitLab.
#
# This script:
#   1. Finds the runner by description via the GitLab API
#   2. Deregisters the runner from GitLab
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
#     ./delete-openshift-vm.sh fullsend-gitlab-runner-01
#
#   # List existing runner VMs:
#   NAMESPACE=my-ns ./delete-openshift-vm.sh --list
#
set -euo pipefail

GITLAB_URL="${GITLAB_URL:-}"
NAMESPACE="${NAMESPACE:-}"
RUNNER_TAG="${RUNNER_TAG:-fullsend-gitlab-runner}"
PREFIX="fullsend-gitlab-runner"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# shellcheck source=lib.sh
source "${SCRIPT_DIR}/lib.sh"

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
if ! [[ "${GL_TOKEN}" =~ ^[A-Za-z0-9._-]+$ ]]; then
  echo "ERROR: GL_TOKEN contains invalid characters" >&2
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

had_errors=false
for vm_name in "$@"; do
  if ! [[ "${vm_name}" =~ ^${PREFIX}-[0-9]+$ ]]; then
    echo "ERROR: invalid VM name '${vm_name}' — expected format: ${PREFIX}-NN" >&2
    had_errors=true
    continue
  fi

  echo "==> Deleting ${vm_name}"

  # ------------------------------------------------------------------
  # 1. Find the runner ID via the GitLab API
  # ------------------------------------------------------------------
  runner_id=""
  lookup_failed=false

  # Look up the runner by description via the GitLab API (paginated).
  # Uses /runners (user-scoped) instead of /runners/all (admin-only).
  encoded_tag=$(python3 -c "import urllib.parse, sys; print(urllib.parse.quote(sys.argv[1]))" "${RUNNER_TAG}")
  page=1
  while [ -z "${runner_id}" ] && [ "${page}" -le 50 ]; do
    if ! page_json=$(gl_curl \
      "${GITLAB_URL}/api/v4/runners?per_page=100&page=${page}&tag_list=${encoded_tag}" 2>/dev/null); then
      lookup_failed=true
      break
    fi
    runner_id=$(echo "${page_json}" | python3 -c "
import sys, json
ns, vm = sys.argv[1], sys.argv[2]
runners = json.load(sys.stdin)
for r in runners:
    desc = r.get('description', '')
    if desc == ns + '/' + vm:
        print(r['id'])
        break
" "${NAMESPACE}" "${vm_name}" 2>/dev/null) || true
    [ -n "${runner_id}" ] && break
    count=$(echo "${page_json}" | python3 -c "import sys,json; print(len(json.load(sys.stdin)))" 2>/dev/null) || { lookup_failed=true; break; }
    [ "${count}" -lt 100 ] && break
    page=$((page + 1))
  done

  if [ "${lookup_failed}" = true ]; then
    echo "  ERROR: GitLab API request failed — refusing to delete VM without deregistering runner" >&2
    echo "  Hint: check GL_TOKEN scopes (needs api + manage_runner) and network connectivity" >&2
    echo "  To force: manually deregister at ${GITLAB_URL}, then: oc -n ${NAMESPACE} delete vm -- ${vm_name}" >&2
    had_errors=true
    continue
  fi

  # ------------------------------------------------------------------
  # 2. Deregister from GitLab
  # ------------------------------------------------------------------
  if [ -n "${runner_id}" ]; then
    if gl_curl -X DELETE \
      "${GITLAB_URL}/api/v4/runners/${runner_id}" >/dev/null 2>&1; then
      echo "  OK: deregistered runner ID ${runner_id}"
    else
      echo "  ERROR: failed to deregister runner ID ${runner_id} — refusing to delete VM (would orphan the registration)" >&2
      echo "  Hint: deregister at ${GITLAB_URL}, then re-run, or use: oc -n ${NAMESPACE} delete vm -- ${vm_name}" >&2
      had_errors=true
      continue
    fi
  else
    echo "  WARN: no matching runner found — skipping deregistration"
  fi

  # ------------------------------------------------------------------
  # 3. Delete the VM
  # ------------------------------------------------------------------
  # Only a genuine NotFound is benign here. Anything else (RBAC, webhook
  # rejection, API outage) must set had_errors — otherwise the script prints
  # "Done." and exits 0 while the VM keeps running, and the runner has already
  # been deregistered above, leaving nothing to find it by.
  if delete_err=$(oc -n "${NAMESPACE}" delete vm --wait=false -- "${vm_name}" 2>&1); then
    echo "  OK: VM ${vm_name} deletion initiated"
  elif printf '%s' "${delete_err}" | grep -q '(NotFound)'; then
    echo "  WARN: VM ${vm_name} not found — nothing to delete"
  else
    echo "  ERROR: failed to delete VM ${vm_name}: ${delete_err}" >&2
    had_errors=true
  fi

  # DataVolume shares the VM name — delete it too, with the same
  # NotFound-vs-error split as the VM above.
  if delete_err=$(oc -n "${NAMESPACE}" delete dv --wait=false -- "${vm_name}" 2>&1); then
    echo "  OK: DataVolume ${vm_name} deletion initiated"
  elif printf '%s' "${delete_err}" | grep -q '(NotFound)'; then
    : # no DataVolume — nothing to delete
  else
    echo "  ERROR: failed to delete DataVolume ${vm_name}: ${delete_err}" >&2
    had_errors=true
  fi

  echo ""
done

if [ "${had_errors}" = true ]; then
  echo "Done (with errors — some VMs were skipped)."
  exit 1
fi
echo "Done."
