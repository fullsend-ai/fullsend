#!/usr/bin/env bash
# Post-script: push the agent's commit and create a PR.
#
# Runs on the GitHub Actions runner AFTER the sandbox is destroyed.
# This script has write access to the target repo — it is the most
# security-sensitive component in the pipeline.
#
# Security layers (defense-in-depth):
#   1. Authoritative secret scan — final gate before any push
#   2. Authoritative pre-commit — run repo hooks on changed files
#   3. Branch validation — refuse to push main/master
#   4. Token isolation — PUSH_TOKEN never enters the sandbox
#
# Pre-commit tool deps are auto-installed from .pre-commit-tools.yaml
# before step 2 to ensure hooks have the binaries they need.
#
# Protected-path enforcement lives in post-review.sh: the review agent
# cannot approve PRs that touch sensitive paths (e.g. .github/, CODEOWNERS,
# agents/). The code agent is free to propose changes to any path.
#
# Required environment variables:
#   PUSH_TOKEN        — token with contents:write + pull-requests:write on target repo
#                       (GitHub App installation token or PAT)
#   REPO_FULL_NAME    — owner/repo (e.g. my-org/my-repo)
#   ISSUE_NUMBER      — GitHub issue number
#   REPO_DIR          — path to extracted repo (default: current directory)
#
# Optional environment variables:
#   PUSH_TOKEN_SOURCE — "github-app" (for logging; default: unknown)
#   CODE_ALLOWED_TARGET_BRANCHES
#                     — comma-separated list of branches the agent may target,
#                       or "*" for any. When unset, only the repo's default
#                       branch is allowed. (default: auto-detected)
#
# Exit codes:
#   0  — branch pushed and PR created, OR agent determined nothing to do
#   1  — validation failure or error (nothing pushed)
set -euo pipefail

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------
GITLEAKS_VERSION="8.30.1"
GITLEAKS_SHA256="551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb"

# ---------------------------------------------------------------------------
# Setup
# ---------------------------------------------------------------------------
REPO_DIR="${REPO_DIR:-repo}"
RUN_DIR="$(pwd)"

if [ "${REPO_DIR}" != "." ]; then
  if [ ! -d "${REPO_DIR}" ]; then
    echo "::error::Extracted repo not found at ${REPO_DIR}" >&2
    exit 1
  fi
  cd "${REPO_DIR}"
fi

: "${PUSH_TOKEN:?PUSH_TOKEN is required}"
: "${REPO_FULL_NAME:?REPO_FULL_NAME is required}"
: "${ISSUE_NUMBER:?ISSUE_NUMBER is required}"
# ---------------------------------------------------------------------------
# Resolve target branch (ADR 0053)
#
# Priority: agent output > allowed-list validation > auto-detect default
# The agent writes its chosen branch to code-result.json. The post-script
# validates it against CODE_ALLOWED_TARGET_BRANCHES (comma-separated list
# or "*" for any). When unset, only the auto-detected default branch is
# allowed. Falls back to "main" if the API call fails.
# ---------------------------------------------------------------------------
AGENT_TARGET=""
RESULT_FILE=""
for dir in "${RUN_DIR}"/iteration-*/output; do
  if [ -f "${dir}/code-result.json" ]; then
    RESULT_FILE="${dir}/code-result.json"
  fi
done
if [ -n "${RESULT_FILE}" ]; then
  AGENT_TARGET="$(jq -r '.target_branch // empty' "${RESULT_FILE}" 2>/dev/null || true)"
fi
if [[ -n "${AGENT_TARGET}" && ! "${AGENT_TARGET}" =~ ^[a-zA-Z0-9._/-]+$ ]]; then
  echo "Error: invalid branch name from agent output: '${AGENT_TARGET}'"
  exit 1
fi

DEFAULT_BRANCH="$(GH_TOKEN="${PUSH_TOKEN}" gh api "repos/${REPO_FULL_NAME}" --jq '.default_branch' 2>/dev/null || echo 'main')"

if [ -n "${AGENT_TARGET}" ]; then
  ALLOWED="${CODE_ALLOWED_TARGET_BRANCHES:-${DEFAULT_BRANCH}}"
  if [ "${ALLOWED}" = "*" ] || echo ",${ALLOWED}," | grep -qF ",${AGENT_TARGET},"; then
    TARGET_BRANCH="${AGENT_TARGET}"
    echo "Agent requested branch '${TARGET_BRANCH}' — allowed"
  else
    echo "Error: agent requested branch '${AGENT_TARGET}' but allowed branches are: ${ALLOWED}"
    exit 1
  fi
else
  TARGET_BRANCH="${DEFAULT_BRANCH}"
  echo "No agent branch preference — using repo default: ${TARGET_BRANCH}"
fi

echo "::add-mask::${PUSH_TOKEN}"

# ---------------------------------------------------------------------------
# Error reporting — post a comment on the issue when the post-script fails.
#
# This ensures humans get feedback without checking workflow logs. The
# function is called from a trap on ERR. It is a best-effort operation:
# if the comment fails (e.g. token expired), we still exit non-zero.
# ---------------------------------------------------------------------------
report_failure_to_issue() {
  local exit_code=$?
  # Only report if we have the necessary context
  if [ -z "${GH_TOKEN:-}" ]; then
    export GH_TOKEN="${PUSH_TOKEN}"
  fi
  local run_url="${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_REPOSITORY:-${REPO_FULL_NAME}}/actions/runs/${GITHUB_RUN_ID:-unknown}"
  local comment_body="⚠️ **Post-code script failed** (exit code ${exit_code})

The code agent completed, but the post-code script failed while \
pushing the branch or creating the PR.

**Workflow run:** ${run_url}

Please check the workflow logs for details and retry with \`/fs-code\` \
if appropriate."

  echo "::warning::Posting failure comment to issue #${ISSUE_NUMBER}..."
  gh issue comment "${ISSUE_NUMBER}" \
    --repo "${REPO_FULL_NAME}" \
    --body "${comment_body}" 2>/dev/null || \
    echo "::warning::Failed to post error comment to issue #${ISSUE_NUMBER}"
}
trap report_failure_to_issue ERR

# ---------------------------------------------------------------------------
# 1. Verify feature branch
# ---------------------------------------------------------------------------
BRANCH="$(git branch --show-current)"

if [ -z "${BRANCH}" ] || [ "${BRANCH}" = "main" ] || [ "${BRANCH}" = "master" ]; then
  echo "::notice::Agent did not create a feature branch (current: '${BRANCH:-detached HEAD}') — nothing to do"
  exit 0
fi

echo "Branch: ${BRANCH}"
echo "Token source: ${PUSH_TOKEN_SOURCE:-unknown}"

# ---------------------------------------------------------------------------
# 2. Compute changed files
# ---------------------------------------------------------------------------
MERGE_BASE="$(git merge-base "origin/${TARGET_BRANCH}" HEAD 2>/dev/null)" || MERGE_BASE=""
if [ -n "${MERGE_BASE}" ]; then
  CHANGED_FILES="$(git diff --name-only "${MERGE_BASE}..HEAD")"
else
  echo "::warning::Could not determine merge-base — trying origin/${TARGET_BRANCH}..HEAD"
  CHANGED_FILES="$(git diff --name-only "origin/${TARGET_BRANCH}..HEAD" 2>/dev/null \
    || git diff --name-only HEAD~1..HEAD 2>/dev/null || true)"
fi

if [ -z "${CHANGED_FILES}" ]; then
  echo "::notice::No changed files in agent's commit(s) — nothing to do"
  exit 0
fi

echo "Changed files:"
echo "${CHANGED_FILES}" | sed 's/^/  /'

# ---------------------------------------------------------------------------
# 2b. Strip agent working directories (defense-in-depth)
#
# Agent working dirs (.agentready/, .fullsend-workspace/) should never
# appear in commits. The harness excludes them via .git/info/exclude, but
# if an agent manages to stage them anyway, strip them here before push.
# ---------------------------------------------------------------------------
AGENT_ARTIFACT_PATTERNS=".agentready/ .fullsend-workspace/"
STRIPPED_FILES=""
for file in ${CHANGED_FILES}; do
  is_artifact=false
  for pattern in ${AGENT_ARTIFACT_PATTERNS}; do
    dir="${pattern%/}"  # strip trailing slash for prefix matching
    case "${file}" in
      "${dir}"/*|"${dir}") is_artifact=true; break ;;
      */"${dir}"/*|*/"${dir}") is_artifact=true; break ;;
    esac
  done
  if [ "${is_artifact}" = "true" ]; then
    echo "::warning::Stripping agent artifact from commit: ${file}"
    STRIPPED_FILES="${STRIPPED_FILES} ${file}"
  fi
done

if [ -n "${STRIPPED_FILES}" ]; then
  echo "::warning::Agent committed working directory artifacts — stripping before push"
  # shellcheck disable=SC2086
  git rm --cached --quiet ${STRIPPED_FILES}
  git commit --amend --no-edit

  # Rebuild CHANGED_FILES without the stripped artifacts.
  CLEAN_FILES=""
  for file in ${CHANGED_FILES}; do
    is_stripped=false
    for sf in ${STRIPPED_FILES}; do
      if [ "${file}" = "${sf}" ]; then
        is_stripped=true
        break
      fi
    done
    if [ "${is_stripped}" = "false" ]; then
      CLEAN_FILES="${CLEAN_FILES}${CLEAN_FILES:+
}${file}"
    fi
  done
  CHANGED_FILES="${CLEAN_FILES}"

  if [ -z "${CHANGED_FILES}" ]; then
    echo "::notice::All changed files were agent artifacts — nothing to push"
    exit 0
  fi
fi

# ---------------------------------------------------------------------------
# 3. Authoritative secret scan
# ---------------------------------------------------------------------------
echo "Running authoritative secret scan on agent's commit..."

if ! command -v gitleaks >/dev/null 2>&1; then
  echo "Installing gitleaks v${GITLEAKS_VERSION}..."
  mkdir -p "${HOME}/.local/bin"
  curl -fsSL \
    "https://github.com/gitleaks/gitleaks/releases/download/v${GITLEAKS_VERSION}/gitleaks_${GITLEAKS_VERSION}_linux_x64.tar.gz" \
    -o /tmp/gitleaks.tar.gz \
    && echo "${GITLEAKS_SHA256}  /tmp/gitleaks.tar.gz" | sha256sum -c - \
    && tar xzf /tmp/gitleaks.tar.gz -C "${HOME}/.local/bin" gitleaks \
    && rm /tmp/gitleaks.tar.gz
  export PATH="${HOME}/.local/bin:${PATH}"
fi

if [ -n "${MERGE_BASE}" ]; then
  SCAN_RANGE="${MERGE_BASE}..HEAD"
else
  SCAN_RANGE="HEAD~1..HEAD"
fi

gitleaks detect --source . --log-opts="${SCAN_RANGE}" --redact
echo "Secret scan passed — no leaks in agent's commit(s)"

# ---------------------------------------------------------------------------
# 3b. Reject Signed-off-by trailers
#
# Agents must never produce Signed-off-by trailers. DCO is a human
# attestation — the DCO app already waives the check for bot authors.
# The bot noreply email makes the trailer ~90 characters, which causes
# gitlint body-max-line-length failures in repos with a 72-char limit.
# ---------------------------------------------------------------------------
echo "Checking for Signed-off-by trailers in agent's commit(s)..."
if git log --format='%b' "${SCAN_RANGE}" | grep -q '^Signed-off-by:'; then
  echo "::error::BLOCKED — agent commit contains a Signed-off-by trailer" >&2
  echo "::error::Agents must not use 'git commit -s' or append Signed-off-by trailers." >&2
  echo "::error::DCO is a human attestation; the DCO app waives the check for bots." >&2
  exit 1
fi
echo "Signed-off-by scan passed — no trailers in agent's commit(s)"

# ---------------------------------------------------------------------------
# 4. Auto-install pre-commit tool dependencies
# ---------------------------------------------------------------------------
SCRIPT_DIR_POST="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RESOLVE_SCRIPT="${SCRIPT_DIR_POST}/resolve-precommit-tools.py"
INSTALL_SCRIPT="${SCRIPT_DIR_POST}/install-precommit-tools.sh"

if [ -f .pre-commit-config.yaml ] \
   && [ -f "${RESOLVE_SCRIPT}" ] \
   && [ -f "${INSTALL_SCRIPT}" ]; then
  MANIFEST="$(mktemp)"
  LOCAL_REG="$(mktemp)"
  RESOLVE_ARGS=(".")
  if git show "origin/${TARGET_BRANCH}:.pre-commit-tools.yaml" > "${LOCAL_REG}" 2>/dev/null; then
    RESOLVE_ARGS+=("--local-registry" "${LOCAL_REG}")
  fi
  if python3 "${RESOLVE_SCRIPT}" "${RESOLVE_ARGS[@]}" > "${MANIFEST}"; then
    if [ -s "${MANIFEST}" ] && jq -e '.tools | length > 0' "${MANIFEST}" >/dev/null 2>&1; then
      bash "${INSTALL_SCRIPT}" "${MANIFEST}"
    fi
  else
    echo "::warning::Pre-commit tool resolution failed — continuing without auto-install"
  fi
  rm -f "${MANIFEST}" "${LOCAL_REG}"
fi
export PATH="${HOME}/.local/bin:${PATH}"

# ---------------------------------------------------------------------------
# 5. Authoritative pre-commit check
# ---------------------------------------------------------------------------
if [ -f .pre-commit-config.yaml ]; then
  echo "Running authoritative pre-commit on agent's changed files..."

  if ! command -v pre-commit >/dev/null 2>&1; then
    echo "Installing pre-commit..."
    pip install "pre-commit==4.5.1" 2>/dev/null \
      || pip3 install "pre-commit==4.5.1" 2>/dev/null \
      || pipx install "pre-commit==4.5.1" 2>/dev/null \
      || echo "::warning::Failed to install pre-commit"
  fi

  if command -v pre-commit >/dev/null 2>&1; then
    mapfile -t changed_array <<< "${CHANGED_FILES}"
    # SYNC: parallel retry block in post-fix.sh section 3 — keep structure
    #       in sync (variable names differ: CHANGED_FILES here vs
    #       BRANCH_CHANGED_FILES there; SCAN_RANGE scopes differ by design).
    if pre-commit run --files "${changed_array[@]}"; then
      echo "Pre-commit passed — all hooks clean"
    else
      # Single retry only — do not convert to a loop without adding a cap.
      # Scope detection/staging to changed_array so hooks can't inject files
      # outside the pre-commit scope into the commit.
      if git diff --name-only -- "${changed_array[@]}" | grep -q .; then
        echo "::warning::Pre-commit hooks auto-fixed files — re-staging and retrying"
        echo "Auto-fixed files:"
        git diff --name-only -- "${changed_array[@]}" | sed 's/^/  /'
        git diff --name-only -z -- "${changed_array[@]}" | xargs -0 -r git add --
        git commit --amend --no-edit

        echo "Re-running secret scan on amended commit..."
        if ! gitleaks detect --source . --log-opts="${SCAN_RANGE}" --redact; then
          echo "::error::BLOCKED — secret detected in amended commit after auto-fix" >&2
          exit 1
        fi
        if git log --format='%b' "${SCAN_RANGE}" | grep -q '^Signed-off-by:'; then
          echo "::error::BLOCKED — amended commit contains a Signed-off-by trailer" >&2
          exit 1
        fi

        if [ -n "${MERGE_BASE}" ]; then
          CHANGED_FILES="$(git diff --name-only "${MERGE_BASE}..HEAD")"
        else
          CHANGED_FILES="$(git diff --name-only "origin/${TARGET_BRANCH}..HEAD" 2>/dev/null \
            || git diff --name-only HEAD~1..HEAD 2>/dev/null || true)"
        fi
        if [ -z "${CHANGED_FILES}" ]; then
          echo "::error::BLOCKED — pre-commit hooks removed all changes; commit is now empty" >&2
          exit 1
        fi
        mapfile -t changed_array <<< "${CHANGED_FILES}"
        if pre-commit run --files "${changed_array[@]}"; then
          if git diff --name-only -- "${changed_array[@]}" | grep -q .; then
            echo "::error::BLOCKED — retry pre-commit left additional unstaged changes" >&2
            echo "::error::Committed content would diverge from what pre-commit validated." >&2
            exit 1
          fi
          echo "Pre-commit passed after auto-fix re-stage"
        else
          echo "::error::BLOCKED — pre-commit hooks still fail after auto-fix" >&2
          echo "::error::The agent's code does not pass the repo's pre-commit hooks." >&2
          echo "::error::Fix the issues and re-run, or update the pre-commit config." >&2
          exit 1
        fi
      else
        echo "::error::BLOCKED — pre-commit hooks failed on agent's changes" >&2
        echo "::error::The agent's code does not pass the repo's pre-commit hooks." >&2
        echo "::error::Fix the issues and re-run, or update the pre-commit config." >&2
        exit 1
      fi
    fi
  else
    echo "::warning::pre-commit not available on runner — skipping authoritative check"
    echo "::warning::CI pre-commit will still run on the PR"
  fi
else
  echo "No .pre-commit-config.yaml — skipping pre-commit check"
fi

# ---------------------------------------------------------------------------
# 6. Push branch
# ---------------------------------------------------------------------------
git remote set-url origin \
  "https://x-access-token:${PUSH_TOKEN}@github.com/${REPO_FULL_NAME}.git"

export GH_TOKEN="${PUSH_TOKEN}"

# ---------------------------------------------------------------------------
# 7a. Delete stale remote branch if it exists with no open PR.
#
# When a human closes a code agent PR and re-triggers /fs-code, the old
# remote branch still exists. A plain push will fail with non-fast-forward
# because the local branch was created fresh from origin/main. Delete the
# stale remote branch so the push succeeds.
# ---------------------------------------------------------------------------
REMOTE_REF="$(git ls-remote --heads origin "${BRANCH}" 2>/dev/null | head -1 || true)"
if [ -n "${REMOTE_REF}" ]; then
  echo "Remote branch ${BRANCH} already exists — checking for open PRs..."
  OPEN_PR="$(gh pr list --repo "${REPO_FULL_NAME}" --head "${BRANCH}" \
    --state open --json number --jq '.[0].number' 2>/dev/null || true)"
  if [ -z "${OPEN_PR}" ]; then
    echo "No open PR uses ${BRANCH} — deleting stale remote branch"
    git push origin --delete "${BRANCH}" 2>&1 || \
      echo "::warning::Failed to delete stale remote branch ${BRANCH}"
  else
    echo "Open PR #${OPEN_PR} uses ${BRANCH} — keeping remote branch"
  fi
fi

# ---------------------------------------------------------------------------
# 7b. Push, with --force-with-lease fallback for non-fast-forward errors.
# ---------------------------------------------------------------------------
echo "Pushing branch ${BRANCH}..."
PUSH_OUTPUT="$(git push -u origin -- "${BRANCH}" 2>&1)" && PUSH_RC=0 || PUSH_RC=$?
echo "${PUSH_OUTPUT}"

if [ "${PUSH_RC}" -ne 0 ]; then
  if echo "${PUSH_OUTPUT}" | grep -qi "non-fast-forward\|rejected\|fetch first"; then
    echo "::warning::Plain push failed (non-fast-forward) — retrying with --force-with-lease"
    if ! git push --force-with-lease -u origin -- "${BRANCH}" 2>&1; then
      echo "::error::Force-with-lease push also failed"
      exit 1
    fi
  else
    echo "::error::Push failed with unexpected error (git push origin ${BRANCH})" >&2
    echo "::error::Push output: ${PUSH_OUTPUT}" >&2
    exit 1
  fi
fi

# ---------------------------------------------------------------------------
# 8. Create PR
# ---------------------------------------------------------------------------

EXISTING_PR_NUM="$(gh pr list --repo "${REPO_FULL_NAME}" --head "${BRANCH}" \
  --json number --jq '.[0].number' 2>/dev/null || true)"

if [ -n "${EXISTING_PR_NUM}" ]; then
  EXISTING_PR_URL="$(gh pr list --repo "${REPO_FULL_NAME}" --head "${BRANCH}" \
    --json url --jq '.[0].url' 2>/dev/null || true)"
  echo "PR #${EXISTING_PR_NUM} already exists — branch updated with new commits"
  echo "PR: ${EXISTING_PR_URL}"
  echo "pr_url=${EXISTING_PR_URL}" >> "${GITHUB_OUTPUT:-/dev/null}"
  exit 0
fi

echo "Creating PR..."

COMMIT_SUBJECT="$(git log -1 --format='%s' HEAD)"
COMMIT_BODY_RAW="$(git log -1 --format='%b' HEAD | sed '/^Signed-off-by:/d' | sed '/^Closes #/d' | sed -e :a -e '/^\n*$/{ $d; N; ba; }')"

COMMIT_BODY="$(echo "${COMMIT_BODY_RAW}" | awk '
  /^$/           { if (buf) print buf; print; buf=""; next }
  /^[-*#>]|^  /  { if (buf) print buf; buf=""; print; next }
  /^Closes /     { if (buf) print buf; buf=""; print; next }
                 { buf = (buf ? buf " " $0 : $0) }
  END            { if (buf) print buf }
')"

# ---------------------------------------------------------------------------
# Ensure PR title includes an issue reference.
#
# Many repos enforce PR title conventions like "type(TICKET): description".
# The code agent may produce a plain "type: description" commit subject that
# omits the issue reference. When the title follows conventional commit format
# (word + colon), inject the issue number as a scope if no scope is present.
# ---------------------------------------------------------------------------
if echo "${COMMIT_SUBJECT}" | grep -qE '^[a-z]+\('; then
  # Already has a scope — e.g. "fix(#42): ..." or "feat(PROJ-123): ..."
  PR_TITLE="${COMMIT_SUBJECT}"
elif echo "${COMMIT_SUBJECT}" | grep -qE '^[a-z]+: '; then
  # Conventional commit without scope — inject issue reference
  PR_TITLE="$(echo "${COMMIT_SUBJECT}" | sed "s/^\([a-z]*\): /\1(#${ISSUE_NUMBER}): /")"
else
  # Non-conventional title — leave as-is
  PR_TITLE="${COMMIT_SUBJECT}"
fi

if [ -z "${COMMIT_BODY}" ]; then
  DESCRIPTION="Automated implementation for issue #${ISSUE_NUMBER}."
else
  DESCRIPTION="${COMMIT_BODY}"
fi

PR_BODY="${DESCRIPTION}

---

Closes #${ISSUE_NUMBER}

### Post-script verification

- [x] Branch is not main/master (\`${BRANCH}\`)
- [x] Secret scan passed (gitleaks — \`${SCAN_RANGE}\`)
- [x] Pre-commit hooks passed (authoritative run on runner)
- [x] Tests ran inside sandbox"

PR_CREATE_STDERR=$(mktemp)
if ! PR_URL=$(gh pr create \
  --repo "${REPO_FULL_NAME}" \
  --head "${BRANCH}" \
  --base "${TARGET_BRANCH}" \
  --title "${PR_TITLE}" \
  --body "${PR_BODY}" 2>"${PR_CREATE_STDERR}"); then
  echo "::error::Failed to create PR for ${REPO_FULL_NAME} (head: ${BRANCH}, base: ${TARGET_BRANCH})" >&2
  [ -s "${PR_CREATE_STDERR}" ] && cat "${PR_CREATE_STDERR}" >&2
  rm -f "${PR_CREATE_STDERR}"
  exit 1
fi
rm -f "${PR_CREATE_STDERR}"

echo "PR created: ${PR_URL}"
echo "pr_url=${PR_URL}" >> "${GITHUB_OUTPUT:-/dev/null}"

# Apply ready-for-review label so the review agent is dispatched via the
# issues.labeled path. The pull_request_target.opened event requires the PR
# author to pass is_event_actor_authorized, which fails for bot accounts
# (GitHub App bots have no collaborator role). The label-based path has no
# explicit auth gate — label application itself requires write access.
PR_NUMBER_FROM_URL="${PR_URL##*/}"
gh issue edit "${PR_NUMBER_FROM_URL}" \
  --repo "${REPO_FULL_NAME}" \
  --add-label "ready-for-review" 2>/dev/null || \
  echo "::warning::Failed to apply ready-for-review label to PR #${PR_NUMBER_FROM_URL}"
