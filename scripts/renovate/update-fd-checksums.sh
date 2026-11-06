#!/usr/bin/env bash
# Called by Renovate postUpgradeTasks after a sharkdp/fd bump. fd ships no
# checksum file, so the musl release tarballs are checked against the sha256
# digest GitHub records for each release asset. Updates
# FD_SHA256_{AMD64,ARM64} in the sandbox Containerfile.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib-checksums.sh"

asset_url() {
  local triple
  case "$1" in
    AMD64) triple="x86_64-unknown-linux-musl" ;;
    ARM64) triple="aarch64-unknown-linux-musl" ;;
    *) return 1 ;;
  esac
  echo "https://github.com/sharkdp/fd/releases/download/v${VERSION}/fd-v${VERSION}-${triple}.tar.gz"
}
published_sha() { github_asset_digest sharkdp/fd "v${VERSION}" "${2##*/}"; }

update_pin FD multi images/sandbox/Containerfile
