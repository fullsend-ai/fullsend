#!/usr/bin/env bash
# verify-release-tag.sh — Verify that a release tag still resolves to the
# commit this workflow run was started for.
#
# The release pipeline gates publication on agents functional tests that
# clone fullsend by *tag name* (the called workflow only accepts `main` or
# a `v*` ref). A tag move between the gate and the publish step would mean
# the binary is built from one commit while the gate validated another, so
# this check runs on both sides of the gate: once before validation starts
# and once immediately before GoReleaser publishes.
#
# Exits 0 if the tag resolves to EXPECTED_SHA.
# Exits 1 if it resolves elsewhere, or if the ref cannot be resolved to a
# commit.
#
# Inputs (env vars):
#   TAG           — tag name to verify (default: $GITHUB_REF_NAME)
#   EXPECTED_SHA  — commit the tag must point at (default: $GITHUB_SHA)
#   REPO          — owner/name to query (default: $GITHUB_REPOSITORY)
#
# Requires: gh CLI authenticated with read access to REPO.

set -euo pipefail

TAG="${TAG:-${GITHUB_REF_NAME:-}}"
EXPECTED_SHA="${EXPECTED_SHA:-${GITHUB_SHA:-}}"
REPO="${REPO:-${GITHUB_REPOSITORY:-}}"

# Peeling an annotated tag can take more than one hop (a tag object may
# point at another tag object). Bound the walk so a cycle cannot hang the
# job.
MAX_PEEL_DEPTH=5

if [[ -z "${TAG}" || -z "${EXPECTED_SHA}" || -z "${REPO}" ]]; then
  echo "::error::verify-release-tag.sh requires TAG, EXPECTED_SHA and REPO"
  exit 1
fi

if [[ ! "${EXPECTED_SHA}" =~ ^[a-f0-9]{40}$ ]]; then
  echo "::error::EXPECTED_SHA is not a commit SHA: ${EXPECTED_SHA//::/}"
  exit 1
fi

REF_JSON=$(gh api "repos/${REPO}/git/ref/tags/${TAG}") || {
  echo "::error::Could not read tag ${TAG//::/} from ${REPO//::/}"
  exit 1
}

OBJ_TYPE=$(jq -r '.object.type' <<<"${REF_JSON}")
OBJ_SHA=$(jq -r '.object.sha' <<<"${REF_JSON}")

# Dereference annotated tag objects down to the commit they name. Each SHA
# is shape-checked before it is fed back into the API path, so a malformed
# response cannot become part of a request.
depth=0
while :; do
  if [[ ! "${OBJ_SHA}" =~ ^[a-f0-9]{40}$ ]]; then
    echo "::error::Tag ${TAG//::/} resolved to an unexpected value: ${OBJ_SHA//::/}"
    exit 1
  fi
  [[ "${OBJ_TYPE}" == "tag" ]] || break
  if (( depth >= MAX_PEEL_DEPTH )); then
    echo "::error::Tag ${TAG//::/} is nested more than ${MAX_PEEL_DEPTH} levels deep"
    exit 1
  fi
  TAG_JSON=$(gh api "repos/${REPO}/git/tags/${OBJ_SHA}") || {
    echo "::error::Could not dereference tag object ${OBJ_SHA//::/} in ${REPO//::/}"
    exit 1
  }
  OBJ_TYPE=$(jq -r '.object.type' <<<"${TAG_JSON}")
  OBJ_SHA=$(jq -r '.object.sha' <<<"${TAG_JSON}")
  depth=$(( depth + 1 ))
done

if [[ "${OBJ_TYPE}" != "commit" ]]; then
  echo "::error::Tag ${TAG//::/} does not name a commit (got '${OBJ_TYPE//::/}')"
  exit 1
fi

if [[ "${OBJ_SHA}" != "${EXPECTED_SHA}" ]]; then
  echo "::error::Release tag ${TAG//::/} resolves to ${OBJ_SHA//::/}, expected ${EXPECTED_SHA//::/}"
  exit 1
fi

echo "::notice::Release tag ${TAG//::/} resolves to ${EXPECTED_SHA}"
