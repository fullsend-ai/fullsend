#!/usr/bin/env bash
# warn-install-only-unpinned-version.sh — Warn when __install_only__
# resolves the CLI to latest.
#
# SHA-pinning the composite action does not pin the CLI binary: the
# `version` input defaults to latest independently of github.action_ref.
# That mismatch has broken callers when a newer CLI required a newer
# openshell (or other action-bundled dependency) than the pinned action
# shipped. See https://github.com/fullsend-ai/fullsend/issues/6850.
#
# Inputs (env vars):
#   AGENT      — action `agent` input
#   VERSION    — normalized `version` input (empty treated as latest)
#   ACTION_REF — github.action_ref (empty for local composite actions)
#
# Always exits 0. Emits ::warning:: when agent is __install_only__ and
# version is omitted or latest.

set -euo pipefail

_sanitize_for_annotation() {
  local val="$1"
  val="${val//::/__}"
  val="${val//$'\n'/}"
  val="${val//$'\r'/}"
  val="${val//%25/}"
  val="${val//%0A/}"
  val="${val//%0a/}"
  val="${val//%0D/}"
  val="${val//%0d/}"
  printf '%s' "${val}"
}

_is_pin_ref() {
  local ref="$1"
  # Semver tags (optional pre-release / build suffix) or a full commit SHA.
  # Moving refs such as main, latest, or v0 are not pins.
  if [[ "${ref}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]; then
    return 0
  fi
  if [[ "${ref}" =~ ^[0-9a-f]{40}$ ]]; then
    return 0
  fi
  return 1
}

agent="$(printf '%s' "${AGENT:-}" | tr -d '[:space:]')"
version="$(printf '%s' "${VERSION:-}" | tr -d '[:space:]')"
version="${version:-latest}"
action_ref="$(printf '%s' "${ACTION_REF:-}" | tr -d '[:space:]')"

if [[ "${agent}" != "__install_only__" ]]; then
  exit 0
fi

if [[ "${version}" != "latest" ]]; then
  exit 0
fi

safe_ref="$(_sanitize_for_annotation "${action_ref}")"

if [[ -n "${safe_ref}" ]] && _is_pin_ref "${action_ref}"; then
  echo "::warning::agent '__install_only__' was invoked without a pinned version input, so the CLI will resolve to latest. SHA-pinning this action does not pin the CLI (action ref: '${safe_ref}'). Set 'version: ${safe_ref}' to keep the CLI aligned with the action."
elif [[ -n "${safe_ref}" ]]; then
  echo "::warning::agent '__install_only__' was invoked without a pinned version input, so the CLI will resolve to latest. SHA-pinning this action does not pin the CLI (action ref: '${safe_ref}'). Set the 'version' input to a release tag or commit SHA so the CLI matches the action."
else
  echo "::warning::agent '__install_only__' was invoked without a pinned version input, so the CLI will resolve to latest. SHA-pinning this action does not pin the CLI. Set the 'version' input to a release tag or commit SHA so the CLI matches the action."
fi
