#!/usr/bin/env bash
# post-scribe.sh — Parse scribe agent JSON output, apply security gate,
# and write to GitHub (comments on existing issues, new issues).
#
# Runs on the host after sandbox cleanup.
#
# Required env vars:
#   SCRIBE_REPO    — GitHub repository (owner/name)
#   GH_TOKEN       — GitHub token with issues read/write scope
#   SCRIBE_DRY_RUN — "true" to preview without writing (ALWAYS true during dev)
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
  echo "${text}" \
    | grep -qEi '(ghp|gho|ghs|ghr)_[A-Za-z0-9_]{36,}|\b(AKIA|ABIA|ACCA|ASIA)[0-9A-Z]{16}\b|-----BEGIN.*(PRIVATE KEY)' \
    && return 0
  echo "${text}" \
    | grep -qE '\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b' \
    && return 0
  echo "${text}" \
    | grep -qE '\b[0-9]{3}-[0-9]{2}-[0-9]{4}\b' \
    && return 0
  return 1
}

gate_reject() {
  local topic="$1" reason="$2"
  echo "  GATE REJECTED: [${topic}] — ${reason}"
  REJECTED=$((REJECTED + 1))
}

# ============================================================
# Process comment topics (existing issues)
# ============================================================
TOPIC_COUNT=$(jq '.topics | length' "${RESULT_FILE}")
echo "Processing ${TOPIC_COUNT} topics for existing issues..."

for i in $(seq 0 $((TOPIC_COUNT - 1))); do
  TOPIC=$(jq -r ".topics[${i}].topic" "${RESULT_FILE}")
  SUMMARY=$(jq -r ".topics[${i}].summary" "${RESULT_FILE}")
  CONFIDENCE=$(jq -r ".topics[${i}].confidence" "${RESULT_FILE}")
  ISSUE_NUM=$(jq -r ".topics[${i}].existing_issue // empty" "${RESULT_FILE}")
  OMIT=$(jq -r ".topics[${i}].omit_reason // empty" "${RESULT_FILE}")

  if [[ -n "${OMIT}" ]]; then
    echo "  OMITTED: [${TOPIC}] — ${OMIT}"
    continue
  fi

  if [[ -z "${ISSUE_NUM}" || "${ISSUE_NUM}" == "null" ]]; then
    continue
  fi

  # Gate: confidence
  if (( $(echo "${CONFIDENCE} < ${MIN_CONFIDENCE}" | bc -l) )); then
    gate_reject "${TOPIC}" "confidence ${CONFIDENCE} below threshold ${MIN_CONFIDENCE}"
    continue
  fi

  # Gate: sensitive content
  if contains_sensitive "${SUMMARY}" || contains_sensitive "${TOPIC}"; then
    gate_reject "${TOPIC}" "contains sensitive content (PII, secrets)"
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

  echo "  PASS: [${TOPIC}] → comment on #${ISSUE_NUM} (confidence: ${CONFIDENCE})"

  if [[ "${DRY_RUN}" == "true" ]]; then
    echo "    [DRY RUN] Would post comment to ${SCRIBE_REPO}#${ISSUE_NUM}"
  else
    # Idempotency: check if we already commented with this notes URL
    NOTES_URL=$(jq -r ".topics[${i}].summary" "${RESULT_FILE}" | grep -oP '\[Meeting notes\]\(\K[^)]+' || echo "")
    if [[ -n "${NOTES_URL}" ]]; then
      EXISTING=$(gh api "repos/${SCRIBE_REPO}/issues/${ISSUE_NUM}/comments" \
        --jq "[.[] | select(.body | contains(\"${NOTES_URL}\"))] | length" 2>/dev/null || echo "0")
      if [[ "${EXISTING}" -gt 0 ]]; then
        echo "    SKIP: duplicate comment (notes URL already posted)"
        continue
      fi
    fi

    printf '%s' "${SUMMARY}" | gh issue comment "${ISSUE_NUM}" --repo "${SCRIBE_REPO}" --body-file -
    POSTED=$((POSTED + 1))
  fi
done

# ============================================================
# Process new issues
# ============================================================
NEW_COUNT=$(jq '.new_issues | length' "${RESULT_FILE}")
echo "Processing ${NEW_COUNT} new issue proposals..."

for i in $(seq 0 $((NEW_COUNT - 1))); do
  TITLE=$(jq -r ".new_issues[${i}].title" "${RESULT_FILE}")
  BODY=$(jq -r ".new_issues[${i}].body" "${RESULT_FILE}")
  CONFIDENCE=$(jq -r ".new_issues[${i}].confidence" "${RESULT_FILE}")
  LABELS=$(jq -r ".new_issues[${i}].labels // [\"meeting-notes\"] | join(\",\")" "${RESULT_FILE}")

  # Gate: confidence
  if (( $(echo "${CONFIDENCE} < ${MIN_CONFIDENCE}" | bc -l) )); then
    gate_reject "${TITLE}" "confidence ${CONFIDENCE} below threshold ${MIN_CONFIDENCE}"
    continue
  fi

  # Gate: sensitive content
  if contains_sensitive "${TITLE}" || contains_sensitive "${BODY}"; then
    gate_reject "${TITLE}" "contains sensitive content"
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

  echo "  PASS: [${TITLE}] → new issue (confidence: ${CONFIDENCE})"

  if [[ "${DRY_RUN}" == "true" ]]; then
    echo "    [DRY RUN] Would create issue: ${TITLE}"
    echo "    [DRY RUN] Labels: ${LABELS}"
    echo "    [DRY RUN] Body length: ${BODY_LEN} chars"
  else
    printf '%s' "${BODY}" | gh issue create \
      --repo "${SCRIBE_REPO}" \
      --title "${TITLE}" \
      --label "${LABELS}" \
      --body-file -
    CREATED=$((CREATED + 1))
  fi
done

# ============================================================
# Summary
# ============================================================
echo ""
echo "=== Scribe Post-Script Summary ==="
echo "  Mode: $([ "${DRY_RUN}" == "true" ] && echo "DRY RUN" || echo "LIVE")"
echo "  Topics processed: ${TOPIC_COUNT}"
echo "  Comments posted: ${POSTED}"
echo "  New issues created: ${CREATED}"
echo "  Gate rejections: ${REJECTED}"
echo "  New proposals reviewed: ${NEW_COUNT}"
echo "=================================="
