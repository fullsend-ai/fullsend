#!/usr/bin/env bash
# Install a fullsend release binary locally after verifying its checksum.
#
# Usage: install-binary.sh <tag> [install-dir]
#
# Arguments:
#   tag          Release tag (e.g. v0.5.0)
#   install-dir  Where to place the binary (default: ~/.local/bin)
#
# The binary is installed as fullsend-<tag> so multiple versions can coexist.
set -euo pipefail

TAG="${1:?Usage: install-binary.sh <tag> [install-dir]}"
if [[ ! "$TAG" =~ ^v[0-9] ]]; then
  echo "ERROR: Tag must start with 'v' followed by a digit (e.g. v0.5.0)" >&2
  exit 1
fi
INSTALL_DIR="${2:-$HOME/.local/bin}"

# macOS returns arm64 natively; Linux returns aarch64 which we map to arm64.
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
# GoReleaser's name_template uses .Version (no v prefix).
VERSION="${TAG#v}"
ARCHIVE="fullsend_${VERSION}_${OS}_${ARCH}.tar.gz"

WORKDIR=$(mktemp -d)
trap 'rm -rf "$WORKDIR"' EXIT
cd "$WORKDIR"

echo "Downloading ${TAG} for ${OS}/${ARCH}..."
gh release download "$TAG" \
  --repo fullsend-ai/fullsend \
  --pattern "${ARCHIVE}" \
  --pattern "checksums.txt" \
  --clobber

echo "Verifying checksum..."
if command -v sha256sum >/dev/null 2>&1; then
  grep -F "${ARCHIVE}" checksums.txt | sha256sum -c -
elif command -v shasum >/dev/null 2>&1; then
  grep -F "${ARCHIVE}" checksums.txt | shasum -a 256 -c -
else
  echo "ERROR: No sha256sum or shasum found" >&2
  exit 1
fi

echo "Extracting..."
tar xzf "${ARCHIVE}" fullsend

mkdir -p "${INSTALL_DIR}"
mv fullsend "${INSTALL_DIR}/fullsend-${TAG}"
chmod +x "${INSTALL_DIR}/fullsend-${TAG}"

echo "Installed: ${INSTALL_DIR}/fullsend-${TAG}"
"${INSTALL_DIR}/fullsend-${TAG}" --version
