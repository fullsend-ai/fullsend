#!/usr/bin/env bash
# Tests for label_actions support in review-result.schema.json
set -euo pipefail

SCHEMA="$(cd "$(dirname "$0")" && pwd)/review-result.schema.json"
FAILURES=0

fail() {
  echo "FAIL: $1"
  FAILURES=$((FAILURES + 1))
}

validate() {
  local desc="$1"
  local json="$2"
  local expect_pass="$3"

  if echo "${json}" | python3 -c "
import sys, json
from jsonschema import validate, ValidationError, Draft202012Validator
schema = json.load(open('${SCHEMA}'))
instance = json.load(sys.stdin)
Draft202012Validator(schema).validate(instance)
sys.exit(0)
" 2>/dev/null; then
    if [ "${expect_pass}" = "true" ]; then
      echo "PASS: ${desc}"
    else
      fail "${desc} (expected rejection but schema accepted it)"
    fi
  else
    if [ "${expect_pass}" = "false" ]; then
      echo "PASS: ${desc}"
    else
      fail "${desc} (expected acceptance but schema rejected it)"
    fi
  fi
}

# 1. approve without label_actions (baseline)
validate "approve-without-label-actions" '{
  "action": "approve",
  "pr_number": 42,
  "repo": "org/repo",
  "head_sha": "abc1234",
  "body": "Looks good to me."
}' true

# 2. approve with valid label_actions
validate "approve-with-label-actions" '{
  "action": "approve",
  "pr_number": 42,
  "repo": "org/repo",
  "head_sha": "abc1234",
  "body": "Looks good to me.",
  "label_actions": {
    "reason": "Approved PR, adding reviewed label",
    "actions": [
      { "action": "add", "label": "reviewed" }
    ]
  }
}' true

# 3. request-changes with label_actions
validate "request-changes-with-label-actions" '{
  "action": "request-changes",
  "pr_number": 42,
  "repo": "org/repo",
  "head_sha": "abc1234",
  "body": "Please fix the issues.",
  "findings": [
    {
      "severity": "high",
      "category": "security",
      "file": "main.go",
      "description": "SQL injection vulnerability",
      "verified_variables": [],
      "unchecked_variables": []
    }
  ],
  "label_actions": {
    "reason": "Security issue found, flagging for review",
    "actions": [
      { "action": "add", "label": "security" },
      { "action": "remove", "label": "needs-review" }
    ]
  }
}' true

# 4. failure with label_actions
validate "failure-with-label-actions" '{
  "action": "failure",
  "pr_number": 42,
  "repo": "org/repo",
  "reason": "tool-failure",
  "label_actions": {
    "reason": "Tool failure, marking for manual review",
    "actions": [
      { "action": "add", "label": "needs-manual-review" }
    ]
  }
}' true

# 5. label_actions missing reason — should fail
validate "label-actions-missing-reason" '{
  "action": "approve",
  "pr_number": 42,
  "repo": "org/repo",
  "head_sha": "abc1234",
  "body": "LGTM",
  "label_actions": {
    "actions": [
      { "action": "add", "label": "reviewed" }
    ]
  }
}' false

# 6. label_actions with empty actions array — should fail
validate "label-actions-empty-actions" '{
  "action": "approve",
  "pr_number": 42,
  "repo": "org/repo",
  "head_sha": "abc1234",
  "body": "LGTM",
  "label_actions": {
    "reason": "No labels to change",
    "actions": []
  }
}' false

# 7. label_actions with invalid action verb — should fail
validate "label-actions-invalid-verb" '{
  "action": "approve",
  "pr_number": 42,
  "repo": "org/repo",
  "head_sha": "abc1234",
  "body": "LGTM",
  "label_actions": {
    "reason": "Replace a label",
    "actions": [
      { "action": "replace", "label": "old-label" }
    ]
  }
}' false

# 8. label_actions with extra property — should fail
validate "label-actions-extra-property" '{
  "action": "approve",
  "pr_number": 42,
  "repo": "org/repo",
  "head_sha": "abc1234",
  "body": "LGTM",
  "label_actions": {
    "reason": "Adding label",
    "actions": [
      { "action": "add", "label": "reviewed" }
    ],
    "priority": "high"
  }
}' false

echo ""
if [ "${FAILURES}" -gt 0 ]; then
  echo "${FAILURES} test(s) failed."
  exit 1
else
  echo "All tests passed."
fi
