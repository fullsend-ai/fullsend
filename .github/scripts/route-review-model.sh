#!/usr/bin/env bash
# route-review-model.sh — Route a trivial diff to a cheaper review model.
#
# Author type is a proxy for complexity, not complexity itself: a one-line
# human edit to versions.json costs the same as a refactor when the model is
# fixed by the harness. This classifies the diff and, when it is trivial,
# exports FULLSEND_MODEL so the review runs on a cheaper tier. See #5777.
#
# Inputs (env vars):
#   GH_TOKEN           — GitHub token for API calls
#   SOURCE_REPO        — Repository in owner/repo format
#   PR_NUMBER          — Pull request number
#   TRIVIAL_MODEL      — Model to route trivial diffs to. "off"/"none"/empty
#                        disables routing entirely. Default: sonnet
#   TRIVIAL_MAX_LINES  — Upper bound on additions+deletions. Default: 10
#   GITHUB_ENV         — Set by Actions; the export target
#
# Always exits 0. Cost routing must never be the reason a review fails to
# run: every uncertain path leaves the model untouched, which means the
# harness default (the more capable model) stands.
#
# Precedence: this runs *before* setup-agent-env.sh, which re-exports
# FULLSEND_MODEL from the REVIEW_FULLSEND_MODEL / FULLSEND_MODEL repository
# variables. A repo that pins a model therefore keeps it — explicit
# configuration outranks inferred routing.

set -euo pipefail

TRIVIAL_MODEL="${TRIVIAL_MODEL:-sonnet}"
TRIVIAL_MAX_LINES="${TRIVIAL_MAX_LINES:-10}"

case "${TRIVIAL_MODEL}" in
  "" | off | none | false)
    echo "Complexity-based model routing disabled (TRIVIAL_MODEL=${TRIVIAL_MODEL:-unset})"
    exit 0
    ;;
esac

if [[ ! "${TRIVIAL_MAX_LINES}" =~ ^[0-9]+$ ]]; then
  echo "::warning::REVIEW_TRIVIAL_MAX_LINES is not a number (${TRIVIAL_MAX_LINES}) — leaving the model unchanged"
  exit 0
fi

if [[ -z "${PR_NUMBER:-}" || "${PR_NUMBER}" == "null" ]]; then
  exit 0
fi

# jq classifier. Kept as one program so the whole verdict is computed from a
# single pass over the file list, and so the test can exercise exactly what
# ships rather than a paraphrase of it.
#
# A diff is trivial only when every changed file is:
#   - status "modified" — an added, removed or renamed file is structural
#     even when it is small, and a renamed config file is not a value edit;
#   - a data/config extension — code is never trivial regardless of size;
#   - not a dependency lockfile — npm resolves from the lockfile, so a
#     one-line integrity swap is a supply-chain change wearing a trivial
#     diff, and is the last thing to review on a cheaper model;
#   - not a path that executes or governs — anything under a dot directory
#     (.github/, .claude/), scripts/, hack/, or the agent-behaviour and
#     policy trees, plus root files like CODEOWNERS and Dockerfile. This is
#     deliberately a subset of REVIEW_PROTECTED_PATHS: that list decides
#     whether a review may auto-approve, this one decides whether a cheaper
#     model may form the opinion.
read -r -d '' JQ_CLASSIFIER <<'JQEOF' || true
def is_data:    test("\\.(json|ya?ml|toml|txt|md|ini|cfg|conf|properties)$"; "i");
def is_lock:    test("(^|/)(package-lock\\.json|yarn\\.lock|pnpm-lock\\.yaml|npm-shrinkwrap\\.json|go\\.sum|Cargo\\.lock|Gemfile\\.lock|poetry\\.lock|composer\\.lock)$"; "i");
def is_guarded: test("(^|/)\\.[^/]+/") or test("^(scripts|hack|agents|skills|harness|images|plugins|policies|profiles|providers|api-servers)/")
                or test("^(CODEOWNERS|AGENTS\\.md|CLAUDE\\.md|Dockerfile|Containerfile)$");
{
  files: length,
  total: (map(.changes // 0) | add // 0),
  blockers: [
    .[]
    | select(
        (.status != "modified")
        or ((.filename | is_data) | not)
        or (.filename | is_lock)
        or (.filename | is_guarded)
      )
    | .filename
  ],
}
JQEOF

if ! FILES_JSON=$(gh api "repos/${SOURCE_REPO}/pulls/${PR_NUMBER}/files" \
  --paginate -F per_page=100 --jq '{filename, status, changes}' 2>/dev/null); then
  echo "::warning::Could not read changed files for PR #${PR_NUMBER} — leaving the model unchanged"
  exit 0
fi

if [[ -z "${FILES_JSON}" ]]; then
  exit 0
fi

if ! VERDICT=$(printf '%s\n' "${FILES_JSON}" | jq -s "${JQ_CLASSIFIER}" 2>/dev/null); then
  echo "::warning::Could not classify the diff for PR #${PR_NUMBER} — leaving the model unchanged"
  exit 0
fi

FILE_COUNT=$(jq -r '.files' <<<"${VERDICT}")
TOTAL_LINES=$(jq -r '.total' <<<"${VERDICT}")
BLOCKERS=$(jq -r '.blockers | join(", ")' <<<"${VERDICT}")

# GitHub caps this endpoint at 3000 files and stops paginating without
# erroring, so a larger PR would look small from a truncated head.
if ((FILE_COUNT >= 3000)); then
  echo "PR #${PR_NUMBER} file list may be truncated — leaving the model unchanged"
  exit 0
fi

if [[ -n "${BLOCKERS}" ]]; then
  echo "Not trivial: ${BLOCKERS}"
  exit 0
fi

if ((TOTAL_LINES >= TRIVIAL_MAX_LINES)); then
  echo "Not trivial: ${TOTAL_LINES} lines changed (threshold ${TRIVIAL_MAX_LINES})"
  exit 0
fi

echo "FULLSEND_MODEL=${TRIVIAL_MODEL}" >>"${GITHUB_ENV}"
SUMMARY="Review model routed to \`${TRIVIAL_MODEL}\`: ${TOTAL_LINES} line(s) changed across ${FILE_COUNT} data file(s), all value edits."
echo "${SUMMARY}"
if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  echo "${SUMMARY}" >>"${GITHUB_STEP_SUMMARY}"
fi
