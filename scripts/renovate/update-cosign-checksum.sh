#!/usr/bin/env bash
# Called by Renovate postUpgradeTasks after a cosign version bump.
# Downloads the new cosign binary, verifies its sigstore bundle signature
# using the old (currently installed) cosign binary, then updates
# COSIGN_SHA256 in update-tirith-checksums.sh.
set -euo pipefail

SCRIPT="scripts/renovate/update-tirith-checksums.sh"

# --- Read old and new versions ---
OLD_VERSION=$(git show HEAD:"${SCRIPT}" | grep -oP '^COSIGN_VERSION=\K\S+' || true)
if [[ -z "${OLD_VERSION}" ]]; then
  echo "error: could not extract COSIGN_VERSION from committed ${SCRIPT}" >&2
  exit 1
fi
if [[ ! "${OLD_VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "error: OLD_VERSION is not a valid semver: ${OLD_VERSION}" >&2
  exit 1
fi

OLD_SHA256=$(git show HEAD:"${SCRIPT}" | grep -oP '^COSIGN_SHA256=\K\S+' || true)
if [[ -z "${OLD_SHA256}" ]]; then
  echo "error: could not extract COSIGN_SHA256 from committed ${SCRIPT}" >&2
  exit 1
fi
if [[ ! "${OLD_SHA256}" =~ ^[0-9a-f]{64}$ ]]; then
  echo "error: OLD_SHA256 is not a valid sha256 hex digest: ${OLD_SHA256}" >&2
  exit 1
fi

NEW_VERSION=$(grep -oP '^COSIGN_VERSION=\K\S+' "${SCRIPT}" || true)
if [[ -z "${NEW_VERSION}" ]]; then
  echo "error: could not extract COSIGN_VERSION from working-tree ${SCRIPT}" >&2
  exit 1
fi
if [[ ! "${NEW_VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "error: NEW_VERSION is not a valid semver: ${NEW_VERSION}" >&2
  exit 1
fi

if [[ "${OLD_VERSION}" == "${NEW_VERSION}" ]]; then
  echo "cosign version unchanged (${OLD_VERSION}), nothing to do"
  exit 0
fi

echo "cosign: ${OLD_VERSION} -> ${NEW_VERSION}"

WORKDIR="$(mktemp -d)"
trap 'rm -rf "${WORKDIR}"' EXIT

BASE_URL="https://github.com/sigstore/cosign/releases/download"

# --- Bootstrap the old cosign binary to use as verifier ---
OLD_BINARY="${WORKDIR}/cosign-old"
curl -fsSL "${BASE_URL}/v${OLD_VERSION}/cosign-linux-amd64" -o "${OLD_BINARY}"
echo "${OLD_SHA256}  ${OLD_BINARY}" | sha256sum -c -
chmod +x "${OLD_BINARY}"

# --- Download new cosign binary and its sigstore bundle ---
NEW_BINARY="${WORKDIR}/cosign-linux-amd64"
curl -fsSL "${BASE_URL}/v${NEW_VERSION}/cosign-linux-amd64" -o "${NEW_BINARY}"
curl -fsSL "${BASE_URL}/v${NEW_VERSION}/cosign-linux-amd64.sigstore.json" -o "${NEW_BINARY}.sigstore.json"

# --- Verify the new binary's signature using the old cosign ---
"${OLD_BINARY}" verify-blob \
  --bundle "${NEW_BINARY}.sigstore.json" \
  --certificate-identity "keyless@projectsigstore.iam.gserviceaccount.com" \
  --certificate-oidc-issuer "https://accounts.google.com" \
  "${NEW_BINARY}"

echo "cosign signature verified for v${NEW_VERSION}"

# --- Compute and update the SHA256 ---
NEW_SHA256=$(sha256sum "${NEW_BINARY}" | awk '{print $1}')

if [[ ! "${NEW_SHA256}" =~ ^[0-9a-f]{64}$ ]]; then
  echo "error: computed checksum is not a valid sha256 hex digest: ${NEW_SHA256}" >&2
  exit 1
fi

sed -i "s/^COSIGN_SHA256=.*/COSIGN_SHA256=${NEW_SHA256}/" "${SCRIPT}"

echo "updated COSIGN_SHA256 to ${NEW_SHA256}"
