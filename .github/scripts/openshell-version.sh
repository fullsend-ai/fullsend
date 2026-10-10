#!/usr/bin/env bash
# Single source of truth for the pinned OpenShell version.
#
# Source this script to set OPENSHELL_VERSION and OPENSHELL_SHA in the
# current shell. In GitHub Actions it also exports them to GITHUB_ENV
# for downstream steps.
#
# OPENSHELL_VERSION is tracked by Renovate via a customManager in
# renovate.json (not this file's magic comments — a bare "# renovate:"
# comment here would be inert since no built-in manager scans .sh files).
# OPENSHELL_SHA is refreshed automatically after each version bump by
# scripts/renovate/update-openshell-sha.sh (see renovate.json postUpgradeTasks).
#
# Usage:
#   source .github/scripts/openshell-version.sh

OPENSHELL_VERSION=0.1.3
OPENSHELL_SHA=e1f3c82caa3ed3b65de22889ae7ef32a774878ef

export OPENSHELL_VERSION OPENSHELL_SHA

if [[ -n "${GITHUB_ENV:-}" ]]; then
  echo "OPENSHELL_VERSION=${OPENSHELL_VERSION}" >> "${GITHUB_ENV}"
  echo "OPENSHELL_SHA=${OPENSHELL_SHA}" >> "${GITHUB_ENV}"
fi
