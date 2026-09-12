#!/usr/bin/env bash
#
# delete-gcp-vm.sh — Drain and delete a GitLab Runner GCE VM.
#
# This script:
#   1. Drains the runner on each VM (SIGQUIT + wait for runner-*
#      containers) unless --no-drain is set. Bounded by DRAIN_TIMEOUT_SEC
#      (default 600s); on cap overrun, warns and proceeds.
#   2. Deregisters the GitLab runner when this VM has its own registration
#      (description matches GCP_PROJECT/vm-name). Skipped in fleet mode.
#   3. Deletes the GCE instance.
#
# Two modes:
#   Fleet (RUNNER_TOKEN is set, or no proj/vm runner match):
#     Does not touch the shared fleet runner. GL_TOKEN is not required.
#     GitLab prunes the offline system_id.
#   Individual (GL_TOKEN is set and a proj/vm runner is found):
#     Deregisters that runner, then deletes the VM. GL_TOKEN is required
#     only when a deregistration will actually occur.
#
# Required environment variables:
#   GCP_PROJECT  — GCP project ID
#
# Mode-specific environment variables:
#   RUNNER_TOKEN — when set, skip GitLab deregistration (fleet mode).
#   GL_TOKEN     — GitLab PAT (scopes: api + manage_runner). Required unless
#                  RUNNER_TOKEN is set (fleet mode).
#   GITLAB_URL   — GitLab instance URL. Required unless RUNNER_TOKEN is set.
#
# Optional environment variables:
#   GCP_ZONE           — GCE zone (default: us-east1-b)
#   RUNNER_TAG         — runner tag used for lookup (default: fullsend-gitlab-runner)
#   DRAIN_TIMEOUT_SEC  — drain cap in seconds (default: 600)
#   RUNNER_USER        — Unix account gitlab-runner/podman run as on the VM
#                        (setup.sh's RUNNER_USER, i.e. whichever identity ran
#                        setup.sh). Used to drain as the correct identity when
#                        the caller's `gcloud compute ssh` connects as someone
#                        else. Default: unset (drain runs as the connecting
#                        identity; may under-report idle if that identity
#                        differs from the one gitlab-runner runs as).
#
# Arguments:
#   --no-drain  — skip the drain step and delete immediately
#   --list      — list runner VMs and exit
#   <vm-name>   — one or more VMs (fullsend-gitlab-runner-NN)
#
# Usage:
#   # Fleet VM (no GL_TOKEN):
#   RUNNER_TOKEN=glrt-xxx GCP_PROJECT=my-gcp-project \
#     ./delete-gcp-vm.sh fullsend-gitlab-runner-01
#
#   # Individual runner:
#   GL_TOKEN=glpat-xxx GITLAB_URL=https://gitlab.example.com \
#     GCP_PROJECT=my-gcp-project \
#     ./delete-gcp-vm.sh fullsend-gitlab-runner-01
#
#   # Skip drain:
#   GCP_PROJECT=my-gcp-project ./delete-gcp-vm.sh --no-drain \
#     fullsend-gitlab-runner-01
#
#   # List existing runner VMs:
#   GCP_PROJECT=my-gcp-project ./delete-gcp-vm.sh --list
#
set -euo pipefail

GITLAB_URL="${GITLAB_URL:-}"
GCP_PROJECT="${GCP_PROJECT:-}"
GCP_ZONE="${GCP_ZONE:-us-east1-b}"
RUNNER_TAG="${RUNNER_TAG:-fullsend-gitlab-runner}"
RUNNER_USER="${RUNNER_USER:-}"
PREFIX="fullsend-gitlab-runner"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
no_drain=false

# shellcheck source=lib.sh
source "${SCRIPT_DIR}/lib.sh"

usage() {
  echo "Usage: GCP_PROJECT=<project> $0 [--no-drain] <vm-name> [vm-name ...]"
  echo "       GCP_PROJECT=<project> $0 --list"
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

vm_names=()
for arg in "$@"; do
  if [ "${arg}" = "--no-drain" ]; then
    no_drain=true
  else
    vm_names+=("${arg}")
  fi
done

if [ "${#vm_names[@]}" -eq 0 ]; then
  echo "ERROR: specify at least one VM name to delete" >&2
  usage >&2
  exit 1
fi

if [ -n "${RUNNER_USER}" ] && ! [[ "${RUNNER_USER}" =~ ^[a-z_][a-z0-9_-]*$ ]]; then
  echo "ERROR: RUNNER_USER must be a plain Unix user name (got: ${RUNNER_USER})" >&2
  exit 1
fi

# GL_TOKEN is required unless RUNNER_TOKEN signals fleet mode. Fleet mode
# never deregisters, so it never needs GitLab credentials. Otherwise we must
# be able to look up a proj/vm match before deciding whether to deregister —
# omitting GL_TOKEN is not itself authorization to skip deregistration.
lookup_runners=false
if uses_runner_token; then
  :
elif [ -n "${GL_TOKEN:-}" ]; then
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
  lookup_runners=true
else
  echo "ERROR: GL_TOKEN is required unless RUNNER_TOKEN is set (fleet mode)" >&2
  echo "  Hint: set RUNNER_TOKEN for a fleet VM, or GL_TOKEN + GITLAB_URL to look up and deregister an individually-registered runner" >&2
  exit 1
fi

# SSH helper for drain_runner_vm. Reads vm_name from the caller's loop.
gcp_drain_ssh() {
  gcloud compute ssh "${vm_name}" \
    --project="${GCP_PROJECT}" \
    --zone="${GCP_ZONE}" \
    --tunnel-through-iap \
    --ssh-flag="-o StrictHostKeyChecking=no" \
    --ssh-flag="-o UserKnownHostsFile=/dev/null" \
    --ssh-flag="-o ConnectTimeout=10" \
    --command="$1"
}

had_errors=false
for vm_name in "${vm_names[@]}"; do
  if ! [[ "${vm_name}" =~ ^${PREFIX}-[0-9]+$ ]]; then
    echo "ERROR: invalid VM name '${vm_name}' — expected format: ${PREFIX}-NN" >&2
    had_errors=true
    continue
  fi

  echo "==> Deleting ${vm_name}"

  # ------------------------------------------------------------------
  # 0. Drain in-flight jobs (SIGQUIT + runner-* containers)
  # ------------------------------------------------------------------
  if [ "${no_drain}" = "true" ]; then
    echo "  skipping drain (--no-drain)"
  else
    drain_runner_vm gcp_drain_ssh "${RUNNER_USER}"
  fi

  # ------------------------------------------------------------------
  # 1. Find the runner ID via the GitLab API (individual mode only)
  # ------------------------------------------------------------------
  runner_id=""
  lookup_failed=false

  if [ "${lookup_runners}" = "true" ]; then
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

    if [ "${lookup_failed}" = "true" ]; then
      echo "  ERROR: GitLab API request failed — refusing to delete VM without deregistering runner" >&2
      echo "  Hint: check GL_TOKEN scopes (needs api + manage_runner) and network connectivity" >&2
      echo "  To force: manually deregister at ${GITLAB_URL}, then: gcloud compute instances delete ${vm_name} --project=${GCP_PROJECT} --zone=${GCP_ZONE} --quiet" >&2
      had_errors=true
      continue
    fi
  else
    echo "  skipping GitLab deregistration (fleet mode)"
  fi

  # ------------------------------------------------------------------
  # 2. Deregister from GitLab (only when a proj/vm match was found)
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
  elif [ "${lookup_runners}" = "true" ]; then
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

if [ "${had_errors}" = "true" ]; then
  echo "Done (with errors — some VMs were skipped)."
  exit 1
fi
echo "Done."
