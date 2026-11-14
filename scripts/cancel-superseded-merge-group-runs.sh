#!/usr/bin/env bash
# cancel-superseded-merge-group-runs.sh — Cancel workflow runs left behind by
# discarded merge-queue groups (#9353).
#
# When the merge queue rebuilds, GitHub deletes the old
# gh-readonly-queue/<base>/pr-<n>-<sha> branches and creates new ones, but the
# runs started for the old branches keep going. Concurrency groups cannot stop
# them: the merge_group ref contains the group's head SHA, so every group gets
# its own concurrency group, and workflow expressions cannot cut the pull
# request number out of the ref. See docs/contributing/ci-workflows.md.
#
# The script:
#   1. lists unfinished runs (requested, waiting, pending, queued, in
#      progress) with event merge_group;
#   2. lists the gh-readonly-queue/ branches that exist now;
#   3. for each run whose head_branch is under gh-readonly-queue/, missing
#      from that listing, and at least MIN_AGE_SECONDS old, confirms the branch
#      is gone with GET git/ref/heads/<branch> and cancels the run only when
#      that answer is a definite HTTP 404.
# Runs are listed before branches, and the listing is the first line of
# defence. GitHub does not promise that the listing is complete or
# read-after-write consistent, so a branch missing from it is only a
# candidate; the per-branch GET decides. Any answer other than 404 (200, 5xx,
# rate limit, network error, unparseable) keeps the run.
#
# Safety gate: if the branch listing fails, is empty, or does not contain this
# run's own branch, the listing cannot be trusted and nothing is cancelled.
# A run whose event is not merge_group, and the current run (GITHUB_RUN_ID),
# are never cancelled.
#
# Merged groups: when a group merges, its branch is deleted too, so a run of
# that group still going (a non-required workflow; the required checks have
# finished) is cancelled as well. See docs/contributing/ci-workflows.md.
#
# Environment:
#   GH_TOKEN           — token with actions: write (read-only with DRY_RUN=1)
#   GITHUB_REPOSITORY  — owner/repo
#   OWN_REF or OWN_BRANCH — this run's merge-group ref (refs/heads/...) or
#                        branch name
#   GITHUB_RUN_ID      — this run's id; never cancelled (optional)
#   DRY_RUN=1          — print would-cancel lines; makes no POST call (the
#                        read-only confirm GET still runs)
#   NOW_EPOCH          — override "now" in Unix seconds (tests only)
#
# Prints one line per run: kept (with the reason), cancelled, cancel-failed or
# would-cancel.
# Exits 0 in every handled case, including API failures, so it never fails the
# merge group. Exits 2 on missing configuration.

set -euo pipefail

QUEUE_PREFIX="gh-readonly-queue/"
# Runs younger than this are kept: their group may be too new to show up in
# the branch listing.
MIN_AGE_SECONDS=120

repository="${GITHUB_REPOSITORY:-}"
own_branch="${OWN_BRANCH:-}"
if [[ -z "${own_branch}" && -n "${OWN_REF:-}" ]]; then
  own_branch="${OWN_REF#refs/heads/}"
fi
current_run_id="${GITHUB_RUN_ID:-}"
now="${NOW_EPOCH:-$(date +%s)}"
dry_run=false
[[ "${DRY_RUN:-}" == "1" || "${DRY_RUN:-}" == "true" ]] && dry_run=true

if [[ "${repository}" != */* || -z "${own_branch}" ]]; then
  echo "::error::GITHUB_REPOSITORY (owner/repo) and OWN_REF or OWN_BRANCH are required" >&2
  exit 2
fi

skip() {
  echo "::notice::cancel-superseded: $1; cancelling nothing"
  exit 0
}

if [[ "${own_branch}" != "${QUEUE_PREFIX}"* ]]; then
  skip "own branch ${own_branch} is not a merge-queue branch"
fi

# 1. Runs first (see the header). One call per status; --paginate with a
# per-item --jq is safe, the pages are joined by the separate jq -s.
runs=""
#
# Every unfinished status is listed, earliest in a run's life first: a run
# that moves on between two calls (queued to in_progress, say) is then still
# caught by the later call, and unique_by drops the duplicate when both
# calls return it.
for status in requested waiting pending queued in_progress; do
  if ! page_runs="$(gh api --paginate \
    "repos/${repository}/actions/runs?event=merge_group&status=${status}&per_page=100" \
    --jq '.workflow_runs[] | {id, name, event, head_branch, created_at}' | jq -cs '.')"; then
    skip "listing ${status} merge_group runs failed"
  fi
  if ! runs="$(jq -cs 'add | unique_by(.id)' <<<"${runs:-[]}"$'\n'"${page_runs}")"; then
    skip "merging the ${status} run listing failed"
  fi
done

# 2. Live merge-queue branches.
if ! branches="$(gh api --paginate \
  "repos/${repository}/git/matching-refs/heads/${QUEUE_PREFIX}" \
  --jq '.[].ref | ltrimstr("refs/heads/")')"; then
  skip "listing ${QUEUE_PREFIX} branches failed"
fi
if [[ -z "${branches}" ]]; then
  skip "branch listing is empty"
fi
if ! grep -qxF -- "${own_branch}" <<<"${branches}"; then
  skip "own branch ${own_branch} is not in the branch listing"
fi

# Read-only. Prints the HTTP status code of GET git/ref/heads/<branch>, or
# nothing when no status line came back (network error, unparseable output).
# gh api exits 1 for both a 404 and a 5xx, so the exit code is not used;
# --include puts the status line ("HTTP/2.0 404 Not Found") first on stdout.
# Ref paths take the branch's slashes literally.
ref_status() {
  local out first
  out="$(gh api --include "repos/${repository}/git/ref/heads/$1" 2>/dev/null)" || true
  first="${out%%$'\n'*}"
  first="${first%$'\r'}"
  if [[ "${first}" =~ ^HTTP/[0-9.]+\ ([0-9]{3})(\ |$) ]]; then
    echo "${BASH_REMATCH[1]}"
  fi
}

# The only state-changing call in this script. DRY_RUN never reaches it.
cancel_run() {
  gh api -X POST "repos/${repository}/actions/runs/$1/cancel" >/dev/null
}

# 3. Decide per run. Every field is re-checked here; the query filter is not
# trusted on its own. Fields are joined with the unit separator (not IFS
# whitespace, so empty fields keep their column), and control characters are
# stripped from them so a run name cannot inject workflow commands
# (::warning:: etc.) on a new log line. created_at becomes Unix seconds, or ""
# when missing or unparseable.
while IFS=$'\x1f' read -r id event branch created name; do
  [[ -n "${id}" ]] || continue
  line="run ${id} (${name}) on ${branch}"
  if [[ "${event}" != "merge_group" ]]; then
    echo "kept: ${line}: event ${event} is not merge_group"
  elif [[ -n "${current_run_id}" && "${id}" == "${current_run_id}" ]]; then
    echo "kept: ${line}: current run"
  elif [[ "${branch}" != "${QUEUE_PREFIX}"* ]]; then
    echo "kept: ${line}: not a merge-queue branch"
  elif grep -qxF -- "${branch}" <<<"${branches}"; then
    echo "kept: ${line}: branch exists"
  elif [[ ! "${created}" =~ ^[0-9]+$ ]]; then
    echo "kept: ${line}: no usable created_at"
  elif ((now - created < MIN_AGE_SECONDS)); then
    echo "kept: ${line}: too new"
  else
    code="$(ref_status "${branch}")"
    if [[ "${code}" == "200" ]]; then
      echo "kept: ${line}: branch exists (confirm GET 200)"
    elif [[ "${code}" != "404" ]]; then
      echo "kept: ${line}: branch not confirmed gone (confirm GET status ${code:-none})"
    elif [[ "${dry_run}" == "true" ]]; then
      echo "would-cancel: ${line}: branch deleted (confirm GET 404)"
    elif cancel_run "${id}"; then
      echo "cancelled: ${line}: branch deleted (confirm GET 404)"
    else
      echo "::warning::cancel-failed: ${line}"
    fi
  fi
done < <(jq -r '
  def clean: tostring | explode | map(if . < 32 or . == 127 then 32 else . end) | implode;
  .[]
  | [ (.id | clean),
      (.event // "" | clean),
      (.head_branch // "" | clean),
      (.created_at // "" | try fromdateiso8601 catch "" | tostring),
      (.name // "" | clean) ]
  | join("\u001f")' <<<"${runs}")

exit 0
