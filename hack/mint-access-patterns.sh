#!/usr/bin/env bash
# mint-access-patterns — exercise mint repos-field + workflow-host access patterns
# via GHA OIDC (ADR 0082 / PR #5916).
#
# Pushes ephemeral on-push workflows that call the mint with curl (not the
# fullsend binary), then asserts minted/denied outcomes and token scope.
#
# Most mint-test cases use a shim that calls a reusable workflow in {org}/fullsend
# (listed on WORKFLOW_HOST_REPOS). .fullsend hosts its own mint job for org-mode
# cases. One negative case hosts the mint job in mint-test itself (must deny).
#
# Usage:
#   hack/mint-access-patterns.sh --mint-url URL [options]
#
# Options:
#   --org ORG             GitHub org (default: fullsand-ai)
#   --mint-url URL        Mint base URL (or set FULLSEND_MINT_URL)
#   --foreign-org ORG     Target org for e2e foreign-mint cases (default: halfsend)
#   --role ROLE           Run only cases for this role (default: all roles in matrix)
#   --mode MODE           per-repo (default) | per-org | both
#   --project GCP_PROJECT Optional: surgically enroll/unenroll for MODE
#   --region REGION       GCP region for mint CLI (default: us-central1)
#   --timeout SECONDS     Per-case workflow wait timeout (default: 600)
#   --poll-interval SEC   Poll interval while waiting for runs (default: 5)
#   -h, --help            Show this help
#
# Requires: gh, jq, git, curl; gh authenticated (or GH_TOKEN) with repo create +
# contents/actions. Git clone/push use HTTPS with a token credential helper.
# With --project: gcloud + mise/go for fullsend mint CLI.
#
# Output: one line per case with colored PASS/FAIL/ERROR (TTY, respects NO_COLOR),
# mint outcome (minted|denied), and scope when minted. Exit 0 only if all PASS.
# Generated workflows always succeed (no GitHub failure emails); case results are
# reported only by this script from the mint-ap-result artifact.
#
# Foreign e2e: blank repos expect denied; repos=["*"] expect minted in
# per-repo/both (shim via {org}/fullsend). In per-org the same shim is
# denied — per-org hosts are hard-wired to {org}/.fullsend and upstream.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

ORG="fullsand-ai"
MINT_URL="${FULLSEND_MINT_URL:-}"
FOREIGN_ORG="halfsend"
ROLE_FILTER=""
MODE="per-repo"
GCP_PROJECT=""
GCP_REGION="us-central1"
TIMEOUT=600
POLL_INTERVAL=5

UPSTREAM_WORKFLOW_HOST="fullsend-ai/fullsend"
WORKFLOW_REPO="fullsend"

COUNT_OK=0
COUNT_FAIL=0
COUNT_ERROR=0
GH_API_TOKEN=""
GH_CRED_HELPER=""
TMPROOT=""
REUSABLE_BRANCH=""
ACTIVE_BRANCH_REPOS=()
ACTIVE_BRANCH_NAMES=()

usage() {
  cat <<'USAGE'
mint-access-patterns — exercise mint repos-field + workflow-host access patterns
via GHA OIDC (ADR 0082 / PR #5916).

Usage:
  hack/mint-access-patterns.sh --mint-url URL [options]

Options:
  --org ORG             GitHub org (default: fullsand-ai)
  --mint-url URL        Mint base URL (or set FULLSEND_MINT_URL)
  --foreign-org ORG     Target org for e2e foreign-mint cases (default: halfsend)
  --role ROLE           Run only cases for this role (default: all roles in matrix)
  --mode MODE           per-repo (default) | per-org | both
  --project GCP_PROJECT Optional: surgically enroll/unenroll for MODE
  --region REGION       GCP region for mint CLI (default: us-central1)
  --timeout SECONDS     Per-case workflow wait timeout (default: 600)
  --poll-interval SEC   Poll interval while waiting for runs (default: 5)
  -h, --help            Show this help
USAGE
}

die() {
  echo "ERROR: $*" >&2
  exit 2
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --org)
      ORG="${2:?--org requires a value}"
      shift 2
      ;;
    --mint-url)
      MINT_URL="${2:?--mint-url requires a value}"
      shift 2
      ;;
    --foreign-org)
      FOREIGN_ORG="${2:?--foreign-org requires a value}"
      shift 2
      ;;
    --role)
      ROLE_FILTER="${2:?--role requires a value}"
      shift 2
      ;;
    --mode)
      MODE="${2:?--mode requires a value}"
      shift 2
      ;;
    --project)
      GCP_PROJECT="${2:?--project requires a value}"
      shift 2
      ;;
    --region)
      GCP_REGION="${2:?--region requires a value}"
      shift 2
      ;;
    --timeout)
      TIMEOUT="${2:?--timeout requires a value}"
      shift 2
      ;;
    --poll-interval)
      POLL_INTERVAL="${2:?--poll-interval requires a value}"
      shift 2
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      usage >&2
      die "unknown option: $1"
      ;;
  esac
done

# TODO(ADR-0044): Remove per-org and both modes after existing users complete
# migration to the sole supported per-repo installation model.
case "$MODE" in
  per-repo | per-org | both) ;;
  *) die "--mode must be per-repo, per-org, or both (got: ${MODE})" ;;
esac

case "$TIMEOUT" in
  '' | *[!0-9]*) die "--timeout must be a positive integer (got: ${TIMEOUT})" ;;
esac
((TIMEOUT > 0)) || die "--timeout must be greater than zero"

case "$POLL_INTERVAL" in
  '' | *[!0-9]*) die "--poll-interval must be a positive integer (got: ${POLL_INTERVAL})" ;;
esac
((POLL_INTERVAL > 0)) || die "--poll-interval must be greater than zero"

case "$ROLE_FILTER" in
  '' | triage | coder | review | retro | prioritize | fullsend | e2e) ;;
  *) die "--role must be one of triage, coder, review, retro, prioritize, fullsend, or e2e (got: ${ROLE_FILTER})" ;;
esac

[[ -n "$MINT_URL" ]] || die "--mint-url or FULLSEND_MINT_URL is required"
MINT_URL="${MINT_URL%/}"

for cmd in gh jq git curl; do
  command -v "$cmd" &>/dev/null || die "$cmd is required but not found in PATH"
done

GH_API_TOKEN="${GH_TOKEN:-}"
if [[ -z "$GH_API_TOKEN" ]]; then
  GH_API_TOKEN="$(gh auth token 2>/dev/null || true)"
fi
[[ -n "$GH_API_TOKEN" ]] || die "set GH_TOKEN or run gh auth login (needed for HTTPS git)"
# Credential helper for HTTPS remotes (same pattern as eval/scripts/setup-fixture.sh).
GH_CRED_HELPER="!f(){ echo \"password=${GH_API_TOKEN}\"; };f"

TMPROOT=$(mktemp -d)
REUSABLE_BRANCH="mint-ap-reusable-$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')"

register_branch() {
  ACTIVE_BRANCH_REPOS+=("$1")
  ACTIVE_BRANCH_NAMES+=("$2")
}

unregister_branch() {
  local repo="$1" branch="$2" i
  for i in "${!ACTIVE_BRANCH_NAMES[@]}"; do
    if [[ "${ACTIVE_BRANCH_REPOS[$i]}" == "$repo" && "${ACTIVE_BRANCH_NAMES[$i]}" == "$branch" ]]; then
      unset 'ACTIVE_BRANCH_REPOS[i]' 'ACTIVE_BRANCH_NAMES[i]'
    fi
  done
}

# Invoked indirectly by the EXIT trap.
# shellcheck disable=SC2317
cleanup() {
  local i
  for i in "${!ACTIVE_BRANCH_NAMES[@]}"; do
    gh api -X DELETE "/repos/${ACTIVE_BRANCH_REPOS[$i]}/git/refs/heads/${ACTIVE_BRANCH_NAMES[$i]}" \
      --silent >/dev/null 2>&1 || true
  done
  rm -rf "$TMPROOT"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# --- colors (status token only) ---
C_PASS=""
C_FAIL=""
C_ERROR=""
C_RESET=""
if [[ -t 1 && -z "${NO_COLOR:-}" ]]; then
  C_PASS=$'\033[32m'
  C_FAIL=$'\033[31m'
  C_ERROR=$'\033[33m'
  C_RESET=$'\033[0m'
fi

rand_hex() {
  od -An -N4 -tx1 /dev/urandom | tr -d ' \n'
}

# --- repo ensure ---
ensure_repo() {
  local name="$1"
  local full="${ORG}/${name}"
  if gh api "/repos/${full}" --silent &>/dev/null; then
    return 0
  fi
  echo "creating repo ${full}" >&2
  if gh repo create "${full}" --public --add-readme --description "mint access-pattern test fixture" &>/dev/null; then
    return 0
  fi
  gh api -X POST "/orgs/${ORG}/repos" \
    -f name="${name}" \
    -f visibility=public \
    -F auto_init=true \
    -f description="mint access-pattern test fixture" \
    --silent >/dev/null
}

# --- formatting ---
format_scope() {
  local json="$1"
  local selection repos perms
  selection=$(jq -r '.repository_selection // empty' <<<"$json")
  repos=$(jq -r '.granted_repos // [] | join(",")' <<<"$json")
  perms=$(jq -r '.granted_permissions // {} | to_entries | map("\(.key)=\(.value)") | join(",")' <<<"$json")
  local out="scope=${selection}"
  [[ -n "$repos" ]] && out+=" repos=${repos}"
  [[ -n "$perms" ]] && out+=" perms=${perms}"
  printf '%s' "$out"
}

print_case_line() {
  local status="$1" label="$2" rest="$3"
  local color=""
  case "$status" in
    PASS) color="$C_PASS" ;;
    FAIL) color="$C_FAIL" ;;
    ERROR) color="$C_ERROR" ;;
  esac
  printf '%s%-5s%s %-28s %s\n' "$color" "$status" "$C_RESET" "$label" "$rest"
}

# --- mint CLI helpers (optional --project) ---
fullsend_mint() {
  (cd "${REPO_ROOT}" && mise exec -- go run ./cmd/fullsend mint "$@")
}

workflow_host_add() {
  local repo="$1"
  echo "enrollment: workflow-host add ${repo}" >&2
  fullsend_mint workflow-host add "${repo}" --project="${GCP_PROJECT}" --region="${GCP_REGION}"
}

configure_mint_enrollment() {
  echo "enrollment: configuring mint for mode=${MODE} project=${GCP_PROJECT} region=${GCP_REGION}" >&2
  case "$MODE" in
    per-repo)
      echo "enrollment: unenroll org ${ORG}" >&2
      fullsend_mint unenroll "${ORG}" --project="${GCP_PROJECT}" --region="${GCP_REGION}" --yolo
      echo "enrollment: enroll repo ${ORG}/mint-test" >&2
      fullsend_mint enroll "${ORG}/mint-test" --project="${GCP_PROJECT}" --region="${GCP_REGION}"
      workflow_host_add "${UPSTREAM_WORKFLOW_HOST}"
      workflow_host_add "${ORG}/${WORKFLOW_REPO}"
      ;;
    per-org)
      echo "enrollment: unenroll repo ${ORG}/mint-test" >&2
      fullsend_mint unenroll "${ORG}/mint-test" --project="${GCP_PROJECT}" --region="${GCP_REGION}" --yolo
      echo "enrollment: enroll org ${ORG}" >&2
      fullsend_mint enroll "${ORG}" --project="${GCP_PROJECT}" --region="${GCP_REGION}"
      workflow_host_add "${UPSTREAM_WORKFLOW_HOST}"
      workflow_host_add "${ORG}/${WORKFLOW_REPO}"
      ;;
    both)
      echo "enrollment: enroll org ${ORG}" >&2
      fullsend_mint enroll "${ORG}" --project="${GCP_PROJECT}" --region="${GCP_REGION}"
      echo "enrollment: enroll repo ${ORG}/mint-test" >&2
      fullsend_mint enroll "${ORG}/mint-test" --project="${GCP_PROJECT}" --region="${GCP_REGION}"
      workflow_host_add "${UPSTREAM_WORKFLOW_HOST}"
      workflow_host_add "${ORG}/${WORKFLOW_REPO}"
      ;;
  esac
  echo "enrollment: done" >&2
}

# Mint curl body used by reusable + in-repo / .fullsend direct workflows.
# Args expand at generation time into the workflow file.
mint_curl_run_script() {
  cat <<'RUNSCRIPT'
          set -euo pipefail
          # Always exit 0 so GitHub does not email on "failed" runs; outcome lives
          # in mint-ap-result.json for the driver.
          finish() { exit 0; }
          trap finish EXIT

          if ! OIDC_RESPONSE=$(curl -sSf --retry 3 --retry-delay 2 --retry-all-errors \
            -H "Authorization: bearer $ACTIONS_ID_TOKEN_REQUEST_TOKEN" \
            "${ACTIONS_ID_TOKEN_REQUEST_URL}&audience=fullsend-mint"); then
            jq -n '{outcome:"error",error:"oidc_transport"}' > mint-ap-result.json
            echo "MINT_AP_RESULT=$(cat mint-ap-result.json)"
            exit 0
          fi
          OIDC_TOKEN=$(jq -r '.value // empty' <<<"$OIDC_RESPONSE" 2>/dev/null || true)
          if [[ -z "$OIDC_TOKEN" || "$OIDC_TOKEN" == "null" ]]; then
            echo "Failed to obtain OIDC token" >&2
            jq -n '{outcome:"error",error:"oidc_response"}' > mint-ap-result.json
            echo "MINT_AP_RESULT=$(cat mint-ap-result.json)"
            exit 0
          fi
          echo "::add-mask::$OIDC_TOKEN"

          if [[ -z "${REPOS_JSON}" ]]; then
            if [[ -n "${TARGET_ORG}" ]]; then
              BODY=$(jq -nc --arg role "$ROLE" --arg target_org "$TARGET_ORG" \
                '{role: $role, target_org: $target_org}')
            else
              BODY=$(jq -nc --arg role "$ROLE" '{role: $role}')
            fi
          else
            if [[ -n "${TARGET_ORG}" ]]; then
              BODY=$(jq -nc --arg role "$ROLE" --argjson repos "$REPOS_JSON" --arg target_org "$TARGET_ORG" \
                '{role: $role, repos: $repos, target_org: $target_org}')
            else
              BODY=$(jq -nc --arg role "$ROLE" --argjson repos "$REPOS_JSON" \
                '{role: $role, repos: $repos}')
            fi
          fi

          if ! HTTP_CODE=$(curl -sS --retry 2 --retry-delay 2 --retry-all-errors \
            -o mint-ap-response.json -w '%{http_code}' \
            -H "Authorization: Bearer $OIDC_TOKEN" \
            -H "Content-Type: application/json" \
            -d "$BODY" \
            "${MINT_URL}/v1/token"); then
            jq -n '{outcome:"error",error:"mint_transport"}' > mint-ap-result.json
            echo "MINT_AP_RESULT=$(cat mint-ap-result.json)"
            exit 0
          fi

          if [[ "$HTTP_CODE" == "403" ]]; then
            echo "Mint denied HTTP $HTTP_CODE"
            jq -n --arg code "$HTTP_CODE" '{outcome:"denied",http_status:($code|tonumber)}' \
              > mint-ap-result.json
            echo "MINT_AP_RESULT=$(cat mint-ap-result.json)"
            exit 0
          fi
          if [[ ! "$HTTP_CODE" =~ ^2[0-9][0-9]$ ]]; then
            echo "Mint failed HTTP $HTTP_CODE" >&2
            jq -n --arg code "$HTTP_CODE" '{outcome:"error",error:"mint_http",http_status:($code|tonumber)}' \
              > mint-ap-result.json
            echo "MINT_AP_RESULT=$(cat mint-ap-result.json)"
            exit 0
          fi

          if ! TOKEN=$(jq -er '.token | select(type == "string" and length > 0)' mint-ap-response.json); then
            TOKEN=""
          fi
          if [[ -z "$TOKEN" || "$TOKEN" == "null" ]]; then
            echo "Mint response missing token" >&2
            jq -n '{outcome:"error",error:"mint_response"}' > mint-ap-result.json
            echo "MINT_AP_RESULT=$(cat mint-ap-result.json)"
            exit 0
          fi
          echo "::add-mask::$TOKEN"

          if ! SELECTION=$(jq -er '.repository_selection | select(type == "string")' mint-ap-response.json) ||
            ! REPOS=$(jq -ec '.granted_repos // [] | select(type == "array") | sort' mint-ap-response.json) ||
            ! PERMS=$(jq -ec '.granted_permissions // {} | select(type == "object")' mint-ap-response.json); then
            jq -n '{outcome:"error",error:"mint_scope_response"}' > mint-ap-result.json
            echo "MINT_AP_RESULT=$(cat mint-ap-result.json)"
            exit 0
          fi

          jq -n \
            --arg outcome minted \
            --arg selection "$SELECTION" \
            --argjson repos "$REPOS" \
            --argjson perms "$PERMS" \
            '{outcome:$outcome, repository_selection:$selection, granted_repos:$repos, granted_permissions:$perms}' \
            > mint-ap-result.json

          case "$ROLE" in
            triage) EXPECTED_PERMS='{"contents":"read","issues":"write","metadata":"read"}' ;;
            coder) EXPECTED_PERMS='{"checks":"read","contents":"write","issues":"write","metadata":"read","pull_requests":"write"}' ;;
            review) EXPECTED_PERMS='{"checks":"read","contents":"read","issues":"write","metadata":"read","pull_requests":"write"}' ;;
            retro) EXPECTED_PERMS='{"actions":"read","contents":"read","issues":"write","metadata":"read","pull_requests":"write"}' ;;
            prioritize) EXPECTED_PERMS='{"contents":"read","issues":"write","metadata":"read","organization_projects":"write"}' ;;
            fullsend) EXPECTED_PERMS='{"actions":"write","actions_variables":"read","contents":"write","metadata":"read","pull_requests":"write","workflows":"write"}' ;;
            e2e) EXPECTED_PERMS='{"actions":"write","actions_variables":"write","administration":"write","contents":"write","issues":"write","members":"write","metadata":"read","organization_actions_variables":"write","organization_administration":"write","pull_requests":"write","secrets":"write","workflows":"write"}' ;;
          esac

          SCOPE_VALID=true
          if [[ -n "$EXPECTED_SCOPE_REPO" ]]; then
            if [[ "$SELECTION" != "selected" ]]; then
              echo "Expected repository_selection=selected, got '${SELECTION}'" >&2
              SCOPE_VALID=false
            elif ! jq -e --arg want "$EXPECTED_SCOPE_REPO" \
              '.granted_repos == [$want]' mint-ap-result.json >/dev/null; then
              echo "Expected granted_repos to equal [$EXPECTED_SCOPE_REPO], got $REPOS" >&2
              SCOPE_VALID=false
            fi
          elif jq -e 'length == 1 and .[0] == "*"' <<<"${REPOS_JSON:-[]}" >/dev/null 2>&1 &&
            { [[ "$SELECTION" != "all" ]] || [[ "$REPOS" != "[]" ]]; }; then
            echo "Expected installation-wide scope with no explicit repositories, got selection=$SELECTION repos=$REPOS" >&2
            SCOPE_VALID=false
          fi
          if ! jq -e --argjson expected "$EXPECTED_PERMS" '.granted_permissions == $expected' \
            mint-ap-result.json >/dev/null; then
            echo "Granted permissions do not exactly match role $ROLE" >&2
            SCOPE_VALID=false
          fi
          if [[ "$SCOPE_VALID" != true ]]; then
            jq -n \
              --arg selection "$SELECTION" \
              --argjson repos "$REPOS" \
              --argjson perms "$PERMS" \
              '{outcome:"error",error:"scope",repository_selection:$selection,granted_repos:$repos,granted_permissions:$perms}' \
              > mint-ap-result.json
          fi
          echo "MINT_AP_RESULT=$(cat mint-ap-result.json)"
RUNSCRIPT
}

# Write the reusable workflow to a disposable branch removed by cleanup().
ensure_fullsend_reusable() {
  local full_repo="${ORG}/${WORKFLOW_REPO}"
  local work tmp
  echo "setup: ensuring reusable workflow on ${full_repo}" >&2
  work=$(mktemp -d "${TMPROOT}/reusable.XXXXXX")
  tmp=$(mktemp "${TMPROOT}/reusable-error.XXXXXX")
  register_branch "$full_repo" "$REUSABLE_BRANCH"
  (
    set -euo pipefail
    git -c "credential.helper=${GH_CRED_HELPER}" \
      clone --depth 1 --quiet "https://x-access-token@github.com/${full_repo}.git" "${work}/repo"
    cd "${work}/repo"
    git config credential.helper "${GH_CRED_HELPER}"
    git config user.email "mint-ap@localhost"
    git config user.name "mint-access-patterns"
    git config commit.gpgsign false
    mkdir -p .github/workflows
    {
      cat <<'HDR'
name: mint-ap-reusable
on:
  workflow_call:
    inputs:
      mint_url:
        required: true
        type: string
      role:
        required: true
        type: string
      repos_json:
        required: false
        type: string
        default: ""
      target_org:
        required: false
        type: string
        default: ""
      expected_scope_repo:
        required: false
        type: string
        default: ""
permissions:
  id-token: write
  contents: read
  actions: write
jobs:
  mint:
    runs-on: ubuntu-latest
    steps:
      - name: Mint via curl
        id: mint
        env:
          MINT_URL: ${{ inputs.mint_url }}
          ROLE: ${{ inputs.role }}
          REPOS_JSON: ${{ inputs.repos_json }}
          EXPECTED_SCOPE_REPO: ${{ inputs.expected_scope_repo }}
          TARGET_ORG: ${{ inputs.target_org }}
        run: |
HDR
      mint_curl_run_script
      cat <<'FTR'

      - name: Upload mint-ap result
        if: always()
        uses: actions/upload-artifact@v4
        with:
          name: mint-ap-result
          path: mint-ap-result.json
          if-no-files-found: ignore
FTR
    } >.github/workflows/mint-ap-reusable.yml
    git add .github/workflows/mint-ap-reusable.yml
    git commit --allow-empty -m "mint-ap: disposable reusable workflow" --quiet
    git push origin "HEAD:${REUSABLE_BRANCH}" --quiet
    echo "setup: pushed reusable workflow to ${full_repo}@${REUSABLE_BRANCH}" >&2
  ) 2>"${tmp}" || {
    echo "ERROR: failed to ensure reusable on ${full_repo}: $(tr '\n' ' ' <"${tmp}" | head -c 300)" >&2
    rm -rf "${work}" "${tmp}"
    exit 1
  }
  rm -rf "${work}" "${tmp}"
}

# Direct mint workflow (on: push) for .fullsend or in-repo-host negative.
# Arguments:
#   $1 destination directory
#   $2 mint role
#   $3 requested repositories as JSON, or "-" to omit repos
#   $4 expected fully-qualified repository scope, or empty
#   $5 optional target organization
write_direct_mint_workflow() {
  local dest_dir="$1"
  local role="$2"
  local repos_json="$3"
  local expected_scope_full="$4"
  local target_org="${5:-}"

  local repos_yaml
  if [[ "$repos_json" == "-" ]]; then
    repos_yaml='""'
  else
    repos_yaml="'${repos_json//\'/\'\'}'"
  fi

  mkdir -p "${dest_dir}/.github/workflows"
  {
    cat <<EOF
name: mint-ap
on: push
permissions:
  id-token: write
  contents: read
  actions: write
jobs:
  mint:
    runs-on: ubuntu-latest
    steps:
      - name: Mint via curl
        id: mint
        env:
          MINT_URL: '${MINT_URL//\'/\'\'}'
          ROLE: '${role//\'/\'\'}'
          REPOS_JSON: ${repos_yaml}
          EXPECTED_SCOPE_REPO: '${expected_scope_full//\'/\'\'}'
          TARGET_ORG: '${target_org//\'/\'\'}'
        run: |
EOF
    mint_curl_run_script
    cat <<'FTR'

      - name: Upload mint-ap result
        if: always()
        uses: actions/upload-artifact@v4
        with:
          name: mint-ap-result
          path: mint-ap-result.json
          if-no-files-found: ignore
FTR
  } >"${dest_dir}/.github/workflows/mint-ap.yml"
}

# Thin shim on mint-test that calls {org}/fullsend reusable.
# Arguments:
#   $1 destination directory
#   $2 mint role
#   $3 requested repositories as JSON, or "-" to omit repos
#   $4 expected fully-qualified repository scope, or empty
#   $5 optional target organization
write_shim_workflow() {
  local dest_dir="$1"
  local role="$2"
  local repos_json="$3"
  local expected_scope_full="$4"
  local target_org="${5:-}"

  local repos_input=""
  if [[ "$repos_json" != "-" ]]; then
    repos_input="${repos_json}"
  fi

  mkdir -p "${dest_dir}/.github/workflows"
  cat >"${dest_dir}/.github/workflows/mint-ap.yml" <<EOF
name: mint-ap
on: push
permissions:
  id-token: write
  contents: read
  actions: write
jobs:
  mint:
    uses: ${ORG}/${WORKFLOW_REPO}/.github/workflows/mint-ap-reusable.yml@${REUSABLE_BRANCH}
    with:
      mint_url: '${MINT_URL//\'/\'\'}'
      role: '${role//\'/\'\'}'
      repos_json: '${repos_input//\'/\'\'}'
      target_org: '${target_org//\'/\'\'}'
      expected_scope_repo: '${expected_scope_full//\'/\'\'}'
EOF
}

# Fetch result JSON for a completed run (artifact preferred, log fallback).
fetch_result_json() {
  local full_repo="$1" run_id="$2"
  local tmp attempt
  tmp=$(mktemp -d "${TMPROOT}/result.XXXXXX")

  for attempt in 1 2 3 4 5; do
    rm -rf "${tmp:?}"/*
    if gh run download "${run_id}" --repo "${full_repo}" -n mint-ap-result -D "${tmp}" &>/dev/null; then
      if [[ -f "${tmp}/mint-ap-result.json" ]]; then
        cat "${tmp}/mint-ap-result.json"
        rm -rf "${tmp}"
        return 0
      fi
    fi
    echo "result artifact unavailable (attempt ${attempt}/5); retrying" >&2
    sleep 2
  done

  local log line
  if log=$(gh run view "${run_id}" --repo "${full_repo}" --log 2>/dev/null); then
    line=$(grep -o 'MINT_AP_RESULT={.*}' <<<"$log" | tail -1 || true)
    if [[ -n "$line" ]]; then
      printf '%s\n' "${line#MINT_AP_RESULT=}"
      rm -rf "${tmp}"
      return 0
    fi
  fi
  rm -rf "${tmp}"
  return 1
}

wait_for_run() {
  local full_repo="$1" branch="$2" head_sha="$3"
  local deadline=$((SECONDS + TIMEOUT))
  local run_json run_id status

  while ((SECONDS < deadline)); do
    run_json=$(gh run list --repo "${full_repo}" --branch "${branch}" --limit 20 \
      --json databaseId,status,conclusion,url,headSha 2>/dev/null || echo '[]')
    run_id=$(jq -r --arg sha "$head_sha" '[.[] | select(.headSha == $sha)][0].databaseId // empty' <<<"$run_json")
    if [[ -n "$run_id" ]]; then
      break
    fi
    sleep "${POLL_INTERVAL}"
  done

  if [[ -z "${run_id:-}" ]]; then
    echo "timeout waiting for workflow run to appear" >&2
    return 1
  fi

  while ((SECONDS < deadline)); do
    if ! run_json=$(gh run view "${run_id}" --repo "${full_repo}" \
      --json databaseId,status,conclusion,url,headSha 2>/dev/null); then
      echo "transient error reading workflow run ${run_id}; retrying" >&2
      sleep "${POLL_INTERVAL}"
      continue
    fi
    if [[ "$(jq -r '.headSha // empty' <<<"$run_json")" != "$head_sha" ]]; then
      echo "workflow run ${run_id} has unexpected head SHA" >&2
      return 1
    fi
    status=$(jq -r '.status' <<<"$run_json")
    if [[ "$status" == "completed" ]]; then
      printf '%s\n' "$run_json"
      return 0
    fi
    sleep "${POLL_INTERVAL}"
  done

  echo "timeout waiting for workflow run ${run_id} to complete" >&2
  return 1
}

# --- mtest ---
# mtest <id> <role> <caller_repo> <repos_json|-> <expect> <scope_repo|-> [target_org|-] [host_style]
#
# host_style: shim (default) | direct | in-repo-host
# repos_json: JSON array, or "-" to omit the repos key (blank)
# expect:     minted | denied
# scope_repo: bare repo name required in granted_repos when minted, or "-" when denied
# target_org: optional cross-org mint target; omit or "-" for same-org
mtest() {
  local id="$1" role="$2" caller_repo="$3" repos_json="$4" expect="$5" scope_repo="$6"
  local target_org="${7:--}"
  local host_style="${8:-shim}"
  local label="${id}/${role}"
  local full_repo="${ORG}/${caller_repo}"
  local branch work tmp_err head_sha run_json run_id run_url conclusion actual result_json scope_rest
  local expected_scope_full=""
  local target_org_arg=""

  if [[ -n "$ROLE_FILTER" && "$role" != "$ROLE_FILTER" ]]; then
    return 0
  fi

  if [[ "$expect" != "minted" && "$expect" != "denied" ]]; then
    print_case_line ERROR "$label" "invalid expect=${expect}"
    COUNT_ERROR=$((COUNT_ERROR + 1))
    return 0
  fi

  if [[ "$target_org" != "-" && -n "$target_org" ]]; then
    target_org_arg="$target_org"
  fi

  if [[ "$expect" == "minted" && "$scope_repo" != "-" && -n "$scope_repo" ]]; then
    expected_scope_full="${ORG}/${scope_repo}"
  fi

  branch="mint-ap-${id}-${role}-$(rand_hex)"
  branch=$(tr -c 'a-zA-Z0-9._-' '-' <<<"$branch" | sed 's/-\+/-/g; s/^-//; s/-$//')

  work=$(mktemp -d "${TMPROOT}/case.XXXXXX")
  tmp_err=$(mktemp "${TMPROOT}/case-error.XXXXXX")

  if ! (
    set -euo pipefail
    git -c "credential.helper=${GH_CRED_HELPER}" \
      clone --depth 1 --quiet "https://x-access-token@github.com/${full_repo}.git" "${work}/repo"
    cd "${work}/repo"
    git config credential.helper "${GH_CRED_HELPER}"
    git checkout -b "${branch}" >/dev/null
    git config user.email "mint-ap@localhost"
    git config user.name "mint-access-patterns"
    git config commit.gpgsign false
    case "$host_style" in
      shim)
        write_shim_workflow "$(pwd)" "${role}" "${repos_json}" "${expected_scope_full}" "${target_org_arg}"
        ;;
      direct | in-repo-host)
        write_direct_mint_workflow "$(pwd)" "${role}" "${repos_json}" "${expected_scope_full}" "${target_org_arg}"
        ;;
      *)
        echo "invalid host_style=${host_style}" >&2
        exit 1
        ;;
    esac
    git add .github/workflows/mint-ap.yml
    git commit -m "mint-ap: ${id} ${role} ${host_style}" --quiet
    git push -u origin "HEAD:${branch}" --quiet
  ) 2>"${tmp_err}"; then
    print_case_line ERROR "$label" "push failed: $(tr '\n' ' ' <"${tmp_err}" | head -c 200)"
    COUNT_ERROR=$((COUNT_ERROR + 1))
    rm -rf "${work}" "${tmp_err}"
    return 0
  fi
  rm -f "${tmp_err}"
  head_sha=$(git -C "${work}/repo" rev-parse HEAD)
  register_branch "$full_repo" "$branch"

  if ! run_json=$(wait_for_run "${full_repo}" "${branch}" "$head_sha" 2>"${work}/wait.err"); then
    print_case_line ERROR "$label" "$(tr '\n' ' ' <"${work}/wait.err" | head -c 200)"
    COUNT_ERROR=$((COUNT_ERROR + 1))
    git -C "${work}/repo" push origin --delete "${branch}" --quiet 2>/dev/null || true
    unregister_branch "$full_repo" "$branch"
    rm -rf "${work}"
    return 0
  fi

  run_id=$(jq -r '.databaseId' <<<"$run_json")
  run_url=$(jq -r '.url' <<<"$run_json")
  conclusion=$(jq -r '.conclusion // empty' <<<"$run_json")

  if [[ "$conclusion" != "success" ]]; then
    print_case_line ERROR "$label" "workflow conclusion=${conclusion:-unknown}  ${run_url}"
    COUNT_ERROR=$((COUNT_ERROR + 1))
    git -C "${work}/repo" push origin --delete "${branch}" --quiet 2>/dev/null || true
    unregister_branch "$full_repo" "$branch"
    rm -rf "${work}"
    return 0
  fi

  result_json=$(fetch_result_json "${full_repo}" "${run_id}" 2>/dev/null || true)
  actual=$(jq -r '.outcome // empty' <<<"${result_json:-{}}" 2>/dev/null || true)
  if [[ "$actual" == "error" ]]; then
    print_case_line ERROR "$label" "mint workflow error=$(jq -r '.error // "unknown"' <<<"$result_json")  ${run_url}"
    COUNT_ERROR=$((COUNT_ERROR + 1))
    git -C "${work}/repo" push origin --delete "${branch}" --quiet 2>/dev/null || true
    unregister_branch "$full_repo" "$branch"
    rm -rf "${work}"
    return 0
  fi
  if [[ "$actual" != "minted" && "$actual" != "denied" ]]; then
    print_case_line ERROR "$label" "missing or invalid mint-ap-result artifact  ${run_url}"
    COUNT_ERROR=$((COUNT_ERROR + 1))
    git -C "${work}/repo" push origin --delete "${branch}" --quiet 2>/dev/null || true
    unregister_branch "$full_repo" "$branch"
    rm -rf "${work}"
    return 0
  fi

  scope_rest=""
  if [[ -n "${result_json}" ]] && jq -e 'has("repository_selection") or ((.granted_repos // []) | length) > 0' <<<"$result_json" &>/dev/null; then
    if [[ "$actual" == "minted" ]] || jq -e '.error == "scope"' <<<"$result_json" &>/dev/null; then
      scope_rest=$(format_scope "$result_json")
    fi
  fi

  git -C "${work}/repo" push origin --delete "${branch}" --quiet 2>/dev/null || true
  unregister_branch "$full_repo" "$branch"
  rm -rf "${work}"

  if [[ "$actual" == "$expect" ]]; then
    if [[ "$actual" == "minted" && -n "$scope_rest" ]]; then
      print_case_line PASS "$label" "minted  ${scope_rest}"
    else
      print_case_line PASS "$label" "$actual"
    fi
    COUNT_OK=$((COUNT_OK + 1))
  else
    local detail="expect=${expect} actual=${actual}"
    [[ -n "$scope_rest" ]] && detail+="  ${scope_rest}"
    detail+="  ${run_url}"
    print_case_line FAIL "$label" "$detail"
    COUNT_FAIL=$((COUNT_FAIL + 1))
  fi
}

# --- startup banner ---
echo "mint-access-patterns: mode=${MODE}"
echo "  org=${ORG} mint-url=${MINT_URL} foreign-org=${FOREIGN_ORG}"
if [[ -n "$GCP_PROJECT" ]]; then
  echo "  project=${GCP_PROJECT} region=${GCP_REGION}"
else
  echo "  enrollment: assumed preconfigured for mode=${MODE}"
fi
[[ -n "$ROLE_FILTER" ]] && echo "  role-filter=${ROLE_FILTER}"

# --- setup ---
ensure_repo ".fullsend"
ensure_repo "mint-test"
ensure_repo "mint-test-other"
ensure_repo "${WORKFLOW_REPO}"
ensure_fullsend_reusable

if [[ -n "$GCP_PROJECT" ]]; then
  configure_mint_enrollment
fi

# Mode-derived expects
shim_same_expect=denied
fullsend_tgt_expect=denied
foreign_wild_expect=denied
case "$MODE" in
  per-repo)
    shim_same_expect=minted
    fullsend_tgt_expect=denied
    foreign_wild_expect=minted
    ;;
  per-org)
    shim_same_expect=denied
    fullsend_tgt_expect=minted
    foreign_wild_expect=denied
    ;;
  both)
    shim_same_expect=minted
    fullsend_tgt_expect=minted
    foreign_wild_expect=minted
    ;;
esac

# Negative: mint job hosted in mint-test (must deny). host_style=in-repo-host
# id                 role         caller      repos_json              expect  scope   target  host_style
mtest in-repo-host   triage       mint-test   '["mint-test"]'         denied  -       -       in-repo-host
mtest in-repo-host   coder        mint-test   '["mint-test"]'         denied  -       -       in-repo-host
mtest in-repo-host   review       mint-test   '["mint-test"]'         denied  -       -       in-repo-host
mtest in-repo-host   retro        mint-test   '["mint-test"]'         denied  -       -       in-repo-host
mtest in-repo-host   prioritize   mint-test   '["mint-test"]'         denied  -       -       in-repo-host
mtest in-repo-host   fullsend     mint-test   '["mint-test"]'         denied  -       -       in-repo-host
mtest in-repo-host   e2e          mint-test   '["mint-test"]'         denied  -       -       in-repo-host

# Same-org via mint-test shim → {org}/fullsend reusable
# id                 role         caller      repos_json              expect                  scope      target  host_style
mtest blank-repos    triage       mint-test   -                      denied                  -          -       shim
mtest same-repo      triage       mint-test   '["mint-test"]'         "$shim_same_expect"    mint-test  -       shim
mtest wildcard       triage       mint-test   '["*"]'                 denied                  -          -       shim
mtest other-repo     triage       mint-test   '["mint-test-other"]'   denied                  -          -       shim
mtest fullsend-tgt   triage       .fullsend   '["mint-test"]'         "$fullsend_tgt_expect" mint-test  -       direct

mtest blank-repos    coder        mint-test   -                      denied                  -          -       shim
mtest same-repo      coder        mint-test   '["mint-test"]'         "$shim_same_expect"    mint-test  -       shim
mtest wildcard       coder        mint-test   '["*"]'                 denied                  -          -       shim
mtest other-repo     coder        mint-test   '["mint-test-other"]'   denied                  -          -       shim
mtest fullsend-tgt   coder        .fullsend   '["mint-test"]'         "$fullsend_tgt_expect" mint-test  -       direct

mtest blank-repos    review       mint-test   -                      denied                  -          -       shim
mtest same-repo      review       mint-test   '["mint-test"]'         "$shim_same_expect"    mint-test  -       shim
mtest wildcard       review       mint-test   '["*"]'                 denied                  -          -       shim
mtest other-repo     review       mint-test   '["mint-test-other"]'   denied                  -          -       shim
mtest fullsend-tgt   review       .fullsend   '["mint-test"]'         "$fullsend_tgt_expect" mint-test  -       direct

mtest blank-repos    retro        mint-test   -                      denied                  -          -       shim
mtest same-repo      retro        mint-test   '["mint-test"]'         "$shim_same_expect"    mint-test  -       shim
mtest wildcard       retro        mint-test   '["*"]'                 denied                  -          -       shim
mtest other-repo     retro        mint-test   '["mint-test-other"]'   denied                  -          -       shim
mtest fullsend-tgt   retro        .fullsend   '["mint-test"]'         "$fullsend_tgt_expect" mint-test  -       direct

mtest blank-repos    prioritize   mint-test   -                      denied                  -          -       shim
mtest same-repo      prioritize   mint-test   '["mint-test"]'         "$shim_same_expect"    mint-test  -       shim
mtest wildcard       prioritize   mint-test   '["*"]'                 denied                  -          -       shim
mtest other-repo     prioritize   mint-test   '["mint-test-other"]'   denied                  -          -       shim
mtest fullsend-tgt   prioritize   .fullsend   '["mint-test"]'         "$fullsend_tgt_expect" mint-test  -       direct

mtest blank-repos    fullsend     mint-test   -                      denied                  -          -       shim
mtest same-repo      fullsend     mint-test   '["mint-test"]'         "$shim_same_expect"    mint-test  -       shim
mtest wildcard       fullsend     mint-test   '["*"]'                 denied                  -          -       shim
mtest other-repo     fullsend     mint-test   '["mint-test-other"]'   denied                  -          -       shim
mtest fullsend-tgt   fullsend     .fullsend   '["mint-test"]'         "$fullsend_tgt_expect" mint-test  -       direct

# e2e foreign via mint-test shim → {org}/fullsend (.fullsend not used).
# per-org denies the shim host; per-repo/both allow it via WORKFLOW_HOST_REPOS.
mtest foreign-blank  e2e          mint-test   -                      denied                  -          "$FOREIGN_ORG" shim
mtest foreign-wild   e2e          mint-test   '["*"]'                 "$foreign_wild_expect"  -          "$FOREIGN_ORG" shim

# --- summary ---
echo "ok  ${COUNT_OK}"
if ((COUNT_FAIL > 0)); then
  echo "FAIL  ${COUNT_FAIL}"
fi
if ((COUNT_ERROR > 0)); then
  echo "ERROR ${COUNT_ERROR}"
fi

if ((COUNT_FAIL > 0 || COUNT_ERROR > 0)); then
  exit 1
fi
exit 0
