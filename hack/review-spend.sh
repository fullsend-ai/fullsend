#!/usr/bin/env bash
# review-spend.sh — How much review-agent spend goes to docs/test-only PRs?
#
# For the last N merged PRs of a repo, counts review-agent runs (PR reviews
# authored by the review bot) and flags PRs whose changed files are all
# docs/test-only — the ones scripts/review-scope-precheck.sh would skip.
# Uses gh only.
#
# Usage: hack/review-spend.sh <owner/repo> [N=50]
# Env:   REVIEW_BOT (default fullsend-ai-review; gh JSON omits the [bot] suffix)
#        REVIEW_SCOPE_SKIP_PATTERNS (comma-separated ERE, same as the precheck)
set -euo pipefail

REPO="${1:?usage: $0 <owner/repo> [N]}"
N="${2:-50}"
BOT="${REVIEW_BOT:-fullsend-ai-review}"
DEFAULT_PATTERNS='\.md$,^docs/,_test\.go$,^tests?/,\.txt$'
IFS=',' read -r -a PATTERNS <<< "${REVIEW_SCOPE_SKIP_PATTERNS:-${DEFAULT_PATTERNS}}"

# docs_only <newline-separated files> → 0 if every file matches a pattern
docs_only() {
  local f p matched
  while IFS= read -r f; do
    [[ -z "${f}" ]] && continue
    matched=0
    for p in "${PATTERNS[@]}"; do [[ "${f}" =~ ${p} ]] && { matched=1; break; }; done
    [[ "${matched}" -eq 0 ]] && return 1
  done
  return 0
}

total_prs=0 total_runs=0 skippable_prs=0 skippable_runs=0
printf '%-7s %-6s %-9s %s\n' PR RUNS SCOPE TITLE
printf '%-7s %-6s %-9s %s\n' ------- ------ --------- -----

# ponytail: one gh call per PR (files + reviews); fine for N<=200.
while IFS=$'\t' read -r num title; do
  json="$(gh pr view "${num}" --repo "${REPO}" --json files,reviews)"
  files="$(jq -r '.files[].path' <<< "${json}")"
  runs="$(jq -r --arg bot "${BOT}" '[.reviews[] | select(.author.login == $bot)] | length' <<< "${json}")"
  scope=code
  if docs_only <<< "${files}"; then scope=docs/test; fi
  total_prs=$((total_prs + 1))
  total_runs=$((total_runs + runs))
  if [[ "${scope}" == "docs/test" ]]; then
    skippable_prs=$((skippable_prs + 1))
    skippable_runs=$((skippable_runs + runs))
  fi
  printf '#%-6s %-6s %-9s %s\n' "${num}" "${runs}" "${scope}" "${title:0:60}"
done < <(gh pr list --repo "${REPO}" --state merged --limit "${N}" --json number,title --jq '.[] | [.number, .title] | @tsv')

pct() { [[ "$2" -eq 0 ]] && echo 0 || echo $(( 100 * $1 / $2 )); }
echo
echo "PRs: ${total_prs} (docs/test-only: ${skippable_prs}, $(pct "${skippable_prs}" "${total_prs}")%)"
echo "Review runs by ${BOT}: ${total_runs} (on docs/test-only PRs: ${skippable_runs}, $(pct "${skippable_runs}" "${total_runs}")%)"
