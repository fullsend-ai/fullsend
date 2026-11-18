#!/usr/bin/env bash
# Called by Renovate postUpgradeTasks after a cli/cli (gh) bump. Downloads
# the linux release tarballs, checks them against gh_<version>_checksums.txt,
# and updates GH_SHA256_{AMD64,ARM64} in the runner Containerfile.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib-checksums.sh"

BASE="https://github.com/cli/cli/releases/download"

asset_url() {
  local arch
  case "$1" in
    AMD64) arch="amd64" ;;
    ARM64) arch="arm64" ;;
    *) return 1 ;;
  esac
  echo "${BASE}/v${VERSION}/gh_${VERSION}_linux_${arch}.tar.gz"
}
published_sha() { checksum_file_sha "${BASE}/v${VERSION}/gh_${VERSION}_checksums.txt" "${2##*/}"; }

update_pin GH multi images/runner/Containerfile
