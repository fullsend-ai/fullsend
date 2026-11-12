#!/usr/bin/env bash
# cancel-superseded-merge-group-runs-test.sh — Tests for
# cancel-superseded-merge-group-runs.sh with a stubbed gh and canned JSON.
#
# Run from repo root: bash scripts/cancel-superseded-merge-group-runs-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CANCEL="${SCRIPT_DIR}/cancel-superseded-merge-group-runs.sh"
FAILURES=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

MOCK_BIN="${TMPDIR}/bin"
FIX="${TMPDIR}/fixtures"
GH_LOG="${TMPDIR}/gh.log"
mkdir -p "${MOCK_BIN}"

REAL_JQ="$(command -v jq)"

# The stub applies the script's --jq expression to each canned page with the
# real jq, as gh does, and returns pages after the first only with --paginate.
# GET git/ref/heads/<branch> answers like gh api --include: a status line on
# stdout, exit 1 and "gh: ... (HTTP n)" on stderr for 4xx/5xx. The default
# answer is 404; REF_CODES="branch=code ..." overrides it per branch, where
# code "net" prints nothing and "junk" prints an unparseable first line.
cat >"${MOCK_BIN}/gh" <<'EOF'
#!/usr/bin/env bash
paginate=false
include=false
method=GET
path=""
expr="."
[[ "${1:-}" == "api" ]] || { echo "unexpected gh call: $*" >&2; exit 1; }
shift
while [[ $# -gt 0 ]]; do
  case "$1" in
    --paginate) paginate=true ;;
    -i | --include) include=true ;;
    -X) method="$2"; shift ;;
    --jq) expr="$2"; shift ;;
    repos/*) path="$1" ;;
    *) echo "unexpected gh argument: $1" >&2; exit 1 ;;
  esac
  shift
done
echo "${method} ${path}" >>"${GH_LOG}"

emit() {
  local f
  for f in "${FIX}/$1"_page*.json; do
    [[ -f "${f}" ]] || continue
    jq -rc "${expr}" "${f}" || exit 1
    [[ "${paginate}" == "true" ]] || break
  done
}

if [[ "${RUNS_FAIL:-}" == "1" && "${method}:${path}" == GET:*/actions/runs\?* ]]; then
  echo "gh: Server Error (HTTP 502)" >&2
  exit 1
fi

case "${method}:${path}" in
  GET:*/actions/runs\?*status=in_progress*) emit runs_in_progress ;;
  GET:*/actions/runs\?*status=queued*) emit runs_queued ;;
  GET:*/actions/runs\?*status=waiting*) emit runs_waiting ;;
  GET:*/actions/runs\?*status=requested*) emit runs_requested ;;
  GET:*/actions/runs\?*status=pending*) emit runs_pending ;;
  GET:*/git/matching-refs/heads/gh-readonly-queue/)
    if [[ "${REFS_FAIL:-}" == "1" ]]; then
      echo "gh: Server Error (HTTP 502)" >&2
      exit 1
    fi
    emit refs
    ;;
  GET:*/git/ref/heads/*)
    [[ "${include}" == "true" ]] || { echo "confirm GET without --include" >&2; exit 1; }
    branch="${path#*/git/ref/heads/}"
    code=404
    for entry in ${REF_CODES:-}; do
      [[ "${entry%=*}" == "${branch}" ]] && code="${entry##*=}"
    done
    case "${code}" in
      net) echo "gh: dial tcp: i/o timeout" >&2; exit 1 ;;
      junk) echo "garbage"; exit 1 ;;
      200) printf 'HTTP/2.0 200 OK\r\nContent-Type: application/json\r\n\r\n{"ref":"refs/heads/%s"}' "${branch}"; exit 0 ;;
      404) printf 'HTTP/2.0 404 Not Found\r\nContent-Type: application/json\r\n\r\n{"message":"Not Found","status":"404"}'
        echo "gh: Not Found (HTTP 404)" >&2; exit 1 ;;
      *) printf 'HTTP/2.0 %s Error\r\n\r\n{}' "${code}"
        echo "gh: Error (HTTP ${code})" >&2; exit 1 ;;
    esac
    ;;
  POST:*/actions/runs/*/cancel)
    id="${path#*/actions/runs/}"
    id="${id%/cancel}"
    if [[ " ${CANCEL_FAIL_IDS:-} " == *" ${id} "* ]]; then
      echo "gh: Cannot cancel a workflow run that is completed. (HTTP 409)" >&2
      exit 1
    fi
    echo '{}'
    ;;
  *)
    echo "unexpected gh call: ${method} ${path}" >&2
    exit 1
    ;;
esac
EOF
chmod +x "${MOCK_BIN}/gh"

# jq wrapper: JQ_FAIL_MERGE=1 makes the script's run-listing merge step fail.
cat >"${MOCK_BIN}/jq" <<'EOF'
#!/usr/bin/env bash
if [[ "${JQ_FAIL_MERGE:-}" == "1" && "$*" == *"unique_by(.id)"* ]]; then
  echo "jq: error: simulated failure" >&2
  exit 5
fi
exec "${REAL_JQ}" "$@"
EOF
chmod +x "${MOCK_BIN}/jq"

export PATH="${MOCK_BIN}:${PATH}"
export GH_LOG FIX REAL_JQ
export GH_TOKEN="test..."
export GITHUB_REPOSITORY="fullsend-ai/fullsend"
export GITHUB_RUN_ID="900"
NOW=1800000000
export NOW_EPOCH="${NOW}"

R="repos/fullsend-ai/fullsend"
Q="gh-readonly-queue/main"
OWN="${Q}/pr-1-aaaa"
LIVE="${Q}/pr-2-bbbb"
STALE="${Q}/pr-2-0000"
STALE2="${Q}/pr-3-0000"

check() {
  local name="$1"
  shift
  if "$@"; then
    echo "PASS: ${name}"
  else
    echo "FAIL: ${name}"
    FAILURES=$((FAILURES + 1))
  fi
}
logged() { grep -qxF -- "$1" "${GH_LOG}"; }
absent() { ! grep -qxF -- "$1" "${GH_LOG}"; }
no_post() { ! grep -q '^POST ' "${GH_LOG}"; }
says() { grep -qF -- "$1" <<<"${out}"; }

reset() {
  rm -rf "${FIX}"
  mkdir -p "${FIX}"
  : >"${GH_LOG}"
  unset REFS_FAIL RUNS_FAIL REF_CODES JQ_FAIL_MERGE CANCEL_FAIL_IDS DRY_RUN OWN_REF OWN_BRANCH
}

# refs FILE BRANCH... — one page of matching-refs entries.
refs() {
  local file="$1"
  shift
  printf '%s\n' "$@" | jq -R '{ref: ("refs/heads/" + .)}' | jq -s . >"${FIX}/${file}"
}

# runs FILE "ID EVENT BRANCH [AGE_SECONDS]" ... — one page of workflow runs,
# created AGE_SECONDS (default 600) before NOW.
runs() {
  local file="$1" spec id event branch age
  shift
  for spec in "$@"; do
    age=""
    read -r id event branch age <<<"${spec}"
    jq -n --argjson id "${id}" --arg e "${event}" --arg b "${branch}" \
      --argjson c "$((NOW - ${age:-600}))" \
      '{id: $id, name: "E2E Tests", event: $e, head_branch: $b,
        created_at: ($c | todate), status: "in_progress"}'
  done | jq -s '{total_count: length, workflow_runs: .}' >"${FIX}/${file}"
}

# raw_runs FILE JSON_ARRAY — one page of workflow runs given verbatim.
raw_runs() {
  jq '{total_count: length, workflow_runs: .}' <<<"$2" >"${FIX}/$1"
}

run_script() {
  rc=0
  out="$(bash "${CANCEL}" 2>"${TMPDIR}/err")" || rc=$?
}

# --- Stale run cancelled, live run kept ---
reset
refs refs_page1.json "${OWN}" "${LIVE}"
runs runs_in_progress_page1.json "101 merge_group ${STALE}" "102 merge_group ${LIVE}"
runs runs_queued_page1.json "103 merge_group ${STALE2}"
export OWN_REF="refs/heads/${OWN}"
run_script
check "stale/live: exits 0" test "${rc}" -eq 0
check "stale/live: stale in-progress run cancelled" logged "POST ${R}/actions/runs/101/cancel"
check "stale/live: stale queued run cancelled" logged "POST ${R}/actions/runs/103/cancel"
check "stale/live: live run kept" absent "POST ${R}/actions/runs/102/cancel"
check "stale/live: cancelled line printed" says "cancelled: run 101 (E2E Tests) on ${STALE}"
check "stale/live: kept line printed" says "kept: run 102 (E2E Tests) on ${LIVE}: branch exists"
check "stale/live: runs listed before branches" \
  test "$(grep -n 'actions/runs?' "${GH_LOG}" | tail -1 | cut -d: -f1)" \
  -lt "$(grep -n 'matching-refs' "${GH_LOG}" | cut -d: -f1)"

# --- OWN_BRANCH works as well as OWN_REF ---
reset
refs refs_page1.json "${OWN}"
runs runs_in_progress_page1.json "101 merge_group ${STALE}"
export OWN_BRANCH="${OWN}"
run_script
check "own branch: OWN_BRANCH accepted" logged "POST ${R}/actions/runs/101/cancel"

# --- Empty branch list cancels nothing ---
reset
echo '[]' >"${FIX}/refs_page1.json"
runs runs_in_progress_page1.json "101 merge_group ${STALE}"
export OWN_BRANCH="${OWN}"
run_script
check "empty list: exits 0" test "${rc}" -eq 0
check "empty list: no cancel" no_post
check "empty list: says why" says "branch listing is empty"

# --- Own branch missing from the list cancels nothing ---
reset
refs refs_page1.json "${LIVE}"
runs runs_in_progress_page1.json "101 merge_group ${STALE}"
export OWN_BRANCH="${OWN}"
run_script
check "own missing: exits 0" test "${rc}" -eq 0
check "own missing: no cancel" no_post
check "own missing: says why" says "is not in the branch listing"

# --- Listing call fails: nothing cancelled ---
reset
refs refs_page1.json "${OWN}"
runs runs_in_progress_page1.json "101 merge_group ${STALE}"
export OWN_BRANCH="${OWN}" REFS_FAIL=1
run_script
check "list fails: exits 0" test "${rc}" -eq 0
check "list fails: no cancel" no_post
check "list fails: says why" says "branches failed"

# --- Non-merge_group run with a stale-looking branch is never cancelled ---
reset
refs refs_page1.json "${OWN}"
runs runs_in_progress_page1.json "104 pull_request ${STALE}" "105 push ${STALE2}"
export OWN_BRANCH="${OWN}"
run_script
check "non-merge_group: exits 0" test "${rc}" -eq 0
check "non-merge_group: no cancel" no_post
check "non-merge_group: kept line" says "kept: run 104 (E2E Tests) on ${STALE}: event pull_request is not merge_group"

# --- The current run is never cancelled ---
reset
refs refs_page1.json "${OWN}"
runs runs_in_progress_page1.json "900 merge_group ${STALE}" "101 merge_group ${STALE2}"
export OWN_BRANCH="${OWN}"
run_script
check "current run: not cancelled" absent "POST ${R}/actions/runs/900/cancel"
check "current run: other stale run cancelled" logged "POST ${R}/actions/runs/101/cancel"
check "current run: kept line" says "kept: run 900 (E2E Tests) on ${STALE}: current run"

# --- Failed cancel does not fail the script; later runs are still tried ---
reset
refs refs_page1.json "${OWN}"
runs runs_in_progress_page1.json "101 merge_group ${STALE}" "102 merge_group ${STALE2}"
export OWN_BRANCH="${OWN}" CANCEL_FAIL_IDS="101"
run_script
check "cancel fails: exits 0" test "${rc}" -eq 0
check "cancel fails: failure logged" says "cancel-failed: run 101 (E2E Tests) on ${STALE}"
check "cancel fails: next run attempted" logged "POST ${R}/actions/runs/102/cancel"
check "cancel fails: next run cancelled" says "cancelled: run 102 (E2E Tests) on ${STALE2}"

# --- DRY_RUN cancels nothing ---
reset
refs refs_page1.json "${OWN}"
runs runs_in_progress_page1.json "101 merge_group ${STALE}"
export OWN_BRANCH="${OWN}" DRY_RUN=1
run_script
check "dry run: exits 0" test "${rc}" -eq 0
check "dry run: no POST" no_post
check "dry run: would-cancel line" says "would-cancel: run 101 (E2E Tests) on ${STALE}"
check "dry run: confirm GET still made" logged "GET ${R}/git/ref/heads/${STALE}"

# --- Pagination: runs and branches on a second page are seen ---
reset
refs refs_page1.json "${Q}/pr-9-1111"
refs refs_page2.json "${OWN}" "${LIVE}"
runs runs_in_progress_page1.json "101 merge_group ${STALE}"
runs runs_in_progress_page2.json "102 merge_group ${LIVE}" "106 merge_group ${STALE2}"
export OWN_BRANCH="${OWN}"
run_script
check "pagination: exits 0" test "${rc}" -eq 0
check "pagination: page-1 stale run cancelled" logged "POST ${R}/actions/runs/101/cancel"
check "pagination: page-2 stale run cancelled" logged "POST ${R}/actions/runs/106/cancel"
check "pagination: page-2 live run kept" absent "POST ${R}/actions/runs/102/cancel"

# --- A run listed under both statuses is handled once ---
reset
refs refs_page1.json "${OWN}"
runs runs_in_progress_page1.json "101 merge_group ${STALE}"
runs runs_queued_page1.json "101 merge_group ${STALE}"
export OWN_BRANCH="${OWN}"
run_script
check "dedupe: one cancel call" test "$(grep -c "POST ${R}/actions/runs/101/cancel" "${GH_LOG}")" -eq 1

# --- Every unfinished status is listed, earliest state first ---
reset
refs refs_page1.json "${OWN}"
runs runs_requested_page1.json "107 merge_group ${STALE}"
runs runs_waiting_page1.json "108 merge_group ${STALE}"
runs runs_pending_page1.json "109 merge_group ${STALE}"
export OWN_BRANCH="${OWN}"
run_script
check "statuses: stale requested run cancelled" logged "POST ${R}/actions/runs/107/cancel"
check "statuses: stale waiting run cancelled" logged "POST ${R}/actions/runs/108/cancel"
check "statuses: stale pending run cancelled" logged "POST ${R}/actions/runs/109/cancel"
check "statuses: queued is listed before in_progress" \
  test "$(grep -n 'status=queued' "${GH_LOG}" | head -1 | cut -d: -f1)" \
  -lt "$(grep -n 'status=in_progress' "${GH_LOG}" | head -1 | cut -d: -f1)"

# --- Confirm GET: only a definite 404 cancels ---
reset
refs refs_page1.json "${OWN}"
runs runs_in_progress_page1.json \
  "101 merge_group ${Q}/pr-11-0000" "102 merge_group ${Q}/pr-12-0000" \
  "103 merge_group ${Q}/pr-13-0000" "104 merge_group ${Q}/pr-14-0000" \
  "105 merge_group ${Q}/pr-15-0000" "106 merge_group ${Q}/pr-16-0000" \
  "107 merge_group ${Q}/pr-17-0000"
export OWN_BRANCH="${OWN}" REF_CODES="${Q}/pr-11-0000=200 ${Q}/pr-12-0000=404 \
${Q}/pr-13-0000=500 ${Q}/pr-14-0000=403 ${Q}/pr-15-0000=net ${Q}/pr-16-0000=junk \
${Q}/pr-17-0000=429"
run_script
check "confirm: exits 0" test "${rc}" -eq 0
check "confirm: GET made for a listing miss" logged "GET ${R}/git/ref/heads/${Q}/pr-11-0000"
check "confirm 200: kept" absent "POST ${R}/actions/runs/101/cancel"
check "confirm 200: says why" says "kept: run 101 (E2E Tests) on ${Q}/pr-11-0000: branch exists (confirm GET 200)"
check "confirm 404: cancelled" logged "POST ${R}/actions/runs/102/cancel"
check "confirm 404: says why" says "cancelled: run 102 (E2E Tests) on ${Q}/pr-12-0000: branch deleted (confirm GET 404)"
check "confirm 500: kept" absent "POST ${R}/actions/runs/103/cancel"
check "confirm 500: says why" says "run 103 (E2E Tests) on ${Q}/pr-13-0000: branch not confirmed gone (confirm GET status 500)"
check "confirm 403: kept" absent "POST ${R}/actions/runs/104/cancel"
check "confirm network error: kept" absent "POST ${R}/actions/runs/105/cancel"
check "confirm network error: says why" says "run 105 (E2E Tests) on ${Q}/pr-15-0000: branch not confirmed gone (confirm GET status none)"
check "confirm unparseable: kept" absent "POST ${R}/actions/runs/106/cancel"
check "confirm 429: kept" absent "POST ${R}/actions/runs/107/cancel"

# --- Runs younger than 120 seconds are kept without a confirm GET ---
reset
refs refs_page1.json "${OWN}"
runs runs_in_progress_page1.json "101 merge_group ${STALE} 119" "102 merge_group ${STALE2} 120"
export OWN_BRANCH="${OWN}"
run_script
check "too new: exits 0" test "${rc}" -eq 0
check "too new: 119s run kept" absent "POST ${R}/actions/runs/101/cancel"
check "too new: no confirm GET" absent "GET ${R}/git/ref/heads/${STALE}"
check "too new: says why" says "kept: run 101 (E2E Tests) on ${STALE}: too new"
check "too new: 120s run cancelled" logged "POST ${R}/actions/runs/102/cancel"

# --- Missing or unparseable created_at keeps the run ---
reset
refs refs_page1.json "${OWN}"
raw_runs runs_in_progress_page1.json "[
  {\"id\": 101, \"name\": \"E2E Tests\", \"event\": \"merge_group\", \"head_branch\": \"${STALE}\"},
  {\"id\": 102, \"name\": \"E2E Tests\", \"event\": \"merge_group\", \"head_branch\": \"${STALE2}\", \"created_at\": \"yesterday\"}]"
export OWN_BRANCH="${OWN}"
run_script
check "no created_at: exits 0" test "${rc}" -eq 0
check "no created_at: no cancel" no_post
check "no created_at: says why" says "kept: run 101 (E2E Tests) on ${STALE}: no usable created_at"

# --- Empty or null head_branch with a branch-looking name is kept ---
reset
refs refs_page1.json "${OWN}"
raw_runs runs_in_progress_page1.json "[
  {\"id\": 101, \"name\": \"${STALE}\", \"event\": \"merge_group\", \"head_branch\": \"\", \"created_at\": \"2020-01-01T00:00:00Z\"},
  {\"id\": 102, \"name\": \"${STALE2}\", \"event\": \"merge_group\", \"head_branch\": null, \"created_at\": \"2020-01-01T00:00:00Z\"}]"
export OWN_BRANCH="${OWN}"
run_script
check "empty head_branch: exits 0" test "${rc}" -eq 0
check "empty head_branch: no cancel" no_post
check "empty head_branch: kept as non-queue" says "kept: run 101 (${STALE}) on : not a merge-queue branch"
check "null head_branch: kept as non-queue" says "kept: run 102 (${STALE2}) on : not a merge-queue branch"

# --- Control characters in a run name cannot start a workflow command ---
reset
refs refs_page1.json "${OWN}"
raw_runs runs_in_progress_page1.json "[
  {\"id\": 101, \"name\": \"x\\n::error::injected\\r\\n::warning::too\\tz\", \"event\": \"merge_group\", \"head_branch\": \"${STALE}\", \"created_at\": \"2020-01-01T00:00:00Z\"}]"
export OWN_BRANCH="${OWN}"
run_script
check "control chars: exits 0" test "${rc}" -eq 0
check "control chars: no line starts with ::error::" test -z "$(grep '^::error::' <<<"${out}")"
check "control chars: no line starts with ::warning::" test -z "$(grep '^::warning::' <<<"${out}")"
check "control chars: exactly one output line" test "$(wc -l <<<"${out}")" -eq 1
check "control chars: run still handled" logged "POST ${R}/actions/runs/101/cancel"

# --- Runs listing fails: nothing cancelled, no branch listing ---
reset
refs refs_page1.json "${OWN}"
runs runs_in_progress_page1.json "101 merge_group ${STALE}"
export OWN_BRANCH="${OWN}" RUNS_FAIL=1
run_script
check "runs list fails: exits 0" test "${rc}" -eq 0
check "runs list fails: no cancel" no_post
check "runs list fails: says why" says "merge_group runs failed"
check "runs list fails: no confirm GET" test -z "$(grep 'git/ref/' "${GH_LOG}")"

# --- Merging the run listings fails: nothing cancelled, exits 0 ---
reset
refs refs_page1.json "${OWN}"
runs runs_in_progress_page1.json "101 merge_group ${STALE}"
export OWN_BRANCH="${OWN}" JQ_FAIL_MERGE=1
run_script
check "merge fails: exits 0" test "${rc}" -eq 0
check "merge fails: no cancel" no_post
check "merge fails: says why" says "merging the requested run listing failed"

# --- Missing configuration exits 2 without calling gh ---
reset
run_script
check "no own branch: exits 2" test "${rc}" -eq 2
check "no own branch: no gh call" test ! -s "${GH_LOG}"

if [[ "${FAILURES}" -gt 0 ]]; then
  echo "${FAILURES} test(s) failed" >&2
  exit 1
fi
echo "All cancel-superseded-merge-group-runs tests passed"
