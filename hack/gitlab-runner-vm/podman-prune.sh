#!/usr/bin/env bash
# Prune unused rootless Podman containers and images on a GitLab runner VM.
#
# Long-lived runners accumulate superseded job images until the root
# filesystem fills (issue #7663). This script is the ExecStart of the
# systemd --user timer installed by setup.sh.
#
# Safety:
# - Never stops or force-removes a running container.
# - Skips the whole run when a job-related container is in-flight
#   (running, created, paused, restarting) so we cannot race prepare.sh's
#   create→start window or contend with an active job.
# - Does not pass -f to podman rmi, so an image that becomes in-use
#   between listing and removal is left alone.
# - Tagged images listed in the keep-file (the warm cache setup.sh
#   pre-pulled) are never removed. FULLSEND_PODMAN_PRUNE_EXTRA_KEEP, when
#   set, protects one additional ref for that invocation only (gateway.sh's
#   prune_unused_podman_storage uses this to protect the job's own image
#   during prepare.sh's pre-pull prune).
# - Serializes against prepare.sh's own reclaim-then-pull-then-create window
#   via a well-known flock (see acquire_podman_prune_lock in gateway.sh):
#   this timer-driven run skips entirely, the same as the in-flight skip
#   below, when prepare.sh already holds the lock (review on #7669).
#
# Idempotent: safe to re-run; a clean host is a no-op.
set -euo pipefail

KEEP_FILE="${FULLSEND_PODMAN_KEEP_IMAGES:-${HOME}/.config/fullsend-gitlab-runner/keep-images}"
# Must match gateway.sh's PODMAN_PRUNE_LOCK_FILE default.
LOCK_FILE="${FULLSEND_PODMAN_PRUNE_LOCK_FILE:-${HOME}/.local/state/fullsend-gitlab-runner/podman-prune.lock}"
# Stopped leftovers older than this may be reaped. prepare.sh's create→start
# window is milliseconds; 30m is well above that and still reclaims images
# held by killed-job containers that cleanup.sh never saw.
CONTAINER_UNTIL="${FULLSEND_PODMAN_PRUNE_UNTIL:-30m}"

log() { echo "==> $*"; }

# True when a runner-* / openshell-* container is not already stopped.
# Created-but-not-started counts as in-flight: prepare.sh writes the
# container then starts it, and container prune would delete it in between.
job_in_flight() {
  local name state
  while IFS='|' read -r name state; do
    [ -n "${name}" ] || continue
    case "${name}" in
      runner-*|openshell-*) ;;
      *) continue ;;
    esac
    state=$(printf '%s' "${state}" | tr '[:upper:]' '[:lower:]')
    case "${state}" in
      exited|stopped|dead) ;;
      *)
        log "skipping prune: in-flight job container ${name} (${state})"
        return 0
        ;;
    esac
  done < <(podman ps -a --format '{{.Names}}|{{.State}}' 2>/dev/null || true)
  return 1
}

is_keep_ref() {
  local ref="$1" line
  # A caller-supplied extra ref (e.g. prepare.sh protecting the job's own
  # image for its pre-pull invocation) is checked before the persistent
  # keep-file so it applies even on a call with a valid but non-matching
  # keep-file.
  if [ -n "${FULLSEND_PODMAN_PRUNE_EXTRA_KEEP:-}" ] && [ "${ref}" = "${FULLSEND_PODMAN_PRUNE_EXTRA_KEEP}" ]; then
    return 0
  fi
  [ -f "${KEEP_FILE}" ] || return 1
  while IFS= read -r line || [ -n "${line}" ]; do
    case "${line}" in
      ''|\#*) continue ;;
    esac
    if [ "${line}" = "${ref}" ]; then
      return 0
    fi
  done < "${KEEP_FILE}"
  return 1
}

# Remove tagged images that are not in the keep-file. Dangling images are
# handled by `podman image prune`; images in use fail rmi without -f.
prune_unused_tagged_images() {
  local ref
  if [ ! -f "${KEEP_FILE}" ]; then
    log "keep-file ${KEEP_FILE} missing — skipping tagged-image removal"
    return 0
  fi
  while IFS= read -r ref; do
    [ -n "${ref}" ] || continue
    case "${ref}" in
      *'<none>') continue ;;
    esac
    if is_keep_ref "${ref}"; then
      log "keeping ${ref}"
      continue
    fi
    log "removing unused image ${ref}"
    podman rmi -- "${ref}" 2>/dev/null || true
  done < <(podman images --format '{{.Repository}}:{{.Tag}}' 2>/dev/null || true)
}

if ! command -v podman >/dev/null 2>&1; then
  echo "ERROR: podman not found in PATH" >&2
  exit 1
fi

# FULLSEND_PODMAN_PRUNE_LOCK_HELD means a caller (prepare.sh, via
# gateway.sh's acquire_podman_prune_lock) already serialized this
# invocation as part of its own critical section — trust it rather than
# also flocking here, which a child process doing independently would see
# as unavailable (the caller holds it) and skip a prune the caller wants to
# run. Anyone else (in practice, only the hourly timer's direct ExecStart)
# takes the lock itself, non-blocking, and skips the whole run if another
# holder (prepare.sh or cleanup.sh's own prune) already has it.
if [ -z "${FULLSEND_PODMAN_PRUNE_LOCK_HELD:-}" ]; then
  mkdir -p "$(dirname "${LOCK_FILE}")"
  exec {PODMAN_PRUNE_OWN_LOCK_FD}>"${LOCK_FILE}"
  if ! flock -n "${PODMAN_PRUNE_OWN_LOCK_FD}"; then
    log "skipping prune: lock held by another prune/prepare run"
    exit 0
  fi
fi

if job_in_flight; then
  exit 0
fi

log "pruning stopped containers older than ${CONTAINER_UNTIL}"
podman container prune -f --filter "until=${CONTAINER_UNTIL}" || true

log "pruning dangling images"
podman image prune -f || true

log "pruning unused tagged images (preserving keep-file)"
prune_unused_tagged_images

log "podman prune complete"
