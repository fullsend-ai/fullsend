#!/usr/bin/env bash
# Called by Renovate postUpgradeTasks after an fd (sharkdp/fd) version bump.
# Reads the new FD_VERSION from the sandbox Containerfile, downloads the
# linux-musl x86_64 and aarch64 release tarballs the Containerfile fetches,
# computes their sha256 (fd publishes no checksum assets), and patches
# FD_SHA256_AMD64 / FD_SHA256_ARM64 so the image build's `sha256sum -c`
# passes.
#
# Both tarballs are downloaded and hashed before the Containerfile is
# modified, so a failure (missing ARG line, download error) leaves it
# untouched.
set -euo pipefail

FILE="images/sandbox/Containerfile"

# --- Read FD_VERSION ---
if [[ ! -f "${FILE}" ]]; then
  echo "error: ${FILE} not found" >&2
  exit 1
fi
VERSION=$(grep -oP '^ARG FD_VERSION=\K\S+' "${FILE}" || true)
if [[ -z "${VERSION}" ]]; then
  echo "error: could not extract FD_VERSION from ${FILE}" >&2
  exit 1
fi
if [[ ! "${VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "error: FD_VERSION is not a valid semver: ${VERSION}" >&2
  exit 1
fi

for arch in AMD64 ARM64; do
  count=$(grep -c "^ARG FD_SHA256_${arch}=" "${FILE}" || true)
  if [[ "${count}" != "1" ]]; then
    echo "error: expected exactly one 'ARG FD_SHA256_${arch}=' line in ${FILE}, found ${count}" >&2
    exit 1
  fi
done

echo "fd version: ${VERSION}"

WORKDIR="$(mktemp -d)"
trap 'rm -rf "${WORKDIR}"' EXIT

# tarball_sha downloads the release tarball for the given fd target arch
# (x86_64 or aarch64) — the same URL the Containerfile fetches — and prints
# its sha256.
tarball_sha() {
  local arch="$1"
  local name="fd-v${VERSION}-${arch}-unknown-linux-musl.tar.gz"
  local url="https://github.com/sharkdp/fd/releases/download/v${VERSION}/${name}"
  if ! curl -fsSL --retry 3 --retry-delay 5 "${url}" -o "${WORKDIR}/${name}"; then
    echo "error: failed to fetch ${url}" >&2
    return 1
  fi
  sha256sum "${WORKDIR}/${name}" | awk '{print $1}'
}

AMD64=$(tarball_sha x86_64)
ARM64=$(tarball_sha aarch64)

if [[ ! "${AMD64}" =~ ^[0-9a-f]{64}$ ]]; then
  echo "error: amd64 checksum is not a valid sha256 hex digest: ${AMD64}" >&2
  exit 1
fi
if [[ ! "${ARM64}" =~ ^[0-9a-f]{64}$ ]]; then
  echo "error: arm64 checksum is not a valid sha256 hex digest: ${ARM64}" >&2
  exit 1
fi

# --- Patch the Containerfile ---
sed -i "s/^ARG FD_SHA256_AMD64=.*/ARG FD_SHA256_AMD64=${AMD64}/" "${FILE}"
sed -i "s/^ARG FD_SHA256_ARM64=.*/ARG FD_SHA256_ARM64=${ARM64}/" "${FILE}"

if ! grep -q "^ARG FD_SHA256_AMD64=${AMD64}$" "${FILE}" \
  || ! grep -q "^ARG FD_SHA256_ARM64=${ARM64}$" "${FILE}"; then
  echo "error: failed to update FD_SHA256 ARGs in ${FILE}" >&2
  exit 1
fi

echo "updated fd checksums to v${VERSION}: amd64=${AMD64} arm64=${ARM64}"
