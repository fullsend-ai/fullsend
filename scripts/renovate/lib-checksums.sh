#!/usr/bin/env bash
# lib-checksums.sh — shared helpers for the Renovate postUpgradeTasks scripts
# that refresh *_SHA256* ARGs in images/*/Containerfile after a version bump.
#
# Sourced, not executed. Renovate only runs commands matching
# `^bash scripts/renovate/[a-zA-Z0-9_-]+\.sh$` (RENOVATE_ALLOWED_COMMANDS in
# .github/workflows/renovate.yml), so each pin gets its own argument-free
# update-*-checksums.sh wrapper that sources this file and calls update_pin.
#
# A wrapper defines two callbacks and then calls update_pin:
#
#   asset_url <arch>
#       Print the download URL the Containerfile uses for ${VERSION} on
#       <arch> (AMD64 or ARM64, or "" for a single-SHA pin).
#   published_sha <arch> <url>
#       Print the sha256 the upstream publishes for that artifact, or
#       nothing when the upstream publishes none (only allowed when the
#       wrapper sets ALLOW_UNPUBLISHED=1). Exit non-zero on a fetch or parse
#       failure.
#
#   update_pin <PIN> <single|multi> <file>...
#       Reads ARG <PIN>_VERSION from every file (they must agree), downloads
#       each artifact, computes its sha256, checks it against published_sha,
#       and only then rewrites ARG <PIN>_SHA256 (single) or
#       ARG <PIN>_SHA256_{AMD64,ARM64} (multi) in every file. Any failure
#       exits non-zero before a file is touched.
#
# Helpers for published_sha: checksum_file_sha, github_asset_digest,
# release_notes_sha.

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  echo "error: lib-checksums.sh is a library; source it from an update-*-checksums.sh script" >&2
  exit 1
fi

set -euo pipefail

# VERSION is set by update_pin before any callback runs. ALLOW_UNPUBLISHED
# is deliberately not read from the environment: only a wrapper may opt out
# of the published-checksum check, by setting it after sourcing this file.
VERSION=""
ALLOW_UNPUBLISHED=0
SHA_RE='^[0-9a-f]{64}$'

CHECKSUM_WORKDIR="$(mktemp -d)"
trap 'rm -rf "${CHECKSUM_WORKDIR}"' EXIT

die() {
  echo "error: $*" >&2
  exit 1
}

# fetch_cached <url> [header...] — download <url> once per run and print the
# local path. Callbacks run in command substitutions, so the cache lives on
# disk rather than in a shell variable.
fetch_cached() {
  local url="$1"
  shift
  local key path
  key=$(printf '%s' "${url}" | sha256sum | cut -c1-32)
  path="${CHECKSUM_WORKDIR}/fetch-${key}"
  if [[ ! -f "${path}" ]]; then
    local args=(-fsSL --retry 3 --retry-delay 5)
    local h
    for h in "$@"; do
      args+=(-H "${h}")
    done
    if ! curl "${args[@]}" -o "${path}.part" "${url}"; then
      rm -f "${path}.part"
      echo "error: failed to fetch ${url}" >&2
      return 1
    fi
    mv "${path}.part" "${path}"
  fi
  printf '%s\n' "${path}"
}

# checksum_file_sha <checksum-file-url> <asset-name> — print the sha256 for
# <asset-name> from a sha256sum-style file ("<hex>  <name>" or
# "<hex> *<name>"; a leading path on the name is ignored). A file holding a
# single bare digest (some per-asset .sha256 files) is also accepted.
checksum_file_sha() {
  local url="$1" asset="$2" path
  path=$(fetch_cached "${url}") || return 1
  local matches
  matches=$(awk -v want="${asset}" '
    {
      sub(/\r$/, "")
      if (NF == 2) {
        name = $2
        sub(/^\*/, "", name)
        n = split(name, parts, "/")
        if (parts[n] == want) print tolower($1)
      }
    }
  ' "${path}" | sort -u)
  if [[ -z "${matches}" ]]; then
    local tokens
    tokens=$(tr -d '\r' < "${path}" | awk 'NF { for (i = 1; i <= NF; i++) print $i }')
    if [[ "$(printf '%s\n' "${tokens}" | wc -l)" -eq 1 ]]; then
      matches=$(printf '%s' "${tokens}" | tr 'A-F' 'a-f')
    fi
  fi
  if [[ -z "${matches}" ]]; then
    echo "error: no checksum for ${asset} in ${url}" >&2
    return 1
  fi
  if [[ "$(printf '%s\n' "${matches}" | wc -l)" -ne 1 ]]; then
    echo "error: conflicting checksums for ${asset} in ${url}" >&2
    return 1
  fi
  printf '%s\n' "${matches}"
}

# github_release_json <owner/repo> <tag> — fetch the GitHub release for <tag>
# and print the local path. Uses GITHUB_TOKEN when the environment has one;
# anonymous requests work too, within the API's unauthenticated rate limit.
github_release_json() {
  local repo="$1" tag="$2"
  local headers=("Accept: application/vnd.github+json")
  if [[ -n "${GITHUB_TOKEN:-}" ]]; then
    headers+=("Authorization: Bearer ${GITHUB_TOKEN}")
  fi
  fetch_cached "https://api.github.com/repos/${repo}/releases/tags/${tag}" "${headers[@]}"
}

# github_asset_digest <owner/repo> <tag> <asset-name> — print the sha256
# GitHub recorded for a release asset at upload (the API's "digest" field).
# Used for upstreams that ship no checksum file of their own.
github_asset_digest() {
  local repo="$1" tag="$2" asset="$3" path digest
  path=$(github_release_json "${repo}" "${tag}") || return 1
  if ! digest=$(jq -r --arg n "${asset}" '
      [.assets[]? | select(.name == $n) | .digest // empty]
      | if length == 1 then .[0] else empty end
    ' "${path}"); then
    echo "error: could not parse the ${repo} ${tag} release JSON" >&2
    return 1
  fi
  if [[ "${digest}" != sha256:* ]]; then
    echo "error: no sha256 digest for asset ${asset} in the ${repo} ${tag} release" >&2
    return 1
  fi
  printf '%s\n' "${digest#sha256:}"
}

# release_notes_sha <owner/repo> <tag> — print the tarball SHA256 the
# fullsend-ai pi extensions publish in their release notes, in the
# "| SHA256 | `<hex>` |" row of the "For pinned/vendored consumers" table.
release_notes_sha() {
  local repo="$1" tag="$2" path body matches
  path=$(github_release_json "${repo}" "${tag}") || return 1
  if ! body=$(jq -r '.body // ""' "${path}"); then
    echo "error: could not parse the ${repo} ${tag} release JSON" >&2
    return 1
  fi
  matches=$(printf '%s\n' "${body}" \
    | grep -oiE '^\|[[:space:]]*SHA256[[:space:]]*\|[[:space:]]*`[0-9a-fA-F]{64}`' \
    | grep -oE '[0-9a-fA-F]{64}' | tr 'A-F' 'a-f' | sort -u || true)
  if [[ -z "${matches}" ]]; then
    echo "error: no SHA256 row in the ${repo} ${tag} release notes" >&2
    return 1
  fi
  if [[ "$(printf '%s\n' "${matches}" | wc -l)" -ne 1 ]]; then
    echo "error: more than one SHA256 in the ${repo} ${tag} release notes" >&2
    return 1
  fi
  printf '%s\n' "${matches}"
}

# read_pin_version <file> <PIN> — print ARG <PIN>_VERSION from <file>.
read_pin_version() {
  local file="$1" pin="$2" count value
  count=$(grep -c "^ARG ${pin}_VERSION=" "${file}" || true)
  if [[ "${count}" != "1" ]]; then
    die "expected exactly one 'ARG ${pin}_VERSION=' line in ${file}, found ${count}"
  fi
  value=$(grep -oP "^ARG ${pin}_VERSION=\\K\\S+" "${file}" || true)
  if [[ ! "${value}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    die "${pin}_VERSION in ${file} is not a valid semver: '${value}'"
  fi
  printf '%s\n' "${value}"
}

update_pin() {
  local pin="$1" mode="$2"
  shift 2
  local files=("$@")
  local arches
  case "${mode}" in
    single) arches=("") ;;
    multi) arches=(AMD64 ARM64) ;;
    *) die "update_pin: mode must be 'single' or 'multi', got '${mode}'" ;;
  esac
  [[ ${#files[@]} -gt 0 ]] || die "update_pin: no files given"

  # --- Read the bumped version; every file must carry the same one ---
  local f v arch name count
  VERSION=""
  for f in "${files[@]}"; do
    [[ -f "${f}" ]] || die "${f} not found"
    v=$(read_pin_version "${f}" "${pin}")
    if [[ -n "${VERSION}" && "${v}" != "${VERSION}" ]]; then
      die "${pin}_VERSION mismatch: ${files[0]}=${VERSION} ${f}=${v}"
    fi
    VERSION="${v}"
    for arch in "${arches[@]}"; do
      name="${pin}_SHA256${arch:+_${arch}}"
      count=$(grep -c "^ARG ${name}=" "${f}" || true)
      if [[ "${count}" != "1" ]]; then
        die "expected exactly one 'ARG ${name}=' line in ${f}, found ${count}"
      fi
    done
  done
  echo "${pin} version: ${VERSION}"

  # --- Download, hash and verify every artifact before writing anything ---
  local -A new_sha=()
  local url artifact computed published
  for arch in "${arches[@]}"; do
    name="${pin}_SHA256${arch:+_${arch}}"
    url=$(asset_url "${arch}") || die "could not build the ${name} download URL"
    artifact=$(fetch_cached "${url}") || die "failed to download ${url}"
    computed=$(sha256sum "${artifact}" | cut -d' ' -f1)
    [[ "${computed}" =~ ${SHA_RE} ]] || die "computed ${name} is not a sha256 hex digest: '${computed}'"

    published=$(published_sha "${arch}" "${url}") || die "could not get the published checksum for ${url}"
    if [[ -z "${published}" ]]; then
      if [[ "${ALLOW_UNPUBLISHED}" != "1" ]]; then
        die "upstream published no checksum for ${url}"
      fi
      echo "note: upstream publishes no checksum for ${url}; using the computed sha256" >&2
    else
      [[ "${published}" =~ ${SHA_RE} ]] || die "published checksum for ${url} is not a sha256 hex digest: '${published}'"
      if [[ "${published}" != "${computed}" ]]; then
        die "checksum mismatch for ${url}: published ${published}, downloaded ${computed}"
      fi
    fi
    new_sha["${name}"]="${computed}"
  done

  # --- Patch every file ---
  for f in "${files[@]}"; do
    for name in "${!new_sha[@]}"; do
      sed -i "s/^ARG ${name}=.*/ARG ${name}=${new_sha[${name}]}/" "${f}"
      if ! grep -qx "ARG ${name}=${new_sha[${name}]}" "${f}"; then
        die "failed to update ${name} in ${f}"
      fi
    done
  done

  for name in "${!new_sha[@]}"; do
    echo "updated ${name} to ${new_sha[${name}]} (${pin} ${VERSION})"
  done
}
