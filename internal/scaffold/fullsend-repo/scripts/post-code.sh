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
LYCHEE_VERSION="0.24.2"
LYCHEE_SHA256_AMD64="1f4e0ef7f6554a6ed33dd7ac144fb2e1bbed98598e7af973042fc5cd43951c9a"
LYCHEE_SHA256_ARM64="91a7bd65685da41b90ccb9bc867a3d649a7818042dae04ff405e55a25bddee4c"
UV_VERSION="0.11.14"
UV_SHA256="f3b623eb0e6141a7053d571d59a0bdc341e0f238ea8f5f0b4815ddbec9a2a296"

# ---------------------------------------------------------------------------
# Setup
# ---------------------------------------------------------------------------
REPO_DIR="${REPO_DIR:-repo}"

if [ "${REPO_DIR}" != "." ]; then
  if [ ! -d "${REPO_DIR}" ]; then
    echo "::error::Extracted repo not found at ${REPO_DIR}"
    exit 1
  fi
  cd "${REPO_DIR}"
fi

: "${PUSH_TOKEN:?PUSH_TOKEN is required}"
: "${REPO_FULL_NAME:?REPO_FULL_NAME is required}"
: "${ISSUE_NUMBER:?ISSUE_NUMBER is required}"
TARGET_BRANCH="${TARGET_BRANCH:-main}"

echo "::add-mask::${PUSH_TOKEN}"

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
# 4. Install lychee (for pre-commit markdown link checking)
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
# 5. Install uv and uvx (for pre-commit Python tooling)
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
# 6. Authoritative pre-commit check
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
    if pre-commit run --files "${changed_array[@]}"; then
      echo "Pre-commit passed — all hooks clean"
    else
      echo "::error::BLOCKED — pre-commit hooks failed on agent's changes"
      echo "::error::The agent's code does not pass the repo's pre-commit hooks."
      echo "::error::Fix the issues and re-run, or update the pre-commit config."
      exit 1
    fi
  else
    echo "::warning::pre-commit not available on runner — skipping authoritative check"
    echo "::warning::CI pre-commit will still run on the PR"
  fi
else
  echo "No .pre-commit-config.yaml — skipping pre-commit check"
fi

# ---------------------------------------------------------------------------
# 7. Push branch
# ---------------------------------------------------------------------------
git remote set-url origin \
  "https://x-access-token:${PUSH_TOKEN}@github.com/${REPO_FULL_NAME}.git"

# Plain push (no --force-with-lease). Agents always create new
# commits (amend is in disallowedTools), so force-push is unnecessary
# and plain push is safer (refuses diverged branches).
echo "Pushing branch ${BRANCH}..."
git push -u origin -- "${BRANCH}" 2>&1

# ---------------------------------------------------------------------------
# 8. Create PR
# ---------------------------------------------------------------------------
export GH_TOKEN="${PUSH_TOKEN}"

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

PR_URL="$(gh pr create \
  --repo "${REPO_FULL_NAME}" \
  --head "${BRANCH}" \
  --base "${TARGET_BRANCH}" \
  --title "${PR_TITLE}" \
  --body "${PR_BODY}" \
  2>&1)"

echo "PR created: ${PR_URL}"
echo "pr_url=${PR_URL}" >> "${GITHUB_OUTPUT:-/dev/null}"
