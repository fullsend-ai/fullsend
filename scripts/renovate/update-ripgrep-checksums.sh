#!/usr/bin/env bash
# Called by Renovate postUpgradeTasks after a BurntSushi/ripgrep bump.
# Downloads the musl release tarballs, checks them against the release's
# .tar.gz.sha256 assets, and updates RIPGREP_SHA256_{AMD64,ARM64} in the
# sandbox Containerfile.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib-checksums.sh"

asset_url() {
  local triple
  case "$1" in
    AMD64) triple="x86_64-unknown-linux-musl" ;;
    ARM64) triple="aarch64-unknown-linux-musl" ;;
    *) return 1 ;;
  esac
  echo "https://github.com/BurntSushi/ripgrep/releases/download/${VERSION}/ripgrep-${VERSION}-${triple}.tar.gz"
}
published_sha() { checksum_file_sha "$2.sha256" "${2##*/}"; }

update_pin RIPGREP multi images/sandbox/Containerfile
