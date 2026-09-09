#!/usr/bin/env bash
#
# delete-gcp-vm.sh — Delete a GitLab Runner GCE VM and deregister it from GitLab.
#
# This script:
#   1. Finds the runner by description via the GitLab API
#   2. Deregisters the runner from GitLab
#   3. Deletes the GCE instance
#
# Required environment variables:
#   GL_TOKEN     — GitLab personal access token
#   GITLAB_URL   — GitLab instance URL (e.g. https://gitlab.example.com)
#   GCP_PROJECT  — GCP project ID
#
# Optional environment variables:
#   GCP_ZONE    — GCE zone (default: us-east1-b)
#   RUNNER_TAG  — runner tag used for registration (default: fullsend-gitlab-runner)
#
# Usage:
#   GL_TOKEN=glpat-xxx GITLAB_URL=https://gitlab.example.com \
#     GCP_PROJECT=my-gcp-project \
#     ./delete-gcp-vm.sh fullsend-gitlab-runner-01
#
#   # List existing runner VMs:
#   GCP_PROJECT=my-gcp-project ./delete-gcp-vm.sh --list
#
set -euo pipefail

GITLAB_URL="${GITLAB_URL:-}"
GCP_PROJECT="${GCP_PROJECT:-}"
GCP_ZONE="${GCP_ZONE:-us-east1-b}"
RUNNER_TAG="${RUNNER_TAG:-fullsend-gitlab-runner}"
PREFIX="fullsend-gitlab-runner"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# shellcheck source=lib.sh
source "${SCRIPT_DIR}/lib.sh"

usage() {
  echo "Usage: GL_TOKEN=glpat-xxx GCP_PROJECT=<project> $0 <vm-name> [vm-name ...]"
  echo "       GCP_PROJECT=<project> $0 --list"
}

if [ "${1:-}" = "--help" ] || [ "${1:-}" = "-h" ]; then
  head -26 "$0" | tail -24 | sed 's/^# \?//'
  exit 0
fi

if [ -z "${GCP_PROJECT}" ]; then
  echo "ERROR: GCP_PROJECT is required (GCP project ID)" >&2
  exit 1
fi

if [ "${1:-}" = "--list" ]; then
  echo "Runner VMs in ${GCP_PROJECT} (${GCP_ZONE}):"
  gcloud compute instances list \
    --project="${GCP_PROJECT}" \
    --filter="name~'^${PREFIX}-' AND zone:${GCP_ZONE}" \
    --format="table(name, status)" 2>/dev/null \
    | tail -n +2 || echo "  (none)"
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
proj, vm = sys.argv[1], sys.argv[2]
runners = json.load(sys.stdin)
for r in runners:
    desc = r.get('description', '')
    if desc == proj + '/' + vm:
        print(r['id'])
        break
" "${GCP_PROJECT}" "${vm_name}" 2>/dev/null) || true
    [ -n "${runner_id}" ] && break
    count=$(echo "${page_json}" | python3 -c "import sys,json; print(len(json.load(sys.stdin)))" 2>/dev/null) || { lookup_failed=true; break; }
    [ "${count}" -lt 100 ] && break
    page=$((page + 1))
  done

  if [ "${lookup_failed}" = true ]; then
    echo "  ERROR: GitLab API request failed — refusing to delete VM without deregistering runner" >&2
    echo "  Hint: check GL_TOKEN scopes (needs api + manage_runner) and network connectivity" >&2
    echo "  To force: manually deregister at ${GITLAB_URL}, then: gcloud compute instances delete ${vm_name} --project=${GCP_PROJECT} --zone=${GCP_ZONE} --quiet" >&2
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
      echo "  Hint: deregister at ${GITLAB_URL}, then re-run, or use: gcloud compute instances delete ${vm_name} --project=${GCP_PROJECT} --zone=${GCP_ZONE} --quiet" >&2
      had_errors=true
      continue
    fi
  else
    echo "  WARN: no matching runner found — skipping deregistration"
  fi

  # ------------------------------------------------------------------
  # 3. Delete the GCE instance
  # ------------------------------------------------------------------
  if delete_err=$(gcloud compute instances delete "${vm_name}" \
    --project="${GCP_PROJECT}" --zone="${GCP_ZONE}" --quiet 2>&1); then
    echo "  OK: VM ${vm_name} deleted"
  elif printf '%s' "${delete_err}" | grep -qi 'not found'; then
    echo "  WARN: VM ${vm_name} not found — nothing to delete"
  else
    echo "  ERROR: failed to delete VM ${vm_name}: ${delete_err}" >&2
    had_errors=true
  fi

  echo ""
done

if [ "${had_errors}" = true ]; then
  echo "Done (with errors — some VMs were skipped)."
  exit 1
fi
echo "Done."
