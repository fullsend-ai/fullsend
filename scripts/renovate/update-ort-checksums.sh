#!/usr/bin/env bash
# Called by Renovate postUpgradeTasks after a Microsoft.ML.OnnxRuntime bump.
# The runner image takes libonnxruntime.so from Microsoft's official NuGet
# package (github.com/microsoft/onnxruntime answers 404, #9759). One .nupkg
# carries both architectures, so this updates the single ORT_SHA256 in the
# runner Containerfile.
#
# NuGet publishes no sha256 for a package (its catalog records only a
# base64 SHA512 packageHash), so the hash is computed from the HTTPS
# download from api.nuget.org.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib-checksums.sh"

# shellcheck disable=SC2034 # read by update_pin in lib-checksums.sh
ALLOW_UNPUBLISHED=1

asset_url() {
  echo "https://api.nuget.org/v3-flatcontainer/microsoft.ml.onnxruntime/${VERSION}/microsoft.ml.onnxruntime.${VERSION}.nupkg"
}
published_sha() { :; }

update_pin ORT single images/runner/Containerfile
