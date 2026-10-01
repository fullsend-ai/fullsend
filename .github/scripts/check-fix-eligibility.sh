#!/usr/bin/env bash
# check-fix-eligibility.sh — Determine if a bot-triggered fix should auto-run.
#
# Inputs (env vars):
#   GH_TOKEN       — GitHub token for API calls
#   PR_NUM         — Pull request number
#   SOURCE_REPO    — Repository in owner/repo format
#   TRIGGER_SOURCE — Username that triggered the fix
#   HUMAN_PUSH_RECENCY_SECONDS — Skip bot-triggered fixes when HEAD is a
#                    human commit newer than this many seconds (default
#                    1800). Set to 0 to disable the recency check.
#   HUMAN_PUSH_NOW_EPOCH — Optional fixed "now" (unix seconds) for tests.
#
# Exits 0 if fix should proceed, 1 if it should be skipped.
# Emits GitHub Actions annotations (::warning::) for skip reasons.

set -euo pipefail

# Only gate bot-triggered runs; human /fs-fix always proceeds.
if [[ ! "${TRIGGER_SOURCE}" =~ \[bot\]$ ]]; then
  exit 0
fi

PR_INFO=$(gh pr view "${PR_NUM}" --repo "${SOURCE_REPO}" \
  --json labels,author --jq '{labels: [.labels[].name], is_bot: .author.is_bot, login: .author.login}') \
  || { echo "::error::Failed to fetch PR info for #${PR_NUM}"; exit 1; }

HAS_NO_FIX=$(echo "${PR_INFO}" | jq -r '.labels | any(. == "fullsend-no-fix")')
if [[ "${HAS_NO_FIX}" == "true" ]]; then
  echo "::warning::PR #${PR_NUM} has 'fullsend-no-fix' label — skipping bot-triggered fix"
  exit 1
fi

PR_IS_BOT=$(echo "${PR_INFO}" | jq -r '.is_bot')
PR_LOGIN=$(echo "${PR_INFO}" | jq -r '.login')

_sanitize_for_annotation() {
  local val="$1"
  val="${val//::/__}"
  val="${val//$'\n'/}"
  val="${val//$'\r'/}"
  val="${val//%25/}"
  val="${val//%0A/}"
  val="${val//%0a/}"
  val="${val//%0D/}"
  val="${val//%0d/}"
  printf '%s' "${val}"
}

PR_IS_BOT_SAFE=$(_sanitize_for_annotation "${PR_IS_BOT}")
PR_LOGIN_SAFE=$(_sanitize_for_annotation "${PR_LOGIN}")

if [[ "${PR_IS_BOT}" != "true" && "${PR_IS_BOT}" != "false" ]]; then
  echo "::warning::gh pr view did not return is_bot field (got '${PR_IS_BOT_SAFE}') — gh CLI may be too old; treating as non-bot"
fi

# Not the fullsend coder bot — require the fullsend-fix label.
# The app/ prefix is the gh pr view --json format; see docs/contributing/bot-identities.md.
if [[ "${PR_IS_BOT}" != "true" || "${PR_LOGIN}" != "app/fullsend-ai-coder" ]]; then
  HAS_FIX_LABEL=$(echo "${PR_INFO}" | jq -r '.labels | any(. == "fullsend-fix")')
  if [[ "${HAS_FIX_LABEL}" != "true" ]]; then
    echo "::warning::PR #${PR_NUM} (author: ${PR_LOGIN_SAFE}, is_bot: ${PR_IS_BOT_SAFE}) is not the coder bot and lacks 'fullsend-fix' label — skipping bot-triggered fix"
    exit 1
  fi
fi

# Skip bot-triggered fixes when a human pushed to HEAD recently. This is
# waste-prevention (issue #7564), not an authorization gate: a concurrent
# human push already causes post-fix to fail closed rather than overwrite
# the branch. Fail open on lookup/parse errors so a transient API blip
# cannot block a legitimate fix. Human /fs-fix never reaches this check.
WINDOW="${HUMAN_PUSH_RECENCY_SECONDS:-1800}"
if [[ ! "${WINDOW}" =~ ^[0-9]+$ ]]; then
  echo "::warning::Invalid HUMAN_PUSH_RECENCY_SECONDS; using 1800"
  WINDOW=1800
fi
if [[ "${WINDOW}" == "0" ]]; then
  exit 0
fi

HEAD_SHA=""
if ! HEAD_SHA=$(gh api "repos/${SOURCE_REPO}/pulls/${PR_NUM}" --jq '.head.sha'); then
  echo "::warning::Could not fetch PR head SHA for human-activity check — proceeding"
  exit 0
fi
HEAD_SHA="${HEAD_SHA//$'\n'/}"
HEAD_SHA="${HEAD_SHA//$'\r'/}"
if [[ -z "${HEAD_SHA}" || "${HEAD_SHA}" == "null" ]]; then
  echo "::warning::PR head SHA missing for human-activity check — proceeding"
  exit 0
fi
if [[ ! "${HEAD_SHA}" =~ ^[0-9a-fA-F]{7,40}$ ]]; then
  echo "::warning::PR head SHA malformed for human-activity check — proceeding"
  exit 0
fi

COMMIT_META=""
if ! COMMIT_META=$(gh api "repos/${SOURCE_REPO}/commits/${HEAD_SHA}" \
  --jq '{type:(.author.type//""),login:(.author.login//""),committer_type:(.committer.type//""),committer_login:(.committer.login//""),committed_at:(.commit.committer.date//"")}'); then
  echo "::warning::Could not fetch PR head commit for human-activity check — proceeding"
  exit 0
fi

AUTHOR_TYPE=$(echo "${COMMIT_META}" | jq -r '.type // empty')
AUTHOR_LOGIN=$(echo "${COMMIT_META}" | jq -r '.login // empty')
COMMITTER_TYPE=$(echo "${COMMIT_META}" | jq -r '.committer_type // empty')
COMMITTER_LOGIN=$(echo "${COMMIT_META}" | jq -r '.committer_login // empty')
COMMITTED_AT=$(echo "${COMMIT_META}" | jq -r '.committed_at // empty')
COMMITTED_AT="${COMMITTED_AT//$'\n'/}"
COMMITTED_AT="${COMMITTED_AT//$'\r'/}"

# Prefer the git author; if GitHub did not link an author, fall back to
# the committer so unassociated-email human pushes are still detected.
EFFECTIVE_TYPE="${AUTHOR_TYPE}"
EFFECTIVE_LOGIN="${AUTHOR_LOGIN}"
if [[ -z "${EFFECTIVE_TYPE}" && -z "${EFFECTIVE_LOGIN}" ]]; then
  EFFECTIVE_TYPE="${COMMITTER_TYPE}"
  EFFECTIVE_LOGIN="${COMMITTER_LOGIN}"
fi

is_bot_identity() {
  local type="$1" login="$2"
  if [[ "${type}" == "Bot" ]]; then
    return 0
  fi
  if [[ "${login}" == *"[bot]" ]]; then
    return 0
  fi
  return 1
}

if is_bot_identity "${EFFECTIVE_TYPE}" "${EFFECTIVE_LOGIN}"; then
  exit 0
fi
if [[ -z "${EFFECTIVE_TYPE}" && -z "${EFFECTIVE_LOGIN}" ]]; then
  echo "::warning::Head commit has no GitHub author for human-activity check — proceeding"
  exit 0
fi

if [[ -z "${COMMITTED_AT}" ]]; then
  echo "::warning::Head commit timestamp missing for human-activity check — proceeding"
  exit 0
fi

NOW_EPOCH="${HUMAN_PUSH_NOW_EPOCH:-}"
if [[ -z "${NOW_EPOCH}" ]]; then
  NOW_EPOCH=$(date +%s)
fi
if [[ ! "${NOW_EPOCH}" =~ ^[0-9]+$ ]]; then
  echo "::warning::Invalid HUMAN_PUSH_NOW_EPOCH; using current time"
  NOW_EPOCH=$(date +%s)
fi

COMMIT_EPOCH=0
if ! COMMIT_EPOCH=$(date -u -d "${COMMITTED_AT}" +%s); then
  echo "::warning::Could not parse head commit timestamp for human-activity check — proceeding"
  exit 0
fi

AGE=$((NOW_EPOCH - COMMIT_EPOCH))
if [[ "${AGE}" -lt "${WINDOW}" ]]; then
  LOGIN_SAFE=$(_sanitize_for_annotation "${EFFECTIVE_LOGIN}")
  echo "::warning::skipped: recent human push on PR #${PR_NUM} (head by ${LOGIN_SAFE}, ${AGE}s ago) — bot-triggered fix deferred; use /fs-fix once editing is done"
  exit 1
fi
