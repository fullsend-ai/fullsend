#!/usr/bin/env bash
# merge-queue_test.sh — Tests for enqueue-pr.sh and await-and-enqueue.sh
#
# Asserts that a PR URL argument is parsed locally and `gh pr view` is
# invoked as `-R owner/repo <number>`, never with a raw github.com URL.
# Also covers bare PR numbers and omitted arguments (current-branch PR).
#
# Run from the repo root:
#   bash skills/merge-queue/scripts/merge-queue_test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENQUEUE="${SCRIPT_DIR}/enqueue-pr.sh"
AWAIT="${SCRIPT_DIR}/await-and-enqueue.sh"
FAILURES=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

# build_mock creates a mock gh that logs every invocation, rejects any
# github.com URL argument, and returns canned JSON for the calls these
# scripts make.
build_mock() {
  local mock_bin="${TMPDIR}/bin"

  rm -rf "${mock_bin}"
  mkdir -p "${mock_bin}"
  : > "${TMPDIR}/gh.log"
  printf '%s' "${TMPDIR}/gh.log" > "${TMPDIR}/gh-log-path"

  cat > "${mock_bin}/gh" <<'MOCKEOF'
#!/usr/bin/env bash
set -euo pipefail
MOCK_DIR="$(cd "$(dirname "$0")/.." && pwd)"
GH_LOG="$(cat "${MOCK_DIR}/gh-log-path")"

{
  printf 'gh'
  for arg in "$@"; do
    printf ' %s' "${arg}"
  done
  printf '\n'
} >> "${GH_LOG}"

for arg in "$@"; do
  case "${arg}" in
    https://github.com/*)
      echo "mock gh: received github.com URL argument: ${arg}" >&2
      exit 1
      ;;
  esac
done

if [[ "${1:-}" == "pr" && "${2:-}" == "view" ]]; then
  shift 2
  json_fields=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --json)
        if [[ $# -lt 2 ]]; then
          echo "mock gh: --json missing value" >&2
          exit 1
        fi
        json_fields="$2"
        shift 2
        continue
        ;;
      -R|--repo|-q|--jq)
        if [[ $# -lt 2 ]]; then
          echo "mock gh: $1 missing value" >&2
          exit 1
        fi
        shift 2
        continue
        ;;
      *)
        shift
        ;;
    esac
  done
  case "${json_fields}" in
    url,id)
      echo '{"url":"https://github.com/owner/repo/pull/652","id":"PR_NODE_ID"}'
      ;;
    url,baseRefName,number)
      echo '{"url":"https://github.com/owner/repo/pull/652","baseRefName":"main","number":652}'
      ;;
    statusCheckRollup,reviewDecision)
      echo '{"statusCheckRollup":[{"name":"ci","conclusion":"SUCCESS","status":"COMPLETED"}],"reviewDecision":"APPROVED"}'
      ;;
    *)
      echo "mock gh: unexpected --json fields: ${json_fields}" >&2
      exit 1
      ;;
  esac
  exit 0
fi

if [[ "${1:-}" == "api" && "${2:-}" == "graphql" ]]; then
  echo '{"data":{"enqueuePullRequest":{"mergeQueueEntry":{"position":1,"estimatedTimeToMerge":60}}}}'
  exit 0
fi

if [[ "${1:-}" == "api" && "${2:-}" == repos/* ]]; then
  echo '[]'
  exit 0
fi

echo "mock gh: unexpected invocation: $*" >&2
exit 1
MOCKEOF

  chmod +x "${mock_bin}/gh"
  echo "${mock_bin}"
}

fail() {
  echo "FAIL: $1"
  if [[ -f "${TMPDIR}/gh.log" ]]; then
    echo "  gh log:"
    sed 's/^/    /' "${TMPDIR}/gh.log" || true
  fi
  FAILURES=$((FAILURES + 1))
}

pass() {
  echo "PASS: $1"
}

# True if any logged `gh pr view` line contains a github.com URL.
pr_view_has_url() {
  grep -E '^gh pr view ' "${TMPDIR}/gh.log" | grep -q 'https://github.com/'
}

# True if some `gh pr view` line uses -R/--repo <repo> and the number.
pr_view_has_repo_and_number() {
  local repo="$1" number="$2"
  local line
  while IFS= read -r line; do
    case "${line}" in
      gh\ pr\ view*)
        ;;
      *)
        continue
        ;;
    esac
    case "${line}" in
      *" ${number} "*)
        ;;
      *)
        continue
        ;;
    esac
    case "${line}" in
      *" -R ${repo} "*|*" --repo ${repo} "*|*" -R ${repo}"|*" --repo ${repo}")
        return 0
        ;;
    esac
  done < "${TMPDIR}/gh.log"
  return 1
}

run_script() {
  local mock_bin="$1"
  shift
  PATH="${mock_bin}:${PATH}" POLL_INTERVAL=1 "$@"
}

echo "=== enqueue-pr.sh ==="

# URL argument → gh pr view <number> -R owner/repo, never a raw URL.
{
  name="enqueue-pr URL argument"
  mock_bin="$(build_mock)"
  actual_exit=0
  output="$(run_script "${mock_bin}" bash "${ENQUEUE}" \
    "https://github.com/owner/repo/pull/652" 2>&1)" || actual_exit=$?
  if [[ "${actual_exit}" -ne 0 ]]; then
    fail "${name} — expected exit 0, got ${actual_exit}: ${output}"
  elif pr_view_has_url; then
    fail "${name} — gh pr view received a raw URL"
  elif ! pr_view_has_repo_and_number "owner/repo" "652"; then
    fail "${name} — expected gh pr view 652 -R owner/repo"
  else
    pass "${name}"
  fi
}

# URL with a trailing path still parses owner/repo and number.
{
  name="enqueue-pr URL with /files suffix"
  mock_bin="$(build_mock)"
  actual_exit=0
  output="$(run_script "${mock_bin}" bash "${ENQUEUE}" \
    "https://github.com/owner/repo/pull/652/files" 2>&1)" || actual_exit=$?
  if [[ "${actual_exit}" -ne 0 ]]; then
    fail "${name} — expected exit 0, got ${actual_exit}: ${output}"
  elif pr_view_has_url; then
    fail "${name} — gh pr view received a raw URL"
  elif ! pr_view_has_repo_and_number "owner/repo" "652"; then
    fail "${name} — expected gh pr view 652 -R owner/repo"
  else
    pass "${name}"
  fi
}

# Bare number → gh pr view <number>, never a URL.
{
  name="enqueue-pr bare PR number"
  mock_bin="$(build_mock)"
  actual_exit=0
  output="$(run_script "${mock_bin}" bash "${ENQUEUE}" "652" 2>&1)" || actual_exit=$?
  if [[ "${actual_exit}" -ne 0 ]]; then
    fail "${name} — expected exit 0, got ${actual_exit}: ${output}"
  elif pr_view_has_url; then
    fail "${name} — gh pr view received a raw URL"
  elif ! grep -E '^gh pr view 652 ' "${TMPDIR}/gh.log" >/dev/null; then
    fail "${name} — expected gh pr view 652"
  else
    pass "${name}"
  fi
}

# Omitted argument → gh pr view with no positional URL.
{
  name="enqueue-pr omitted argument"
  mock_bin="$(build_mock)"
  actual_exit=0
  output="$(run_script "${mock_bin}" bash "${ENQUEUE}" 2>&1)" || actual_exit=$?
  if [[ "${actual_exit}" -ne 0 ]]; then
    fail "${name} — expected exit 0, got ${actual_exit}: ${output}"
  elif pr_view_has_url; then
    fail "${name} — gh pr view received a raw URL"
  elif ! grep -E '^gh pr view --json ' "${TMPDIR}/gh.log" >/dev/null; then
    fail "${name} — expected gh pr view --json with no positional"
  else
    pass "${name}"
  fi
}

# Unsupported owner/repo#number form errors without calling gh pr view.
{
  name="enqueue-pr owner/repo#number is rejected"
  mock_bin="$(build_mock)"
  actual_exit=0
  output="$(run_script "${mock_bin}" bash "${ENQUEUE}" "owner/repo#652" 2>&1)" || actual_exit=$?
  if [[ "${actual_exit}" -ne 1 ]]; then
    fail "${name} — expected exit 1, got ${actual_exit}: ${output}"
  elif grep -E '^gh pr view ' "${TMPDIR}/gh.log" >/dev/null; then
    fail "${name} — gh pr view should not be called"
  elif [[ "${output}" != *"Error: provide a PR number or URL"* ]]; then
    fail "${name} — expected usage error, got: ${output}"
  else
    pass "${name}"
  fi
}

echo ""
echo "=== await-and-enqueue.sh ==="

# URL argument: every gh pr view uses -R owner/repo and the number.
{
  name="await-and-enqueue URL argument"
  mock_bin="$(build_mock)"
  actual_exit=0
  output="$(run_script "${mock_bin}" bash "${AWAIT}" \
    "https://github.com/owner/repo/pull/652" 2>&1)" || actual_exit=$?
  if [[ "${actual_exit}" -ne 0 ]]; then
    fail "${name} — expected exit 0, got ${actual_exit}: ${output}"
  elif pr_view_has_url; then
    fail "${name} — gh pr view received a raw URL"
  elif ! pr_view_has_repo_and_number "owner/repo" "652"; then
    fail "${name} — expected gh pr view 652 -R owner/repo"
  else
    # The init call, the poll loop, and enqueue-pr.sh must all be normalized.
    view_count="$(grep -cE '^gh pr view ' "${TMPDIR}/gh.log" || true)"
    repo_count="$(grep -cE '^gh pr view .* -R owner/repo ' "${TMPDIR}/gh.log" || true)"
    if [[ "${view_count}" -lt 2 ]]; then
      fail "${name} — expected multiple gh pr view calls, got ${view_count}"
    elif [[ "${repo_count}" -ne "${view_count}" ]]; then
      fail "${name} — every gh pr view should use -R owner/repo (view=${view_count} repo=${repo_count})"
    else
      pass "${name}"
    fi
  fi
}

# Bare number: no URL positional; later calls (after repo is known) use -R.
{
  name="await-and-enqueue bare PR number"
  mock_bin="$(build_mock)"
  actual_exit=0
  output="$(run_script "${mock_bin}" bash "${AWAIT}" "652" 2>&1)" || actual_exit=$?
  if [[ "${actual_exit}" -ne 0 ]]; then
    fail "${name} — expected exit 0, got ${actual_exit}: ${output}"
  elif pr_view_has_url; then
    fail "${name} — gh pr view received a raw URL"
  elif ! grep -E '^gh pr view 652 ' "${TMPDIR}/gh.log" >/dev/null; then
    fail "${name} — expected gh pr view 652"
  elif ! pr_view_has_repo_and_number "owner/repo" "652"; then
    fail "${name} — expected a later gh pr view 652 -R owner/repo"
  else
    pass "${name}"
  fi
}

# Omitted argument: first call has no positional; later calls use -R.
{
  name="await-and-enqueue omitted argument"
  mock_bin="$(build_mock)"
  actual_exit=0
  output="$(run_script "${mock_bin}" bash "${AWAIT}" 2>&1)" || actual_exit=$?
  if [[ "${actual_exit}" -ne 0 ]]; then
    fail "${name} — expected exit 0, got ${actual_exit}: ${output}"
  elif pr_view_has_url; then
    fail "${name} — gh pr view received a raw URL"
  elif ! grep -E '^gh pr view --json url,baseRefName,number' "${TMPDIR}/gh.log" >/dev/null; then
    fail "${name} — expected initial gh pr view --json with no positional"
  elif ! pr_view_has_repo_and_number "owner/repo" "652"; then
    fail "${name} — expected a later gh pr view 652 -R owner/repo"
  else
    pass "${name}"
  fi
}

# Unsupported form errors without calling gh.
{
  name="await-and-enqueue owner/repo#number is rejected"
  mock_bin="$(build_mock)"
  actual_exit=0
  output="$(run_script "${mock_bin}" bash "${AWAIT}" "owner/repo#652" 2>&1)" || actual_exit=$?
  if [[ "${actual_exit}" -ne 1 ]]; then
    fail "${name} — expected exit 1, got ${actual_exit}: ${output}"
  elif grep -E '^gh ' "${TMPDIR}/gh.log" >/dev/null; then
    fail "${name} — gh should not be called"
  elif [[ "${output}" != *"Error: provide a PR number or URL"* ]]; then
    fail "${name} — expected usage error, got: ${output}"
  else
    pass "${name}"
  fi
}

echo ""
if [[ "${FAILURES}" -gt 0 ]]; then
  echo "${FAILURES} test(s) FAILED"
  exit 1
fi
echo "All tests passed"
