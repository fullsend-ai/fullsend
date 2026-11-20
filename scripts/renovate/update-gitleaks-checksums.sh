#!/usr/bin/env bash
# Called by Renovate postUpgradeTasks after a gitleaks bump. Downloads the
# linux release tarballs, checks them against gitleaks_<version>_checksums.txt,
# and updates GITLEAKS_SHA256_{AMD64,ARM64} in the sandbox and runner
# Containerfiles, which must stay on the same version.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib-checksums.sh"

BASE="https://github.com/gitleaks/gitleaks/releases/download"

asset_url() {
  local arch
  case "$1" in
    AMD64) arch="x64" ;;
    ARM64) arch="arm64" ;;
    *) return 1 ;;
  esac
  echo "${BASE}/v${VERSION}/gitleaks_${VERSION}_linux_${arch}.tar.gz"
}
published_sha() { checksum_file_sha "${BASE}/v${VERSION}/gitleaks_${VERSION}_checksums.txt" "${2##*/}"; }

update_pin GITLEAKS multi images/sandbox/Containerfile images/runner/Containerfile
