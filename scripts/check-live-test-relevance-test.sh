#!/usr/bin/env bash
# Regression coverage for the PR/merge-group filters in both live suites.
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
mapfile -t filters < <(sed -n "s/.*grep -qE '\(.*\)'; then/\1/p" \
  "$repo_root/.github/workflows/e2e.yml")

if [[ ${#filters[@]} -ne 2 || ${filters[0]} != "${filters[1]}" ]]; then
  echo 'Expected identical behaviour and playback relevance filters' >&2
  exit 1
fi

for filter in "${filters[@]}"; do
  # Each case is a single-file PR or merge-group change list.
  for path in \
    internal/fetch/fetch.go \
    internal/security/redact.go \
    internal/another-shared-package/helper.go \
    internal/fetch/fetch_test.go \
    internal/sentencetoken/english.json \
    go.mod \
    go.sum \
    action.yml \
    .github/scripts/install-podman.sh \
    .github/scripts/install-openshell.sh \
    .github/scripts/openshell-version.sh \
    e2e/behaviour/features/playback.feature \
    internal/security/hooks/check.sh \
    internal/scaffold/fullsend-repo/template.yaml \
    internal/dispatch/gcf/mintsrc/go.mod \
    .github/workflows/e2e.yml \
    .github/workflows/e2e-ok-to-test.yml \
    .github/actions/check-e2e-authorization/action.yml \
    scripts/check-e2e-authorization.sh \
    Makefile; do
    if ! printf '%s\n' "$path" | grep -qE "$filter"; then
      printf 'Expected live-test relevance for %s\n' "$path" >&2
      exit 1
    fi
  done

  for path in docs/README.md action.yml.bak internal/fetch/fetch.go.md \
    internal/sentencetoken/english.json.bak; do
    if printf '%s\n' "$path" | grep -qE "$filter"; then
      printf 'Unexpected live-test relevance for %s\n' "$path" >&2
      exit 1
    fi
  done
done

echo 'Live-test relevance regression checks passed'
