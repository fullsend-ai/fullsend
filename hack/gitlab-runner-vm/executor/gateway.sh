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

# Renovate-tracked provisioning pin, read from .github/scripts/openshell-version.sh
# (the same file install-openshell.sh and setup.sh source). Provides
# OPENSHELL_VERSION and OPENSHELL_SHA, the version the VM starts on. The
# per-job OpenShell version is NOT taken from this copy: it is resolved from
# the job image's Fullsend source commit (see resolve_fullsend_openshell_pin),
# so a VM serves newly published Fullsend releases without re-provisioning.
# Try the flattened EXECUTOR_DIR layout first (.github/scripts/
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
# host binary. An empty job version (none could be read) is never "different";
# ensure_job_openshell_gateway rejects it before asking.
openshell_versions_differ() {
  local job_ver="$1" host_ver="$2"
  [ -n "${job_ver}" ] && [ "${job_ver}" != "${host_ver}" ]
}

# Oldest OpenShell version a job image may pin. pin_supervisor_image writes
# only the schema-v2 gateway.toml introduced in 0.1. A 0.0.x gateway rejects
# that file (unknown field `compute_driver`) and exits on every restart, so
# ensure_job_openshell_gateway refuses pre-0.1 pins before touching the host
# (#8730). The executor migrates forward only.
OPENSHELL_MIN_JOB_VERSION="0.1.0"

# True when x.y.z version $1 is at least x.y.z version $2.
openshell_version_at_least() {
  local -a have want
  local i h w
  IFS=. read -r -a have <<< "$1"
  IFS=. read -r -a want <<< "$2"
  for i in 0 1 2; do
    h=$((10#${have[i]:-0}))
    w=$((10#${want[i]:-0}))
    if [ "${h}" -gt "${w}" ]; then
      return 0
    fi
    if [ "${h}" -lt "${w}" ]; then
      return 1
    fi
  done
  return 0
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

# Fullsend repository that is the root of trust for a job image's OpenShell
# pin. Constants, not configuration: nothing job-controlled may select them.
FULLSEND_SOURCE_REPO="fullsend-ai/fullsend"
FULLSEND_SOURCE_BRANCH="main"

# curl wrapper for the GitHub endpoints below. $1 is the Accept header,
# $2 the URL (built only from validated tags and 40-hex SHAs).
fullsend_github_get() {
  curl -fsSL --retry 3 --retry-delay 2 --max-time 30 -H "Accept: $1" "$2"
}

# Print the job image's org.opencontainers.image.revision label (a 40-hex
# Fullsend commit) from the already-pulled image. Reading the local image,
# never re-resolving a moving tag, keeps this tied to what will run.
job_image_revision() {
  local image="$1" out status=0 rev
  out=$(podman image inspect \
    --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' \
    -- "${image}" 2>&1) || status=$?
  if [ "${status}" -ne 0 ]; then
    echo "ERROR: could not inspect job image ${image} (podman exit ${status}): ${out}" >&2
    return 1
  fi
  rev="$(printf '%s' "${out}" | tr -d '[:space:]')"
  if ! printf '%s' "${rev}" | grep -Eq '^[0-9a-f]{40}$'; then
    echo "ERROR: job image ${image} has no org.opencontainers.image.revision label naming a Fullsend commit; cannot verify its OpenShell version" >&2
    return 1
  fi
  printf '%s' "${rev}"
}

# Print the release version when the image reference is a Fullsend release tag
# (X.Y.Z, optionally a prerelease; the runner image tags drop the Git tag's
# leading v) and fail otherwise. Digest references and moving tags (dev,
# latest, X.Y, short SHAs) are not release references.
job_image_release_version() {
  local ref="${1%%@*}" last tag
  last="${ref##*/}"
  case "${last}" in
    *:*) tag="${last##*:}" ;;
    *) return 1 ;;
  esac
  printf '%s' "${tag}" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$' || return 1
  printf '%s' "${tag}"
}

# A release image must agree with its Fullsend Git tag: succeed only when
# tag v$1 (peeled to its commit) resolves to commit $2.
verify_fullsend_release_tag() {
  local version="$1" rev="$2" tag_sha
  if ! tag_sha="$(fullsend_github_get 'application/vnd.github.sha' \
    "https://api.github.com/repos/${FULLSEND_SOURCE_REPO}/commits/v${version}" 2>/dev/null)"; then
    echo "ERROR: could not resolve Git tag v${version} in ${FULLSEND_SOURCE_REPO}" >&2
    return 1
  fi
  tag_sha="$(printf '%s' "${tag_sha}" | tr -d '[:space:]')"
  if [ "${tag_sha}" != "${rev}" ]; then
    echo "ERROR: release image ${version} was built from ${rev}, but Git tag v${version} resolves to ${tag_sha}" >&2
    return 1
  fi
}

# Succeed only when commit $1 is on the Fullsend default branch. raw.github
# usercontent and the API serve fork commits under the parent repository's
# path, so without this a fork commit could supply the pin. A verified commit
# stays on the branch, so successes are cached (also spares the API rate limit).
verify_fullsend_commit_on_default_branch() {
  local rev="$1" cache_dir marker body
  cache_dir="${HOME}/.local/state/fullsend/verified-revisions"
  marker="${cache_dir}/${rev}"
  [ -e "${marker}" ] && return 0
  if ! body="$(fullsend_github_get 'application/vnd.github+json' \
    "https://api.github.com/repos/${FULLSEND_SOURCE_REPO}/compare/${FULLSEND_SOURCE_BRANCH}...${rev}?per_page=1" 2>/dev/null)"; then
    echo "ERROR: could not compare commit ${rev} with ${FULLSEND_SOURCE_REPO}@${FULLSEND_SOURCE_BRANCH}" >&2
    return 1
  fi
  if ! grep -Eq '"status"[[:space:]]*:[[:space:]]*"(behind|identical)"' <<< "${body}"; then
    echo "ERROR: commit ${rev} is not on ${FULLSEND_SOURCE_REPO}@${FULLSEND_SOURCE_BRANCH}; refusing to trust its OpenShell pin" >&2
    return 1
  fi
  if mkdir -p "${cache_dir}" 2>/dev/null; then
    : > "${marker}" 2>/dev/null || true
  fi
  return 0
}

# Fullsend's runner image repository on GHCR. Only Fullsend's release workflow
# (.github/workflows/runner-image.yml) can push here, which is what makes a
# digest published under it a statement by Fullsend, unlike the job image's
# own labels. Constants, not configuration: nothing job-controlled may select
# them.
FULLSEND_RUNNER_IMAGE_REPO="fullsend-ai/fullsend-runner"

# Print every sha256 digest podman records for the already-pulled image: its
# manifest digest and the digests in its repo digests (a multi-arch pull
# records the index digest there). One per line. Never fails the caller on an
# empty result; the caller treats "no digests" as unverifiable.
job_image_digests() {
  local image="$1" out status=0
  out=$(podman image inspect \
    --format '{{ .Digest }} {{ range .RepoDigests }}{{ . }} {{ end }}' \
    -- "${image}" 2>&1) || status=$?
  if [ "${status}" -ne 0 ]; then
    echo "ERROR: could not inspect job image ${image} (podman exit ${status}): ${out}" >&2
    return 1
  fi
  printf '%s\n' "${out}" | grep -oE 'sha256:[0-9a-f]{64}' || true
}

# Print an anonymous pull token for Fullsend's runner image repository on GHCR.
fullsend_ghcr_pull_token() {
  local token_json token
  if ! token_json="$(curl -fsSL --retry 3 --retry-delay 2 --max-time 30 \
    "https://ghcr.io/token?service=ghcr.io&scope=repository:${FULLSEND_RUNNER_IMAGE_REPO}:pull" 2>/dev/null)"; then
    echo "ERROR: could not get a GHCR pull token for ${FULLSEND_RUNNER_IMAGE_REPO}" >&2
    return 1
  fi
  token="$(grep -oE '"token"[[:space:]]*:[[:space:]]*"[^"]+"' <<< "${token_json}" | head -1 | cut -d'"' -f4)" || true
  if [ -z "${token}" ]; then
    echo "ERROR: GHCR returned no pull token for ${FULLSEND_RUNNER_IMAGE_REPO}" >&2
    return 1
  fi
  printf '%s' "${token}"
}

# Look up digest $2 in Fullsend's runner image repository, read with pull
# token $1. The manifest is requested by digest, so the answer cannot change
# when a tag moves, and the registry must echo the requested digest back.
# Returns 0 when the repository holds the manifest, 1 when it does not (HTTP
# 404, or a response that does not name the requested digest), and 2 when the
# lookup itself failed (network or TLS error, timeout, or any other HTTP
# status), so the caller can tell "not published" from "could not check".
fullsend_registry_has_digest() {
  local token="$1" digest="$2" out code headers served
  if ! out="$(curl -sSI --retry 3 --retry-delay 2 --max-time 30 \
    -w '\n%{http_code}' \
    -H "Authorization: Bearer ${token}" \
    -H "Accept: application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json" \
    "https://ghcr.io/v2/${FULLSEND_RUNNER_IMAGE_REPO}/manifests/${digest}" 2>/dev/null)"; then
    return 2
  fi
  out="$(tr -d '\r' <<< "${out}")"
  code="$(tail -n 1 <<< "${out}")"
  headers="$(sed '$d' <<< "${out}")"
  case "${code}" in
    200) ;;
    404) return 1 ;;
    *) return 2 ;;
  esac
  served="$(grep -iE '^docker-content-digest:' <<< "${headers}" | grep -oE 'sha256:[0-9a-f]{64}' | head -1)" || true
  [ "${served}" = "${digest}" ] || return 1
}

# Succeed only when the already-pulled image $1 is byte-for-byte an image
# Fullsend's workflow published, which names commit $2 in its revision label.
# The label and `openshell --version` are written by whoever built the image,
# so on their own they prove nothing about where it came from. Only Fullsend
# can push to its runner image repository, so an image whose content digest
# that repository holds was built by Fullsend's workflow, which writes the
# label from the commit it built. The digests come from the already-pulled
# image and are looked up in the registry by digest, never through a tag: tags
# are mutable (main and tag-push builds of one commit both write the bare SHA
# tag, and `dev` moves on every main build), so a tag lookup would start
# refusing an official image once a later build moved the tag. Any one of the
# image's digests (manifest or multi-arch index) held by the repository is
# proof Fullsend published it; none refuses it. A registry lookup that failed
# (rather than answering "not found") is reported as unverifiable, not as an
# unpublished image, so the operator retries instead of replacing the image.
verify_image_published_by_fullsend() {
  local image="$1" rev="$2" local_digests digest token rc lookup_failed=0
  local_digests="$(job_image_digests "${image}")" || return 1
  if [ -n "${local_digests}" ]; then
    token="$(fullsend_ghcr_pull_token)" || return 1
    while IFS= read -r digest; do
      [ -n "${digest}" ] || continue
      rc=0
      fullsend_registry_has_digest "${token}" "${digest}" || rc=$?
      case "${rc}" in
        0) return 0 ;;
        1) ;;
        *) lookup_failed=1 ;;
      esac
    done <<< "${local_digests}"
    if [ "${lookup_failed}" -eq 1 ]; then
      echo "ERROR: could not verify that job image ${image} was published by Fullsend: a lookup in ${FULLSEND_RUNNER_IMAGE_REPO} on ghcr.io failed (network error or unexpected HTTP status); retry the job" >&2
      return 1
    fi
  fi
  echo "ERROR: job image ${image} is not an image Fullsend published for commit ${rev} (no digest of the pulled image is held by ${FULLSEND_RUNNER_IMAGE_REPO}); refusing to trust its OpenShell pin" >&2
  return 1
}

# Print "VERSION SHA": the OpenShell pin at Fullsend commit $1, read from
# .github/scripts/openshell-version.sh at that exact commit. The file is
# parsed, never sourced.
fetch_fullsend_openshell_pin() {
  local rev="$1" body ver sha
  if ! body="$(fullsend_github_get 'text/plain' \
    "https://raw.githubusercontent.com/${FULLSEND_SOURCE_REPO}/${rev}/.github/scripts/openshell-version.sh" 2>/dev/null)"; then
    echo "ERROR: could not fetch .github/scripts/openshell-version.sh at ${FULLSEND_SOURCE_REPO}@${rev}" >&2
    return 1
  fi
  ver="$(printf '%s\n' "${body}" | grep -E '^OPENSHELL_VERSION=[0-9]+\.[0-9]+\.[0-9]+$' | head -1 | cut -d= -f2)" || true
  sha="$(printf '%s\n' "${body}" | grep -E '^OPENSHELL_SHA=[0-9a-f]{40}$' | head -1 | cut -d= -f2)" || true
  if [ -z "${ver}" ] || [ -z "${sha}" ]; then
    echo "ERROR: ${FULLSEND_SOURCE_REPO}@${rev} has no valid OPENSHELL_VERSION/OPENSHELL_SHA pin" >&2
    return 1
  fi
  printf '%s %s' "${ver}" "${sha}"
}

# Resolve the OpenShell "VERSION SHA" a job image requires from its Fullsend
# build provenance: the image's OCI revision label, verified against the
# Fullsend repository (release images: the version's Git tag must resolve to
# that commit; every other image: the commit must be on the default branch),
# the image itself verified to be the one Fullsend published for that commit
# (the label is image-supplied, so naming a real commit is not enough), then
# the pin at that commit. Fails closed for images without verifiable
# provenance, so a job cannot authorize a host install, including a downgrade
# to an older pin, by merely printing a version or copying a label.
resolve_fullsend_openshell_pin() {
  local image="$1" rev release
  rev="$(job_image_revision "${image}")" || return 1
  if release="$(job_image_release_version "${image}")"; then
    verify_fullsend_release_tag "${release}" "${rev}" || return 1
  else
    verify_fullsend_commit_on_default_branch "${rev}" || return 1
  fi
  verify_image_published_by_fullsend "${image}" "${rev}" || return 1
  fetch_fullsend_openshell_pin "${rev}"
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

# Packaged schema-v2 default (OpenShell >= 0.1 RPM). Overridable for tests.
OPENSHELL_PACKAGED_GATEWAY_TOML="${OPENSHELL_PACKAGED_GATEWAY_TOML:-/usr/share/openshell-gateway/gateway.toml.default}"

gateway_toml_is_v2() {
  grep -Eq '^[[:space:]]*version[[:space:]]*=[[:space:]]*2[[:space:]]*(#.*)?$' "$1"
}

# Pin supervisor_image under [openshell.drivers.podman] in a schema-v2
# gateway.toml. OpenShell 0.1 rejects v1 and version-less files at startup,
# so such a file (written for 0.0.x) is moved aside to gateway.toml.pre-0.1.
# A missing file is seeded from the packaged default so compute_driver is
# kept (without it the gateway auto-detects Kubernetes before Podman); the
# literal fallback mirrors that default and action.yml.
pin_supervisor_image() {
  local ver="$1"
  local gateway_toml="${HOME}/.config/openshell/gateway.toml"
  local supervisor_image
  supervisor_image="$(openshell_supervisor_image "${ver}")"
  mkdir -p "${HOME}/.config/openshell"

  if [ -f "${gateway_toml}" ] && ! gateway_toml_is_v2 "${gateway_toml}"; then
    echo "Moving pre-0.1 OpenShell gateway config aside to ${gateway_toml}.pre-0.1"
    mv -f "${gateway_toml}" "${gateway_toml}.pre-0.1"
  fi

  if [ ! -f "${gateway_toml}" ]; then
    if [ -f "${OPENSHELL_PACKAGED_GATEWAY_TOML}" ] && gateway_toml_is_v2 "${OPENSHELL_PACKAGED_GATEWAY_TOML}"; then
      install -m 0644 "${OPENSHELL_PACKAGED_GATEWAY_TOML}" "${gateway_toml}"
    else
      printf '[openshell]\nversion = 2\n\n[openshell.gateway]\ncompute_driver = "podman"\n\n[openshell.drivers.podman]\nhealth_check_interval_secs = 10\n' > "${gateway_toml}"
    fi
  fi

  if grep -q '^[[:space:]]*supervisor_image[[:space:]]*=' "${gateway_toml}"; then
    sed -i "s|^[[:space:]]*supervisor_image[[:space:]]*=.*|supervisor_image = \"${supervisor_image}\"|" "${gateway_toml}"
  elif grep -q '^\[openshell\.drivers\.podman\]' "${gateway_toml}"; then
    sed -i "/^\[openshell\.drivers\.podman\]/a supervisor_image = \"${supervisor_image}\"" "${gateway_toml}"
  else
    printf '\n[openshell.drivers.podman]\nsupervisor_image = "%s"\n' "${supervisor_image}" >> "${gateway_toml}"
  fi
}

# Run before any OpenShell install: install.sh (re)starts the gateway unit,
# and 0.1 refuses a v1 gateway.toml and cannot use 0.0.x state. Tear the
# old gateway down with the still-installed CLI and write the v2 config.
prepare_openshell_upgrade() {
  teardown_openshell_gateway
  pin_supervisor_image "$1"
}

# Install the OpenShell version the job image's verified Fullsend commit pins
# — callers must pass the version and commit SHA from
# resolve_fullsend_openshell_pin (see ensure_job_openshell_gateway).
# install.sh is fetched from that commit SHA, never from a job-supplied
# release tag: the
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
    # install.sh refuses a pre-0.1 -> 0.1 upgrade without the ack; the
    # caller's prepare_openshell_upgrade has already migrated the host.
    if curl -LsSf --retry 3 --retry-delay 5 "${url}" \
      | OPENSHELL_ACK_BREAKING_UPGRADE=1 OPENSHELL_VERSION="v${ver}" sh; then
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
  local job_ver host_ver pin want_ver want_sha installed_ver
  job_ver="$(job_image_openshell_version "${image}")" || return 1
  host_ver="$(host_openshell_version)"

  if [ -z "${job_ver}" ]; then
    echo "ERROR: could not read an OpenShell version from job image ${image}" >&2
    return 1
  fi

  # The job image's self-reported version is job-controlled (any job that can
  # set `image:` on this runner), so it never authorizes anything by itself:
  # the version to install, and the commit its installer comes from, are the
  # pin at the image's verified Fullsend source commit, and the image's CLI
  # must agree with it. This runs on every path, including when the host
  # already has the job's version.
  pin="$(resolve_fullsend_openshell_pin "${image}")" || {
    echo "ERROR: could not verify the OpenShell version required by job image ${image}; use an official Fullsend runner image (see hack/gitlab-runner-vm/README.md#openshell-version-per-job)" >&2
    return 1
  }
  read -r want_ver want_sha <<< "${pin}"
  if [ "${job_ver}" != "${want_ver}" ]; then
    echo "ERROR: job image ${image} reports OpenShell ${job_ver}, but the Fullsend commit it was built from pins ${want_ver}" >&2
    return 1
  fi

  # Refuse before any teardown or install, so the host keeps its current
  # OpenShell and gateway config.
  if ! openshell_version_at_least "${want_ver}" "${OPENSHELL_MIN_JOB_VERSION}"; then
    echo "ERROR: job image ${image} pins OpenShell ${want_ver}, but this runner supports OpenShell ${OPENSHELL_MIN_JOB_VERSION} or newer; OpenShell 0.0.x images are no longer supported. Use a Fullsend runner image that pins OpenShell ${OPENSHELL_MIN_JOB_VERSION} or newer (see hack/gitlab-runner-vm/README.md#openshell-version-per-job)" >&2
    return 1
  fi

  if openshell_versions_differ "${job_ver}" "${host_ver}"; then
    echo "OpenShell host ${host_ver:-none} != job image ${job_ver}; installing the version pinned by the image's Fullsend commit"
    prepare_openshell_upgrade "${job_ver}"
    install_openshell_at_version "${job_ver}" "${want_sha}" || return 1
    # A successful installer run is not proof the host switched (an
    # installer may refuse a downgrade and still exit 0).
    installed_ver="$(host_openshell_version)"
    if [ "${installed_ver}" != "${job_ver}" ]; then
      echo "ERROR: host OpenShell is ${installed_ver:-unknown} after installing ${job_ver}" >&2
      return 1
    fi
    podman pull -- "$(openshell_supervisor_image "${job_ver}")" || return 1
  else
    echo "OpenShell ${job_ver} already matches the job image"
    pin_supervisor_image "${job_ver}"
  fi

  start_fresh_openshell_gateway
}
