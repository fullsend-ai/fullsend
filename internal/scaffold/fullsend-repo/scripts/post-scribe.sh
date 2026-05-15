#!/usr/bin/env bash
# post-scribe.sh — Parse scribe agent JSON output, apply security gate,
# and write to GitHub (comments on existing issues, new issues).
#
# Runs on the host after sandbox cleanup.
#
# Required env vars:
#   SCRIBE_REPO    — Primary GitHub repository (owner/name)
#   GH_TOKEN       — GitHub token with issues read/write scope
#   SCRIBE_DRY_RUN — "true" to preview without writing (ALWAYS true during dev)
#
# Optional env vars:
#   SCRIBE_TARGET_REPOS      — Comma-separated allowlist of target repos.
#                               Falls back to SCRIBE_REPO if unset.
#   SCRIBE_MODE              — "all" (default), "comments_only", "new_issues_only"
#   SCRIBE_SLACK_WEBHOOK_URL — Slack incoming webhook for notification (skip if unset)
#
# SAFETY: This script REFUSES to run if SCRIBE_DRY_RUN is not explicitly set.
# This prevents accidental writes during development.

set -euo pipefail

# ============================================================
# HARD SAFETY GATE — refuse to write if dry-run is not set
# ============================================================
if [[ -z "${SCRIBE_DRY_RUN:-}" ]]; then
  echo "ERROR: SCRIBE_DRY_RUN is not set. Refusing to run."
  echo "Set SCRIBE_DRY_RUN=true for preview or SCRIBE_DRY_RUN=false for live writes."
  exit 1
fi

DRY_RUN="true"
if [[ "${SCRIBE_DRY_RUN}" == "false" ]]; then
  DRY_RUN="false"
fi

if [[ "${DRY_RUN}" == "true" ]]; then
  echo "::notice::DRY RUN — no GitHub writes will be performed"
fi

# ============================================================
# Mode: comments_only, new_issues_only, or all (default)
# ============================================================
SCRIBE_MODE="${SCRIBE_MODE:-all}"
case "${SCRIBE_MODE}" in
  all|comments_only|new_issues_only) ;;
  *)
    echo "ERROR: SCRIBE_MODE must be 'all', 'comments_only', or 'new_issues_only' (got: ${SCRIBE_MODE})"
    exit 1
    ;;
esac
echo "Mode: ${SCRIBE_MODE}"

# ============================================================
# Build target repo allowlist for routing validation
# ============================================================
ALLOWED_REPOS=()
if [[ -n "${SCRIBE_TARGET_REPOS:-}" ]]; then
  IFS=',' read -ra ALLOWED_REPOS <<< "${SCRIBE_TARGET_REPOS}"
  for idx in "${!ALLOWED_REPOS[@]}"; do
    ALLOWED_REPOS[$idx]=$(echo "${ALLOWED_REPOS[$idx]}" | xargs)
  done
else
  ALLOWED_REPOS=("${SCRIBE_REPO}")
fi
echo "Allowed target repos (${#ALLOWED_REPOS[@]}): ${ALLOWED_REPOS[*]}"

is_allowed_repo() {
  local repo="$1"
  for allowed in "${ALLOWED_REPOS[@]}"; do
    if [[ "${repo}" == "${allowed}" ]]; then
      return 0
    fi
  done
  return 1
}

# Resolve the effective target repo for a topic/issue.
# Uses the agent-provided target_repo if valid, otherwise falls back to
# SCRIBE_REPO. Returns the repo via stdout.
resolve_target_repo() {
  local agent_repo="$1"
  if [[ -n "${agent_repo}" && "${agent_repo}" != "null" ]]; then
    if is_allowed_repo "${agent_repo}"; then
      echo "${agent_repo}"
      return 0
    else
      echo ""  # signal rejection
      return 1
    fi
  fi
  # Default to primary repo when target_repo is absent
  echo "${SCRIBE_REPO}"
  return 0
}

# Find the agent result JSON
RESULT_FILE=""
for dir in iteration-*/output; do
  if [[ -f "${dir}/agent-result.json" ]]; then
    RESULT_FILE="${dir}/agent-result.json"
  fi
done

if [[ -z "${RESULT_FILE}" ]]; then
  echo "ERROR: agent-result.json not found in any iteration output directory"
  exit 1
fi

echo "Reading scribe result from: ${RESULT_FILE}"

if ! jq empty "${RESULT_FILE}" 2>/dev/null; then
  echo "ERROR: ${RESULT_FILE} is not valid JSON"
  exit 1
fi

# ============================================================
# Security gate — deterministic checks on every topic
# ============================================================

MIN_CONFIDENCE="${SCRIBE_MIN_CONFIDENCE:-0.6}"
MAX_COMMENT_LEN=2000
MAX_BODY_LEN=15000
MAX_TITLE_LEN=200

if (( $(echo "${MIN_CONFIDENCE} < 0 || ${MIN_CONFIDENCE} > 1" | bc -l) )); then
  echo "ERROR: SCRIBE_MIN_CONFIDENCE must be between 0.0 and 1.0 (got: ${MIN_CONFIDENCE})"
  exit 1
fi
echo "Confidence threshold: ${MIN_CONFIDENCE}"
REJECTED=0
POSTED=0
CREATED=0

contains_sensitive() {
  local text="$1"
  # GitHub PATs, AWS access keys, private keys
  echo "${text}" \
    | grep -qEi '(ghp|gho|ghs|ghr)_[A-Za-z0-9_]{36,}|\b(AKIA|ABIA|ACCA|ASIA)[0-9A-Z]{16}\b|-----BEGIN.*(PRIVATE KEY)' \
    && return 0
  # Email addresses
  echo "${text}" \
    | grep -qE '\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b' \
    && return 0
  # SSN
  echo "${text}" \
    | grep -qE '\b[0-9]{3}-[0-9]{2}-[0-9]{4}\b' \
    && return 0
  # Slack webhooks
  echo "${text}" \
    | grep -qE 'https://hooks\.slack\.com/services/T[A-Z0-9]+/B[A-Z0-9]+/[A-Za-z0-9]+' \
    && return 0
  # JWTs
  echo "${text}" \
    | grep -qE '\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b' \
    && return 0
  # Generic key=value secrets
  echo "${text}" \
    | grep -qEi '(api[_-]?key|token|secret|password|bearer)[[:space:]]*[:=][[:space:]]*['"'"'"]?[A-Za-z0-9_.~+/-]{20,}' \
    && return 0
  return 1
}

contains_suspicious_unicode() {
  local text="$1"
  # Tag characters (U+E0000–E007F), zero-width chars, BOM, bidi overrides/isolates
  echo "${text}" \
    | perl -e 'binmode(STDIN, q(:encoding(UTF-8))); while (<STDIN>) { if (/[\x{E0000}-\x{E007F}\x{200B}\x{200C}\x{200D}\x{FEFF}\x{202A}-\x{202E}\x{2066}-\x{2069}]/) { exit 0 } } exit 1' \
    && return 0
  return 1
}

CONTENT_GATE_REJECTIONS=0

gate_reject() {
  local topic="$1" reason="$2"
  echo "  GATE REJECTED: [${topic}] — ${reason}"
  REJECTED=$((REJECTED + 1))
}

gate_reject_content() {
  local index="$1" total="$2" category="$3"
  echo "  GATE REJECTED: item ${index} of ${total} — content gate: ${category}"
  REJECTED=$((REJECTED + 1))
  CONTENT_GATE_REJECTIONS=$((CONTENT_GATE_REJECTIONS + 1))
}

# ============================================================
# Dedup: merge topics referencing the same existing issue
# ============================================================
# If the LLM produces multiple entries for the same issue despite being asked
# not to, merge them: combine summaries, keep the highest confidence, keep
# public_safe=false if any entry is unsafe.
DEDUP_FILE="${RESULT_FILE}.deduped"
jq '
  .topics as $all |
  ($all | map(select(.existing_issue != null)) | group_by(.existing_issue) |
    map(
      if length == 1 then .[0]
      else
        reduce .[] as $t (.[0];
          .summary = (.summary + "\n\n" + $t.summary) |
          .confidence = ([.confidence, $t.confidence] | max) |
          if $t.public_safe == false then .public_safe = false | .public_safe_category = $t.public_safe_category else . end
        )
      end
    )
  ) as $merged |
  ($all | map(select(.existing_issue == null))) as $rest |
  . + {topics: ($merged + $rest)}
' "${RESULT_FILE}" > "${DEDUP_FILE}"

ORIG_COUNT=$(jq '.topics | length' "${RESULT_FILE}")
DEDUP_COUNT=$(jq '.topics | length' "${DEDUP_FILE}")
if [[ "${ORIG_COUNT}" -ne "${DEDUP_COUNT}" ]]; then
  echo "Dedup: merged ${ORIG_COUNT} → ${DEDUP_COUNT} topics ($(( ORIG_COUNT - DEDUP_COUNT )) duplicates)"
  RESULT_FILE="${DEDUP_FILE}"
else
  rm -f "${DEDUP_FILE}"
fi

# Tracking arrays for step summary (parallel indexed lists)
COMMENT_TOPICS=()
COMMENT_ISSUES=()
NEW_ISSUE_TITLES=()
NEW_ISSUE_URLS=()
SKIPPED_NEW_ISSUES=0

# ============================================================
# Process comment topics (existing issues)
# ============================================================
TOPIC_COUNT=$(jq '.topics | length' "${RESULT_FILE}")

if [[ "${SCRIBE_MODE}" == "new_issues_only" ]]; then
  echo "Skipping ${TOPIC_COUNT} comment topics (mode: new_issues_only)"
  TOPIC_COUNT=0
else
  echo "Processing ${TOPIC_COUNT} topics for existing issues..."
fi

VALID_CATEGORIES="names interpersonal hr strategy security legal confidential"

for i in $(seq 0 $((TOPIC_COUNT - 1))); do
  # Read public_safe FIRST — if false, we must not log topic title or content
  PUBLIC_SAFE=$(jq -r ".topics[${i}].public_safe" "${RESULT_FILE}")
  PUBLIC_SAFE_CAT=$(jq -r ".topics[${i}].public_safe_category // empty" "${RESULT_FILE}")

  if [[ "${PUBLIC_SAFE}" == "false" ]]; then
    if [[ -z "${PUBLIC_SAFE_CAT}" || "${PUBLIC_SAFE_CAT}" == "null" ]]; then
      PUBLIC_SAFE_CAT="unspecified"
    fi
    gate_reject_content "$((i + 1))" "${TOPIC_COUNT}" "${PUBLIC_SAFE_CAT}"
    continue
  fi

  TOPIC=$(jq -r ".topics[${i}].topic" "${RESULT_FILE}")
  SUMMARY=$(jq -r ".topics[${i}].summary" "${RESULT_FILE}")
  CONFIDENCE=$(jq -r ".topics[${i}].confidence" "${RESULT_FILE}")
  ISSUE_NUM=$(jq -r ".topics[${i}].existing_issue // empty" "${RESULT_FILE}")
  AGENT_TARGET_REPO=$(jq -r ".topics[${i}].target_repo // empty" "${RESULT_FILE}")
  OMIT=$(jq -r ".topics[${i}].omit_reason // empty" "${RESULT_FILE}")

  if [[ -n "${OMIT}" ]]; then
    echo "  OMITTED: [${TOPIC}] — ${OMIT}"
    continue
  fi

  if [[ -z "${ISSUE_NUM}" || "${ISSUE_NUM}" == "null" ]]; then
    continue
  fi

  # Resolve target repo for this topic
  EFFECTIVE_REPO=$(resolve_target_repo "${AGENT_TARGET_REPO}") || true
  if [[ -z "${EFFECTIVE_REPO}" ]]; then
    gate_reject "${TOPIC}" "target_repo '${AGENT_TARGET_REPO}' not in allowed list"
    continue
  fi

  # Gate: confidence
  if (( $(echo "${CONFIDENCE} < ${MIN_CONFIDENCE}" | bc -l) )); then
    gate_reject "${TOPIC}" "confidence ${CONFIDENCE} below threshold ${MIN_CONFIDENCE}"
    continue
  fi

  # Gate: sensitive content (deterministic PII/secret patterns)
  if contains_sensitive "${SUMMARY}" || contains_sensitive "${TOPIC}"; then
    gate_reject "${TOPIC}" "contains sensitive content (PII, secrets)"
    continue
  fi

  # Gate: suspicious Unicode (prompt injection defense)
  if contains_suspicious_unicode "${SUMMARY}" || contains_suspicious_unicode "${TOPIC}"; then
    gate_reject "${TOPIC}" "contains suspicious Unicode (potential prompt injection)"
    continue
  fi

  # Gate: length
  SUMMARY_LEN=${#SUMMARY}
  if [[ ${SUMMARY_LEN} -gt ${MAX_COMMENT_LEN} ]]; then
    gate_reject "${TOPIC}" "summary length ${SUMMARY_LEN} exceeds max ${MAX_COMMENT_LEN}"
    continue
  fi

  # Gate: code blocks in comments
  if echo "${SUMMARY}" | grep -q '```'; then
    gate_reject "${TOPIC}" "comment contains code block (unexpected in meeting summary)"
    continue
  fi

  echo "  PASS: [${TOPIC}] → comment on ${EFFECTIVE_REPO}#${ISSUE_NUM} (confidence: ${CONFIDENCE})"
  COMMENT_TOPICS+=("${TOPIC}")
  COMMENT_ISSUES+=("${EFFECTIVE_REPO}#${ISSUE_NUM}")

  if [[ "${DRY_RUN}" == "true" ]]; then
    echo "    [DRY RUN] Would post comment to ${EFFECTIVE_REPO}#${ISSUE_NUM}"
  else
    # Idempotency: check if we already commented with this notes URL
    NOTES_URL=$(jq -r ".topics[${i}].summary" "${RESULT_FILE}" | grep -oP '\[Meeting notes\]\(\K[^)]+' || echo "")
    if [[ -n "${NOTES_URL}" ]]; then
      EXISTING=$(gh api "repos/${EFFECTIVE_REPO}/issues/${ISSUE_NUM}/comments" \
        --jq "[.[] | select(.body | contains(\"${NOTES_URL}\"))] | length" 2>/dev/null || echo "0")
      if [[ "${EXISTING}" -gt 0 ]]; then
        echo "    SKIP: duplicate comment (notes URL already posted)"
        continue
      fi
    fi

    printf '%s' "${SUMMARY}" | gh issue comment "${ISSUE_NUM}" --repo "${EFFECTIVE_REPO}" --body-file -
    POSTED=$((POSTED + 1))
  fi
done

# ============================================================
# Process new issues
# ============================================================
NEW_COUNT=$(jq '.new_issues | length' "${RESULT_FILE}")

if [[ "${SCRIBE_MODE}" == "comments_only" ]]; then
  echo "Skipping ${NEW_COUNT} new issue proposals (mode: comments_only)"
  SKIPPED_NEW_ISSUES=${NEW_COUNT}
  NEW_COUNT=0
else
  echo "Processing ${NEW_COUNT} new issue proposals..."
fi

for i in $(seq 0 $((NEW_COUNT - 1))); do
  # Read public_safe FIRST — if false, suppress all content from logs
  PUBLIC_SAFE=$(jq -r ".new_issues[${i}].public_safe" "${RESULT_FILE}")
  PUBLIC_SAFE_CAT=$(jq -r ".new_issues[${i}].public_safe_category // empty" "${RESULT_FILE}")

  if [[ "${PUBLIC_SAFE}" == "false" ]]; then
    if [[ -z "${PUBLIC_SAFE_CAT}" || "${PUBLIC_SAFE_CAT}" == "null" ]]; then
      PUBLIC_SAFE_CAT="unspecified"
    fi
    gate_reject_content "$((i + 1))" "${NEW_COUNT}" "${PUBLIC_SAFE_CAT}"
    continue
  fi

  TITLE=$(jq -r ".new_issues[${i}].title" "${RESULT_FILE}")
  BODY=$(jq -r ".new_issues[${i}].body" "${RESULT_FILE}")
  CONFIDENCE=$(jq -r ".new_issues[${i}].confidence" "${RESULT_FILE}")
  AGENT_TARGET_REPO=$(jq -r ".new_issues[${i}].target_repo // empty" "${RESULT_FILE}")
  LABELS=$(jq -r ".new_issues[${i}].labels // [\"meeting-notes\"] | join(\",\")" "${RESULT_FILE}")

  # Resolve target repo for this new issue
  EFFECTIVE_REPO=$(resolve_target_repo "${AGENT_TARGET_REPO}") || true
  if [[ -z "${EFFECTIVE_REPO}" ]]; then
    gate_reject "${TITLE}" "target_repo '${AGENT_TARGET_REPO}' not in allowed list"
    continue
  fi

  # Gate: confidence
  if (( $(echo "${CONFIDENCE} < ${MIN_CONFIDENCE}" | bc -l) )); then
    gate_reject "${TITLE}" "confidence ${CONFIDENCE} below threshold ${MIN_CONFIDENCE}"
    continue
  fi

  # Gate: sensitive content (deterministic PII/secret patterns)
  if contains_sensitive "${TITLE}" || contains_sensitive "${BODY}"; then
    gate_reject "${TITLE}" "contains sensitive content"
    continue
  fi

  # Gate: suspicious Unicode (prompt injection defense)
  if contains_suspicious_unicode "${TITLE}" || contains_suspicious_unicode "${BODY}"; then
    gate_reject "${TITLE}" "contains suspicious Unicode (potential prompt injection)"
    continue
  fi

  # Gate: lengths
  TITLE_LEN=${#TITLE}
  BODY_LEN=${#BODY}
  if [[ ${TITLE_LEN} -gt ${MAX_TITLE_LEN} ]]; then
    gate_reject "${TITLE}" "title length ${TITLE_LEN} exceeds max ${MAX_TITLE_LEN}"
    continue
  fi
  if [[ ${BODY_LEN} -gt ${MAX_BODY_LEN} ]]; then
    gate_reject "${TITLE}" "body length ${BODY_LEN} exceeds max ${MAX_BODY_LEN}"
    continue
  fi

  echo "  PASS: [${TITLE}] → new issue in ${EFFECTIVE_REPO} (confidence: ${CONFIDENCE})"
  NEW_ISSUE_TITLES+=("${TITLE}")

  # Prepend auto-generated banner so reviewers know this was machine-created
  BANNER='> [!NOTE]
> This issue was automatically generated from meeting notes by the scribe agent.
> Please review, edit, and add any missing context before prioritizing.

'
  FULL_BODY="${BANNER}${BODY}"

  if [[ "${DRY_RUN}" == "true" ]]; then
    echo "    [DRY RUN] Would create issue in ${EFFECTIVE_REPO}: ${TITLE}"
    echo "    [DRY RUN] Labels: ${LABELS}"
    echo "    [DRY RUN] Body length: ${BODY_LEN} chars"
    NEW_ISSUE_URLS+=("")
  else
    # Label fallback: if labels don't exist in the target repo, retry without
    ISSUE_URL=$(printf '%s' "${FULL_BODY}" | gh issue create \
        --repo "${EFFECTIVE_REPO}" \
        --title "${TITLE}" \
        --label "${LABELS}" \
        --body-file - 2>/dev/null) || \
    ISSUE_URL=$(printf '%s' "${FULL_BODY}" | gh issue create \
        --repo "${EFFECTIVE_REPO}" \
        --title "${TITLE}" \
        --body-file -)
    echo "    Created: ${ISSUE_URL}"
    NEW_ISSUE_URLS+=("${ISSUE_URL}")
    CREATED=$((CREATED + 1))
  fi
done

# ============================================================
# Summary (console)
# ============================================================
RUN_MODE_LABEL="LIVE"
[[ "${DRY_RUN}" == "true" ]] && RUN_MODE_LABEL="DRY RUN"

echo ""
echo "=== Scribe Post-Script Summary ==="
echo "  Run mode: ${RUN_MODE_LABEL}"
echo "  Agent mode: ${SCRIBE_MODE}"
echo "  Target repos: ${#ALLOWED_REPOS[@]} (${ALLOWED_REPOS[*]})"
echo "  Topics processed: ${TOPIC_COUNT}"
echo "  Comments ${DRY_RUN:+would be }posted: ${#COMMENT_TOPICS[@]}"
echo "  New issues ${DRY_RUN:+would be }created: ${#NEW_ISSUE_TITLES[@]}"
echo "  Gate rejections: ${REJECTED}"
echo "    Content gate: ${CONTENT_GATE_REJECTIONS}"
echo "  New proposals reviewed: ${NEW_COUNT}"
[[ "${SKIPPED_NEW_ISSUES}" -gt 0 ]] && echo "  Skipped new issues (mode): ${SKIPPED_NEW_ISSUES}"
echo "=================================="

# ============================================================
# GITHUB_STEP_SUMMARY — markdown report for Actions job page
# ============================================================
if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
{
  echo "### Scribe agent report (${RUN_MODE_LABEL})"
  echo ""
  echo "| Metric | Count |"
  echo "|--------|------:|"
  echo "| Target repos | ${#ALLOWED_REPOS[@]} |"
  echo "| Topics processed | ${TOPIC_COUNT} |"
  echo "| Comments ${DRY_RUN:+would be }posted | ${#COMMENT_TOPICS[@]} |"
  echo "| New issues ${DRY_RUN:+would be }created | ${#NEW_ISSUE_TITLES[@]} |"
  echo "| Gate rejections | ${REJECTED} |"
  echo "| Content gate rejections | ${CONTENT_GATE_REJECTIONS} |"
  if [[ "${SKIPPED_NEW_ISSUES}" -gt 0 ]]; then
    echo "| Skipped (${SCRIBE_MODE}) | ${SKIPPED_NEW_ISSUES} |"
  fi
  echo ""

  if [[ ${#COMMENT_TOPICS[@]} -gt 0 ]]; then
    echo "**Comments ${DRY_RUN:+would be }posted:** ${#COMMENT_TOPICS[@]}"
    for idx in "${!COMMENT_TOPICS[@]}"; do
      # COMMENT_ISSUES entries are "owner/repo#num" — split for URL
      COMMENT_REF="${COMMENT_ISSUES[$idx]}"
      COMMENT_REPO="${COMMENT_REF%%#*}"
      COMMENT_NUM="${COMMENT_REF##*#}"
      echo "- [${COMMENT_REF} — ${COMMENT_TOPICS[$idx]}](https://github.com/${COMMENT_REPO}/issues/${COMMENT_NUM})"
    done
    echo ""
  fi

  if [[ ${#NEW_ISSUE_TITLES[@]} -gt 0 ]]; then
    echo "**New issues ${DRY_RUN:+would be }filed:** ${#NEW_ISSUE_TITLES[@]}"
    for idx in "${!NEW_ISSUE_TITLES[@]}"; do
      if [[ -n "${NEW_ISSUE_URLS[$idx]:-}" ]]; then
        echo "- [${NEW_ISSUE_TITLES[$idx]}](${NEW_ISSUE_URLS[$idx]})"
      else
        echo "- ${NEW_ISSUE_TITLES[$idx]}"
      fi
    done
    echo ""
  fi

  if [[ "${REJECTED}" -gt 0 ]]; then
    echo "> **${REJECTED}** topic(s) rejected by the security gate."
    if [[ "${CONTENT_GATE_REJECTIONS}" -gt 0 ]]; then
      echo "> ${CONTENT_GATE_REJECTIONS} rejected by content gate (details suppressed for safety)."
    fi
    echo ""
  fi

  echo "_Confidence threshold: ${MIN_CONFIDENCE} · Mode: ${SCRIBE_MODE}_"
} >> "${GITHUB_STEP_SUMMARY}"
  echo "Step summary written to GITHUB_STEP_SUMMARY"
fi

# ============================================================
# Slack notification (optional — skip silently if no webhook)
# ============================================================
SLACK_WEBHOOK="${SCRIBE_SLACK_WEBHOOK_URL:-${SLACK_WEBHOOK_URL:-}}"
if [[ -n "${SLACK_WEBHOOK}" ]]; then
  echo "::add-mask::${SLACK_WEBHOOK}"
  RUN_URL="${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_REPOSITORY:-${SCRIBE_REPO}}/actions/runs/${GITHUB_RUN_ID:-0}"

  SLACK_TEXT=":memo: *Scribe agent* (${RUN_MODE_LABEL})"
  SLACK_TEXT+="\nMode: \`${SCRIBE_MODE}\` · Confidence: \`${MIN_CONFIDENCE}\`"
  SLACK_TEXT+="\n• Topics processed: *${TOPIC_COUNT}*"
  SLACK_TEXT+="\n• Comments: *${#COMMENT_TOPICS[@]}*"
  SLACK_TEXT+="\n• New issues: *${#NEW_ISSUE_TITLES[@]}*"
  SLACK_TEXT+="\n• Gate rejections: *${REJECTED}*"
  if [[ "${CONTENT_GATE_REJECTIONS}" -gt 0 ]]; then
    SLACK_TEXT+=" (${CONTENT_GATE_REJECTIONS} content)"
  fi
  if [[ "${SKIPPED_NEW_ISSUES}" -gt 0 ]]; then
    SLACK_TEXT+="\n• Skipped (${SCRIBE_MODE}): *${SKIPPED_NEW_ISSUES}*"
  fi

  if [[ ${#COMMENT_TOPICS[@]} -gt 0 ]]; then
    SLACK_TEXT+="\n\n*Comments:*"
    for idx in "${!COMMENT_TOPICS[@]}"; do
      COMMENT_REF="${COMMENT_ISSUES[$idx]}"
      COMMENT_REPO="${COMMENT_REF%%#*}"
      COMMENT_NUM="${COMMENT_REF##*#}"
      SLACK_TEXT+="\n  • <https://github.com/${COMMENT_REPO}/issues/${COMMENT_NUM}|${COMMENT_REF} — ${COMMENT_TOPICS[$idx]}>"
    done
  fi

  if [[ ${#NEW_ISSUE_TITLES[@]} -gt 0 ]]; then
    SLACK_TEXT+="\n\n*New issues:*"
    for idx in "${!NEW_ISSUE_TITLES[@]}"; do
      if [[ -n "${NEW_ISSUE_URLS[$idx]:-}" ]]; then
        SLACK_TEXT+="\n  • <${NEW_ISSUE_URLS[$idx]}|${NEW_ISSUE_TITLES[$idx]}>"
      else
        SLACK_TEXT+="\n  • ${NEW_ISSUE_TITLES[$idx]}"
      fi
    done
  fi

  SLACK_TEXT+="\n\n<${RUN_URL}|View run>"

  SLACK_PAYLOAD=$(printf '%b' "${SLACK_TEXT}" | jq -Rs '{text: .}')
  if printf '%s' "${SLACK_PAYLOAD}" \
      | curl -fsSL -X POST -H 'Content-Type: application/json' \
        --data-binary @- "${SLACK_WEBHOOK}" >/dev/null 2>&1; then
    echo "Slack notification sent"
  else
    echo "WARNING: Slack notification failed (non-fatal)"
  fi
  unset SLACK_WEBHOOK
else
  echo "No SCRIBE_SLACK_WEBHOOK_URL set — skipping Slack notification"
fi
