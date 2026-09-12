#!/usr/bin/env bash
# Per-job OpenShell gateway lifecycle for the GitLab custom executor.
#
# Sourced by prepare.sh and cleanup.sh. Not executed directly.
#
# GitLab runner VMs are long-lived. A systemd --user gateway that stays up
# across jobs accumulates a stale profile registry (`openshell provider
# profile import` is a no-op on "already exists") and a baked-in OpenShell
# version. GitHub Actions does not have this: each job installs a fresh,
# version-matched gateway with an empty registry, then throws the runner
# away. These helpers give that parity: create in prepare, tear down in
# cleanup, reap leftovers from an abruptly-killed prior job.
#
# The expensive image layers (runner image, supervisor) stay in the VM's
# Podman cache. Only the CLI (~39 MB) and a version-skewed supervisor
# (~30 MB) are fetched when the job's pin differs from the host.

# Renovate-tracked pin for the OpenShell version/commit this host trusts,
# read from .github/scripts/openshell-version.sh (the same file
# install-openshell.sh and setup.sh source). Provides OPENSHELL_VERSION and
# OPENSHELL_SHA. Try the flattened EXECUTOR_DIR layout first (.github/scripts/
# as a child of this script — install_executor in setup.sh ships it there
# since that's where prepare.sh/cleanup.sh actually source this file from at
# per-job runtime), then the VM source-tree layout (.github/scripts/ as a
# sibling of executor/), then the repo checkout layout.
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
  systemctl --user stop openshell-gateway.service 2>/dev/null || true
  # Never leave the unit enabled: a reboot or lingering user session
  # would resurrect the long-lived daemon this per-job model replaces.
  systemctl --user disable openshell-gateway.service 2>/dev/null || true
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

wait_for_openshell_gateway() {
  local i=1
  while [ "${i}" -le 10 ]; do
    if systemctl --user is-active --quiet openshell-gateway.service; then
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

  systemctl --user daemon-reload
  # start, not enable: the unit must not come back on reboot/linger.
  if ! systemctl --user start openshell-gateway.service; then
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
  systemctl --user stop openshell-gateway.service 2>/dev/null || true
  if ! systemctl --user disable openshell-gateway.service 2>/dev/null; then
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
