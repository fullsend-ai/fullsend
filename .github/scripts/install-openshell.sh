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
# shellcheck source=retry-with-backoff.sh
source "${SCRIPT_DIR}/retry-with-backoff.sh"

echo "Installing OpenShell ${OPENSHELL_VERSION} (${OPENSHELL_SHA})"

# A persistent runner may still carry OpenShell 0.0.x, whose gateway state
# 0.1 cannot read. Stop that gateway and move its state aside first; the
# installer then needs the explicit ack to replace a pre-0.1 install.
installed="$(openshell --version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -n1 || true)"
case "${installed}" in
  0.0.*)
    echo "Replacing OpenShell ${installed}: stopping its gateway and moving its state aside"
    systemctl --user stop openshell-gateway 2>/dev/null || true
    state_root="${XDG_STATE_HOME:-${HOME}/.local/state}/openshell"
    stamp="$(date +%s)"
    for d in gateway tls; do
      if [[ -d "${state_root}/${d}" ]]; then
        mv "${state_root}/${d}" "${state_root}/${d}.pre-0.1.${stamp}"
      fi
    done
    ;;
esac
export OPENSHELL_ACK_BREAKING_UPGRADE=1

# Retry the entire install pipeline (curl | sh) with exponential backoff.
# The upstream install.sh internally downloads the .deb from GitHub Releases,
# which can fail on transient CDN errors. Retrying only the outer curl would
# not cover that inner download, so we retry the full pipeline. The
# installer verifies the package checksum itself on every attempt.
# shellcheck disable=SC2034 # RETRY_FAILURE_CATEGORY is read by retry_with_backoff
install_openshell_attempt() {
  local -a status
  curl -LsSf --retry 3 --retry-delay 5 \
    "https://raw.githubusercontent.com/NVIDIA/OpenShell/${OPENSHELL_SHA}/install.sh" \
    | OPENSHELL_VERSION="v${OPENSHELL_VERSION}" sh
  status=("${PIPESTATUS[@]}")
  if (( status[0] == 0 && status[1] == 0 )); then
    return 0
  fi
  if (( status[0] != 0 )); then
    RETRY_FAILURE_CATEGORY="install.sh download (curl exit ${status[0]})"
  else
    RETRY_FAILURE_CATEGORY="installer (package download, checksum or install; exit ${status[1]})"
    # The installer starts the gateway service; show why it did not come up.
    journalctl --user -u openshell-gateway --no-pager -n 40 2>/dev/null || true
  fi
  return 1
}

if ! retry_with_backoff "OpenShell v${OPENSHELL_VERSION} install" install_openshell_attempt; then
  echo "::error::OpenShell setup failed at stage 'install' (version v${OPENSHELL_VERSION}, install.sh at ${OPENSHELL_SHA})" >&2
  exit 1
fi

openshell --version
