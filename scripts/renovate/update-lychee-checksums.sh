#!/usr/bin/env bash
# Called by Renovate postUpgradeTasks after a lycheeverse/lychee bump.
# Downloads the gnu release tarballs, checks them against the release's
# .tar.gz.sha256 assets, and updates LYCHEE_SHA256_{AMD64,ARM64} in the code
# Containerfile.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib-checksums.sh"

asset_url() {
  local triple
  case "$1" in
    AMD64) triple="x86_64-unknown-linux-gnu" ;;
    ARM64) triple="aarch64-unknown-linux-gnu" ;;
    *) return 1 ;;
  esac
  echo "https://github.com/lycheeverse/lychee/releases/download/lychee-v${VERSION}/lychee-${triple}.tar.gz"
}
published_sha() { checksum_file_sha "$2.sha256" "${2##*/}"; }

update_pin LYCHEE multi images/code/Containerfile
