#!/usr/bin/env bash
# Called by Renovate postUpgradeTasks after an anomalyco/opencode bump.
# opencode ships no checksum file, so the glibc release tarballs are checked
# against the sha256 digest GitHub records for each release asset. Updates
# OPENCODE_SHA256_{AMD64,ARM64} in the sandbox Containerfile.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib-checksums.sh"

asset_url() {
  local arch
  case "$1" in
    AMD64) arch="x64" ;;
    ARM64) arch="arm64" ;;
    *) return 1 ;;
  esac
  echo "https://github.com/anomalyco/opencode/releases/download/v${VERSION}/opencode-linux-${arch}.tar.gz"
}
published_sha() { github_asset_digest anomalyco/opencode "v${VERSION}" "${2##*/}"; }

update_pin OPENCODE multi images/sandbox/Containerfile
