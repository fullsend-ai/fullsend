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
  local body="${marker}\n\nold comment"
  if [[ "${body_prefix}" == "embedded" ]]; then
    body="old comment\n\n${marker}"
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
run_case "lookalike app is not trusted" "github-actions" "acme-reviewer[bot]" "acme" "review" "marker" "post"
run_case "marker must start the comment" "github-actions" "fullsend-ai-review[bot]" "" "review" "embedded" "post"
