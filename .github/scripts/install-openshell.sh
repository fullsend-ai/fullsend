#!/usr/bin/env bash
# Install the pinned OpenShell version via upstream install.sh.
#
# Sources openshell-version.sh for the version and commit SHA, then
# runs the upstream installer. Requires sudo for RPM installation.
#
# Usage:
#   .github/scripts/install-openshell.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "${SCRIPT_DIR}/openshell-version.sh"

echo "Installing OpenShell ${OPENSHELL_VERSION} (${OPENSHELL_SHA})"

# Retry the entire install pipeline (curl | sh) with exponential backoff.
# The upstream install.sh internally downloads the .deb from GitHub Releases,
# which can fail on transient CDN errors. Retrying only the outer curl would
# not cover that inner download, so we retry the full pipeline.
max_attempts=3
attempt=1
delay=5
while true; do
  if curl -LsSf --retry 3 --retry-delay 5 \
       "https://raw.githubusercontent.com/NVIDIA/OpenShell/${OPENSHELL_SHA}/install.sh" \
     | OPENSHELL_VERSION="v${OPENSHELL_VERSION}" sh; then
    break
  fi
  if (( attempt >= max_attempts )); then
    echo "::error::OpenShell install failed after ${max_attempts} attempts" >&2
    exit 1
  fi
  echo "::warning::OpenShell install attempt ${attempt}/${max_attempts} failed, retrying in ${delay}s..." >&2
  sleep "${delay}"
  (( attempt++ ))
  (( delay *= 3 ))
done

openshell --version
