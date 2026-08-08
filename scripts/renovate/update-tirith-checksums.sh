#!/usr/bin/env bash
# Called by Renovate postUpgradeTasks after a tirith version bump.
# Fetches the new release's checksums.txt, verifies its cosign signature
# against the Sigstore transparency log, and patches the Containerfile
# so the SHA256 ARGs match the bumped version.
set -euo pipefail

# --- Retrieve the new Tirith version ---
FILE="images/sandbox/Containerfile"
VERSION=$(grep -oP 'ARG TIRITH_VERSION=\K\S+' "${FILE}" || true)
if [[ -z "${VERSION}" ]]; then
    echo "Tried to retrieve Tirith version from ${FILE}, couldn't do it. Exiting."
    exit 1
fi
if [[ ! "${VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "error: VERSION is not a valid semver: ${VERSION}" >&2
    exit 1
fi

BASE_URL="https://github.com/sheeki03/tirith/releases/download/v${VERSION}"

# --- Bootstrap cosign if not already available ---
# Pinned version + SHA256 so this script is self-contained inside the
# Renovate container, which does not ship cosign.
COSIGN_VERSION=3.1.3
COSIGN_SHA256=4629c757b7618056f8ddd7e2625ae9fdd94c0372a65049520bc7d9df9efc7f71

WORKDIR="$(mktemp -d)"
trap 'rm -rf "${WORKDIR}"' EXIT

if command -v cosign &>/dev/null; then
  COSIGN=cosign
else
  COSIGN="${WORKDIR}/cosign"
  curl -fsSL "https://github.com/sigstore/cosign/releases/download/v${COSIGN_VERSION}/cosign-linux-amd64" \
    -o "${COSIGN}"
  echo "${COSIGN_SHA256}  ${COSIGN}" | sha256sum -c -
  chmod +x "${COSIGN}"
fi

# --- Fetch checksums and cosign verification artifacts ---
for f in checksums.txt checksums.txt.sig checksums.txt.pem; do
  if ! curl -fsSL "${BASE_URL}/${f}" -o "${WORKDIR}/${f}"; then
    echo "error: failed to fetch ${BASE_URL}/${f}" >&2
    exit 1
  fi
done

# --- Verify the cosign signature on checksums.txt ---
# Pin the certificate SAN to tirith's .github/workflows/ path (not the whole
# repo), matching the upstream install.sh scope.
"${COSIGN}" verify-blob \
  --certificate "${WORKDIR}/checksums.txt.pem" \
  --signature "${WORKDIR}/checksums.txt.sig" \
  --certificate-identity-regexp "^https://github\\.com/sheeki03/tirith/\\.github/workflows/" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  "${WORKDIR}/checksums.txt"

echo "cosign signature verified for tirith v${VERSION} checksums.txt"

# --- Extract per-architecture SHA256 hashes ---
AMD64=$(grep -E '^[0-9a-f]{64}  tirith-x86_64-unknown-linux-gnu\.tar\.gz$' "${WORKDIR}/checksums.txt" | awk '{print $1}' || true)
ARM64=$(grep -E '^[0-9a-f]{64}  tirith-aarch64-unknown-linux-gnu\.tar\.gz$' "${WORKDIR}/checksums.txt" | awk '{print $1}' || true)

if [[ ! "${AMD64}" =~ ^[0-9a-f]{64}$ ]]; then
  echo "error: amd64 checksum is not a valid sha256 hex digest: ${AMD64}" >&2
  exit 1
fi
if [[ ! "${ARM64}" =~ ^[0-9a-f]{64}$ ]]; then
  echo "error: arm64 checksum is not a valid sha256 hex digest: ${ARM64}" >&2
  exit 1
fi

sed -i "s/^ARG TIRITH_SHA256_AMD64=.*/ARG TIRITH_SHA256_AMD64=${AMD64}/" "${FILE}"
sed -i "s/^ARG TIRITH_SHA256_ARM64=.*/ARG TIRITH_SHA256_ARM64=${ARM64}/" "${FILE}"

echo "updated tirith checksums to v${VERSION}: amd64=${AMD64} arm64=${ARM64}"
