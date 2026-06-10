#!/usr/bin/env bash
# check-e2e-authorization.sh — Decide whether a PR may run e2e tests in CI.
#
# Authorized when the PR author is OWNER/MEMBER/COLLABORATOR, or when a fresh
# ok-to-test label was applied after the latest commit on the PR head.
# Push time uses GitHub issue timeline committed/force-push event timestamps
# (server-side), falling back to committer.date only when timeline is empty.
# Removes stale ok-to-test labels (applied at or before the latest push).
#
# Usage: check-e2e-authorization.sh PR_NUMBER OWNER/REPO
#
# Writes authorized, reason, and label_removed to GITHUB_OUTPUT when set.
# Exits 0 always; callers inspect outputs.

set -euo pipefail

PR_NUMBER="${1:?PR number required}"
REPOSITORY="${2:?repository (owner/repo) required}"

TRUSTED_ASSOCIATIONS="OWNER MEMBER COLLABORATOR"
OK_TO_TEST_LABEL="ok-to-test"

write_error_output() {
  echo "check-e2e-authorization: API or script error (see log above)" >&2
  if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
    {
      echo "authorized=false"
      echo "reason=error"
      echo "label_removed=false"
    } >>"${GITHUB_OUTPUT}"
  fi
  printf 'authorized=false reason=error label_removed=false\n'
}

trap 'write_error_output; exit 0' ERR

is_trusted_author() {
  local assoc="$1"
  case " ${TRUSTED_ASSOCIATIONS} " in
    *" ${assoc} "*) return 0 ;;
    *) return 1 ;;
  esac
}

pr_json="$(gh api "repos/${REPOSITORY}/pulls/${PR_NUMBER}")"
author_association="$(jq -r '.author_association' <<<"${pr_json}")"
has_ok_label="$(jq -r --arg label "${OK_TO_TEST_LABEL}" '[.labels[].name] | index($label) != null' <<<"${pr_json}")"

timeline_json="$(gh api "repos/${REPOSITORY}/issues/${PR_NUMBER}/timeline" --paginate \
  -H "Accept: application/vnd.github+json" | jq -s 'add')"
last_push_at="$(jq -r '
  [.[] | select(.event == "committed" or .event == "head_ref_force_pushed") | .created_at] | max // empty
' <<<"${timeline_json}")"
if [[ -z "${last_push_at}" ]]; then
  commits_json="$(gh api "repos/${REPOSITORY}/pulls/${PR_NUMBER}/commits" --paginate | jq -s 'add')"
  last_push_at="$(jq -r '[.[] | .commit.committer.date] | max // empty' <<<"${commits_json}")"
fi

events_json="$(gh api "repos/${REPOSITORY}/issues/${PR_NUMBER}/events" --paginate | jq -s 'add')"
ok_to_test_at="$(jq -r --arg label "${OK_TO_TEST_LABEL}" '
  [.[] | select(.event == "labeled" and (.label.name // "") == $label) | .created_at] | max // empty
' <<<"${events_json}")"

is_trusted=false
if is_trusted_author "${author_association}"; then
  is_trusted=true
fi

has_fresh_label=false
label_removed=false
authorized=false
reason="unauthorized"

if [[ "${has_ok_label}" == "true" && "${is_trusted}" != "true" ]]; then
  if [[ -n "${ok_to_test_at}" && -n "${last_push_at}" && "${ok_to_test_at}" > "${last_push_at}" ]]; then
    has_fresh_label=true
  else
    if [[ "${CHECK_E2E_AUTH_DRY_RUN:-}" != "true" ]]; then
      gh api -X DELETE "repos/${REPOSITORY}/issues/${PR_NUMBER}/labels/${OK_TO_TEST_LABEL}" >/dev/null
    fi
    label_removed=true
  fi
fi

if [[ "${is_trusted}" == "true" ]]; then
  authorized=true
  reason="trusted_author"
elif [[ "${has_fresh_label}" == "true" ]]; then
  authorized=true
  reason="ok_to_test"
elif [[ "${label_removed}" == "true" ]]; then
  reason="stale_ok_to_test"
fi

trap - ERR

if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  {
    echo "authorized=${authorized}"
    echo "reason=${reason}"
    echo "label_removed=${label_removed}"
  } >>"${GITHUB_OUTPUT}"
fi

printf 'authorized=%s reason=%s label_removed=%s\n' "${authorized}" "${reason}" "${label_removed}"
