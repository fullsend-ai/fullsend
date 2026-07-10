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
OPENSHELL_VERSION=0.0.81
OPENSHELL_SHA=420a855ddc21a20ac528f902bd2ed7f3fc133dc9

export OPENSHELL_VERSION OPENSHELL_SHA

if [[ -n "${GITHUB_ENV:-}" ]]; then
  echo "OPENSHELL_VERSION=${OPENSHELL_VERSION}" >> "${GITHUB_ENV}"
  echo "OPENSHELL_SHA=${OPENSHELL_SHA}" >> "${GITHUB_ENV}"
fi
