#!/usr/bin/env bash
# Called by Renovate postUpgradeTasks after a sigstore/gitsign bump.
# Downloads the linux release binaries, checks them against the release's
# checksums.txt, and updates GITSIGN_SHA256_{AMD64,ARM64} in the code
# Containerfile.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib-checksums.sh"

BASE="https://github.com/sigstore/gitsign/releases/download"

asset_url() {
  local arch
  case "$1" in
    AMD64) arch="amd64" ;;
    ARM64) arch="arm64" ;;
    *) return 1 ;;
  esac
  echo "${BASE}/v${VERSION}/gitsign_${VERSION}_linux_${arch}"
}
published_sha() { checksum_file_sha "${BASE}/v${VERSION}/checksums.txt" "${2##*/}"; }

update_pin GITSIGN multi images/code/Containerfile
