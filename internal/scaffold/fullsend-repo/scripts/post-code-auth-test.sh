#!/usr/bin/env bash
# post-code-auth-test.sh — harness test for post-code pre-push authorization gate
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

mkdir -p "${tmpdir}/repo/.git"
cd "${tmpdir}/repo"
git init -q
git config user.email "test@example.com"
git config user.name "Test"
git branch -m main
echo "base" > README.md
git add README.md
git commit -q -m "base"
git checkout -q -b feature/workflow
mkdir -p .github/workflows
echo "name: ci" > .github/workflows/ci.yml
git add .github/workflows/ci.yml
git commit -q -m "add workflow"

export PUSH_TOKEN="fake"
export REPO_FULL_NAME="test-org/test-repo"
export ISSUE_NUMBER=1
export REPO_DIR="."
export TARGET_BRANCH="main"
export GH_TOKEN="fake"
GITHUB_OUTPUT="$(mktemp)"
export GITHUB_OUTPUT

MOCK_BIN="${tmpdir}/bin"
mkdir -p "${MOCK_BIN}"
cat > "${MOCK_BIN}/fullsend" <<'MOCKEOF'
#!/usr/bin/env bash
if [[ "$1" == "auth" ]]; then
  exit 11
fi
exit 0
MOCKEOF
chmod +x "${MOCK_BIN}/fullsend"
cat > "${MOCK_BIN}/gh" <<'MOCKEOF'
#!/usr/bin/env bash
if [[ "$1" == "api" && "$2" == repos/* && "$*" == *default_branch* ]]; then
  echo "main"
  exit 0
fi
exit 0
MOCKEOF
chmod +x "${MOCK_BIN}/gh"
export PATH="${MOCK_BIN}:${PATH}"

OUTPUT_FILE="${tmpdir}/post-code.out"
if bash "${SCRIPT_DIR}/post-code.sh" >"${OUTPUT_FILE}" 2>&1; then
  echo "FAIL: post-code should block unauthorized workflow push" >&2
  cat "${OUTPUT_FILE}" >&2
  exit 1
fi
grep -q "Workflow-change authorization blocked push" "${OUTPUT_FILE}"
echo "OK: post-code auth gate blocked push"
