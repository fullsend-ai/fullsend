#!/usr/bin/env bash
# check-agents-gate-pin.sh — Verify the validate-agents workflow pin in
# release.yml is current with fullsend-ai/agents main.
#
# Exits 0 if the pin matches agents main HEAD.
# Exits 1 if the pin is behind, unreachable, or missing.
#
# Inputs (env vars):
#   RELEASE_YML — path to release.yml
#     (default: .github/workflows/release.yml)
#
# Requires: gh CLI authenticated with read access to fullsend-ai/agents.

set -euo pipefail

RELEASE_YML="${RELEASE_YML:-.github/workflows/release.yml}"

if [[ ! -f "${RELEASE_YML}" ]]; then
  echo "::error::Release workflow not found: ${RELEASE_YML//::/}"
  exit 1
fi

# Extract the pinned SHA from the uses: directive.
grep_rc=0
PINNED_SHAS=$(
  grep -oE \
    'fullsend-ai/agents/\.github/workflows/functional-tests\.yml@[a-f0-9]{40}' \
    "${RELEASE_YML}" \
  | sed 's/.*@//'
) || grep_rc=$?

# Exit code 1 = no match (handled by the empty-check below).
# Exit code ≥ 2 = file-read or internal grep error — surface it.
if [[ "${grep_rc}" -gt 1 ]]; then
  echo "::error::Failed to read ${RELEASE_YML//::/} (grep exit code ${grep_rc})"
  exit 1
fi

if [[ -z "${PINNED_SHAS}" ]]; then
  echo "::error::Could not find fullsend-ai/agents workflow pin in ${RELEASE_YML//::/}"
  exit 1
fi

# Reject ambiguous multi-pin configs.
SHA_COUNT=$(echo "${PINNED_SHAS}" | wc -l)
if [[ "${SHA_COUNT}" -gt 1 ]]; then
  echo "::error::Ambiguous: found ${SHA_COUNT} fullsend-ai/agents workflow pins in ${RELEASE_YML//::/}"
  exit 1
fi
PINNED_SHA="${PINNED_SHAS}"

# Fetch agents main HEAD SHA.
AGENTS_MAIN_SHA=$(
  gh api repos/fullsend-ai/agents/commits/main --jq '.sha'
) || {
  echo "::error::Failed to fetch fullsend-ai/agents main SHA"
  exit 1
}

if [[ ! "${AGENTS_MAIN_SHA}" =~ ^[a-f0-9]{40}$ ]]; then
  echo "::error::Unexpected SHA returned for fullsend-ai/agents main: ${AGENTS_MAIN_SHA//::/}"
  exit 1
fi

if [[ "${PINNED_SHA}" == "${AGENTS_MAIN_SHA}" ]]; then
  echo "::notice::validate-agents gate pin is current: ${PINNED_SHA}"
  exit 0
fi

# Count how far behind the pin is.
BEHIND_COUNT=$(
  gh api \
    "repos/fullsend-ai/agents/compare/${PINNED_SHA}...${AGENTS_MAIN_SHA}" \
    --jq '.ahead_by'
) || BEHIND_COUNT="unknown"

echo "::error::validate-agents gate pin ${PINNED_SHA//::/} does not match agents main ${AGENTS_MAIN_SHA//::/} (pin is ${BEHIND_COUNT//::/} commit(s) behind; 0/unknown means the pin is not an ancestor of main)"
exit 1
