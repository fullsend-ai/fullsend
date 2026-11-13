#!/usr/bin/env bash
# pad-e2e-org-repos_test.sh — tests for hack/pad-e2e-org-repos.sh against a
# stubbed gh.
#
# Covers: a dry run creates nothing and stops where --max would; --apply creates filler repositories
# until the permanent count reaches the target; slot repositories
# (test-repo-NN and names starting with it) do not count; filler names
# already present are skipped and the numbering fills the gaps; an org at
# the target gets nothing; NN expands to halfsend-NN; --max caps the run
# across orgs; a "name already exists" answer (as real gh reports it: the
# detail in the JSON body on stdout, only the top-level message on stderr)
# is skipped and not counted against --max; a rate-limit
# answer stops the run with a non-zero exit; a permission 403 and a failed
# listing fail; filler repositories are created private; --target and --max
# with a leading zero are read in base 10; a trailing --target or --max and
# an org outside the pool are usage errors.
#
# Run from the repo root:
#   bash hack/pad-e2e-org-repos_test.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PAD="${SCRIPT_DIR}/pad-e2e-org-repos.sh"
FAILURES=0

pass() { echo "ok       $*"; }
fail() {
  echo "FAIL     $*" >&2
  FAILURES=$((FAILURES + 1))
}

SHIM_DIR=$(mktemp -d)
STATE=""
trap 'rm -rf "${SHIM_DIR}" "${STATE}"' EXIT

# --- gh: repositories kept as files under STUB_STATE ------------------------
# ${S}/<org>/<name>         repository exists
# ${S}/<org>/.private/<name> repository was created private
# ${S}/list_error           listing repositories fails with HTTP 500
# ${S}/exists_on_create     every create answers "name already exists"
# ${S}/ratelimit_after      create answers a secondary rate limit once this
#                           many repositories have been created
# ${S}/forbidden            every create answers HTTP 403 (no permission)
cat > "${SHIM_DIR}/gh" <<'EOF'
#!/usr/bin/env bash
S="${STUB_STATE}"
echo "gh $*" >> "${S}/gh.log"
[[ "${1:-}" == "api" ]] || { echo "unexpected gh $*" >&2; exit 2; }
shift
method=GET path="" name="" private=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -X) method="$2"; shift 2 ;;
    --jq) shift 2 ;;
    -f|-F)
      case "$2" in
        name=*) name="${2#name=}" ;;
        private=*) private="${2#private=}" ;;
      esac
      shift 2 ;;
    --silent|--paginate) shift ;;
    /*) path="${1%%\?*}"; shift ;;
    *) echo "unexpected arg $1" >&2; exit 2 ;;
  esac
done
org="${path#/orgs/}"; org="${org%%/*}"
case "${method} ${path}" in
  "GET /orgs/${org}/repos")
    [[ -f "${S}/list_error" ]] && { echo "gh: Server Error (HTTP 500)" >&2; exit 1; }
    [[ -d "${S}/${org}" ]] && ls "${S}/${org}"
    exit 0 ;;
  "POST /orgs/${org}/repos")
    if [[ -f "${S}/forbidden" ]]; then
      echo "gh: Resource not accessible by integration (HTTP 403)" >&2; exit 1
    fi
    if [[ -f "${S}/exists_on_create" ]]; then
      # Real gh: the JSON body goes to stdout, and stderr carries only the
      # top-level message.
      echo '{"message":"Repository creation failed.","errors":[{"resource":"Repository","code":"custom","field":"name","message":"name already exists on this account"}],"status":"422"}'
      echo "gh: Repository creation failed. (HTTP 422)" >&2; exit 1
    fi
    if [[ -f "${S}/ratelimit_after" ]]; then
      made=$(cat "${S}/created" 2>/dev/null || echo 0)
      if (( made >= $(cat "${S}/ratelimit_after") )); then
        echo "gh: You have exceeded a secondary rate limit (HTTP 403)" >&2; exit 1
      fi
    fi
    mkdir -p "${S}/${org}/.private"
    touch "${S}/${org}/${name}"
    [[ "${private}" == "true" ]] && touch "${S}/${org}/.private/${name}"
    echo $(( $(cat "${S}/created" 2>/dev/null || echo 0) + 1 )) > "${S}/created"
    exit 0 ;;
esac
echo "unexpected ${method} ${path}" >&2
exit 2
EOF
chmod +x "${SHIM_DIR}/gh"

# fresh_state ORG NAMES... seeds one org's repositories.
fresh_state() {
  rm -rf "${STATE}"
  STATE=$(mktemp -d)
  local org="$1"; shift
  mkdir -p "${STATE}/${org}"
  local n
  for n in "$@"; do touch "${STATE}/${org}/${n}"; done
}

# run_pad ARGS... runs the script; sets RC and OUT.
run_pad() {
  RC=0
  OUT=$(PATH="${SHIM_DIR}:${PATH}" STUB_STATE="${STATE}" FILLER_PACE_SECONDS=0 \
    bash "${PAD}" "$@" 2>&1) || RC=$?
}

# filler_names ORG prints the org's filler repository names, sorted.
filler_names() {
  local f
  for f in "${STATE}/$1"/quota-filler-*; do
    [[ -e "${f}" ]] && basename "${f}"
  done
  return 0
}
count_fillers() { filler_names "$1" | grep -c . || true; }
# count_files DIR prints how many files DIR holds.
count_files() {
  local f n=0
  for f in "$1"/*; do [[ -e "${f}" ]] && n=$((n + 1)); done
  echo "${n}"
}
creates() {
  if [[ -f "${STATE}/gh.log" ]]; then grep -c 'POST' "${STATE}/gh.log" || true; else echo 0; fi
}

# --- dry run creates nothing ------------------------------------------------
fresh_state halfsend-07 test-repo test-repo-fork e2e-lock
run_pad --target 10 07
if [[ ${RC} -eq 0 && "$(creates)" == 0 ]] && grep -q 'dry run: would create quota-filler-001 … quota-filler-007' <<< "${OUT}"; then
  pass "dry run reports the plan and creates nothing"
else
  fail "dry run: rc=${RC} creates=$(creates) out=${OUT}"
fi

# --- dry run honours --max across orgs --------------------------------------
fresh_state halfsend-07
mkdir -p "${STATE}/halfsend-08" "${STATE}/halfsend-09"
run_pad --target 3 --max 4 07 08 09
if [[ ${RC} -eq 0 && "$(creates)" == 0 ]] \
    && grep -q 'would create quota-filler-001 … quota-filler-003' <<< "${OUT}" \
    && grep -q 'would create quota-filler-001 … quota-filler-001' <<< "${OUT}" \
    && grep -q -- '--max 4 reached' <<< "${OUT}" && ! grep -q 'halfsend-09' <<< "${OUT}"; then
  pass "a dry run stops where --max would stop an --apply run"
else
  fail "dry run max: rc=${RC} creates=$(creates) out=${OUT}"
fi

# --- apply reaches the target; slot repos do not count ----------------------
fresh_state halfsend-07 test-repo test-repo-fork test-repo-03 test-repo-04-mint-host test-repo-05-fork
run_pad --apply --target 10 07
if [[ ${RC} -eq 0 && "$(count_fillers halfsend-07)" == 8 ]]; then
  pass "apply creates fillers up to the target, ignoring slot repos"
else
  fail "apply: rc=${RC} fillers=$(count_fillers halfsend-07) out=${OUT}"
fi
if [[ "$(count_files "${STATE}/halfsend-07/.private")" == 8 ]]; then
  pass "fillers are created private"
else
  fail "fillers not private: $(count_files "${STATE}/halfsend-07/.private")"
fi

# --- existing fillers are skipped and gaps filled ---------------------------
fresh_state halfsend-07 test-repo quota-filler-001 quota-filler-003
run_pad --apply --target 5 halfsend-07
# test-repo is permanent, so 3 exist and 2 are created: 002 and 004.
want="quota-filler-001 quota-filler-002 quota-filler-003 quota-filler-004"
got=$(filler_names halfsend-07 | tr '\n' ' ' | sed 's/ $//')
if [[ ${RC} -eq 0 && "${got}" == "${want}" && "$(creates)" == 2 ]]; then
  pass "existing fillers are skipped and numbering fills gaps"
else
  fail "gaps: rc=${RC} got=${got} creates=$(creates)"
fi

# --- second run is a no-op --------------------------------------------------
: > "${STATE}/gh.log"
run_pad --apply --target 5 halfsend-07
if [[ ${RC} -eq 0 && "$(creates)" == 0 ]] && grep -q 'nothing to do' <<< "${OUT}"; then
  pass "an org at the target gets nothing"
else
  fail "idempotent: rc=${RC} creates=$(creates) out=${OUT}"
fi

# --- --max caps across orgs -------------------------------------------------
fresh_state halfsend-07
mkdir -p "${STATE}/halfsend-08"
run_pad --apply --target 3 --max 4 07 08
if [[ ${RC} -eq 0 && "$(count_fillers halfsend-07)" == 3 && "$(count_fillers halfsend-08)" == 1 ]] \
    && grep -q 'stopped at --max 4' <<< "${OUT}"; then
  pass "--max caps the run across orgs"
else
  fail "max: rc=${RC} 07=$(count_fillers halfsend-07) 08=$(count_fillers halfsend-08) out=${OUT}"
fi

# --- name already exists is skipped and not counted against --max ----------
fresh_state halfsend-07
touch "${STATE}/exists_on_create"
run_pad --apply --target 2 --max 1 07
if [[ ${RC} -eq 0 ]] && grep -q 'quota-filler-002 already exists, skipped' <<< "${OUT}" \
    && grep -q 'created 0 repositories, skipped 2' <<< "${OUT}"; then
  pass "a name-already-exists answer is skipped and not counted against --max"
else
  fail "exists: rc=${RC} out=${OUT}"
fi

# --- leading zeros are base 10 ----------------------------------------------
fresh_state halfsend-07
run_pad --apply --target 010 --max 08 07
if [[ ${RC} -eq 0 && "$(count_fillers halfsend-07)" == 8 ]] && grep -q 'stopped at --max 8' <<< "${OUT}"; then
  pass "--target and --max with leading zeros are read in base 10"
else
  fail "leading zeros: rc=${RC} fillers=$(count_fillers halfsend-07) out=${OUT}"
fi

# --- rate limit stops the run -----------------------------------------------
fresh_state halfsend-07
echo 2 > "${STATE}/ratelimit_after"
run_pad --apply --target 5 07
if [[ ${RC} -ne 0 && "$(count_fillers halfsend-07)" == 2 ]] && grep -q 'run again later' <<< "${OUT}"; then
  pass "a rate-limit answer stops the run with a non-zero exit"
else
  fail "ratelimit: rc=${RC} fillers=$(count_fillers halfsend-07) out=${OUT}"
fi

# --- permission 403 fails ---------------------------------------------------
fresh_state halfsend-07
touch "${STATE}/forbidden"
run_pad --apply --target 2 07
if [[ ${RC} -ne 0 ]] && grep -q 'could not create halfsend-07/quota-filler-001' <<< "${OUT}" \
    && ! grep -q 'rate limited' <<< "${OUT}"; then
  pass "a permission 403 fails and is not reported as a rate limit"
else
  fail "forbidden: rc=${RC} out=${OUT}"
fi

# --- listing error fails ----------------------------------------------------
fresh_state halfsend-07
touch "${STATE}/list_error"
run_pad --apply --target 2 07
if [[ ${RC} -ne 0 && "$(creates)" == 0 ]] && grep -q 'could not list repositories in halfsend-07' <<< "${OUT}"; then
  pass "a failed listing fails without creating anything"
else
  fail "list error: rc=${RC} creates=$(creates) out=${OUT}"
fi

# --- usage errors -----------------------------------------------------------
fresh_state halfsend-07
run_pad --apply
if [[ ${RC} -ne 0 ]]; then pass "no org is a usage error"; else fail "no org: rc=${RC}"; fi
run_pad --target many 07
if [[ ${RC} -ne 0 ]]; then pass "a non-numeric target is rejected"; else fail "bad target: rc=${RC}"; fi
for flag in --target --max; do
  run_pad 07 "${flag}"
  if [[ ${RC} -ne 0 ]] && grep -q -- "${flag} needs a value" <<< "${OUT}"; then
    pass "a trailing ${flag} is a usage error"
  else
    fail "trailing ${flag}: rc=${RC} out=${OUT}"
  fi
done
for bad in my-org halfsend-07x ../halfsend-07 7; do
  run_pad --apply "${bad}"
  if [[ ${RC} -ne 0 && "$(creates)" == 0 ]] && grep -q 'is not a pool org' <<< "${OUT}"; then
    pass "org '${bad}' is rejected"
  else
    fail "org '${bad}': rc=${RC} out=${OUT}"
  fi
done
run_pad halfsend
if [[ ${RC} -eq 0 ]]; then pass "the STAGE org halfsend is accepted"; else fail "halfsend: rc=${RC} out=${OUT}"; fi

if (( FAILURES > 0 )); then
  echo "${FAILURES} failure(s)" >&2
  exit 1
fi
echo "all pad-e2e-org-repos tests passed"
