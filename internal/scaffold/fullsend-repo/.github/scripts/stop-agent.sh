#!/usr/bin/env bash
# stop-agent.sh — Apply fullsend-no-* labels for /fs-stop and /fs-fix-stop.
# Invoked by the shim stop-agent job. Requires env:
#   GH_TOKEN, REPO, ISSUE_NUMBER, COMMENT_USER_LOGIN, ISSUE_USER_LOGIN,
#   COMMENT_BODY, ISSUE_IS_PR ("true"|"false")
set -euo pipefail

post_comment() {
  local body_file="$1"
  if [[ "${ISSUE_IS_PR}" == "true" ]]; then
    gh pr comment "${ISSUE_NUMBER}" --repo "${REPO}" --body-file "${body_file}"
  else
    gh issue comment "${ISSUE_NUMBER}" --repo "${REPO}" --body-file "${body_file}"
  fi
}

make_body_file() {
  local f
  if ! f="$(mktemp)"; then
    echo "::warning::Failed to create temp file for stop-agent comment body"
    return 1
  fi
  printf '%s' "$f"
}

# Trim leading blank lines and leading whitespace on the first non-blank line
# so copy/paste / markdown-quoted comments still work (matches awk tokenization
# used by other slash commands in dispatch).
FIRST="$(printf '%s\n' "${COMMENT_BODY}" | sed '/^[[:space:]]*$/d' | head -1 | sed 's/^[[:space:]]*//' | tr -d '\r')"
CMD="$(printf '%s\n' "${FIRST}" | awk '{print $1}')"
ARG="$(printf '%s\n' "${FIRST}" | awk '{print $2}')"
# Sanitize for workflow-command interpolation (defense in depth).
SAFE_CMD="${CMD//::/_}"

# Agents with auto-trigger paths gated by fullsend-no-* in dispatch.
# prioritize is slash-only and has no auto-trigger to suppress.
# Bare /fs-stop only applies labels meaningful for this item type.
VALID_ALL="triage code review fix retro"
VALID_ISSUE="triage code"
VALID_PR="review fix retro"
if [[ "${ISSUE_IS_PR}" == "true" ]]; then
  VALID_BARE="${VALID_PR}"
else
  VALID_BARE="${VALID_ISSUE}"
fi
VALID="${VALID_ALL}"
AGENTS=()
CROSS_CONTEXT=false
if [[ "${CMD}" == "/fs-fix-stop" ]]; then
  AGENTS=(fix)
  if [[ "${ISSUE_IS_PR}" != "true" ]]; then
    CROSS_CONTEXT=true
  fi
elif [[ "${CMD}" == "/fs-stop" ]]; then
  if [[ -z "${ARG:-}" ]]; then
    # shellcheck disable=SC2206
    AGENTS=(${VALID_BARE})
  elif [[ "${ARG}" =~ ^[a-z]+$ ]] && [[ " ${VALID} " == *" ${ARG} "* ]]; then
    AGENTS=("${ARG}")
    if [[ "${ISSUE_IS_PR}" == "true" ]]; then
      if [[ " ${VALID_ISSUE} " == *" ${ARG} "* ]] && [[ " ${VALID_PR} " != *" ${ARG} "* ]]; then
        CROSS_CONTEXT=true
      fi
    else
      if [[ " ${VALID_PR} " == *" ${ARG} "* ]] && [[ " ${VALID_ISSUE} " != *" ${ARG} "* ]]; then
        CROSS_CONTEXT=true
      fi
    fi
  else
    AGENTS=()
    UNKNOWN_AGENT=true
  fi
else
  echo "::notice::Ignoring unrecognized stop command: ${SAFE_CMD}"
  exit 0
fi

# ADR 0054: authorize via the collaborator permission API
# (admin|maintain|write), not author_association — the latter grants
# contributor status to anyone with a single merged PR (issue #5421).
# Mirrors has_repo_permission() in dispatch.yml; keep the two in sync.
#
# Author escape hatch is intentionally limited to stopping *fix* only
# (historical /fs-fix-stop behavior). Stopping review/triage/code/retro —
# including bare /fs-stop — requires write-level permission so PR authors
# cannot unilaterally suppress security-relevant auto-gates.
authorized=false
is_author=false
if [[ -n "${COMMENT_USER_LOGIN}" && "${COMMENT_USER_LOGIN}" == "${ISSUE_USER_LOGIN}" ]]; then
  is_author=true
fi
author_fix_only=false
if [[ "${is_author}" == "true" && "${UNKNOWN_AGENT:-}" != "true" && "${#AGENTS[@]}" -eq 1 && "${AGENTS[0]}" == "fix" ]]; then
  author_fix_only=true
  authorized=true
fi
if [[ "${authorized}" != "true" ]]; then
  if api_err=$(mktemp); then
    if role=$(gh api "repos/${REPO}/collaborators/${COMMENT_USER_LOGIN}/permission" \
      --jq '.role_name' 2>"${api_err}"); then
      case "${role}" in
        admin|maintain|write) authorized=true ;;
      esac
    else
      api_err_safe="$(tr -d '\r' <"${api_err}" | tr '\n' ' ')"
      api_err_safe="${api_err_safe//::/_}"
      echo "::warning::Permission API call failed for ${COMMENT_USER_LOGIN}: ${api_err_safe}"
    fi
    rm -f "${api_err}"
  else
    echo "::warning::Failed to create temp file for permission check of ${COMMENT_USER_LOGIN}"
  fi
fi
if [[ "${authorized}" != "true" ]]; then
  if [[ "${is_author}" == "true" && "${author_fix_only}" != "true" ]]; then
    echo "::notice::User ${COMMENT_USER_LOGIN} is not authorized to stop these agents (PR/issue authors may only /fs-stop fix or /fs-fix-stop; write access required otherwise)"
  else
    echo "::notice::User ${COMMENT_USER_LOGIN} is not authorized to stop agents (requires write access, or authorship for /fs-stop fix only)"
  fi
  exit 0
fi

if [[ "${UNKNOWN_AGENT:-}" == "true" ]]; then
  BODY_FILE="$(make_body_file)" || exit 0
  {
    printf 'Unknown or unsupported agent.'
    printf ' Valid auto-stop targets: %s.' "${VALID}"
    printf ' Usage: `/fs-stop <agent>` or `/fs-stop` for all meaningful on this item.'
    printf ' Note: prioritize is slash-only (`/fs-prioritize`); there is no auto-trigger to stop.'
  } >"${BODY_FILE}"
  post_comment "${BODY_FILE}" || true
  rm -f "${BODY_FILE}"
  exit 0
fi

APPLIED=()
for agent in "${AGENTS[@]}"; do
  label="fullsend-no-${agent}"
  gh label create "${label}" --repo "${REPO}" \
    --description "Skip auto-triggered ${agent} agent runs" --color "FBCA04" \
    --force 2>/dev/null || true
  if [[ "${ISSUE_IS_PR}" == "true" ]]; then
    if gh pr edit "${ISSUE_NUMBER}" --repo "${REPO}" --add-label "${label}"; then
      APPLIED+=("\`${label}\`")
    else
      echo "::warning::Failed to apply label ${label}"
    fi
  else
    if gh issue edit "${ISSUE_NUMBER}" --repo "${REPO}" --add-label "${label}"; then
      APPLIED+=("\`${label}\`")
    else
      echo "::warning::Failed to apply label ${label}"
    fi
  fi
done

BODY_FILE="$(make_body_file)" || exit 0
if [[ "${#APPLIED[@]}" -eq 0 ]]; then
  printf 'Agent stop requested for #%s, but no labels were applied (label API calls failed — see workflow run logs).\n' \
    "${ISSUE_NUMBER}" >"${BODY_FILE}"
else
  LIST="$(printf '%s, ' "${APPLIED[@]}")"
  LIST="${LIST%, }"
  {
    printf 'Agent stop applied for #%s: %s.\n' "${ISSUE_NUMBER}" "${LIST}"
    printf 'Auto-triggers for these agents are skipped while the label(s) remain.\n'
    printf 'On-demand `/fs-<agent>` commands still work.\n'
    printf 'In-flight runs are not cancelled by this command — remove the label(s) or re-run `/fs-<agent>` to continue.\n'
    if [[ "${CROSS_CONTEXT}" == "true" ]]; then
      printf 'Note: this label has no effect on this item type'
      if [[ "${ISSUE_IS_PR}" == "true" ]]; then
        printf ' (auto-triggers for that agent run on issues, not PRs)'
      else
        printf ' (auto-triggers for that agent run on PRs, not issues)'
      fi
      printf ', and it does not carry over to a linked issue or PR.\n'
    fi
  } >"${BODY_FILE}"
fi
post_comment "${BODY_FILE}" || true
rm -f "${BODY_FILE}"
