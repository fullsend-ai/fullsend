#!/usr/bin/env bash
set -euo pipefail

REPO="fullsend-ai/agents"
DEST="$(cd "$(dirname "$0")/../.." && pwd)/docs/agents"

if [[ "${1:-}" != "--force" ]] && [[ -d "$DEST" ]] && [[ -n "$(ls -A "$DEST" 2>/dev/null)" ]]; then
  echo "docs/agents/ already populated; skipping fetch (use --force to re-fetch)"
  exit 0
fi

REF="${FULLSEND_AGENTS_REF:-"main"}"

echo "Fetching agent docs from ${REPO} @ ${REF}"

WORKDIR=$(mktemp -d)
STAGING="${DEST}.tmp"
trap 'rm -rf "$WORKDIR" "$STAGING"' EXIT

git init -q "$WORKDIR"
git -C "$WORKDIR" fetch --depth 1 "https://github.com/${REPO}.git" "$REF"
git -C "$WORKDIR" checkout -q FETCH_HEAD

rm -rf "$STAGING"
cp -a "$WORKDIR/docs" "$STAGING"
rm -rf "$DEST"
mv "$STAGING" "$DEST"

echo "Agent docs populated from ${REF}"
