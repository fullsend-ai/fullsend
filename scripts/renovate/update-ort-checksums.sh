#!/usr/bin/env bash
# Called by Renovate postUpgradeTasks after a Microsoft.ML.OnnxRuntime (NuGet)
# bump. Downloads the NuGet package the runner Containerfile installs from,
# checks it against the SHA512 NuGet records for that version, and updates
# ORT_SHA256 in the runner Containerfile. One package carries both linux
# architectures, so the pin has a single checksum.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib-checksums.sh"

asset_url() {
  echo "https://api.nuget.org/v3-flatcontainer/microsoft.ml.onnxruntime/${VERSION}/microsoft.ml.onnxruntime.${VERSION}.nupkg"
}
published_sha() { nuget_package_sha256 microsoft.ml.onnxruntime "${VERSION}" "$2"; }

update_pin ORT single images/runner/Containerfile
