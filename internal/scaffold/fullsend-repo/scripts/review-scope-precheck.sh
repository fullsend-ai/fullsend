#!/usr/bin/env bash
# review-scope-precheck.sh — Skip the review agent on docs-only / test-only PRs.
#
# Runs on the host as a harness pre_script. Uses the pre-script output
# protocol (docs/normative/prescript-output/v1): exit 78 = neutral skip,
# with the last stdout line as the skip reason.
#
# File list source (first that applies):
#   BASE + HEAD (env)          — `git diff --name-only BASE HEAD` in
#                                ${REVIEW_TARGET_REPO_DIR:-.}
#   PR_NUMBER + REPO_FULL_NAME — `gh pr view --json files` (needs GH_TOKEN)
# If neither is available the check is inconclusive and the run proceeds.
#
# Optional:
#   REVIEW_SCOPE_SKIP_PATTERNS — comma-separated ERE list overriding the
#                                default docs/test patterns.
set -euo pipefail

DEFAULT_PATTERNS='\.md$,^docs/,_test\.go$,^tests?/,\.txt$'
IFS=',' read -r -a PATTERNS <<< "${REVIEW_SCOPE_SKIP_PATTERNS:-${DEFAULT_PATTERNS}}"

files=""
if [[ -n "${BASE:-}" && -n "${HEAD:-}" ]]; then
  files="$(git -C "${REVIEW_TARGET_REPO_DIR:-.}" diff --name-only "${BASE}" "${HEAD}")"
elif [[ -n "${PR_NUMBER:-}" && -n "${REPO_FULL_NAME:-}" ]] && command -v gh >/dev/null 2>&1; then
  files="$(gh pr view "${PR_NUMBER}" --repo "${REPO_FULL_NAME}" --json files --jq '.files[].path' 2>/dev/null || true)"
fi

if [[ -z "${files}" ]]; then
  echo "review-scope-precheck: no changed files determined — proceeding with review"
  exit 0
fi

# ponytail: linear scan over files x patterns; PRs are small.
while IFS= read -r f; do
  [[ -z "${f}" ]] && continue
  matched=0
  for p in "${PATTERNS[@]}"; do
    if [[ "${f}" =~ ${p} ]]; then matched=1; break; fi
  done
  if [[ "${matched}" -eq 0 ]]; then
    echo "review-scope-precheck: ${f} is in scope — proceeding with review"
    exit 0
  fi
done <<< "${files}"

echo "review-scope-precheck: all changed files are docs/test-only — skipping review" >&2
echo "docs/test-only change, review skipped"
exit 78
