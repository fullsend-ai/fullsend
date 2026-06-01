#!/usr/bin/env bash
# reconcile-repos-test.sh - Regression tests for reconcile-repos.sh.
#
# Uses mocked gh/yq/base64 commands so tests do not hit GitHub.
# Run from the repo root: bash internal/scaffold/fullsend-repo/scripts/reconcile-repos-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RECONCILE_SCRIPT="${SCRIPT_DIR}/reconcile-repos.sh"

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

CONFIG_DIR="${TMPDIR}/config"
MOCK_BIN="${TMPDIR}/bin"
GH_LOG="${TMPDIR}/gh-calls.log"
COMMIT_MSG_LOG="${TMPDIR}/commit-msgs.log"
mkdir -p "${CONFIG_DIR}/templates" "${MOCK_BIN}"

cat > "${CONFIG_DIR}/config.yaml" <<'EOF'
version: 1
repos:
  test-repo:
    enabled: true
EOF

cat > "${CONFIG_DIR}/templates/shim-workflow-call.yaml" <<'EOF'
fresh shim template
EOF

cat > "${MOCK_BIN}/base64" <<'EOF'
#!/usr/bin/env bash
if [[ "${1:-}" == "-w0" ]]; then
  shift
fi
/usr/bin/base64 "$@" | tr -d '\r\n'
EOF
chmod +x "${MOCK_BIN}/base64"

cat > "${MOCK_BIN}/yq" <<'EOF'
#!/usr/bin/env bash
query="${1:-}"
if [[ "$query" == *"enabled == true"* ]]; then
  echo "test-repo"
elif [[ "$query" == *"enabled == false"* ]]; then
  :
else
  echo "unexpected yq query: $*" >&2
  exit 1
fi
EOF
chmod +x "${MOCK_BIN}/yq"

cat > "${MOCK_BIN}/gh" <<EOF
#!/usr/bin/env bash
set -euo pipefail
printf 'gh' >> "${GH_LOG}"
for arg in "\$@"; do
  printf ' %q' "\$arg" >> "${GH_LOG}"
done
printf '\n' >> "${GH_LOG}"

if [[ "\$1" == "pr" && "\$2" == "list" ]]; then
  for arg in "\$@"; do
    if [[ "\$arg" == "fullsend/onboard" ]]; then
      echo "https://github.com/test-org/test-repo/pull/18"
    fi
  done
  exit 0
fi

if [[ "\$1" != "api" ]]; then
  echo "unexpected gh command: \$*" >&2
  exit 1
fi

# Extract --jq filter if present.
jq_filter=""
input_file=""
shift  # consume "api"
endpoint="\$1"; shift
while [[ \$# -gt 0 ]]; do
  case "\$1" in
    --jq) jq_filter="\$2"; shift 2 ;;
    --input) input_file="\$2"; shift 2 ;;
    --method|--field) shift 2 ;;
    --silent) shift ;;
    *) shift ;;
  esac
done

json=""
rc=0
case "\$endpoint" in
  repos/test-org/test-repo/actions/variables/*)
    # Variable not found — 404.
    json='{"status":"404","message":"Not Found"}'
    rc=1
    ;;
  repos/test-org/test-repo/contents/.github/workflows/fullsend.yaml)
    json='{"content":"c3RhbGUgc2hpbSB0ZW1wbGF0ZQo=","sha":"file-sha"}'
    ;;
  repos/test-org/test-repo)
    json='{"default_branch":"main","private":false}'
    ;;
  repos/test-org/test-repo/git/ref/heads/main)
    json='{"object":{"sha":"base-sha"}}'
    ;;
  repos/test-org/test-repo/git/commits/base-sha)
    json='{"tree":{"sha":"base-tree-sha"}}'
    ;;
  repos/test-org/test-repo/git/blobs)
    json='{"sha":"blob-sha"}'
    ;;
  repos/test-org/test-repo/git/trees)
    json='{"sha":"tree-sha"}'
    ;;
  repos/test-org/test-repo/git/commits)
    # Capture the commit message from stdin for verification.
    if [[ "\$input_file" == "-" ]]; then
      stdin=\$(cat)
      printf '%s\n' "\$stdin" >> "${COMMIT_MSG_LOG}"
    fi
    json='{"sha":"desired-commit-sha"}'
    ;;
  repos/test-org/test-repo/git/refs)
    rc=1
    ;;
  repos/test-org/test-repo/git/refs/heads/fullsend/onboard)
    rc=0
    ;;
  repos/test-org/test-repo/git/refs/heads/fullsend/offboard)
    rc=0
    ;;
  *)
    echo "unexpected gh api endpoint: \$endpoint" >&2
    exit 1
    ;;
esac

if [[ -n "\$json" ]]; then
  if [[ -n "\$jq_filter" ]]; then
    printf '%s' "\$json" | jq -r "\$jq_filter"
  else
    printf '%s\n' "\$json"
  fi
fi
exit "\$rc"
EOF
chmod +x "${MOCK_BIN}/gh"

export PATH="${MOCK_BIN}:${PATH}"
export GITHUB_REPOSITORY_OWNER="test-org"
export GITHUB_SHA="test-sha"
export GH_TOKEN="fake-token"

bash "${RECONCILE_SCRIPT}" "${CONFIG_DIR}" > "${TMPDIR}/stdout.log" 2>&1

if grep -q "refs/heads/fullsend/onboard.*sha=base-sha" "${GH_LOG}"; then
  echo "FAIL: fullsend/onboard was reset to the default branch SHA"
  cat "${GH_LOG}"
  exit 1
fi

if ! grep -q "refs/heads/fullsend/onboard.*sha=desired-commit-sha" "${GH_LOG}"; then
  echo "FAIL: fullsend/onboard was not moved directly to the desired shim commit"
  cat "${GH_LOG}"
  exit 1
fi

if grep -q "contents/.github/workflows/fullsend.yaml.*--method PUT" "${GH_LOG}"; then
  echo "FAIL: shim update used Contents API after resetting branch state"
  cat "${GH_LOG}"
  exit 1
fi

echo "PASS: stale shim branch update is atomic"

# --- Test: commit messages include body and Signed-off-by ---

if [ ! -f "${COMMIT_MSG_LOG}" ]; then
  echo "FAIL: no commit messages were captured"
  exit 1
fi

# The commit message is JSON-encoded in the payload; extract the raw
# multi-line message from the first JSON object in the log.
COMMIT_MSG=$(jq -r '.message // empty' "${COMMIT_MSG_LOG}" 2>/dev/null)
if [ -z "$COMMIT_MSG" ]; then
  echo "FAIL: could not extract commit message from API payload"
  cat "${COMMIT_MSG_LOG}"
  exit 1
fi

# B6: body must be non-empty (second paragraph after blank line).
if ! printf '%s' "$COMMIT_MSG" | grep -q '^$'; then
  echo "FAIL: commit message has no blank line separating title from body (gitlint B6 would fail)"
  printf '%s\n' "$COMMIT_MSG"
  exit 1
fi

BODY=$(printf '%s\n' "$COMMIT_MSG" | tail -n +3)
if [ -z "$BODY" ]; then
  echo "FAIL: commit message body is empty (gitlint B6 would fail)"
  printf '%s\n' "$COMMIT_MSG"
  exit 1
fi

# CC1: must contain Signed-off-by trailer.
if ! printf '%s\n' "$COMMIT_MSG" | grep -q 'Signed-off-by:'; then
  echo "FAIL: commit message has no Signed-off-by line (gitlint CC1 would fail)"
  printf '%s\n' "$COMMIT_MSG"
  exit 1
fi

echo "PASS: commit messages include body and Signed-off-by"
