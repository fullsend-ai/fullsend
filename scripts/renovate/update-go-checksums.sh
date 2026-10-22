#!/usr/bin/env bash
# Called by Renovate postUpgradeTasks after a Go toolchain (golang) version
# bump. Reads the new GO_VERSION from the code and runner Containerfiles,
# looks up the published sha256 of the linux-amd64 and linux-arm64 archives
# on https://go.dev/dl/?mode=json&include=all, and patches
# GO_SHA256_AMD64 / GO_SHA256_ARM64 in both files so the image builds'
# `sha256sum -c` passes.
#
# All lookups and validation happen before any file is modified, so a
# failure (unknown version, missing ARG line, bad checksum) leaves both
# Containerfiles untouched.
set -euo pipefail

FILES=("images/code/Containerfile" "images/runner/Containerfile")
DL_URL="https://go.dev/dl/?mode=json&include=all"

# --- Read GO_VERSION from both files; they must agree ---
VERSION=""
for f in "${FILES[@]}"; do
  if [[ ! -f "${f}" ]]; then
    echo "error: ${f} not found" >&2
    exit 1
  fi
  v=$(grep -oP '^ARG GO_VERSION=\K\S+' "${f}" || true)
  if [[ -z "${v}" ]]; then
    echo "error: could not extract GO_VERSION from ${f}" >&2
    exit 1
  fi
  if [[ ! "${v}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "error: GO_VERSION in ${f} is not a valid semver: ${v}" >&2
    exit 1
  fi
  if [[ -n "${VERSION}" && "${v}" != "${VERSION}" ]]; then
    echo "error: GO_VERSION mismatch: ${FILES[0]}=${VERSION} ${f}=${v}" >&2
    exit 1
  fi
  VERSION="${v}"

  for arch in AMD64 ARM64; do
    count=$(grep -c "^ARG GO_SHA256_${arch}=" "${f}" || true)
    if [[ "${count}" != "1" ]]; then
      echo "error: expected exactly one 'ARG GO_SHA256_${arch}=' line in ${f}, found ${count}" >&2
      exit 1
    fi
  done
done

echo "Go toolchain version: ${VERSION}"

# --- Fetch the release index and extract per-architecture checksums ---
WORKDIR="$(mktemp -d)"
trap 'rm -rf "${WORKDIR}"' EXIT

if ! curl -fsSL "${DL_URL}" -o "${WORKDIR}/releases.json"; then
  echo "error: failed to fetch ${DL_URL}" >&2
  exit 1
fi

# Match on the exact archive filename the Containerfiles download
# (go${GO_VERSION}.linux-${TARGETARCH}.tar.gz).
archive_sha() {
  local arch="$1"
  jq -r --arg v "go${VERSION}" --arg fn "go${VERSION}.linux-${arch}.tar.gz" '
    [.[] | select(.version == $v) | .files[]
      | select(.filename == $fn and .kind == "archive") | .sha256]
    | if length == 1 then .[0] else empty end
  ' "${WORKDIR}/releases.json"
}

AMD64=$(archive_sha amd64)
ARM64=$(archive_sha arm64)

if [[ ! "${AMD64}" =~ ^[0-9a-f]{64}$ ]]; then
  echo "error: no valid linux-amd64 archive sha256 for go${VERSION} at ${DL_URL}: '${AMD64}'" >&2
  exit 1
fi
if [[ ! "${ARM64}" =~ ^[0-9a-f]{64}$ ]]; then
  echo "error: no valid linux-arm64 archive sha256 for go${VERSION} at ${DL_URL}: '${ARM64}'" >&2
  exit 1
fi

# --- Patch both Containerfiles ---
for f in "${FILES[@]}"; do
  sed -i "s/^ARG GO_SHA256_AMD64=.*/ARG GO_SHA256_AMD64=${AMD64}/" "${f}"
  sed -i "s/^ARG GO_SHA256_ARM64=.*/ARG GO_SHA256_ARM64=${ARM64}/" "${f}"

  if ! grep -q "^ARG GO_SHA256_AMD64=${AMD64}$" "${f}" \
    || ! grep -q "^ARG GO_SHA256_ARM64=${ARM64}$" "${f}"; then
    echo "error: failed to update GO_SHA256 ARGs in ${f}" >&2
    exit 1
  fi
done

echo "updated Go checksums to go${VERSION}: amd64=${AMD64} arm64=${ARM64}"
