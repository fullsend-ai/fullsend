#!/usr/bin/env bash
# check-e2e-authorization.sh — Decide whether a PR may run e2e tests in CI.
#
# Authorized when the PR author is a trusted bot (e.g. renovate-fullsend[bot]),
# when the collaborator permission API confirms write+ access (admin, maintain,
# write), or when this run is an ok-to-test labeled event from a user with
# write+ access.
#
# Do not base trust on author_association: GitHub reports MEMBER or COLLABORATOR
# for users with read or triage roles, which must not run credentialed CI.
# Trust is based strictly on the collaborator permission API (write, maintain, admin).
#
# A retained ok-to-test label does not approve opened, synchronize, reopened,
# or any other event. On those events a label that does not postdate the
# frozen PR updated_at (PR_UPDATED_AT) is removed. A label applied after that
# timestamp is left for the labeled workflow and does not authorize this run
# (reason=fresh_ok_to_test). Does not use committer.date (author-controlled).
#
# /ok-to-test command mode (EVENT_NAME=issue_comment): a maintainer's
# `/ok-to-test` comment authorizes exactly one PR head sha. The commenter
# must have admin, maintain or write (collaborator permission API). The
# script resolves the PR head sha and refuses it (reason=stale_ok_to_test)
# when the head moved at or after the comment. The time the head sha became
# the head is the latest pull_request workflow run created for that sha on the
# PR's head repository and branch after the PR was opened (set by GitHub, not
# by the PR author). A head_ref_force_pushed event at or after that time that
# moved the head to another commit also makes it stale, because the sha may
# have come back by a push that left no run yet. When no
# such run exists the sha is refused (reason=unknown_head_time).
#
# Usage: check-e2e-authorization.sh PR_NUMBER OWNER/REPO
#
# Environment (optional, from workflow):
#   PR_AUTHOR_ASSOCIATION — github.event.pull_request.author_association
#   PR_AUTHOR_LOGIN — github.event.pull_request.user.login
#   PR_UPDATED_AT — github.event.pull_request.updated_at
#   EVENT_ACTION  — github.event.action
#   LABEL_ACTOR_LOGIN — github.event.sender.login (who applied the label on
#     labeled events)
#   EVENT_NAME — github.event_name; issue_comment selects /ok-to-test mode
#   COMMENTER_LOGIN — github.event.comment.user.login (/ok-to-test mode)
#   COMMENT_ID — github.event.comment.id (/ok-to-test mode). When set, the
#     comment is read from the API: it must belong to PR_NUMBER and to
#     COMMENTER_LOGIN, and its created_at is used.
#   COMMENT_CREATED_AT — github.event.comment.created_at (/ok-to-test mode,
#     used when COMMENT_ID is unset)
#
# Writes authorized, reason, label_removed and head_sha to GITHUB_OUTPUT when
# set. head_sha is empty outside /ok-to-test mode. Exits 0 always; callers
# inspect outputs.

set -euo pipefail

PR_NUMBER="${1:?PR number required}"
REPOSITORY="${2:?repository (owner/repo) required}"

TRUSTED_BOT_LOGINS="renovate-fullsend[bot] fullsend-ai-coder[bot]"
OK_TO_TEST_LABEL="ok-to-test"

write_error_output() {
  echo "check-e2e-authorization: API or script error (see log above)" >&2
  if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
    {
      echo "authorized=false"
      echo "reason=error"
      echo "label_removed=false"
      echo "head_sha="
    } >>"${GITHUB_OUTPUT}"
  fi
  printf 'authorized=false reason=error label_removed=false head_sha=\n'
}

write_outputs() {
  if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
    {
      echo "authorized=${authorized}"
      echo "reason=${reason}"
      echo "label_removed=${label_removed}"
      echo "head_sha=${head_sha}"
    } >>"${GITHUB_OUTPUT}"
  fi
  printf 'authorized=%s reason=%s label_removed=%s head_sha=%s\n' \
    "${authorized}" "${reason}" "${label_removed}" "${head_sha}"
}

trap 'write_error_output; exit 0' ERR

is_trusted_bot() {
  local login="${1:-}"
  case " ${TRUSTED_BOT_LOGINS} " in
    *" ${login} "*) return 0 ;;
    *) return 1 ;;
  esac
}

# Check actor has write+ permission via the collaborator permission API.
# Base trust strictly on write+ permissions (admin, maintain, write), never
# on author_association which can include triage/read members.
# API errors (network, 500, rate limits, malformed JSON) return 1 to trigger
# error handling rather than silently denying or deleting labels.
get_collaborator_role() {
  local username="${1:-}"
  if [[ -z "${username}" ]]; then
    return 1
  fi
  local api_err perm_json role
  api_err="$(mktemp)"
  if ! perm_json="$(gh api "repos/${REPOSITORY}/collaborators/${username}/permission" 2>"${api_err}")"; then
    if grep -qE "HTTP 404|not a collaborator" "${api_err}"; then
      rm -f "${api_err}"
      echo "none"
      return 0
    fi
    cat "${api_err}" >&2
    rm -f "${api_err}"
    return 1
  fi
  rm -f "${api_err}"
  local role perm
  role="$(jq -er '.role_name // ""' <<<"${perm_json}")" || return 1
  perm="$(jq -er '.permission // empty' <<<"${perm_json}")" || return 1
  case "${role}" in
    admin) [[ "${perm}" == "admin" ]] && echo "admin" || echo "none" ;;
    maintain|write) [[ "${perm}" == "write" ]] && echo "${role}" || echo "none" ;;
    triage|read) [[ "${perm}" == "read" ]] && echo "${role}" || echo "none" ;;
    *)
      # Custom role or bot/app with empty role_name: fall back to canonical permission
      case "${perm}" in
        admin|write|read|none) echo "${perm}" ;;
        *) return 1 ;;
      esac
      ;;
  esac
}

is_write_role() {
  case "${1:-}" in
    admin|maintain|write) return 0 ;;
    *) return 1 ;;
  esac
}

# The ERR trap is not inherited by functions: return 1 so it fires at the call.
# Concurrent workflows (e.g. Functional Tests and Behaviour Tests) race to delete
# the label; treat HTTP 404 (Label does not exist) as already removed.
remove_ok_to_test_label() {
  if [[ "${CHECK_E2E_AUTH_DRY_RUN:-}" != "true" ]]; then
    local del_err
    del_err="$(mktemp)"
    if ! gh api -X DELETE "repos/${REPOSITORY}/issues/${PR_NUMBER}/labels/${OK_TO_TEST_LABEL}" >/dev/null 2>"${del_err}"; then
      if grep -qiE "HTTP 404|Label does not exist" "${del_err}"; then
        rm -f "${del_err}"
        label_removed=true
        return 0
      fi
      cat "${del_err}" >&2
      rm -f "${del_err}"
      return 1
    fi
    rm -f "${del_err}"
  fi
  label_removed=true
}

# Look up the latest user who applied ok-to-test from issue event history.
# Used on non-labeled events (e.g. reopened) or to check if a label was re-applied.
latest_labeler() {
  if [[ -z "${events_json:-}" ]]; then
    events_json="$(gh api "repos/${REPOSITORY}/issues/${PR_NUMBER}/events" --paginate | jq -s 'add // []')" || return 1
  fi
  jq -r --arg label "${OK_TO_TEST_LABEL}" '
    [.[] | select(.event == "labeled" and (.label.name // "") == $label)]
    | max_by(.created_at // "") | .actor.login // empty
  ' <<<"${events_json}"
}

label_removed=false
authorized=false
reason="unauthorized"
head_sha=""

# --- /ok-to-test command mode -------------------------------------------------
# Kept at top level (not in a function) so the ERR trap covers every API call.
if [[ "${EVENT_NAME:-}" == "issue_comment" ]]; then
  commenter="${COMMENTER_LOGIN:-}"
  comment_at="${COMMENT_CREATED_AT:-}"

  if [[ -n "${COMMENT_ID:-}" ]]; then
    if [[ ! "${COMMENT_ID}" =~ ^[0-9]+$ ]]; then
      write_error_output
      exit 0
    fi
    comment_json="$(gh api "repos/${REPOSITORY}/issues/comments/${COMMENT_ID}")"
    comment_login="$(jq -r '.user.login // empty' <<<"${comment_json}")"
    comment_issue_url="$(jq -r '.issue_url // empty' <<<"${comment_json}")"
    # Fail closed when the comment is on another issue or by someone else.
    if [[ "${comment_issue_url}" != *"/repos/${REPOSITORY}/issues/${PR_NUMBER}" ]] ||
      [[ -n "${commenter}" && "${comment_login}" != "${commenter}" ]]; then
      echo "check-e2e-authorization: comment ${COMMENT_ID} does not match PR ${PR_NUMBER} / ${commenter}" >&2
      write_error_output
      exit 0
    fi
    commenter="${comment_login}"
    comment_at="$(jq -r '.created_at // empty' <<<"${comment_json}")"
  fi

  if [[ -z "${commenter}" || -z "${comment_at}" ]]; then
    write_error_output
    exit 0
  fi

  if ! commenter_role="$(get_collaborator_role "${commenter}")"; then
    write_error_output
    exit 0
  fi

  if ! is_write_role "${commenter_role}"; then
    reason="untrusted_commenter"
  else
    pr_json="$(gh api "repos/${REPOSITORY}/pulls/${PR_NUMBER}")"
    head_sha="$(jq -er '.head.sha' <<<"${pr_json}")"
    head_ref="$(jq -r '.head.ref // empty' <<<"${pr_json}")"
    head_repo="$(jq -r '.head.repo.full_name // empty' <<<"${pr_json}")"
    pr_created_at="$(jq -r '.created_at // empty' <<<"${pr_json}")"

    # Latest pull_request run for this sha on the PR's own head branch, created
    # after this PR was opened. Runs from other branches or forks that carried
    # the same sha do not count, and neither do runs from an earlier PR on the
    # same branch: an author could seed a run for a commit there, open a new PR
    # at an ancestor, and fast-forward to the seeded commit after /ok-to-test.
    runs_json="$(gh api "repos/${REPOSITORY}/actions/runs?event=pull_request&head_sha=${head_sha}&per_page=100" --paginate |
      jq -s '[.[].workflow_runs[]?]')"
    head_at="$(jq -r --arg ref "${head_ref}" --arg repo "${head_repo}" --arg opened "${pr_created_at}" '
      [.[] | select(.head_branch == $ref and (.head_repository.full_name // "") == $repo
                    and (.created_at // "") >= $opened)
           | .created_at // empty] | max // empty
    ' <<<"${runs_json}")"

    if [[ -z "${head_ref}" || -z "${head_repo}" || -z "${pr_created_at}" || -z "${head_at}" ]]; then
      reason="unknown_head_time"
    elif [[ ! "${head_at}" < "${comment_at}" ]]; then
      reason="stale_ok_to_test"
    else
      events_json="$(gh api "repos/${REPOSITORY}/issues/${PR_NUMBER}/events" --paginate | jq -s 'add // []')"
      # A force push at or after the head's run that moved the head to another
      # commit (commit_id is the new head) means the head left and may have
      # come back without a new run. A force push to the head sha itself can
      # share the run's second and must not refuse it forever.
      forced="$(jq -r --arg at "${head_at}" --arg sha "${head_sha}" '
        [.[] | select(.event == "head_ref_force_pushed" and (.created_at // "") >= $at
                      and (.commit_id // "") != $sha)] | length
      ' <<<"${events_json}")"
      if [[ "${forced}" != "0" ]]; then
        reason="stale_ok_to_test"
      else
        authorized=true
        reason="ok_to_test"
      fi
    fi
  fi

  trap - ERR
  write_outputs
  exit 0
fi

# Ensure PR_AUTHOR_LOGIN is populated if not provided via environment.
if [[ -z "${PR_AUTHOR_LOGIN:-}" ]]; then
  pr_json="$(gh api "repos/${REPOSITORY}/pulls/${PR_NUMBER}")"
  PR_AUTHOR_LOGIN="$(jq -r '.user.login // empty' <<<"${pr_json}")"
fi

# Fail closed if author identity is missing
if [[ -z "${PR_AUTHOR_LOGIN:-}" ]]; then
  write_error_output
  exit 0
fi

if is_trusted_bot "${PR_AUTHOR_LOGIN:-}"; then
  authorized=true
  reason="trusted_bot"
else
  if ! author_role="$(get_collaborator_role "${PR_AUTHOR_LOGIN:-}")"; then
    write_error_output
    exit 0
  fi
  if is_write_role "${author_role}"; then
    authorized=true
    reason="trusted_author"
  else
    pr_json="${pr_json:-$(gh api "repos/${REPOSITORY}/pulls/${PR_NUMBER}")}"
    has_ok_label="$(jq -r --arg label "${OK_TO_TEST_LABEL}" '[.labels[].name] | index($label) != null' <<<"${pr_json}")"

    if [[ "${has_ok_label}" == "true" ]]; then
      if [[ "${EVENT_ACTION:-}" == "labeled" ]]; then
        authorized=true
        reason="ok_to_test"
      else
        # Only the labeled event approves a run. An older label is stale.
        # A label applied after this event belongs to the labeled workflow.
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
          reason="fresh_ok_to_test"
        else
          remove_ok_to_test_label
          reason="stale_ok_to_test"
        fi
      fi
    fi
  fi
fi

if [[ "${reason}" == "ok_to_test" ]]; then
  # On labeled events the frozen payload names the labeler of this run. A
  # labeled event MUST have a sender; it must never fall back to live issue events
  # which could pick up a later label applied by another user.
  if [[ "${EVENT_ACTION:-}" == "labeled" ]]; then
    labeler_login="${LABEL_ACTOR_LOGIN:-}"
  else
    labeler_login="$(latest_labeler)"
  fi

  # Any missing identity results in denial (Fail Closed).
  if [[ -z "${labeler_login}" ]]; then
    authorized=false
    reason="untrusted_labeler"
  else
    if ! labeler_role="$(get_collaborator_role "${labeler_login}")"; then
      write_error_output
      exit 0
    fi

    # Remove the label so a maintainer's re-apply fires a new labeled event.
    # Keep it when someone else has re-applied it since: that run decides.
    if ! is_write_role "${labeler_role}"; then
      authorized=false
      reason="untrusted_labeler"
      if [[ "${EVENT_ACTION:-}" == "labeled" && -n "${LABEL_ACTOR_LOGIN:-}" ]]; then
        if ! current_labeler="$(latest_labeler)"; then
          write_error_output
          exit 0
        fi
        if [[ "${current_labeler}" == "${LABEL_ACTOR_LOGIN}" ]]; then
          remove_ok_to_test_label
        fi
      else
        remove_ok_to_test_label
      fi
    fi
  fi
fi

trap - ERR

write_outputs
