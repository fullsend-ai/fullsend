#!/usr/bin/env bash
# pre-triage.sh — Router that dispatches to the source-specific pre-triage script.
#
# Requires TRIAGE_SOURCE env var (e.g., "github", "jira").
# Delegates to scripts/pre-triage-${TRIAGE_SOURCE}.sh.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SOURCE="${TRIAGE_SOURCE:?TRIAGE_SOURCE must be set (github, jira, ...)}"

IMPL="${SCRIPT_DIR}/pre-triage-${SOURCE}.sh"
if [[ ! -f "${IMPL}" ]]; then
  echo "ERROR: unsupported TRIAGE_SOURCE '${SOURCE}' — no ${IMPL} found"
  exit 1
fi

exec bash "${IMPL}"
