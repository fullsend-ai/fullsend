#!/usr/bin/env bash
# check-fix-eligibility.sh — Determine if a bot-triggered fix should auto-run.
#
# Inputs (env vars):
#   GH_TOKEN       — GitHub token for API calls
#   PR_NUM         — Pull request number
#   SOURCE_REPO    — Repository in owner/repo format
#   TRIGGER_SOURCE — Username that triggered the fix
#   FULLSEND_APP_SET — Optional configured GitHub App set prefix
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

# Recognize the shared coder bot and the exact coder identity derived from a
# configured app set. The app/ prefix is the gh pr view --json format; see
# docs/contributing/bot-identities.md.
CUSTOM_CODER_LOGIN=""
if [[ -n "${FULLSEND_APP_SET:-}" ]]; then
  CUSTOM_CODER_LOGIN="app/${FULLSEND_APP_SET}-coder"
fi
KNOWN_CODER=false
if [[ "${PR_IS_BOT}" == "true" &&
      ( "${PR_LOGIN}" == "app/fullsend-ai-coder" ||
        "${PR_LOGIN}" == "${CUSTOM_CODER_LOGIN}" ) ]]; then
  KNOWN_CODER=true
fi

# Unknown bots and human-authored PRs require the fullsend-fix label.
if [[ "${KNOWN_CODER}" != "true" ]]; then
  HAS_FIX_LABEL=$(echo "${PR_INFO}" | jq -r '.labels | any(. == "fullsend-fix")')
  if [[ "${HAS_FIX_LABEL}" != "true" ]]; then
    echo "::warning::PR #${PR_NUM} (author: ${PR_LOGIN_SAFE}, is_bot: ${PR_IS_BOT_SAFE}) is not the coder bot and lacks 'fullsend-fix' label — skipping bot-triggered fix"
    exit 1
  fi
fi
