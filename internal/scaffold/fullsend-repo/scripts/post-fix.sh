#!/usr/bin/env bash
# Post-script: push the fix agent's commit and process structured output.
#
# Runs on the GitHub Actions runner AFTER the sandbox is destroyed.
# This script has write access to the target repo — it is the most
# security-sensitive component in the fix pipeline.
#
# Security layers (defense-in-depth):
#   - Protected-path check — reject if agent touched forbidden paths
#   - Authoritative secret scan — final gate before any push
#   - Authoritative pre-commit — run repo hooks on changed files
#   - Branch validation — refuse to push main/master
#   - Token isolation — PUSH_TOKEN never enters the sandbox
#
# Steps:
#   0. Check for agent commits
#   1. Protected-path check
#   2. Authoritative secret scan
#   3. Install lychee
#   4. Install uv and uvx
#   5. Authoritative pre-commit check
#   6. Push branch
#   7. Process structured output
#   8. Iteration-cap warning label
#   9. Summary
#
# After pushing, this script processes fix-result.json to:
#   - Post a summary comment on the PR documenting fixes and disagreements
#   - Apply labels (needs-human) if the iteration cap is approaching
#
# Required environment variables:
#   PUSH_TOKEN        — token with contents:write + pull-requests:write
#   REPO_FULL_NAME    — owner/repo
#   PR_NUMBER         — PR number
#   REPO_DIR          — path to extracted repo (default: current directory)
#   TRIGGER_SOURCE    — GitHub username that triggered the fix (usernames ending in [bot] are bot triggers)
#
# Optional environment variables:
#   FIX_ITERATION     — current iteration count
#   ITERATION_CAP     — max iterations (default: 5)
#   PUSH_TOKEN_SOURCE — "github-app" (for logging)
#
# Exit codes:
#   0  — branch pushed, PR updated
#   1  — validation failure or error (nothing pushed)
set -euo pipefail

# ---------------------------------------------------------------------------
# Helper: Bot user detection
# ---------------------------------------------------------------------------
is_bot_user() {
  [[ "${1:-}" =~ \[bot\]$ ]]
}

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------
PROTECTED_PATHS=(
  ".github/"
  ".claude/"
  "agents/"
  "harness/"
  "policies/"
  "scripts/"
  "api-servers/"
  "CODEOWNERS"
  ".pre-commit-config.yaml"
  ".gitattributes"
)

GITLEAKS_VERSION="8.30.1"
GITLEAKS_SHA256="551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb"
LYCHEE_VERSION="0.24.2"
LYCHEE_SHA256_AMD64="1f4e0ef7f6554a6ed33dd7ac144fb2e1bbed98598e7af973042fc5cd43951c9a"
LYCHEE_SHA256_ARM64="91a7bd65685da41b90ccb9bc867a3d649a7818042dae04ff405e55a25bddee4c"
UV_VERSION="0.11.14"
UV_SHA256="f3b623eb0e6141a7053d571d59a0bdc341e0f238ea8f5f0b4815ddbec9a2a296"

# ---------------------------------------------------------------------------
# Setup
# ---------------------------------------------------------------------------
REPO_DIR="${REPO_DIR:-repo}"
RUN_DIR="$(pwd)"

if [ "${REPO_DIR}" != "." ]; then
  if [ ! -d "${REPO_DIR}" ]; then
    echo "::error::Extracted repo not found at ${REPO_DIR}"
    exit 1
  fi
  cd "${REPO_DIR}"
fi

: "${PUSH_TOKEN:?PUSH_TOKEN is required}"
: "${REPO_FULL_NAME:?REPO_FULL_NAME is required}"
: "${PR_NUMBER:?PR_NUMBER is required}"
: "${TRIGGER_SOURCE:?TRIGGER_SOURCE is required}"
TARGET_BRANCH="${TARGET_BRANCH:-main}"

echo "::add-mask::${PUSH_TOKEN}"

# ---------------------------------------------------------------------------
# 0. Check for agent commits
# ---------------------------------------------------------------------------
BRANCH="$(git branch --show-current)"

if [ -z "${BRANCH}" ] || [ "${BRANCH}" = "main" ] || [ "${BRANCH}" = "master" ]; then
  echo "::warning::Agent did not produce a commit on a feature branch (current: '${BRANCH:-detached HEAD}')"
  echo "::warning::Processing structured output only (no push)."
  # Still process fix-result.json to post a summary comment.
  NO_PUSH=true
else
  NO_PUSH=false
fi

# Scope to the agent's commit(s) only — not the entire branch. PRE_AGENT_HEAD
# is set by fix.yml to the HEAD SHA before the harness runs, so this diff
# captures every commit the agent made (including validation_loop retries).
# Falls back to HEAD~1 if PRE_AGENT_HEAD is unset (shouldn't happen in CI).
DIFF_BASE="${PRE_AGENT_HEAD:-$(git rev-parse HEAD~1 2>/dev/null || echo HEAD)}"
CHANGED_FILES="$(git diff --name-only "${DIFF_BASE}..HEAD" 2>/dev/null || true)"

if [ -z "${CHANGED_FILES}" ] && [ "${NO_PUSH}" = "false" ]; then
  echo "::warning::No changed files in agent's commit(s) — nothing to push"
  NO_PUSH=true
fi

# ---------------------------------------------------------------------------
# 1. Protected-path check (only if pushing)
# ---------------------------------------------------------------------------
if [ "${NO_PUSH}" = "false" ]; then
  echo "Changed files:"
  echo "${CHANGED_FILES}" | sed 's/^/  /'

  while IFS= read -r file; do
    [ -z "${file}" ] && continue
    for pattern in "${PROTECTED_PATHS[@]}"; do
      if [[ "${file}" == ${pattern}* ]]; then
        echo "::error::BLOCKED — agent modified protected path: ${pattern}"
        echo "::error::  ${file}"
        exit 1
      fi
    done
  done <<< "${CHANGED_FILES}"

  echo "Protected-path check passed"
fi

# ---------------------------------------------------------------------------
# 2. Authoritative secret scan (only if pushing)
# ---------------------------------------------------------------------------
if [ "${NO_PUSH}" = "false" ]; then
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

  SCAN_RANGE="${DIFF_BASE}..HEAD"

  gitleaks detect --source . --log-opts="${SCAN_RANGE}" --redact
  echo "Secret scan passed — no leaks in agent's commit(s)"
fi

# ---------------------------------------------------------------------------
# 3. Install lychee (for pre-commit markdown link checking)
# ---------------------------------------------------------------------------
if ! command -v lychee >/dev/null 2>&1; then
  echo "Installing lychee v${LYCHEE_VERSION}..."
  mkdir -p "${HOME}/.local/bin"
  case "$(uname -m)" in
    x86_64)  LY_TRIPLE="x86_64-unknown-linux-gnu";  LY_SHA="${LYCHEE_SHA256_AMD64}" ;;
    aarch64) LY_TRIPLE="aarch64-unknown-linux-gnu"; LY_SHA="${LYCHEE_SHA256_ARM64}" ;;
    *) echo "::error::Unsupported architecture for lychee: $(uname -m)"; exit 1 ;;
  esac
  curl -fsSL \
    "https://github.com/lycheeverse/lychee/releases/download/lychee-v${LYCHEE_VERSION}/lychee-${LY_TRIPLE}.tar.gz" \
    -o /tmp/lychee.tar.gz \
    && echo "${LY_SHA}  /tmp/lychee.tar.gz" | sha256sum -c - \
    && tar xzf /tmp/lychee.tar.gz -C /tmp \
    && mv "/tmp/lychee-${LY_TRIPLE}/lychee" "${HOME}/.local/bin/" \
    && rm -rf /tmp/lychee.tar.gz "/tmp/lychee-${LY_TRIPLE}"
  export PATH="${HOME}/.local/bin:${PATH}"
fi

# ---------------------------------------------------------------------------
# 4. Install uv and uvx (for pre-commit Python tooling)
# ---------------------------------------------------------------------------
if ! command -v uvx >/dev/null 2>&1; then
  echo "Installing uv v${UV_VERSION} (includes uvx)..."
  mkdir -p "${HOME}/.local/bin"
  curl -fsSL \
    "https://github.com/astral-sh/uv/releases/download/${UV_VERSION}/uv-x86_64-unknown-linux-gnu.tar.gz" \
    -o /tmp/uv.tar.gz \
    && echo "${UV_SHA256}  /tmp/uv.tar.gz" | sha256sum -c - \
    && tar xzf /tmp/uv.tar.gz -C /tmp \
    && mv /tmp/uv-x86_64-unknown-linux-gnu/uv "${HOME}/.local/bin/" \
    && mv /tmp/uv-x86_64-unknown-linux-gnu/uvx "${HOME}/.local/bin/" \
    && rm -rf /tmp/uv.tar.gz /tmp/uv-x86_64-unknown-linux-gnu
  export PATH="${HOME}/.local/bin:${PATH}"
fi

# ---------------------------------------------------------------------------
# 5. Authoritative pre-commit check (only if pushing)
# ---------------------------------------------------------------------------
if [ "${NO_PUSH}" = "false" ] && [ -f .pre-commit-config.yaml ]; then
  echo "Running authoritative pre-commit on agent's changed files..."

  if ! command -v pre-commit >/dev/null 2>&1; then
    pip install "pre-commit==4.5.1" 2>/dev/null \
      || pip3 install "pre-commit==4.5.1" 2>/dev/null \
      || pipx install "pre-commit==4.5.1" 2>/dev/null \
      || echo "::warning::Failed to install pre-commit"
  fi

  if command -v pre-commit >/dev/null 2>&1; then
    mapfile -t changed_array <<< "${CHANGED_FILES}"
    if pre-commit run --files "${changed_array[@]}"; then
      echo "Pre-commit passed — all hooks clean"
    else
      echo "::error::BLOCKED — pre-commit hooks failed on agent's changes"
      exit 1
    fi
  else
    echo "::warning::pre-commit not available — skipping authoritative check"
  fi
fi

# ---------------------------------------------------------------------------
# 6. Push branch (only if we have commits)
# ---------------------------------------------------------------------------
if [ "${NO_PUSH}" = "false" ]; then
  git remote set-url origin \
    "https://x-access-token:${PUSH_TOKEN}@github.com/${REPO_FULL_NAME}.git"

  # Plain push (no --force-with-lease). Agents always create new
  # commits (amend is in disallowedTools), so force-push is unnecessary
  # and plain push is safer (refuses diverged branches).
  echo "Pushing branch ${BRANCH}..."
  git push -u origin -- "${BRANCH}" 2>&1
  echo "Branch ${BRANCH} pushed successfully"
fi

# ---------------------------------------------------------------------------
# 7. Process structured output (fix-result.json)
# ---------------------------------------------------------------------------
export GH_TOKEN="${PUSH_TOKEN}"

# Locate process-fix-result.py relative to this script.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROCESS_SCRIPT="${SCRIPT_DIR}/process-fix-result.py"

# Find fix-result.json in the output directory.
# RUN_DIR is the original cwd (runDir = <outputBase>/<sandboxName>), saved
# before we cd'd into REPO_DIR. The agent writes its structured output to
# iteration-<N>/output/fix-result.json within runDir. Uses glob order
# (naturally ascending iteration numbers) to find the last iteration,
# matching the pattern in post-triage.sh.
RESULT_FILE=""
for dir in "${RUN_DIR}"/iteration-*/output; do
  if [ -f "${dir}/fix-result.json" ]; then
    RESULT_FILE="${dir}/fix-result.json"
  fi
done

if [ -z "${RESULT_FILE}" ] || [ ! -f "${RESULT_FILE}" ]; then
  echo "::warning::No fix-result.json found — skipping summary comment"
elif [ ! -f "${PROCESS_SCRIPT}" ]; then
  echo "::warning::process-fix-result.py not found at ${PROCESS_SCRIPT} — skipping"
else
  # Scan fix-result.json for secrets before posting content as a PR comment.
  # The agent could have been tricked into embedding sensitive data in the
  # structured output via prompt injection in the review body.
  if command -v gitleaks >/dev/null 2>&1; then
    echo "Scanning fix-result.json for secrets before posting..."
    SCAN_DIR="$(mktemp -d)"
    cp "${RESULT_FILE}" "${SCAN_DIR}/fix-result.json"
    if ! gitleaks detect --source "${SCAN_DIR}" --no-git --redact 2>/dev/null; then
      echo "::error::Secret detected in fix-result.json — refusing to post PR comment"
      rm -rf "${SCAN_DIR}"
      exit 1
    fi
    rm -rf "${SCAN_DIR}"
  fi

  echo "Processing fix-result.json: ${RESULT_FILE}"
  PROCESS_EXIT=0
  python3 "${PROCESS_SCRIPT}" "${RESULT_FILE}" "${REPO_FULL_NAME}" "${PR_NUMBER}" || PROCESS_EXIT=$?
  if [ "${PROCESS_EXIT}" -eq 1 ]; then
    exit 1  # hard failure (bad input)
  elif [ "${PROCESS_EXIT}" -ne 0 ]; then
    echo "::warning::process-fix-result.py exited ${PROCESS_EXIT} — continuing with labels/summary"
  fi
fi

# ---------------------------------------------------------------------------
# 8. Iteration-cap warning label
# ---------------------------------------------------------------------------
ITERATION="${FIX_ITERATION:-1}"
BOT_CAP="${ITERATION_CAP:-5}"
WARN_THRESHOLD=$(( BOT_CAP - 1 ))

# The needs-human label is based on the bot cap — it signals that the
# autonomous review→fix loop needs human direction. Human-triggered /fix
# runs have a separate, higher cap (ITERATION_CAP_HUMAN).
if [ "${ITERATION}" -ge "${WARN_THRESHOLD}" ] && is_bot_user "${TRIGGER_SOURCE}"; then
  echo "::warning::Fix iteration ${ITERATION} is approaching bot cap of ${BOT_CAP}"
  gh label create "needs-human" --repo "${REPO_FULL_NAME}" \
    --description "Agent loop needs human intervention" --color "D93F0B" \
    2>/dev/null || true
  gh pr edit "${PR_NUMBER}" --repo "${REPO_FULL_NAME}" \
    --add-label "needs-human" 2>/dev/null || true
fi

# ---------------------------------------------------------------------------
# 9. Summary
# ---------------------------------------------------------------------------
echo ""
echo "Fix post-script complete:"
echo "  Branch: ${BRANCH:-none}"
echo "  PR: #${PR_NUMBER}"
if [ "${NO_PUSH}" = "true" ]; then echo "  Pushed: no"; else echo "  Pushed: yes"; fi
echo "  Trigger: ${TRIGGER_SOURCE}"
if is_bot_user "${TRIGGER_SOURCE}"; then
  echo "  Iteration: ${ITERATION} of ${BOT_CAP} (bot cap)"
else
  echo "  Iteration: ${ITERATION} of ${ITERATION_CAP_HUMAN:-10} (human cap, total across bot+human)"
fi
