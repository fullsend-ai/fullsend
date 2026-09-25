#!/usr/bin/env bash
# Per-job OpenShell gateway lifecycle for the GitLab custom executor.
#
# Sourced by prepare.sh and cleanup.sh. Not executed directly.
# Idempotent: every helper is safe to re-run and must stay that way
# (teardown/wipe/reap are no-ops on a clean host; start_fresh always
# recreates from a wiped store).
#
# GitLab runner VMs are long-lived. A systemd --user gateway that stays up
# across jobs accumulates a stale profile registry (`openshell provider
# profile import` is a no-op on "already exists") and a baked-in OpenShell
# version. GitHub Actions does not have this: each job installs a fresh,
# version-matched gateway with an empty registry, then throws the runner
# away. These helpers give that parity: create in prepare, tear down in
# cleanup, reap leftovers from an abruptly-killed prior job.
# GitLab Runner is a system service, so `systemctl --user` has no login
# session: user_systemctl pins XDG_RUNTIME_DIR / DBUS_SESSION_BUS_ADDRESS
# to the linger instance before every user-systemd call (#7453).
#
# The expensive image layers (runner image, supervisor) stay in the VM's
# Podman cache. Only the CLI (~39 MB) and a version-skewed supervisor
# (~30 MB) are fetched when the job's pin differs from the host.
# Unused superseded images are reclaimed by podman-prune.sh (hourly
# timer plus a call from prepare/cleanup) so the ~30 GiB root disk
# cannot fill (#7663).

# Renovate-tracked pin for the OpenShell version/commit this host trusts,
# read from .github/scripts/openshell-version.sh (the same file
# install-openshell.sh and setup.sh source). Provides OPENSHELL_VERSION and
# OPENSHELL_SHA. Try the flattened EXECUTOR_DIR layout first (.github/scripts/
# as a child of this script — install_executor in setup.sh ships it there
# since that's where prepare.sh/cleanup.sh actually source this file from at
# per-job runtime), then the VM source-tree layout (.github/scripts/ as a
# sibling of executor/), then the repo checkout layout.
# See ../README.md#executor-script-layout for the flattening constraint any
# new executor script that reaches outside its own directory must follow.
_gateway_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
_openshell_version_sh="${_gateway_dir}/.github/scripts/openshell-version.sh"
if [ ! -f "${_openshell_version_sh}" ]; then
  _openshell_version_sh="${_gateway_dir}/../.github/scripts/openshell-version.sh"
fi
if [ ! -f "${_openshell_version_sh}" ]; then
  _openshell_version_sh="${_gateway_dir}/../../../.github/scripts/openshell-version.sh"
fi
if [ -f "${_openshell_version_sh}" ]; then
  # shellcheck source=../../../.github/scripts/openshell-version.sh
  source "${_openshell_version_sh}"
fi

# GitLab Runner is a systemd *system* service running as RUNNER_USER
# (setup_runner_user). It does not go through pam_systemd, so this process
# never inherits XDG_RUNTIME_DIR / DBUS_SESSION_BUS_ADDRESS. Linger
# (setup_podman) keeps user@UID.service and its bus alive, but
# `systemctl --user` still needs those variables *here* to find the bus —
# otherwise it fails with:
#   Failed to connect to user scope bus via local transport:
#   $DBUS_SESSION_BUS_ADDRESS and $XDG_RUNTIME_DIR not defined
# Always pin them to the linger instance's canonical paths rather than
# trusting inherited values (a parent may have an empty or stale session).
ensure_user_systemd_env() {
  local uid
  uid="$(id -u)"
  export XDG_RUNTIME_DIR="/run/user/${uid}"
  export DBUS_SESSION_BUS_ADDRESS="unix:path=${XDG_RUNTIME_DIR}/bus"
}

# Every user-systemd call in this file (and in setup.sh, which sources us)
# goes through this wrapper so a missing login session cannot skip the env.
user_systemctl() {
  ensure_user_systemd_env
  command systemctl --user "$@"
}

# Reclaim unused rootless Podman storage. Installed by setup.sh
# (install_podman_prune); no-op on VMs that have not been re-provisioned.
# timeout + || true: never fail the job stage if prune is slow or errors.
#
# $1, if given, is an extra image ref (repository:tag) to protect for this
# invocation only, on top of the persistent keep-file. prepare.sh passes the
# job's own image: the keep-file only lists the provision-time warm cache
# (RUNNER_IMAGE, OpenShell supervisor), not CUSTOM_ENV_CI_JOB_IMAGE, and
# `podman images` lists newest-first — a cached copy of the image about to
# be pulled is a plausible rmi target otherwise, forcing a re-pull right
# after the prune that was supposed to make room for it (#7663).
#
# When FULLSEND_PODMAN_PRUNE_LOCK_HELD is already exported (prepare.sh's
# caller holds the serialization lock via acquire_podman_prune_lock below),
# that flag propagates to the podman-prune.sh child process and tells it not
# to also try to flock the file — see acquire_podman_prune_lock for why.
prune_unused_podman_storage() {
  local prune="${HOME}/.local/lib/fullsend/podman-prune.sh"
  local extra_keep="${1:-}"
  if [ -x "${prune}" ]; then
    echo "Pruning unused Podman storage"
    FULLSEND_PODMAN_PRUNE_EXTRA_KEEP="${extra_keep}" timeout --kill-after=5 30 "${prune}" || true
  fi
}

# Well-known lock serializing the hourly podman-prune.sh timer against
# prepare.sh's own reclaim-then-pull-then-create window. job_in_flight() in
# podman-prune.sh only treats a non-exited runner-*/openshell-* container as
# in-flight, but prepare.sh reaps those leftovers and does not create
# runner-${JOB_ID} until after the image pull and
# ensure_job_openshell_gateway — so during that window no such container
# exists yet and a concurrent timer tick sees nothing in-flight. It can then
# run `podman image prune -f` / `podman rmi` while prepare.sh's own pull is
# still writing layers, deleting a dangling layer mid-write or (once the
# per-invocation extra-keep protection from prune_unused_podman_storage's
# own call has ended) the job's freshly cached image (review on #7669).
PODMAN_PRUNE_LOCK_FILE="${FULLSEND_PODMAN_PRUNE_LOCK_FILE:-${HOME}/.local/state/fullsend-gitlab-runner/podman-prune.lock}"

# Acquire the lock for the rest of the caller's critical section (prepare.sh
# holds it from just before prune_unused_podman_storage until after `podman
# start`). Exports FULLSEND_PODMAN_PRUNE_LOCK_HELD so a podman-prune.sh
# invocation made from inside that section trusts the caller instead of
# trying to flock the same path itself — a child process locking it
# independently would see the parent's lock as unavailable and skip a prune
# the caller actually wants to run.
#
# The lock lives on this shell's open file descriptor, so it is released by
# the kernel when this process exits for any reason — normal completion,
# `set -e`, or a signal — even if release_podman_prune_lock below is never
# reached. That is why cleanup.sh needs no matching unlock call for
# prepare.sh's failure paths.
acquire_podman_prune_lock() {
  mkdir -p "$(dirname "${PODMAN_PRUNE_LOCK_FILE}")"
  exec {PODMAN_PRUNE_LOCK_FD}>"${PODMAN_PRUNE_LOCK_FILE}"
  flock -x "${PODMAN_PRUNE_LOCK_FD}"
  export FULLSEND_PODMAN_PRUNE_LOCK_HELD=1
}

# Release a lock taken by acquire_podman_prune_lock. Safe to call even when
# no lock was acquired (e.g. a second, defensive call).
release_podman_prune_lock() {
  if [ -n "${PODMAN_PRUNE_LOCK_FD:-}" ]; then
    flock -u "${PODMAN_PRUNE_LOCK_FD}" 2>/dev/null || true
    exec {PODMAN_PRUNE_LOCK_FD}>&- 2>/dev/null || true
    unset PODMAN_PRUNE_LOCK_FD
  fi
  unset FULLSEND_PODMAN_PRUNE_LOCK_HELD
}

# OpenShell's systemd user unit sets StateDirectory=openshell/gateway, which
# for a user service is ~/.local/state/openshell/gateway (SQLite). TLS
# material lives in ~/.local/state/openshell/tls. Wiping both and restarting
# yields an empty profile registry and new mTLS certs — a process restart
# alone does not.
openshell_state_root() {
  printf '%s' "${HOME}/.local/state/openshell"
}

# True when the job image's OpenShell version is known and differs from the
# host binary. An empty job version means we could not read one (stubbed
# runtime, image without a CLI) and should keep the host install.
openshell_versions_differ() {
  local job_ver="$1" host_ver="$2"
  [ -n "${job_ver}" ] && [ "${job_ver}" != "${host_ver}" ]
}

# Print the first x.y.z in $1, or empty. Never fails (prepare.sh uses -e).
parse_openshell_version() {
  local ver
  ver=$(printf '%s\n' "$1" | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1) || true
  printf '%s' "${ver}"
}

job_image_openshell_version() {
  local image="$1" out status=0
  # Hardened the same as the real job container (prepare.sh): a crafted
  # `openshell` entrypoint should not get a weaker-isolation execution just
  # to have its self-reported version read.
  out=$(podman run --rm --cap-drop=ALL --security-opt=no-new-privileges \
    --pids-limit 4096 --entrypoint openshell -- "${image}" --version 2>&1) || status=$?
  if [ "${status}" -ne 0 ]; then
    echo "ERROR: could not probe OpenShell version from job image ${image} (podman exit ${status}): ${out}" >&2
    return 1
  fi
  parse_openshell_version "${out}"
}

host_openshell_version() {
  local out
  out=$(openshell --version 2>/dev/null || true)
  parse_openshell_version "${out}"
}

# $1 is a commit SHA (never a mutable release tag — see
# install_openshell_at_version for why).
openshell_install_script_url() {
  printf 'https://raw.githubusercontent.com/NVIDIA/OpenShell/%s/install.sh' "$1"
}

openshell_supervisor_image() {
  printf 'ghcr.io/nvidia/openshell/supervisor:%s' "$1"
}

stop_openshell_gateway() {
  user_systemctl stop openshell-gateway.service 2>/dev/null || true
  # Never leave the unit enabled: a reboot or lingering user session
  # would resurrect the long-lived daemon this per-job model replaces.
  user_systemctl disable openshell-gateway.service 2>/dev/null || true
}

wipe_openshell_gateway_store() {
  local root
  root="$(openshell_state_root)"
  rm -rf "${root}/gateway" "${root}/tls"
}

# Sandboxes are Podman containers labeled openshell.managed=true (OpenShell
# Podman driver) and named openshell-*. A killed job leaves them behind;
# the leaked sandbox is what pinned the stale profile on runner-01.
reap_openshell_sandboxes() {
  command -v podman >/dev/null 2>&1 || return 0
  local id name
  while IFS= read -r id; do
    [ -n "${id}" ] || continue
    echo "Removing leftover OpenShell sandbox: ${id}"
    podman rm -f -- "${id}" 2>/dev/null || true
  done < <(podman ps -aq --filter label=openshell.managed=true 2>/dev/null || true)

  while IFS= read -r name; do
    [ -n "${name}" ] || continue
    case "${name}" in
      openshell-*)
        echo "Removing leftover OpenShell container: ${name}"
        podman rm -f -- "${name}" 2>/dev/null || true
        ;;
    esac
  done < <(podman ps -a --format '{{.Names}}' 2>/dev/null || true)
}

teardown_openshell_gateway() {
  reap_openshell_sandboxes
  stop_openshell_gateway
  wipe_openshell_gateway_store
  openshell gateway remove openshell >/dev/null 2>&1 || true
}

# prepare.sh calls this first so a prior job that skipped cleanup (runner
# crash, timeout kill) cannot leak a gateway or sandbox into this job.
reap_orphaned_openshell() {
  echo "Reaping leftover OpenShell gateway/sandboxes from a previous job"
  teardown_openshell_gateway
}

# Reap a leftover job container from an abruptly killed prior job (runner
# process crash, timeout kill — cleanup.sh never ran). $1 is the container
# name this job is about to create; it is excluded even though it cannot
# exist yet, for clarity at the call site.
#
# These VMs register exactly one runner with the default concurrency of 1
# (see setup.sh's patch_config: "Single-runner VM assumption"), so any other
# runner-* container found here belongs to a job GitLab already considers
# finished — it can only be a leftover. Without this reap, a leftover stuck
# in a non-exited state pins podman-prune.sh's job_in_flight() check forever:
# prepare.sh, cleanup.sh, and the hourly timer would all skip the entire
# reclaim (container prune, dangling images, and tagged-image rmi)
# indefinitely, reproducing the disk-exhaustion failure mode (#7663).
reap_orphaned_runner_containers() {
  command -v podman >/dev/null 2>&1 || return 0
  local keep="${1:-}" name
  while IFS= read -r name; do
    [ -n "${name}" ] || continue
    case "${name}" in
      runner-*)
        [ "${name}" = "${keep}" ] && continue
        echo "Removing leftover job container: ${name}"
        podman rm -f -- "${name}" 2>/dev/null || true
        ;;
    esac
  done < <(podman ps -a --format '{{.Names}}' 2>/dev/null || true)
}

wait_for_openshell_gateway() {
  local i=1
  while [ "${i}" -le 10 ]; do
    if user_systemctl is-active --quiet openshell-gateway.service; then
      return 0
    fi
    i=$((i + 1))
    sleep 1
  done
  echo "ERROR: gateway did not start after 10s — check: journalctl --user -u openshell-gateway" >&2
  return 1
}

register_openshell_gateway() {
  # ExecStartPre regenerates TLS certs after a store wipe. Drop the stale
  # CLI registration (old certs) and re-add so mTLS succeeds.
  openshell gateway remove openshell >/dev/null 2>&1 || true

  local add_err
  if ! add_err=$(openshell gateway add --local https://127.0.0.1:17670 2>&1) \
    && ! openshell gateway select openshell >/dev/null 2>&1; then
    echo "ERROR: could not register or select the OpenShell gateway: ${add_err}" >&2
    return 1
  fi
  if ! openshell gateway list 2>/dev/null | sed 's/\x1b\[[0-9;]*m//g' | grep -Eq '^[[:space:]]*\*'; then
    echo "ERROR: no active OpenShell gateway after add/select" >&2
    return 1
  fi
  return 0
}

# Recreate the gateway process AND its store. A restart without the wipe
# keeps the stale profile registry — the original bug.
start_fresh_openshell_gateway() {
  stop_openshell_gateway
  reap_openshell_sandboxes
  wipe_openshell_gateway_store

  user_systemctl daemon-reload
  # start, not enable: the unit must not come back on reboot/linger.
  if ! user_systemctl start openshell-gateway.service; then
    echo "ERROR: systemctl failed to start openshell-gateway.service" >&2
    return 1
  fi
  wait_for_openshell_gateway || return 1
  register_openshell_gateway || return 1
  echo "OpenShell gateway is running with an empty profile registry"
}

pin_supervisor_image() {
  local ver="$1"
  local gateway_toml="${HOME}/.config/openshell/gateway.toml"
  local supervisor_image
  supervisor_image="$(openshell_supervisor_image "${ver}")"
  mkdir -p "${HOME}/.config/openshell"
  if [ -f "${gateway_toml}" ] && grep -q "supervisor_image" "${gateway_toml}"; then
    sed -i "s|supervisor_image = .*|supervisor_image = \"${supervisor_image}\"|" "${gateway_toml}"
  elif [ -f "${gateway_toml}" ] && grep -q '^\[openshell\.gateway\]' "${gateway_toml}"; then
    sed -i "/^\[openshell\.gateway\]/a supervisor_image = \"${supervisor_image}\"" "${gateway_toml}"
  elif [ -f "${gateway_toml}" ]; then
    printf '\n[openshell.gateway]\nsupervisor_image = "%s"\n' "${supervisor_image}" >> "${gateway_toml}"
  fi
}

# Install the OpenShell version the job pins, but only when that version is
# the Renovate-tracked pin (OPENSHELL_VERSION/OPENSHELL_SHA, sourced above
# from .github/scripts/openshell-version.sh) — callers must verify that match
# before calling this (see ensure_job_openshell_gateway). install.sh is
# fetched from that commit SHA, never from a job-supplied release tag: the
# job image's self-reported `openshell --version` is job-controlled input,
# and a mutable vX.Y.Z tag could be retagged or rolled back, bypassing the
# commit-SHA allowlist .github/scripts/install-openshell.sh deliberately
# enforces for VM provisioning.
install_openshell_at_version() {
  local ver="${1:-}" sha="${2:-}"
  if ! printf '%s' "${ver}" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
    echo "ERROR: invalid OpenShell version: ${ver}" >&2
    return 1
  fi
  if ! printf '%s' "${sha}" | grep -Eq '^[0-9a-f]{40}$'; then
    echo "ERROR: invalid OpenShell commit SHA: ${sha}" >&2
    return 1
  fi
  echo "Installing OpenShell ${ver} (${sha}, job-matched)"
  local url
  url="$(openshell_install_script_url "${sha}")"
  local max_attempts=3 attempt=1 delay=5
  while true; do
    if curl -LsSf --retry 3 --retry-delay 5 "${url}" \
      | OPENSHELL_VERSION="v${ver}" sh; then
      break
    fi
    if [ "${attempt}" -ge "${max_attempts}" ]; then
      echo "ERROR: OpenShell ${ver} install failed after ${max_attempts} attempts" >&2
      return 1
    fi
    echo "WARN: OpenShell install attempt ${attempt}/${max_attempts} failed, retrying in ${delay}s..." >&2
    sleep "${delay}"
    attempt=$((attempt + 1))
    delay=$((delay * 3))
  done
  # The RPM %post may enable the user unit; pin it back to per-job. Stop
  # first so a running unit doesn't block disable, and fail if disable does
  # not succeed — otherwise a reboot before the next prepare/cleanup could
  # resurrect the long-lived daemon this per-job model replaces.
  user_systemctl stop openshell-gateway.service 2>/dev/null || true
  if ! user_systemctl disable openshell-gateway.service 2>/dev/null; then
    echo "ERROR: could not disable openshell-gateway.service after installing OpenShell ${ver}" >&2
    return 1
  fi
}

# Pull the job image's OpenShell version onto the host if needed, then start
# a gateway with an empty registry. Called from prepare.sh after the image
# pull so `podman run --entrypoint openshell` can read the pin.
ensure_job_openshell_gateway() {
  local image="$1"
  local job_ver host_ver
  job_ver="$(job_image_openshell_version "${image}")" || return 1
  host_ver="$(host_openshell_version)"

  if openshell_versions_differ "${job_ver}" "${host_ver}"; then
    # The job image's self-reported version is job-controlled (any job that
    # can set `image:` on this runner). Only ever install the host's
    # Renovate-tracked pin — never a version the job merely claims to want —
    # so a job cannot direct the host to fetch and run an
    # arbitrary/retagged/older NVIDIA/OpenShell installer.
    if [ "${job_ver}" != "${OPENSHELL_VERSION:-}" ]; then
      echo "ERROR: job image reports OpenShell ${job_ver}, but this host only trusts the Renovate-pinned ${OPENSHELL_VERSION:-<unset>} (commit ${OPENSHELL_SHA:-<unset>})" >&2
      echo "ERROR: bump .github/scripts/openshell-version.sh (operator-approved) before running jobs that need a different OpenShell version" >&2
      return 1
    fi
    echo "OpenShell host ${host_ver:-none} != job image ${job_ver}; installing the pinned version"
    install_openshell_at_version "${job_ver}" "${OPENSHELL_SHA:-}" || return 1
    pin_supervisor_image "${job_ver}"
    podman pull -- "$(openshell_supervisor_image "${job_ver}")" || return 1
  elif [ -n "${job_ver}" ]; then
    echo "OpenShell ${job_ver} already matches the job image"
    pin_supervisor_image "${job_ver}"
  else
    echo "Could not read OpenShell version from job image; using host ${host_ver:-unknown}"
  fi

  start_fresh_openshell_gateway
}
