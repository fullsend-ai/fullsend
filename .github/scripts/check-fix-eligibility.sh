#!/usr/bin/env bash
# check-fix-eligibility.sh — Determine if a bot-triggered fix should auto-run.
#
# Inputs (env vars):
#   GH_TOKEN              — GitHub token for API calls
#   PR_NUM                — Pull request number
#   SOURCE_REPO           — Repository in owner/repo format
#   TRIGGER_SOURCE        — Username that triggered the fix
#   REVIEW_MAX_FIX_CYCLES — Cap on review-bot CHANGES_REQUESTED cycles before
#                           blocking further bot-triggered fixes. Default 3,
#                           0 disables. Non-numeric values warn and fall back
#                           to the default. CI-runtime gating knob, not an
#                           agent behavior knob — see ADR 0081.
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

# Cap automated fix cycles: block further bot-triggered fixes once the
# review bot has requested changes REVIEW_MAX_FIX_CYCLES times on this PR.
# Each CHANGES_REQUESTED review is one trip around the review->fix loop;
# uncapped, a standing disagreement between the review and fix agents can
# oscillate until someone notices the bill. Human /fs-fix is unaffected —
# it already exited at the TRIGGER_SOURCE check above, before this gate.
REVIEW_MAX_FIX_CYCLES="${REVIEW_MAX_FIX_CYCLES:-3}"
if [[ ! "${REVIEW_MAX_FIX_CYCLES}" =~ ^[0-9]+$ ]]; then
  # Mirrors route-review-model.sh's TRIVIAL_MAX_LINES handling, but falls
  # back to the default instead of bailing out entirely — an unenforceable
  # cap must not silently become "no cap".
  echo "::warning::REVIEW_MAX_FIX_CYCLES is not a number (${REVIEW_MAX_FIX_CYCLES}) — using default of 3"
  REVIEW_MAX_FIX_CYCLES=3
fi

if [[ "${REVIEW_MAX_FIX_CYCLES}" != "0" ]]; then
  # The review bot's REST login is "<org>-review[bot]" (see
  # docs/contributing/bot-identities.md). SOURCE_REPO already carries the
  # org, so it's derived here rather than threaded in as a new input —
  # the same construction as the REVIEW_BOT var in the "Pre-fetch review
  # body" step of reusable-dispatch.yml.
  REVIEW_BOT_LOGIN="${SOURCE_REPO%%/*}-review[bot]"

  if CYCLE_COUNT=$(gh api "repos/${SOURCE_REPO}/pulls/${PR_NUM}/reviews" \
    --paginate 2>/dev/null \
    | jq -s --arg login "${REVIEW_BOT_LOGIN}" \
      'add | [.[] | select(.state == "CHANGES_REQUESTED" and .user.login == $login)] | length'); then
    if (( CYCLE_COUNT >= REVIEW_MAX_FIX_CYCLES )); then
      echo "::warning::PR #${PR_NUM} has reached ${CYCLE_COUNT} automated fix cycles (cap ${REVIEW_MAX_FIX_CYCLES}) — a human needs to look"

      MARKER='<!-- fullsend-fix-cycle-cap -->'
      EXISTING_ID=$(gh api "repos/${SOURCE_REPO}/issues/${PR_NUM}/comments" \
        --paginate 2>/dev/null \
        | jq -r --arg marker "${MARKER}" '.[] | select(.body | contains($marker)) | .id' \
        | head -n1 || true)

      if [[ -z "${EXISTING_ID}" ]]; then
        COMMENT_BODY="${MARKER}
${CYCLE_COUNT} automated fix cycles reached on this PR — a human needs to look. Trigger \`/fs-fix\` manually to run another cycle."
        jq -n --arg body "${COMMENT_BODY}" '{body: $body}' \
          | gh api -X POST "repos/${SOURCE_REPO}/issues/${PR_NUM}/comments" --input - >/dev/null \
          || echo "::warning::Could not post fix-cycle-cap comment on PR #${PR_NUM}"
      fi

      exit 1
    fi
  else
    echo "::warning::Could not count review cycles for PR #${PR_NUM} — proceeding without the cap"
  fi
fi
