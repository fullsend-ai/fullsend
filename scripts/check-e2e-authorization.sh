#!/usr/bin/env bash
# check-e2e-authorization.sh — Decide whether a PR may run e2e tests in CI.
#
# Authorized when the PR author is OWNER/MEMBER/COLLABORATOR, or when the
# collaborator permission API confirms write+ access, or when a fresh
# ok-to-test label was applied after the latest push.
#
# The author_association field from the event payload can misreport org members
# whose membership visibility is private (returns CONTRIBUTOR/NONE instead of
# MEMBER). When author_association is untrusted, the script falls back to the
# collaborator permission API which correctly resolves regardless of visibility.
#
# Freshness uses PR updated_at from the frozen workflow event (PR_UPDATED_AT).
# On ok-to-test labeled events, authorization is immediate. Does not use
# committer.date (author-controlled).
#
# Usage: check-e2e-authorization.sh PR_NUMBER OWNER/REPO
#
# Environment (optional, from workflow):
#   PR_AUTHOR_ASSOCIATION — github.event.pull_request.author_association
#   PR_AUTHOR_LOGIN — github.event.pull_request.user.login
#   PR_UPDATED_AT — github.event.pull_request.updated_at
#   EVENT_ACTION  — github.event.action
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

# Fallback: check actor has write+ permission via the collaborator permission
# API, which correctly resolves org membership regardless of visibility
# (private vs public). Same approach as the dispatch workflow.
has_write_permission() {
  local username="${1:-}"
  if [[ -z "${username}" ]]; then
    return 1
  fi
  local perm_json role
  perm_json=$(gh api "repos/${REPOSITORY}/collaborators/${username}/permission" 2>/dev/null) || return 1
  role=$(jq -r '.role_name' <<<"${perm_json}") || return 1
  case "${role}" in
    admin|maintain|write) return 0 ;;
    *) return 1 ;;
  esac
}

label_removed=false
authorized=false
reason="unauthorized"

# Try the frozen workflow event payload first (fast path). If it reports an
# untrusted association, has_write_permission falls back to the collaborator
# permission API which resolves correctly regardless of membership visibility.
if [[ -n "${PR_AUTHOR_ASSOCIATION:-}" ]]; then
  author_association="${PR_AUTHOR_ASSOCIATION}"
else
  pr_json="$(gh api "repos/${REPOSITORY}/pulls/${PR_NUMBER}")"
  author_association="$(jq -r '.author_association' <<<"${pr_json}")"
fi

if is_trusted_author "${author_association}"; then
  authorized=true
  reason="trusted_author"
elif has_write_permission "${PR_AUTHOR_LOGIN:-}" 2>/dev/null; then
  # author_association was wrong (e.g. private org membership); collaborator
  # permission API confirms write+ access.
  authorized=true
  reason="trusted_author"
else
  pr_json="${pr_json:-$(gh api "repos/${REPOSITORY}/pulls/${PR_NUMBER}")}"
  has_ok_label="$(jq -r --arg label "${OK_TO_TEST_LABEL}" '[.labels[].name] | index($label) != null' <<<"${pr_json}")"

  if [[ "${has_ok_label}" == "true" && "${EVENT_ACTION:-}" == "labeled" ]]; then
    authorized=true
    reason="ok_to_test"
  elif [[ "${has_ok_label}" == "true" ]]; then
    events_json="$(gh api "repos/${REPOSITORY}/issues/${PR_NUMBER}/events" --paginate | jq -s 'add // []')"
    ok_to_test_at="$(jq -r --arg label "${OK_TO_TEST_LABEL}" '
      [.[] | select(.event == "labeled" and (.label.name // "") == $label) | .created_at] | max // empty
    ' <<<"${events_json}")"

    last_push_at="${PR_UPDATED_AT:-}"
    if [[ -z "${last_push_at}" ]]; then
      # Fallback: live updated_at is noisy (bumped by comments, labels, etc.)
      # and may over-reject. Prefer the frozen event-payload value (PR_UPDATED_AT).
      last_push_at="$(jq -r '.updated_at // empty' <<<"${pr_json}")"
    fi

    if [[ -n "${ok_to_test_at}" && -n "${last_push_at}" && "${ok_to_test_at}" > "${last_push_at}" ]]; then
      authorized=true
      reason="ok_to_test"
    else
      if [[ "${CHECK_E2E_AUTH_DRY_RUN:-}" != "true" ]]; then
        gh api -X DELETE "repos/${REPOSITORY}/issues/${PR_NUMBER}/labels/${OK_TO_TEST_LABEL}" >/dev/null
      fi
      label_removed=true
      reason="stale_ok_to_test"
    fi
  fi
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
