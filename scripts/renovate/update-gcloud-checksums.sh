#!/usr/bin/env bash
# Called by Renovate postUpgradeTasks after a gcloud CLI bump. Downloads the
# versioned linux release tarballs from dl.google.com and updates
# GCLOUD_SHA256_{AMD64,ARM64} in the runner Containerfile.
#
# Google lists checksums only for the current release, on the HTML install
# page (https://cloud.google.com/sdk/docs/install), not in a file per
# versioned archive, so the hash is computed from the HTTPS download.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib-checksums.sh"

# shellcheck disable=SC2034 # read by update_pin in lib-checksums.sh
ALLOW_UNPUBLISHED=1

asset_url() {
  local arch
  case "$1" in
    AMD64) arch="x86_64" ;;
    ARM64) arch="arm" ;;
    *) return 1 ;;
  esac
  echo "https://dl.google.com/dl/cloudsdk/channels/rapid/downloads/google-cloud-cli-${VERSION}-linux-${arch}.tar.gz"
}
published_sha() { :; }

update_pin GCLOUD multi images/runner/Containerfile
