#!/usr/bin/env bash
# create-children.sh — Create child issues from an approved refinement plan.
#
# Reusable script that reads a refinement result JSON and creates child issues
# in topological order using parent_title references for hierarchy.
#
# Can be called from:
#   - post-critique.sh (auto-approval path)
#   - create-children.yml workflow (human-approval path)
#
# Required env vars:
#   RESULT_FILE        — Path to the approved agent-result.json
#   ISSUE_KEY          — Parent issue identifier (Jira key or GH issue number)
#   ISSUE_SOURCE       — "jira" or "github"
#   GH_TOKEN           — GitHub token
#
# GitHub flow env vars:
#   GITHUB_ISSUE_NUMBER — GitHub issue number
#   REPO_FULL_NAME      — owner/repo
#   PUSH_TOKEN          — Token with write access
#
# Jira flow env vars:
#   JIRA_HOST, JIRA_EMAIL, JIRA_API_TOKEN

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ -f "${SCRIPT_DIR}/pipeline-events.sh" ]]; then
  source "${SCRIPT_DIR}/pipeline-events.sh"
  # shellcheck disable=SC2034  # HAS_PE used by callers that source this script
  HAS_PE=true
else
  # shellcheck disable=SC2034
  HAS_PE=false
  pe_start() { :; }
  pe_end() { :; }
fi

if [[ -z "${RESULT_FILE:-}" ]]; then
  echo "ERROR: RESULT_FILE env var not set"
  exit 1
fi

if [[ ! -f "${RESULT_FILE}" ]]; then
  echo "ERROR: Result file not found: ${RESULT_FILE}"
  exit 1
fi

if ! jq empty "${RESULT_FILE}" 2>/dev/null; then
  echo "ERROR: ${RESULT_FILE} is not valid JSON"
  exit 1
fi

# Dry-run validation: verify children structure before touching external APIs
VALIDATION_ERRORS=$(jq -r '
  if (.children | type) != "array" then "RESULT_FILE has no .children array"
  elif (.children | length) == 0 then ".children array is empty — nothing to create"
  else
    [.children | to_entries[] |
      (if (.value.title // "" | length) == 0 then "child[\(.key)]: missing title" else empty end),
      (if (.value.type // "" | length) == 0 then "child[\(.key)]: missing type" else empty end),
      (if (.value.description // "" | length) == 0 then "child[\(.key)]: missing description" else empty end)
    ] |
    if length > 0 then join("\n") else empty end
  end // empty
' "${RESULT_FILE}" 2>/dev/null)

if [[ -n "${VALIDATION_ERRORS}" ]]; then
  echo "::error::Dry-run validation failed — child issue structure is invalid:"
  echo "${VALIDATION_ERRORS}" | while IFS= read -r line; do
    echo "::error::  ${line}"
  done
  echo "::error::Fix the refine agent output and retry. No issues were created."
  exit 1
fi

# Validate parent_title references resolve within the tree
ORPHAN_REFS=$(jq -r '
  [.children[].title] as $titles |
  [.children[] | select(.parent_title != null and .parent_title != "") |
    select([.parent_title] | inside($titles) | not) |
    "parent_title \"\(.parent_title)\" (in child \"\(.title)\") not found in children list"
  ] | if length > 0 then join("\n") else empty end
' "${RESULT_FILE}" 2>/dev/null)

if [[ -n "${ORPHAN_REFS}" ]]; then
  echo "::warning::Some parent_title references don't match any child title (will fall back to root):"
  echo "${ORPHAN_REFS}" | while IFS= read -r line; do
    echo "::warning::  ${line}"
  done
fi

echo "Dry-run validation passed: $(jq '.children | length' "${RESULT_FILE}") children, structure OK"

USE_GITHUB=false
if [[ -n "${GITHUB_ISSUE_NUMBER:-}" && "${GITHUB_ISSUE_NUMBER}" != "" && "${GITHUB_ISSUE_NUMBER}" != "N/A" ]]; then
  USE_GITHUB=true
elif [[ "${ISSUE_SOURCE:-}" == "github" ]]; then
  USE_GITHUB=true
  GITHUB_ISSUE_NUMBER="${ISSUE_KEY}"
fi

# --- Helper functions ---

github_create_issue() {
  local repo="$1" title="$2" body="$3" labels="$4" parent_number="${5:-}"
  local args=(--repo "$repo" --title "$title")
  if [[ -n "$labels" && "$labels" != "null" ]]; then
    while IFS= read -r label; do
      if [[ -n "$label" ]]; then
        gh label create "$label" --repo "$repo" --force 2>/dev/null || true
        args+=(--label "$label")
      fi
    done < <(echo "$labels" | jq -r '.[]')
  fi
  local result
  result=$(printf '%s' "$body" | gh issue create "${args[@]}" --body-file - 2>&1) || {
    echo "::warning::Failed to create issue '${title}': ${result}" >&2
    echo "FAILED"
    return 0
  }

  local issue_number
  issue_number=$(echo "$result" | grep -oP '/issues/\K[0-9]+' || true)

  if [[ -n "$parent_number" && -n "$issue_number" ]]; then
    local child_id
    child_id=$(gh api "repos/${repo}/issues/${issue_number}" --jq '.id' 2>/dev/null)
    if [[ -n "$child_id" ]]; then
      gh api "repos/${repo}/issues/${parent_number}/sub_issues" \
        -F sub_issue_id="$child_id" \
        --silent 2>/dev/null || \
        echo "::warning::Could not link #${issue_number} as sub-issue of #${parent_number}" >&2
    fi
  fi

  echo "$issue_number"
}

jira_create_issue() {
  local project="$1" type="$2" summary="$3" description="$4" parent_key="${5:-}"
  local auth
  auth=$(printf '%s:%s' "$JIRA_EMAIL" "$JIRA_API_TOKEN" | base64 -w0)

  # Build ADF description: split on double-newlines into paragraphs,
  # single newlines become hardBreak nodes within a paragraph.
  local adf_desc
  adf_desc=$(python3 -c "
import json, sys

text = sys.argv[1]
paragraphs = text.split('\n\n')
content = []
for para in paragraphs:
    para = para.strip()
    if not para:
        continue
    # Split single newlines into text + hardBreak sequences
    lines = para.split('\n')
    inline_content = []
    for i, line in enumerate(lines):
        if line.strip():
            inline_content.append({'type': 'text', 'text': line})
        if i < len(lines) - 1:
            inline_content.append({'type': 'hardBreak'})
    if inline_content:
        content.append({'type': 'paragraph', 'content': inline_content})

doc = {'type': 'doc', 'version': 1, 'content': content if content else [{'type': 'paragraph', 'content': [{'type': 'text', 'text': ' '}]}]}
print(json.dumps(doc))
" "$description")

  local payload
  payload=$(jq -n \
    --arg proj "$project" \
    --arg type "$type" \
    --arg summary "$summary" \
    --argjson desc "$adf_desc" \
    --arg parent "$parent_key" \
    '{
      fields: ({
        project: {key: $proj},
        issuetype: {name: $type},
        summary: $summary,
        description: $desc
      } + (if $parent != "" then {parent: {key: $parent}} else {} end))
    }')

  local response http_code
  response=$(curl -sS -w "\n%{http_code}" -X POST \
    -H "Authorization: Basic $auth" \
    -H "Content-Type: application/json" \
    -d "$payload" \
    "https://${JIRA_HOST}/rest/api/3/issue")

  http_code=$(echo "$response" | tail -1)
  local body
  body=$(echo "$response" | sed '$d')

  if [[ "$http_code" -ge 400 ]]; then
    echo "::warning::Jira API returned ${http_code} creating '${summary}' (type: ${type}, parent: ${parent_key}): ${body}" >&2
    # If parent hierarchy fails, retry without parent
    if [[ -n "$parent_key" && "$http_code" == "400" ]]; then
      echo "  Retrying without parent..." >&2
      payload=$(jq -n \
        --arg proj "$project" \
        --arg type "$type" \
        --arg summary "$summary" \
        --argjson desc "$adf_desc" \
        '{
          fields: {
            project: {key: $proj},
            issuetype: {name: $type},
            summary: $summary,
            description: $desc
          }
        }')
      response=$(curl -sS -w "\n%{http_code}" -X POST \
        -H "Authorization: Basic $auth" \
        -H "Content-Type: application/json" \
        -d "$payload" \
        "https://${JIRA_HOST}/rest/api/3/issue")
      http_code=$(echo "$response" | tail -1)
      body=$(echo "$response" | sed '$d')
      if [[ "$http_code" -ge 400 ]]; then
        echo "::warning::Retry without parent also failed (${http_code}): ${body}" >&2
        echo ""
        return 0
      fi
    else
      echo ""
      return 0
    fi
  fi

  echo "$body" | jq -r '.key'
}

resolve_jira_type() {
  local requested_type="$1"
  local available_types="${2:-}"

  if [[ -z "$available_types" || "$available_types" == "[]" ]]; then
    case "${requested_type,,}" in
      feature) echo "Feature" ;;
      epic)    echo "Epic" ;;
      story)   echo "Story" ;;
      task)    echo "Task" ;;
      spike)   echo "Task" ;;
      bug)     echo "Bug" ;;
      *)       echo "Story" ;;
    esac
    return
  fi

  local match
  match=$(echo "$available_types" | jq -r --arg t "$requested_type" \
    '[.[].name] | map(select(ascii_downcase == ($t | ascii_downcase))) | .[0] // empty')

  if [[ -n "$match" ]]; then
    echo "$match"
    return
  fi

  local fallback
  fallback=$(echo "$available_types" | jq -r '
    [.[] | select(.subtask != true) | .name] |
    if any(. == "Story") then "Story"
    elif any(. == "Task") then "Task"
    elif any(. == "Bug") then "Bug"
    else .[0] // "Story"
    end')

  echo "$fallback"
}

# Load available issue types from issue context if present
AVAILABLE_TYPES="[]"
ISSUE_CONTEXT_FILE="/tmp/workspace/issue-context.json"
if [[ -f "$ISSUE_CONTEXT_FILE" ]]; then
  AVAILABLE_TYPES=$(jq -c '.project.available_issue_types // []' "$ISSUE_CONTEXT_FILE")
fi

# --- Create children in topological order ---

pe_start "create-children" "create-children"

CHILD_COUNT=$(jq '.children | length' "${RESULT_FILE}")
echo "Creating ${CHILD_COUNT} child issue(s) with hierarchy..."

declare -A TITLE_TO_KEY
CREATED_KEYS=()
CREATED_COUNT=0
MAX_PASSES=5
PASS=0

declare -A CREATED_IDX

while [[ $CREATED_COUNT -lt $CHILD_COUNT && $PASS -lt $MAX_PASSES ]]; do
  PASS=$((PASS + 1))
  PROGRESS=false

  for i in $(seq 0 $((CHILD_COUNT - 1))); do
    if [[ -n "${CREATED_IDX[$i]:-}" ]]; then continue; fi

    CHILD_TITLE=$(jq -r ".children[${i}].title" "${RESULT_FILE}")
    CHILD_PARENT_TITLE=$(jq -r ".children[${i}].parent_title // \"\"" "${RESULT_FILE}")
    CHILD_TYPE=$(jq -r ".children[${i}].type" "${RESULT_FILE}")
    CHILD_DESC=$(jq -r ".children[${i}].description" "${RESULT_FILE}")
    CHILD_AC=$(jq -r ".children[${i}].acceptance_criteria | map(\"- [ ] \" + .) | join(\"\n\")" "${RESULT_FILE}")
    CHILD_LABELS=$(jq -c ".children[${i}].labels // []" "${RESULT_FILE}")
    CHILD_PRIORITY=$(jq -r ".children[${i}].priority // \"medium\"" "${RESULT_FILE}")
    CHILD_SCOPE=$(jq -r ".children[${i}].estimated_scope // \"M\"" "${RESULT_FILE}")

    PARENT_KEY_FOR_CHILD=""
    if [[ -z "$CHILD_PARENT_TITLE" || "$CHILD_PARENT_TITLE" == "null" ]]; then
      PARENT_KEY_FOR_CHILD="$ISSUE_KEY"
    elif [[ -n "${TITLE_TO_KEY[$CHILD_PARENT_TITLE]:-}" ]]; then
      PARENT_KEY_FOR_CHILD="${TITLE_TO_KEY[$CHILD_PARENT_TITLE]}"
    else
      continue
    fi

    FULL_BODY="${CHILD_DESC}

## Acceptance Criteria

${CHILD_AC}

---
*Priority: ${CHILD_PRIORITY} | Scope: ${CHILD_SCOPE} | Generated by fullsend refine agent*"

    if $USE_GITHUB; then
      TYPE_LABEL="$CHILD_TYPE"
      COMBINED_LABELS=$(echo "$CHILD_LABELS" | jq --arg t "$TYPE_LABEL" '. + [$t]')
      NEW_ISSUE=$(github_create_issue "${REPO_FULL_NAME}" "$CHILD_TITLE" "$FULL_BODY" "$COMBINED_LABELS" "$PARENT_KEY_FOR_CHILD")
      if [[ -z "$NEW_ISSUE" || "$NEW_ISSUE" == "FAILED" ]]; then
        echo "  [pass ${PASS}] FAILED to create ${CHILD_TYPE}: ${CHILD_TITLE}"
        continue
      fi
      echo "  [pass ${PASS}] Created ${CHILD_TYPE} #${NEW_ISSUE} under #${PARENT_KEY_FOR_CHILD}"
      TITLE_TO_KEY["$CHILD_TITLE"]="$NEW_ISSUE"
      CREATED_KEYS+=("#$NEW_ISSUE")
    else
      PROJECT_KEY=$(echo "$ISSUE_KEY" | sed 's/-.*//')
      JIRA_TYPE=$(resolve_jira_type "$CHILD_TYPE" "$AVAILABLE_TYPES")
      NEW_KEY=$(jira_create_issue "$PROJECT_KEY" "$JIRA_TYPE" "$CHILD_TITLE" "$FULL_BODY" "$PARENT_KEY_FOR_CHILD")
      if [[ -z "$NEW_KEY" ]]; then
        echo "  [pass ${PASS}] FAILED to create ${JIRA_TYPE}: ${CHILD_TITLE}"
        continue
      fi
      echo "  [pass ${PASS}] Created ${JIRA_TYPE} ${NEW_KEY} under ${PARENT_KEY_FOR_CHILD} (requested: ${CHILD_TYPE})"
      TITLE_TO_KEY["$CHILD_TITLE"]="$NEW_KEY"
      CREATED_KEYS+=("$NEW_KEY")
    fi

    CREATED_IDX[$i]=1
    CREATED_COUNT=$((CREATED_COUNT + 1))
    PROGRESS=true
  done

  if ! $PROGRESS; then
    echo "::warning::Pass ${PASS} made no progress — $((CHILD_COUNT - CREATED_COUNT)) items have unresolvable parent_title references"
    break
  fi
done

# Orphans fall back to root parent
if [[ $CREATED_COUNT -lt $CHILD_COUNT ]]; then
  echo "::warning::Creating remaining orphaned items under root issue"
  for i in $(seq 0 $((CHILD_COUNT - 1))); do
    if [[ -n "${CREATED_IDX[$i]:-}" ]]; then continue; fi

    CHILD_TITLE=$(jq -r ".children[${i}].title" "${RESULT_FILE}")
    CHILD_TYPE=$(jq -r ".children[${i}].type" "${RESULT_FILE}")
    CHILD_DESC=$(jq -r ".children[${i}].description" "${RESULT_FILE}")
    CHILD_AC=$(jq -r ".children[${i}].acceptance_criteria | map(\"- [ ] \" + .) | join(\"\n\")" "${RESULT_FILE}")
    CHILD_LABELS=$(jq -c ".children[${i}].labels // []" "${RESULT_FILE}")
    CHILD_PRIORITY=$(jq -r ".children[${i}].priority // \"medium\"" "${RESULT_FILE}")
    CHILD_SCOPE=$(jq -r ".children[${i}].estimated_scope // \"M\"" "${RESULT_FILE}")

    FULL_BODY="${CHILD_DESC}

## Acceptance Criteria

${CHILD_AC}

---
*Priority: ${CHILD_PRIORITY} | Scope: ${CHILD_SCOPE} | Generated by fullsend refine agent*"

    if $USE_GITHUB; then
      TYPE_LABEL="$CHILD_TYPE"
      COMBINED_LABELS=$(echo "$CHILD_LABELS" | jq --arg t "$TYPE_LABEL" '. + [$t]')
      NEW_ISSUE=$(github_create_issue "${REPO_FULL_NAME}" "$CHILD_TITLE" "$FULL_BODY" "$COMBINED_LABELS" "$ISSUE_KEY")
      if [[ -z "$NEW_ISSUE" || "$NEW_ISSUE" == "FAILED" ]]; then
        echo "  [orphan] FAILED to create: ${CHILD_TITLE}"
        continue
      fi
      echo "  [orphan] Created #${NEW_ISSUE} under #${ISSUE_KEY}"
      CREATED_KEYS+=("#$NEW_ISSUE")
    else
      PROJECT_KEY=$(echo "$ISSUE_KEY" | sed 's/-.*//')
      JIRA_TYPE=$(resolve_jira_type "$CHILD_TYPE" "$AVAILABLE_TYPES")
      NEW_KEY=$(jira_create_issue "$PROJECT_KEY" "$JIRA_TYPE" "$CHILD_TITLE" "$FULL_BODY" "$ISSUE_KEY")
      if [[ -z "$NEW_KEY" ]]; then
        echo "  [orphan] FAILED to create ${JIRA_TYPE}: ${CHILD_TITLE}"
        continue
      fi
      echo "  [orphan] Created ${JIRA_TYPE}: ${NEW_KEY} (requested: ${CHILD_TYPE})"
      CREATED_KEYS+=("$NEW_KEY")
    fi
  done
fi

pe_end "create-children" "create-children" "$(jq -nc --argjson total "$CHILD_COUNT" --argjson created "${#CREATED_KEYS[@]}" --argjson orphaned "$((CHILD_COUNT - CREATED_COUNT))" '{total:$total, created:$created, orphaned:$orphaned}')"

echo "::notice::Created ${#CREATED_KEYS[@]} child issue(s): ${CREATED_KEYS[*]}"

# Export for callers that need the result
export CREATED_CHILD_COUNT="${#CREATED_KEYS[@]}"
export CREATED_CHILD_KEYS="${CREATED_KEYS[*]}"
