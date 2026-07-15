#!/usr/bin/env bash
# Single source of truth for the pinned OpenShell version.
#
# Source this script to set OPENSHELL_VERSION and OPENSHELL_SHA in the
# current shell. In GitHub Actions it also exports them to GITHUB_ENV
# for downstream steps.
#
# Usage:
#   source .github/scripts/openshell-version.sh

# renovate: datasource=github-tags depName=NVIDIA/OpenShell
OPENSHELL_VERSION=0.0.83
OPENSHELL_SHA=e3d26dd3ae0dee247bbc5db368545832757ac493

export OPENSHELL_VERSION OPENSHELL_SHA

if [[ -n "${GITHUB_ENV:-}" ]]; then
  echo "OPENSHELL_VERSION=${OPENSHELL_VERSION}" >> "${GITHUB_ENV}"
  echo "OPENSHELL_SHA=${OPENSHELL_SHA}" >> "${GITHUB_ENV}"
fi
