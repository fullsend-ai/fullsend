#!/usr/bin/env bash
# Called by Renovate postUpgradeTasks after a microsoft/onnxruntime bump.
# ONNX Runtime ships no checksum file, so the linux CPU release tarballs are
# checked against the sha256 digest GitHub records for each release asset.
# Updates ORT_SHA256_{AMD64,ARM64} in the runner Containerfile.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib-checksums.sh"

asset_url() {
  local arch
  case "$1" in
    AMD64) arch="x64" ;;
    ARM64) arch="aarch64" ;;
    *) return 1 ;;
  esac
  echo "https://github.com/microsoft/onnxruntime/releases/download/v${VERSION}/onnxruntime-linux-${arch}-${VERSION}.tgz"
}
published_sha() { github_asset_digest microsoft/onnxruntime "v${VERSION}" "${2##*/}"; }

update_pin ORT multi images/runner/Containerfile
