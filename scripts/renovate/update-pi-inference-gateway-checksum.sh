#!/usr/bin/env bash
# Called by Renovate postUpgradeTasks after a fullsend-ai/pi-inference-gateway
# tag bump. Downloads the tag tarball, checks its sha256 against the one the
# release notes publish, and updates PI_INFERENCE_GATEWAY_SHA256 in the
# sandbox Containerfile.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib-checksums.sh"

REPO="fullsend-ai/pi-inference-gateway"

asset_url() { echo "https://github.com/${REPO}/archive/refs/tags/v${VERSION}.tar.gz"; }
published_sha() { release_notes_sha "${REPO}" "v${VERSION}"; }

update_pin PI_INFERENCE_GATEWAY single images/sandbox/Containerfile
