#!/usr/bin/env bash
# pre-explore.sh — Fetch issue data and prepare context for the explore agent.
#
# Runs on the host before the sandbox is created. Fetches issue data from
# Jira or GitHub using credentials that never enter the sandbox.
#
# Required env vars:
#   ISSUE_KEY        — Jira key (e.g., SECURESIGN-1620) or GitHub issue number
#   ISSUE_SOURCE     — "jira" or "github"
#   GH_TOKEN         — GitHub token
#
# Jira-only env vars:
#   JIRA_HOST        — Jira hostname (e.g., stage-redhat.atlassian.net)
#   JIRA_EMAIL       — Jira user email
#   JIRA_API_TOKEN   — Jira API token
#
# GitHub-only env vars:
#   REPO_FULL_NAME   — owner/repo (e.g., fullsend-ai/features)

set -euo pipefail

WORKSPACE="/tmp/workspace"
mkdir -p "$WORKSPACE"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/pipeline-events.sh"

pe_start "pre-explore" "pre-explore"

echo "::notice::Pre-explore: fetching issue data (source=${ISSUE_SOURCE}, key=${ISSUE_KEY})"

# --- Safety check: block private Jira on public repos ---
# The risk: private Jira data leaking into public GitHub Actions artifacts/logs.
# This only matters when the Jira project is private. Public Jira on a public
# repo is fine — the data is already public.
#
# JIRA_PROJECT_VISIBILITY controls this. Values:
#   "public"  — Jira project is publicly accessible, skip the check
#   "private" — Jira project is private, require a private config repo (default)
#   unset     — treated as "private" (safe default)
if [[ "${ISSUE_SOURCE}" == "jira" ]]; then
  JIRA_VIS="${JIRA_PROJECT_VISIBILITY:-private}"
  if [[ "$JIRA_VIS" != "public" ]]; then
    CONFIG_REPO="${GITHUB_REPOSITORY:-}"
    if [[ -n "$CONFIG_REPO" ]]; then
      REPO_VISIBILITY=$(gh api "repos/${CONFIG_REPO}" --jq '.visibility' 2>/dev/null || echo "unknown")
      if [[ "$REPO_VISIBILITY" == "public" ]]; then
        echo "::error::SECURITY: Private Jira source blocked — this repo (${CONFIG_REPO}) is public."
        echo "::error::Private Jira data would leak into public workflow artifacts and logs."
        echo "::error::Options:"
        echo "::error::  1. Run fullsend from a PRIVATE config repo"
        echo "::error::  2. Set JIRA_PROJECT_VISIBILITY=public if this Jira project is publicly accessible"
        echo "::error::  3. Use ISSUE_SOURCE=github instead"
        exit 1
      fi
    fi
  fi
fi

# Validate inputs to prevent injection
if [[ "${ISSUE_SOURCE}" != "jira" && "${ISSUE_SOURCE}" != "github" ]]; then
  echo "ERROR: ISSUE_SOURCE must be 'jira' or 'github', got: ${ISSUE_SOURCE}"
  exit 1
fi

if [[ "${ISSUE_SOURCE}" == "jira" && ! "${ISSUE_KEY}" =~ ^[A-Z][A-Z0-9]+-[0-9]+$ ]]; then
  echo "ERROR: ISSUE_KEY does not match Jira key pattern (e.g., PROJECT-123): ${ISSUE_KEY}"
  exit 1
fi

if [[ "${ISSUE_SOURCE}" == "github" && ! "${ISSUE_KEY}" =~ ^[0-9]+$ ]]; then
  echo "ERROR: ISSUE_KEY must be a numeric GitHub issue number, got: ${ISSUE_KEY}"
  exit 1
fi

if [[ "${ISSUE_SOURCE}" == "jira" ]]; then
  if [[ -z "${JIRA_HOST:-}" || -z "${JIRA_EMAIL:-}" || -z "${JIRA_API_TOKEN:-}" ]]; then
    echo "ERROR: Jira credentials not set (JIRA_HOST, JIRA_EMAIL, JIRA_API_TOKEN)"
    exit 1
  fi

  JIRA_BASE="https://${JIRA_HOST}/rest/api/3"
  AUTH=$(printf '%s:%s' "$JIRA_EMAIL" "$JIRA_API_TOKEN" | base64 -w0)

  # Preflight: verify Jira token is valid before doing any real work
  PREFLIGHT_HTTP=$(curl -sS -o /dev/null -w "%{http_code}" \
    -H "Authorization: Basic $AUTH" \
    "${JIRA_BASE}/myself" 2>/dev/null || echo "000")

  if [[ "$PREFLIGHT_HTTP" == "401" || "$PREFLIGHT_HTTP" == "403" ]]; then
    echo "::error::Jira API token is invalid or expired (HTTP ${PREFLIGHT_HTTP})."
    echo "::error::Update JIRA_API_TOKEN in your .fullsend config repo secrets."
    echo "::error::Pipeline halted — no Jira API calls will be attempted."
    exit 1
  elif [[ "$PREFLIGHT_HTTP" == "000" ]]; then
    echo "::warning::Could not reach Jira at ${JIRA_HOST} — continuing (may fail later)"
  fi

  jira_get() {
    curl -sSf -H "Authorization: Basic $AUTH" \
      -H "Accept: application/json" "$1"
  }

  pe_start "pre-explore" "fetch-issue"
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

  pe_end "pre-explore" "fetch-issue" "$(jq -nc --arg key "$ISSUE_KEY" --arg type "$ISSUE_TYPE" --arg level "$LEVEL" --arg status "$STATUS" '{key:$key, type:$type, level:$level, status:$status}')"

  pe_start "pre-explore" "fetch-parent"
  PARENT_KEY=$(echo "$ISSUE_JSON" | jq -r '.fields.parent.key // ""')
  PARENT_JSON="null"
  if [[ -n "$PARENT_KEY" ]]; then
    PARENT_ISSUE=$(jira_get "${JIRA_BASE}/issue/${PARENT_KEY}" 2>/dev/null || echo "{}")
    PARENT_SUMMARY=$(echo "$PARENT_ISSUE" | jq -r '.fields.summary // ""')
    PARENT_DESC=$(echo "$PARENT_ISSUE" | jq -r '.fields.description // "" | if type == "object" then (.content // [] | map(.content // [] | map(.text // "") | join("")) | join("\n")) else . end')
    PARENT_JSON=$(jq -n --arg k "$PARENT_KEY" --arg s "$PARENT_SUMMARY" --arg d "$PARENT_DESC" \
      '{"key": $k, "summary": $s, "description": $d}')
  fi

  pe_end "pre-explore" "fetch-parent" "$(jq -nc --arg parent_key "${PARENT_KEY:-none}" '{parent_key:$parent_key}')"

  pe_start "pre-explore" "fetch-children"
  CHILDREN_JSON=$(jira_get "${JIRA_BASE}/search?jql=parent=${ISSUE_KEY}&fields=summary,status,issuetype&maxResults=50" 2>/dev/null \
    | jq '[.issues[] | {key: .key, summary: .fields.summary, status: .fields.status.name, type: .fields.issuetype.name}]' \
    || echo "[]")

  CHILD_COUNT=$(echo "$CHILDREN_JSON" | jq 'length')
  pe_end "pre-explore" "fetch-children" "$(jq -nc --argjson count "$CHILD_COUNT" '{child_count:$count}')"

  pe_start "pre-explore" "fetch-comments"
  COMMENTS_JSON=$(jira_get "${JIRA_BASE}/issue/${ISSUE_KEY}/comment?maxResults=50" 2>/dev/null \
    | jq '[.comments[] | {author: .author.emailAddress, created: .created, body: (.body | if type == "object" then (.content // [] | map(.content // [] | map(.text // "") | join("")) | join("\n")) else . end)}]' \
    || echo "[]")

  COMMENT_COUNT=$(echo "$COMMENTS_JSON" | jq 'length')
  pe_end "pre-explore" "fetch-comments" "$(jq -nc --argjson count "$COMMENT_COUNT" '{comment_count:$count}')"

  pe_start "pre-explore" "fetch-links-and-project"
  LINKS_JSON=$(echo "$ISSUE_JSON" | jq '[.fields.issuelinks // [] | .[] | {
    type: (.type.outward // .type.name),
    key: (.outwardIssue.key // .inwardIssue.key),
    summary: (.outwardIssue.fields.summary // .inwardIssue.fields.summary),
    status: (.outwardIssue.fields.status.name // .inwardIssue.fields.status.name)
  }]')

  # Fetch project metadata
  PROJECT_KEY=$(echo "$ISSUE_JSON" | jq -r '.fields.project.key')
  PROJECT_NAME=$(echo "$ISSUE_JSON" | jq -r '.fields.project.name')

  # Fetch available issue types for the project
  PROJECT_ISSUE_TYPES=$(jira_get "${JIRA_BASE}/project/${PROJECT_KEY}" 2>/dev/null \
    | jq '[.issueTypes[]? | {name: .name, subtask: .subtask, hierarchyLevel: .hierarchyLevel, description: .description}]' \
    || echo "[]")

  # If project endpoint didn't return issue types, try createmeta
  if [[ "$PROJECT_ISSUE_TYPES" == "[]" || "$PROJECT_ISSUE_TYPES" == "null" ]]; then
    PROJECT_ISSUE_TYPES=$(jira_get "${JIRA_BASE}/issue/createmeta?projectKeys=${PROJECT_KEY}&expand=projects.issuetypes" 2>/dev/null \
      | jq '[.projects[0].issuetypes[]? | {name: .name, subtask: .subtask, hierarchyLevel: .hierarchyLevel, description: .description}]' \
      || echo "[]")
  fi

  echo "Available issue types for ${PROJECT_KEY}: $(echo "$PROJECT_ISSUE_TYPES" | jq -r '[.[].name] | join(", ")')"

  # Sample existing children to learn team conventions (type distribution)
  TEAM_USAGE=$(jira_get "${JIRA_BASE}/search?jql=project=${PROJECT_KEY}+AND+issuetype+in+(Story,Task,Epic,Feature,Bug,Spike)+ORDER+BY+created+DESC&fields=issuetype,labels&maxResults=50" 2>/dev/null \
    | jq '{
        type_counts: [.issues[]? | .fields.issuetype.name] | group_by(.) | map({type: .[0], count: length}) | sort_by(-.count),
        common_labels: [.issues[]? | .fields.labels[]?] | group_by(.) | map({label: .[0], count: length}) | sort_by(-.count) | .[0:10]
      }' \
    || echo '{"type_counts": [], "common_labels": []}')

  LINK_COUNT=$(echo "$LINKS_JSON" | jq 'length')
  TYPE_COUNT=$(echo "$PROJECT_ISSUE_TYPES" | jq 'length')
  pe_end "pre-explore" "fetch-links-and-project" "$(jq -nc --argjson links "$LINK_COUNT" --argjson types "$TYPE_COUNT" --arg proj "$PROJECT_KEY" '{link_count:$links, issue_type_count:$types, project:$proj}')"

  pe_start "pre-explore" "build-context"
  jq -n \
    --arg source "jira" \
    --arg host "$JIRA_HOST" \
    --arg key "$ISSUE_KEY" \
    --arg level "$LEVEL" \
    --arg summary "$SUMMARY" \
    --arg description "$DESCRIPTION" \
    --arg status "$STATUS" \
    --arg priority "$PRIORITY" \
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
    '{
      source: $source,
      host: $host,
      key: $key,
      level: $level,
      summary: $summary,
      description: $description,
      status: $status,
      priority: $priority,
      labels: $labels,
      reporter: $reporter,
      created: $created,
      updated: $updated,
      parent: $parent,
      children: $children,
      comments: $comments,
      linked_issues: $linked_issues,
      project: {key: $project_key, name: $project_name, available_issue_types: $available_issue_types, team_usage: $team_usage}
    }' > "$WORKSPACE/issue-context.json"

elif [[ "${ISSUE_SOURCE}" == "github" ]]; then
  if [[ -z "${REPO_FULL_NAME:-}" ]]; then
    echo "ERROR: REPO_FULL_NAME not set for GitHub source"
    exit 1
  fi

  pe_start "pre-explore" "fetch-issue"
  ISSUE_JSON=$(gh issue view "$ISSUE_KEY" --repo "$REPO_FULL_NAME" \
    --json number,title,body,labels,comments,state,milestone,assignees,createdAt,updatedAt,author)

  TITLE=$(echo "$ISSUE_JSON" | jq -r '.title')
  BODY=$(echo "$ISSUE_JSON" | jq -r '.body // ""')
  STATE=$(echo "$ISSUE_JSON" | jq -r '.state')
  LABELS=$(echo "$ISSUE_JSON" | jq -c '[.labels[].name]')
  AUTHOR=$(echo "$ISSUE_JSON" | jq -r '.author.login')
  CREATED=$(echo "$ISSUE_JSON" | jq -r '.createdAt')
  UPDATED=$(echo "$ISSUE_JSON" | jq -r '.updatedAt')

  # Determine level from labels
  LEVEL="issue"
  while IFS= read -r label; do
    case "${label,,}" in
      feature) LEVEL="feature" ;;
      epic)    LEVEL="epic" ;;
      story)   LEVEL="story" ;;
      task)    LEVEL="task" ;;
    esac
  done < <(echo "$LABELS" | jq -r '.[]')

  COMMENT_COUNT=$(echo "$ISSUE_JSON" | jq '.comments | length')
  pe_end "pre-explore" "fetch-issue" "$(jq -nc --arg key "#${ISSUE_KEY}" --arg level "$LEVEL" --arg status "$STATE" --argjson comments "$COMMENT_COUNT" '{key:$key, level:$level, status:$status, comment_count:$comments}')"

  pe_start "pre-explore" "fetch-comments"
  COMMENTS=$(echo "$ISSUE_JSON" | jq '[.comments[] | {author: .author.login, created: .createdAt, body: .body}]')
  pe_end "pre-explore" "fetch-comments" "$(jq -nc --argjson count "$COMMENT_COUNT" '{comment_count:$count}')"

  pe_start "pre-explore" "fetch-children"
  SUB_ISSUES=$(gh issue list --repo "$REPO_FULL_NAME" --state all \
    --search "parent:#${ISSUE_KEY}" --json number,title,state,labels --limit 30 2>/dev/null \
    | jq '[.[] | {key: ("#" + (.number | tostring)), summary: .title, status: .state, type: "issue"}]' \
    || echo "[]")
  CHILD_COUNT=$(echo "$SUB_ISSUES" | jq 'length')
  pe_end "pre-explore" "fetch-children" "$(jq -nc --argjson count "$CHILD_COUNT" '{child_count:$count}')"

  jq -n \
    --arg source "github" \
    --arg key "#${ISSUE_KEY}" \
    --arg level "$LEVEL" \
    --arg summary "$TITLE" \
    --arg description "$BODY" \
    --arg status "$STATE" \
    --argjson labels "$LABELS" \
    --arg reporter "$AUTHOR" \
    --arg created "$CREATED" \
    --arg updated "$UPDATED" \
    --argjson children "$SUB_ISSUES" \
    --argjson comments "$COMMENTS" \
    --arg repo "$REPO_FULL_NAME" \
    '{
      source: $source,
      key: $key,
      level: $level,
      summary: $summary,
      description: $description,
      status: $status,
      labels: $labels,
      reporter: $reporter,
      created: $created,
      updated: $updated,
      parent: null,
      children: $children,
      comments: $comments,
      linked_issues: [],
      project: {key: $repo, name: $repo}
    }' > "$WORKSPACE/issue-context.json"

else
  echo "ERROR: Unknown ISSUE_SOURCE: ${ISSUE_SOURCE}"
  exit 1
fi

pe_end "pre-explore" "build-context" '{}'

echo "Issue context written to $WORKSPACE/issue-context.json"
echo "::notice::Issue: ${ISSUE_KEY} (${ISSUE_SOURCE}, level=$(jq -r .level "$WORKSPACE/issue-context.json"))"

pe_end "pre-explore" "pre-explore" "$(jq -nc --arg source "$ISSUE_SOURCE" --arg key "$ISSUE_KEY" --arg level "$(jq -r .level "$WORKSPACE/issue-context.json")" '{source:$source, key:$key, level:$level}')"

# Export paths for the agent
echo "ISSUE_CONTEXT=$WORKSPACE/issue-context.json" >> "${GITHUB_ENV:-/dev/null}"
