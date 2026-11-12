#!/usr/bin/env bash
# Tests the startup-failure reporter with REST-shaped bot identities.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="${SCRIPT_DIR}/report-agent-startup-failure.sh"
TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

MOCK_BIN="${TMPDIR}/bin"
mkdir -p "${MOCK_BIN}"
cat > "${MOCK_BIN}/gh" <<'MOCKEOF'
#!/usr/bin/env bash
set -euo pipefail

MOCK_DIR="$(cd "$(dirname "$0")/.." && pwd)"
if [[ "$1" != "api" ]]; then
  echo "unexpected gh call: $*" >&2
  exit 1
fi

if [[ "$*" == *"graphql"* ]]; then
  if [[ "${MOCK_GRAPHQL_FAIL:-}" == "true" ]]; then
    exit 1
  fi
  printf '%s\n' "${MOCK_VIEWER_LOGIN}"
elif [[ "$*" == *"--method PATCH"* ]]; then
  printf 'patch\n' >> "${MOCK_DIR}/calls"
elif [[ "$*" == *"--method POST"* ]]; then
  printf 'post\n' >> "${MOCK_DIR}/calls"
else
  printf '%s\n' "${MOCK_COMMENTS_JSON}"
fi
MOCKEOF
chmod +x "${MOCK_BIN}/gh"

run_case() {
  local name="$1" viewer="$2" comment_login="$3" trusted_bots="${4:-}" role="${5:-review}" body_prefix="${6:-marker}" expected="${7:-patch}"
  local marker='<!-- fullsend:agent-startup-failure:review:123 -->'
  local body="${marker}\n\nold comment\n\n[View the workflow run](https://github.com/org/repo/actions/runs/123)"
  if [[ "${body_prefix}" == "embedded" ]]; then
    body="old comment\n\n${marker}"
  elif [[ "${body_prefix}" == "old-run" ]]; then
    body="${marker}\n\nold comment\n\n[View the workflow run](https://github.com/org/repo/actions/runs/99)"
  elif [[ "${body_prefix}" == "prefix-run" ]]; then
    body="${marker}\n\nold comment\n\n[View the workflow run](https://github.com/org/repo/actions/runs/1234)"
  fi
  export MOCK_VIEWER_LOGIN="${viewer}"
  export MOCK_COMMENTS_JSON="[[{\"id\":42,\"user\":{\"type\":\"Bot\",\"login\":\"${comment_login}\"},\"body\":\"${body}\"}]]"
  : > "${TMPDIR}/calls"

  PATH="${MOCK_BIN}:${PATH}" \
    GH_TOKEN=test \
    RUNNER_TEMP="${TMPDIR}" \
    FULLSEND_FAILURE_REPOSITORY=org/repo \
    FULLSEND_FAILURE_NUMBER=123 \
    FULLSEND_FAILURE_AGENT=review \
    FULLSEND_FAILURE_ROLE="${role}" \
    FULLSEND_FAILURE_TRUSTED_BOTS="${trusted_bots}" \
    FULLSEND_FAILURE_RUN_URL=https://github.com/org/repo/actions/runs/123 \
    FULLSEND_FAILURE_CATEGORY="workflow setup" \
    bash "${SCRIPT}" >/dev/null

  local actual
  actual="$(cat "${TMPDIR}/calls")"
  if [[ "${actual}" == "${expected}" ]]; then
    printf 'PASS: %s\n' "${name}"
  else
    printf 'FAIL: %s — expected %s, got %s\n' "${name}" "${expected:-no call}" "${actual:-no call}" >&2
    return 1
  fi
}

run_case "job token finds app-bot comment" "github-actions" "fullsend-ai-review[bot]"
run_case "app token finds job-bot comment" "fullsend-ai-review" "github-actions[bot]"
run_case "custom app set finds job-bot comment" "acme-review" "github-actions[bot]" "acme"
run_case "job token finds custom app-set comment" "github-actions" "acme-review[bot]" "acme"
run_case "non-listed role is trusted" "github-actions" "acme-scribe[bot]" "acme" "scribe"
run_case "custom app-set whitespace is trimmed" "github-actions" "acme-scribe[bot]" " acme " "scribe"
run_case "lookalike app is not trusted" "github-actions" "acme-reviewer[bot]" "acme" "review" "marker" "post"
run_case "marker must start the comment" "github-actions" "fullsend-ai-review[bot]" "" "review" "embedded" "post"
run_case "different run posts a new historical comment" "github-actions" "fullsend-ai-review[bot]" "" "review" "old-run" "post"
run_case "prefix-matching run posts a new historical comment" "github-actions" "fullsend-ai-review[bot]" "" "review" "prefix-run" "post"

export MOCK_VIEWER_LOGIN="github-actions"
export MOCK_GRAPHQL_FAIL=true
export MOCK_COMMENTS_JSON='[[{"id":42,"user":{"type":"Bot","login":"fullsend-ai-review[bot]"},"body":"<!-- fullsend:agent-startup-failure:review:123 -->\n\nold comment\n\n[View the workflow run](https://github.com/org/repo/actions/runs/123)"}]]'
: > "${TMPDIR}/calls"
PATH="${MOCK_BIN}:${PATH}" \
  GH_TOKEN=test \
  RUNNER_TEMP="${TMPDIR}" \
  FULLSEND_FAILURE_REPOSITORY=org/repo \
  FULLSEND_FAILURE_NUMBER=123 \
  FULLSEND_FAILURE_AGENT=review \
  FULLSEND_FAILURE_ROLE=review \
  FULLSEND_FAILURE_RUN_URL=https://github.com/org/repo/actions/runs/123 \
  FULLSEND_FAILURE_CATEGORY="workflow setup" \
  bash "${SCRIPT}" >/dev/null
if [[ "$(cat "${TMPDIR}/calls")" == "patch" ]]; then
  printf 'PASS: GraphQL viewer failure does not prevent reporting\n'
else
  printf 'FAIL: GraphQL viewer failure did not report\n' >&2
  exit 1
fi
