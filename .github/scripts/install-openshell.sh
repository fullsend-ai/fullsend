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
curl -LsSf "https://raw.githubusercontent.com/NVIDIA/OpenShell/${OPENSHELL_SHA}/install.sh" \
  | OPENSHELL_VERSION="v${OPENSHELL_VERSION}" sh

openshell --version
