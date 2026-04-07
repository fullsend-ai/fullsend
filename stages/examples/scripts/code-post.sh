#!/usr/bin/env bash
set -euo pipefail
cd workspace

# Verify the code agent actually committed something
if [ "$(git log --oneline origin/main..HEAD | wc -l)" -eq 0 ]; then
  echo "Code agent produced no commits. Skipping PR creation."
  exit 0
fi

# At this point both agents have run:
# 1. Code agent committed its fix
# 2. Review agent evaluated the branch locally
# If the review agent flagged issues, it would have exited non-zero
# and the stage would have stopped before reaching this post-script.

# Push the branch
git push origin "${AGENT_BRANCH}"

# Create or update the PR
EXISTING_PR=$(gh pr list --head "${AGENT_BRANCH}" --json number --jq '.[0].number // empty')
if [ -n "${EXISTING_PR}" ]; then
  echo "Updating existing PR #${EXISTING_PR}"
else
  gh pr create \
    --title "fix: ${ISSUE_TITLE}" \
    --body "Closes #${ISSUE_NUMBER}" \
    --head "${AGENT_BRANCH}" \
    --base main
fi

# Label transition — code stage is done, PR is ready for CI + review
gh issue edit "${ISSUE_NUMBER}" --remove-label "ready-to-code"
