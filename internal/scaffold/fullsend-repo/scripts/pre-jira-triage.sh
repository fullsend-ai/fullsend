#!/usr/bin/env bash
# pre-jira-triage.sh — Fetch Jira issue data and prepare context for the triage agent.
#
# Runs on the GitHub Actions host before the sandbox is created. Fetches issue
# data from Jira using credentials that never enter the sandbox.
#
# Required env vars:
#   ISSUE_KEY           — Jira issue key (e.g., PROJ-123)
#   JIRA_HOST           — Jira hostname (e.g., myorg.atlassian.net)
#   JIRA_EMAIL          — Jira user email
#   JIRA_API_TOKEN      — Jira API token
#
# Output: writes issue-context.json to /tmp/fullsend-jira-context-${ISSUE_KEY}.json
# The harness mounts this into the sandbox at /sandbox/workspace/issue-context.json.

set -euo pipefail

# --- Validate required env vars ---

if [[ ! "${ISSUE_KEY:-}" =~ ^[A-Z][A-Z0-9]+-[0-9]+$ ]]; then
  echo "ERROR: ISSUE_KEY does not match Jira key pattern (e.g., PROJECT-123): ${ISSUE_KEY:-<unset>}"
  exit 1
fi

if [[ -z "${JIRA_HOST:-}" ]]; then
  echo "ERROR: JIRA_HOST is not set"
  exit 1
fi

if [[ -z "${JIRA_EMAIL:-}" ]]; then
  echo "ERROR: JIRA_EMAIL is not set"
  exit 1
fi

if [[ -z "${JIRA_API_TOKEN:-}" ]]; then
  echo "ERROR: JIRA_API_TOKEN is not set"
  exit 1
fi

# FULLSEND_WORKSPACE is an in-sandbox path not available to pre-scripts.
# Write to a deterministic host temp path; the harness mounts it into the sandbox.
CONTEXT_FILE="/tmp/fullsend-jira-context-${ISSUE_KEY}.json"

JIRA_BASE="https://${JIRA_HOST}/rest/api/3"
# base64 -w0 is Linux-specific; macOS base64 wraps at 76 chars by default.
# Use -w0 when available, otherwise strip newlines with tr.
AUTH=$(printf '%s:%s' "$JIRA_EMAIL" "$JIRA_API_TOKEN" | base64 -w0 2>/dev/null \
  || printf '%s:%s' "$JIRA_EMAIL" "$JIRA_API_TOKEN" | base64 | tr -d '\n')

jira_get() {
  curl -sSf \
    -H "Authorization: Basic $AUTH" \
    -H "Accept: application/json" \
    "$1"
}

jira_put() {
  curl -sSf -X PUT \
    -H "Authorization: Basic $AUTH" \
    -H "Content-Type: application/json" \
    -d "$1" \
    "${JIRA_BASE}/issue/${ISSUE_KEY}"
}

# --- Step 1: Strip existing fullsend:* control labels ---

echo "::notice::Step 1: Stripping existing fullsend:* control labels from ${ISSUE_KEY}"

FULLSEND_CONTROL_LABELS=(
  "fullsend:needs-info"
  "fullsend:ready-to-code"
  "fullsend:duplicate"
  "fullsend:blocked"
  "fullsend:triaged"
  "fullsend:feature"
  "fullsend:question"
)

REMOVE_OPS="["
for i in "${!FULLSEND_CONTROL_LABELS[@]}"; do
  if [[ $i -gt 0 ]]; then
    REMOVE_OPS+=","
  fi
  REMOVE_OPS+="{\"remove\":\"${FULLSEND_CONTROL_LABELS[$i]}\"}"
done
REMOVE_OPS+="]"

jira_put "{\"update\":{\"labels\":${REMOVE_OPS}}}" || true

echo "Control label strip complete."

# --- Step 2: Fetch issue data ---

echo "::notice::Step 2: Fetching issue data for ${ISSUE_KEY}"

ISSUE_JSON=$(jira_get "${JIRA_BASE}/issue/${ISSUE_KEY}?expand=names")

SUMMARY=$(echo "$ISSUE_JSON" | jq -r '.fields.summary // ""')
DESCRIPTION=$(echo "$ISSUE_JSON" | jq -r '.fields.description // "" | if type == "object" then (.content // [] | map(.content // [] | map(.text // "") | join("")) | join("\n")) else . end')
STATUS=$(echo "$ISSUE_JSON" | jq -r '.fields.status.name // ""')
PRIORITY=$(echo "$ISSUE_JSON" | jq -r '.fields.priority.name // ""')
ISSUE_TYPE=$(echo "$ISSUE_JSON" | jq -r '.fields.issuetype.name // ""')
REPORTER=$(echo "$ISSUE_JSON" | jq -r '.fields.reporter.emailAddress // ""')
LABELS=$(echo "$ISSUE_JSON" | jq -c '.fields.labels // []')
CREATED=$(echo "$ISSUE_JSON" | jq -r '.fields.created // ""')
UPDATED=$(echo "$ISSUE_JSON" | jq -r '.fields.updated // ""')

# Determine level from issue type
LEVEL="issue"
case "${ISSUE_TYPE,,}" in
  outcome) LEVEL="outcome" ;;
  feature) LEVEL="feature" ;;
  epic)    LEVEL="epic" ;;
  story)   LEVEL="story" ;;
  task|sub-task) LEVEL="task" ;;
esac

echo "Issue: ${ISSUE_KEY} | Type: ${ISSUE_TYPE} | Status: ${STATUS} | Priority: ${PRIORITY}"

# Fetch parent
PARENT_KEY=$(echo "$ISSUE_JSON" | jq -r '.fields.parent.key // ""')
PARENT_JSON="null"
if [[ -n "$PARENT_KEY" ]]; then
  PARENT_ISSUE=$(jira_get "${JIRA_BASE}/issue/${PARENT_KEY}" 2>/dev/null || echo "{}")
  PARENT_SUMMARY=$(echo "$PARENT_ISSUE" | jq -r '.fields.summary // ""')
  PARENT_DESC=$(echo "$PARENT_ISSUE" | jq -r '.fields.description // "" | if type == "object" then (.content // [] | map(.content // [] | map(.text // "") | join("")) | join("\n")) else . end')
  PARENT_JSON=$(jq -n --arg k "$PARENT_KEY" --arg s "$PARENT_SUMMARY" --arg d "$PARENT_DESC" \
    '{"key": $k, "summary": $s, "description": $d}')
fi

# Fetch children
CHILDREN_JSON=$(jira_get "${JIRA_BASE}/search?jql=parent=${ISSUE_KEY}&fields=summary,status,issuetype&maxResults=50" 2>/dev/null \
  | jq '[.issues[] | {key: .key, summary: .fields.summary, status: .fields.status.name, type: .fields.issuetype.name}]' \
  || echo "[]")

CHILD_COUNT=$(echo "$CHILDREN_JSON" | jq 'length')
echo "Children: ${CHILD_COUNT}"

# Fetch comments
COMMENTS_JSON=$(jira_get "${JIRA_BASE}/issue/${ISSUE_KEY}/comment?maxResults=50" 2>/dev/null \
  | jq '[.comments[] | {author: .author.emailAddress, created: .created, body: (.body | if type == "object" then (.content // [] | map(.content // [] | map(.text // "") | join("")) | join("\n")) else . end)}]' \
  || echo "[]")

COMMENT_COUNT=$(echo "$COMMENTS_JSON" | jq 'length')
echo "Comments: ${COMMENT_COUNT}"

# Fetch linked issues and project metadata
LINKS_JSON=$(echo "$ISSUE_JSON" | jq '[.fields.issuelinks // [] | .[] | {
  type: (.type.outward // .type.name),
  key: (.outwardIssue.key // .inwardIssue.key),
  summary: (.outwardIssue.fields.summary // .inwardIssue.fields.summary),
  status: (.outwardIssue.fields.status.name // .inwardIssue.fields.status.name)
}]')

PROJECT_KEY=$(echo "$ISSUE_JSON" | jq -r '.fields.project.key')
PROJECT_NAME=$(echo "$ISSUE_JSON" | jq -r '.fields.project.name')

PROJECT_ISSUE_TYPES=$(jira_get "${JIRA_BASE}/project/${PROJECT_KEY}" 2>/dev/null \
  | jq '[.issueTypes[]? | {name: .name, subtask: .subtask, hierarchyLevel: .hierarchyLevel, description: .description}]' \
  || echo "[]")

if [[ "$PROJECT_ISSUE_TYPES" == "[]" || "$PROJECT_ISSUE_TYPES" == "null" ]]; then
  PROJECT_ISSUE_TYPES=$(jira_get "${JIRA_BASE}/issue/createmeta?projectKeys=${PROJECT_KEY}&expand=projects.issuetypes" 2>/dev/null \
    | jq '[.projects[0].issuetypes[]? | {name: .name, subtask: .subtask, hierarchyLevel: .hierarchyLevel, description: .description}]' \
    || echo "[]")
fi

TEAM_USAGE=$(jira_get "${JIRA_BASE}/search?jql=project=${PROJECT_KEY}+AND+issuetype+in+(Story,Task,Epic,Feature,Bug,Spike)+ORDER+BY+created+DESC&fields=issuetype,labels&maxResults=50" 2>/dev/null \
  | jq '{
      type_counts: [.issues[]? | .fields.issuetype.name] | group_by(.) | map({type: .[0], count: length}) | sort_by(-.count),
      common_labels: [.issues[]? | .fields.labels[]?] | group_by(.) | map({label: .[0], count: length}) | sort_by(-.count) | .[0:10]
    }' \
  || echo '{"type_counts": [], "common_labels": []}')

echo "::notice::Step 2 complete: issue=${ISSUE_KEY}, type=${ISSUE_TYPE}, level=${LEVEL}, status=${STATUS}"

# --- Step 3: JQL search for related (potentially duplicate) issues ---

echo "::notice::Step 3: Searching for related issues (potential duplicates)"

# Extract keywords from summary by stripping short words (≤3 chars)
KEYWORDS=$(echo "$SUMMARY" \
  | tr '[:upper:]' '[:lower:]' \
  | tr -cs 'a-z0-9' ' ' \
  | tr ' ' '\n' \
  | awk 'length > 3' \
  | head -5 \
  | tr '\n' ' ' \
  | sed 's/ *$//')

RELATED_ISSUES_JSON="[]"
if [[ -n "$KEYWORDS" ]]; then
  # Build JQL keyword expression: each word separated by AND for narrower results
  KEYWORD_JQL=$(echo "$KEYWORDS" | tr ' ' '\n' | awk '{printf "%s\"%s\"", (NR>1?" AND ":""), $0}')
  JQL="project=${PROJECT_KEY} AND summary ~ ${KEYWORD_JQL} AND status != Done AND issue != ${ISSUE_KEY} ORDER BY created DESC"
  ENCODED_JQL=$(printf '%s' "$JQL" | jq -sRr @uri)
  RELATED_ISSUES_JSON=$(jira_get "${JIRA_BASE}/search?jql=${ENCODED_JQL}&fields=summary,status,issuetype,priority&maxResults=20" 2>/dev/null \
    | jq '[.issues[] | {key: .key, summary: .fields.summary, status: .fields.status.name, type: .fields.issuetype.name, priority: .fields.priority.name}]' \
    || echo "[]")
fi

RELATED_COUNT=$(echo "$RELATED_ISSUES_JSON" | jq 'length')
echo "Related issues found: ${RELATED_COUNT}"
echo "::notice::Step 3 complete: ${RELATED_COUNT} related issues found"

# --- Step 4: Write issue-context.json ---

echo "::notice::Step 4: Writing issue context to ${CONTEXT_FILE}"

jq -n \
  --arg source "jira" \
  --arg host "$JIRA_HOST" \
  --arg key "$ISSUE_KEY" \
  --arg level "$LEVEL" \
  --arg summary "$SUMMARY" \
  --arg description "$DESCRIPTION" \
  --arg status "$STATUS" \
  --arg priority "$PRIORITY" \
  --arg issue_type "$ISSUE_TYPE" \
  --argjson labels "$LABELS" \
  --arg reporter "$REPORTER" \
  --arg created "$CREATED" \
  --arg updated "$UPDATED" \
  --argjson parent "$PARENT_JSON" \
  --argjson children "$CHILDREN_JSON" \
  --argjson comments "$COMMENTS_JSON" \
  --argjson linked_issues "$LINKS_JSON" \
  --arg project_key "$PROJECT_KEY" \
  --arg project_name "$PROJECT_NAME" \
  --argjson available_issue_types "$PROJECT_ISSUE_TYPES" \
  --argjson team_usage "$TEAM_USAGE" \
  --argjson related_issues "$RELATED_ISSUES_JSON" \
  '{
    source: $source,
    host: $host,
    key: $key,
    level: $level,
    summary: $summary,
    description: $description,
    status: $status,
    priority: $priority,
    issue_type: $issue_type,
    labels: $labels,
    reporter: $reporter,
    created: $created,
    updated: $updated,
    parent: $parent,
    children: $children,
    comments: $comments,
    linked_issues: $linked_issues,
    related_issues: $related_issues,
    project: {key: $project_key, name: $project_name, available_issue_types: $available_issue_types, team_usage: $team_usage}
  }' > "${CONTEXT_FILE}"

echo "Issue context written to ${CONTEXT_FILE}"
echo "::notice::Pre-jira-triage complete: ${ISSUE_KEY} (level=${LEVEL}, related=${RELATED_COUNT})"
