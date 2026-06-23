#!/usr/bin/env bash
# post-code-auth-test.sh — harness test for post-code pre-push authorization gate
set -euo pipefail

tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

mkdir -p "${tmpdir}/repo/.git"
cd "${tmpdir}/repo"
git init -q
git config user.email "test@example.com"
git config user.name "Test"
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
export GITHUB_OUTPUT="$(mktemp)"

fullsend() {
  if [[ "$1" == "auth" ]]; then
    return 11
  fi
  return 0
}
export -f fullsend

gh() { return 0; }
export -f gh

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if bash "${SCRIPT_DIR}/post-code.sh" >/tmp/post-code-auth.out 2>&1; then
  echo "FAIL: post-code should block unauthorized workflow push" >&2
  cat /tmp/post-code-auth.out >&2
  exit 1
fi
grep -q "Workflow-change authorization blocked push" /tmp/post-code-auth.out
echo "OK: post-code auth gate blocked push"
