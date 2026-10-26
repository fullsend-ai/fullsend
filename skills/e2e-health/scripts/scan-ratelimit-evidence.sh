#!/usr/bin/env bash
# scan-ratelimit-evidence — evaluate #6702's rate-limit closure criterion over
# a window of e2e.yml behaviour runs on main (design D3-D5 of the
# instrument-github-rate-limits change). Read-only: gh GETs only.
#
# Usage: skills/e2e-health/scripts/scan-ratelimit-evidence.sh [--runs N]
#            [--repo OWNER/NAME]
#   --runs N     scan the N most recent completed e2e.yml runs on main whose
#                behaviour job ran (integer 1-100, default 20). Runs whose
#                behaviour job was skipped in every attempt (for example
#                workflow_dispatch runs) contribute no evidence and do not
#                count toward the window; a thinned window is visible in the
#                report (window vs runs_scanned), never silent.
#   --repo R     repository to scan, OWNER/NAME (default: the repository gh
#                resolves from the checkout — the upstream remote before
#                the fork's origin)
#
# Per kept run, every attempt is walked via the actions attempts API
# (runs/{run_id}/attempts/{n}); each attempt's behaviour-job logs are fetched
# and scanned for the blind-wait diagnostic markers, and conclusions are
# resolved from run and attempt metadata, never from log parsing. The latest
# attempt's conclusion is the run-listing conclusion (the run object is that
# attempt's state); earlier attempts come from GET .../attempts/{n}.
#
# Output (stdout carries only the machine-readable report, so
# `script > report.json` captures it cleanly; everything human-facing —
# progress, warnings, the summary table — goes to stderr):
#   stdout  the JSON report: repository, workflow, branch, window,
#           runs_scanned, incomplete, one entry per run (conclusion,
#           per-attempt markers with snippets, derived has_markers and
#           ultimately_passed), and the verdict at both signal levels
#   stderr  per-run progress, log-fetch and analyzer warnings, the summary table
#
# Exit codes: 0 scan complete; 1 the scan failed, or the report is incomplete
# because a behaviour-job log could not be fetched or analyzed (the verdict
# may undercount — see the per-attempt log_error entries in the report).
#
# Marker contract — the production strings of harnessTimeoutDiagnostics
# (pkg/behaviourtest/drivers/ci/githubactions/githubactions.go), reported as:
#   run-listing-failed   "run listing failed:"
#   same-error-polls     "(same error on N of M polls during the wait)"
#   failed-lookups       "(failed on N of M lookups during the wait; last: …)"
# The poll tails render the same-error shape only when identical poll errors
# repeat, so the poll near-miss "(failed on N of M polls during the wait;
# last: …)" is deliberately not a marker: the criterion targets the
# blind-wait signature of identical repetition. The lookup tail always
# renders the counted-failures shape — the diagnostics pass no current
# lookup error, so the identical-repetition variant cannot appear for
# lookups — and that counted shape is itself the rate-limit evidence.
#
# The verdict reports both signal levels from one scan: runs with markers
# (the strict reading of the closure criterion) and runs with markers that
# ultimately passed (retry-masked occurrences, the lenient reading's input).
# Either reading is applied to the same report without re-scanning.
#
# Structure: the analysis layer (analyze_log, build_attempt_record,
# build_run_evidence, verdict_from_runs, render_report, render_table) is
# pure — log text and JSON in, JSON and text out, no network — so the paired
# test drives it from fixture logs. Only scan_run and main talk to GitHub.
set -euo pipefail

# --- Output helpers ---------------------------------------------------------
# Everything the operator reads goes to stderr; stdout is reserved for the
# JSON report so command substitution and redirection stay clean (see
# docs/contributing/shell-scripting.md, stdout contamination).

usage() {
  sed -n '2,/^$/{s/^# \{0,1\}//;p;}' "$0"
}

die() {
  printf 'scan-ratelimit-evidence: %s\n' "$*" >&2
  exit 1
}

say() {
  printf '%s\n' "$*" >&2
}

# --- Analysis layer (pure; no network, no globals) --------------------------

# analyze_log — stdin: one behaviour-job log; stdout: a JSON array of
# {marker, snippet}, one entry per marker hit in log order. A single line can
# carry several hits (a run-listing failure whose describe() tail repeats on
# the same line, or several wait sections concatenated), so every check runs
# and each marker match loops. Snippets are bounded windows (60 chars
# before the match through 110 after) so long diagnostic lines stay
# readable; tabs are collapsed so the TSV hand-off to jq holds.
analyze_log() {
  awk '
    {
      line = $0
      gsub(/\r/, "", line)
      at = index(line, "run listing failed:")
      if (at > 0)
        emit("run-listing-failed", line, at)
      pos = 1
      while (match(substr(line, pos), /\(same error on [0-9]+ of [0-9]+ polls during the wait\)/)) {
        at = pos + RSTART - 1
        emit("same-error-polls", line, at)
        pos = at + RLENGTH
      }
      # The last: error text of the lookup tail can itself contain parens,
      # so this match stops at the counted-units phrase rather than
      # requiring the closing paren of the full parenthetical.
      pos = 1
      while (match(substr(line, pos), /\(failed on [0-9]+ of [0-9]+ lookups during the wait/)) {
        at = pos + RSTART - 1
        emit("failed-lookups", line, at)
        pos = at + RLENGTH
      }
    }
    function emit(kind, line, at) {
      from = at - 60
      prefix = from > 1 ? "..." : ""
      if (from < 1) from = 1
      snip = prefix substr(line, from, 170)
      if (length(line) > from + 169) snip = snip "..."
      gsub(/\t/, " ", snip)
      printf "%s\t%s\n", kind, snip
    }
  ' | jq -Rn '[inputs | split("\t") | {marker: .[0], snippet: (.[1] // "")}]'
}

# jobs_page_shape_ok FILE — exit 0 when FILE holds a well-formed jobs-page
# envelope: an object whose jobs field is an array and whose total_count is
# a number. GitHub returns this shape for every successful jobs listing; a
# 200 response carrying an error envelope ({"message": …}) fails here, so
# the scan dies instead of classifying the attempt's behaviour job as
# absent and silently excluding the run. Pure: file in, exit status out.
jobs_page_shape_ok() {
  jq -e 'type == "object" and (.jobs | type) == "array" and (.total_count | type) == "number"' "$1" >/dev/null
}

# build_attempt_record ATTEMPT CONCLUSION JOB_STATE MARKERS_JSON [LOG_ERROR]
# — stdout: one attempt record. JOB_STATE is "ran", "skipped" (the behaviour
# job was filtered out for this attempt), or "absent" (no behaviour job in
# the attempt's listing). LOG_ERROR is set only when the log fetch failed,
# which means the markers are unknown, not empty.
build_attempt_record() {
  jq -n --argjson attempt "$1" --arg conclusion "$2" --arg behaviour_job "$3" \
      --argjson markers "$4" --arg log_error "${5:-}" '
    {
      attempt: $attempt,
      conclusion: ($conclusion | if . == "" then null else . end),
      behaviour_job: $behaviour_job,
      markers: $markers
    } + (if $log_error == "" then {} else {log_error: $log_error} end)'
}

# build_run_evidence — stdin: {"run": <run-listing row>, "attempts":
# [attempt records]}; stdout: the run's evidence entry with the derived
# flags the verdict reads. The run's ultimate conclusion comes from the
# listing row; per-attempt conclusions come from attempt metadata.
build_run_evidence() {
  jq '{
    run_id: .run.databaseId,
    url: .run.url,
    created_at: .run.createdAt,
    display_title: .run.displayTitle,
    head_sha: .run.headSha,
    conclusion: .run.conclusion,
    attempts: .attempts,
    has_markers: any(.attempts[]; (.markers | length) > 0),
    ultimately_passed: (.run.conclusion == "success")
  }'
}

# verdict_from_runs — stdin: the runs evidence array; stdout: the verdict at
# both signal levels. Computed from per-run evidence so either reading of
# the closure criterion applies to the same report without re-scanning.
verdict_from_runs() {
  jq '{
    runs_scanned: length,
    runs_with_markers: (map(select(.has_markers)) | length),
    runs_with_markers_that_passed: (map(select(.has_markers and .ultimately_passed)) | length)
  }'
}

# render_report REPO WINDOW — stdin: the runs evidence array; stdout: the
# JSON report. Buffers stdin once because the verdict is derived from the
# same array the report embeds.
render_report() {
  local repo="$1" window="$2"
  local runs_json verdict
  runs_json=$(cat)
  verdict=$(printf '%s' "${runs_json}" | verdict_from_runs)
  printf '%s' "${runs_json}" | jq \
    --arg repository "${repo}" --argjson window "${window}" --argjson verdict "${verdict}" '
    {
      repository: $repository,
      workflow: "e2e.yml",
      branch: "main",
      generated_at: (now | todate),
      window: {runs: $window},
      runs_scanned: length,
      incomplete: (any(.[]; any(.attempts[]?; has("log_error")))),
      runs: .,
      verdict: $verdict
    }'
}

# render_table — stdin: the report JSON; stdout: the human-readable summary
# (the caller sends it to stderr). Lists every scanned run with its
# conclusion and marker status, then the verdict at both signal levels.
render_table() {
  jq -r '
    def col($w): tostring | . + (" " * (if $w > length then $w - length else 0 end));
    "rate-limit evidence: \(.workflow) behaviour runs on \(.repository) (\(.branch))",
    "window: \(.window.runs) most recent completed runs whose behaviour job ran; \(.runs_scanned) scanned"
      + (if .incomplete then " — INCOMPLETE: some behaviour-job logs could not be fetched" else "" end),
    "",
    ("run" | col(12)) + ("conclusion" | col(13)) + ("markers" | col(8)) + "marker detail",
    ("-" * 46),
    (.runs[] | (.run_id | col(12)) + ((.conclusion // "unknown") | col(13))
      + (([.attempts[].markers[]] | length | tostring) | col(8))
      + ([.attempts[] | if ((.markers | length) > 0) or has("log_error")
         then "attempt \(.attempt): "
              + (if has("log_error") then "log unavailable" else ([.markers[].marker] | join(", ")) end)
         else empty end]
         | if length == 0 then "-" else join("; ") end)),
    "",
    "verdict: \(.verdict.runs_with_markers) run(s) with markers (strict signal: any occurrence)",
    "         \(.verdict.runs_with_markers_that_passed) of those ultimately passed (lenient signal: retry-masked occurrences)"'
}

# --- Fetch and scan layer (network) -----------------------------------------

# scan_run REPO ROW OUTDIR — scans one candidate run into OUTDIR: walks
# every attempt (1..ROW.attempt; re-runs increment run_attempt by one, so
# the range is dense), lists each attempt's jobs via the per-attempt form
# (the plain /jobs listing is ambiguous about which attempt it answers
# for), fetches the behaviour job's logs when it ran, and writes one
# attempt record per attempt plus the run's evidence entry.
#
# Returns 2 when no attempt ran the behaviour job — the run contributes no
# evidence and does not count toward the window. Metadata fetch failures
# are fatal (die): a missing run, attempt or jobs listing compromises the
# window itself. A failed log fetch is not fatal: the attempt records a
# log_error, the scan is flagged incomplete, and the report shows what
# happened — an unfetchable log must never read as a clean attempt.
scan_run() {
  local repo="$1" row="$2" dir="$3"
  local run_id attempt_count run_conclusion
  run_id=$(jq -r '.databaseId' <<<"${row}")
  attempt_count=$(jq -r '.attempt' <<<"${row}")
  run_conclusion=$(jq -r '.conclusion // ""' <<<"${row}")
  if ! [[ "${attempt_count}" =~ ^[1-9][0-9]*$ ]]; then
    die "run ${run_id}: the listing returned attempt count '${attempt_count}', want a positive integer"
  fi
  mkdir -p "${dir}"

  local n nn job_id job_conclusion job_state attempt_conclusion markers log_error analyzer_rc
  local ran_any=0
  n=0
  while (( n < attempt_count )); do
    n=$(( n + 1 ))
    nn=$(printf '%02d' "${n}")

    # Single-page by construction (e2e.yml has three jobs, per_page=100), so
    # no --paginate — and the total_count guard fails loudly if a workflow
    # ever outgrew one page instead of silently missing attempts' jobs.
    if ! gh api "repos/${repo}/actions/runs/${run_id}/attempts/${n}/jobs?per_page=100" \
        > "${dir}/jobs-${nn}.json" 2>"${ERRFILE}"; then
      die "listing the jobs of run ${run_id} attempt ${n} failed: $(<"${ERRFILE}")"
    fi
    # Validate the envelope before extracting anything from it: a 200
    # response can still carry an error body ({"message": …}) with no jobs
    # array, which must fail the scan instead of classifying the attempt's
    # behaviour job as absent — that would silently exclude the run and let
    # an older one fill the window.
    if ! jobs_page_shape_ok "${dir}/jobs-${nn}.json"; then
      die "the jobs listing of run ${run_id} attempt ${n} is not a jobs page (expected an object with a jobs array and a numeric total_count): $(head -c 200 "${dir}/jobs-${nn}.json")"
    fi
    if ! jq -e '.total_count <= (.jobs | length)' \
        "${dir}/jobs-${nn}.json" >/dev/null; then
      die "the jobs listing of run ${run_id} attempt ${n} spans more than one page; the scanner needs pagination support"
    fi
    job_id=$(jq -r '[.jobs[] | select(.name == "behaviour")][0].id // empty' "${dir}/jobs-${nn}.json")
    job_conclusion=$(jq -r '[.jobs[] | select(.name == "behaviour")][0].conclusion // empty' "${dir}/jobs-${nn}.json")
    if [[ -z "${job_id}" ]]; then
      job_state=absent
    elif [[ "${job_conclusion}" == "skipped" ]]; then
      job_state=skipped
    else
      job_state=ran
    fi

    # Conclusions come from metadata, never from log parsing: the latest
    # attempt's conclusion is the run's own; earlier attempts are fetched.
    if (( n == attempt_count )); then
      attempt_conclusion="${run_conclusion}"
    elif ! attempt_conclusion=$(gh api "repos/${repo}/actions/runs/${run_id}/attempts/${n}" \
        --jq '.conclusion // ""' 2>"${ERRFILE}"); then
      die "fetching attempt ${n} of run ${run_id} failed: $(<"${ERRFILE}")"
    fi

    markers='[]'
    log_error=''
    if [[ "${job_state}" == "ran" ]]; then
      ran_any=1
      if gh api "repos/${repo}/actions/jobs/${job_id}/logs" \
          > "${dir}/log-${nn}.txt" 2>"${ERRFILE}"; then
        # scan_run runs on the left of || in main, so errexit is suspended
        # here: an analyzer failure must be handled explicitly, or jq turns
        # a dead awk into [] and the scan reports a clean, marker-free run.
        markers=$(analyze_log < "${dir}/log-${nn}.txt") || {
          analyzer_rc=$?
          markers='[]'
          log_error="log analyzer failed (exit ${analyzer_rc})"
          INCOMPLETE=1
          say "warning: the behaviour-job log of run ${run_id} attempt ${n} could not be analyzed: ${log_error}"
        }
      else
        log_error=$(head -n 1 "${ERRFILE}")
        if [[ -z "${log_error}" ]]; then
          log_error="gh api failed without a message"
        fi
        INCOMPLETE=1
        say "warning: the behaviour-job log of run ${run_id} attempt ${n} could not be fetched: ${log_error}"
      fi
    fi
    build_attempt_record "${n}" "${attempt_conclusion}" "${job_state}" \
      "${markers}" "${log_error}" > "${dir}/rec-${nn}.json"
  done

  if (( ran_any == 0 )); then
    return 2
  fi

  jq -s 'sort_by(.attempt)' "${dir}"/rec-*.json > "${dir}/recs.json"
  jq -n --argjson run "${row}" --slurpfile recs "${dir}/recs.json" \
    '{run: $run, attempts: ($recs[0] // [])}' | build_run_evidence > "${dir}/evidence.json"
}

# --- Main -------------------------------------------------------------------

main() {
  local runs=20 repo=''
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --runs)
        if [[ $# -lt 2 ]]; then
          die "--runs needs a value."
        fi
        runs="$2"; shift 2 ;;
      --runs=*) runs="${1#--runs=}"; shift ;;
      --repo)
        if [[ $# -lt 2 ]]; then
          die "--repo needs a value."
        fi
        repo="$2"; shift 2 ;;
      --repo=*) repo="${1#--repo=}"; shift ;;
      -h|--help) usage; exit 0 ;;
      *)
        usage >&2
        die "unknown argument: ${1}" ;;
    esac
  done
  if ! [[ "${runs}" =~ ^[1-9][0-9]*$ ]] || (( runs > 100 )); then
    die "--runs must be an integer between 1 and 100 (got ${runs})."
  fi
  if [[ -n "${repo}" ]] && ! [[ "${repo}" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
    die "--repo must look like OWNER/NAME (got ${repo})."
  fi
  local tool
  for tool in gh jq awk; do
    if ! command -v "${tool}" >/dev/null; then
      die "${tool} is required."
    fi
  done

  WORKDIR=$(mktemp -d "${TMPDIR:-/tmp}/scan-ratelimit-evidence.XXXXXX")
  ERRFILE="${WORKDIR}/gh-error.txt"
  INCOMPLETE=0
  trap 'rm -rf "${WORKDIR}"' EXIT

  if [[ -z "${repo}" ]]; then
    repo=$(gh repo view --json nameWithOwner --jq .nameWithOwner 2>"${ERRFILE}") \
      || die "resolving the repository from this checkout failed (pass --repo OWNER/NAME): $(<"${ERRFILE}")"
  fi

  # Candidates: completed e2e.yml runs on main, most recent first (the
  # list-runs.sh pattern). 5x headroom over the window for runs whose
  # behaviour job never ran, floored at 50 and capped at 500 like
  # list-runs.sh's bound.
  local list_limit=$(( runs * 5 ))
  if (( list_limit < 50 )); then list_limit=50; fi
  if (( list_limit > 500 )); then list_limit=500; fi
  if ! gh run list --repo "${repo}" --workflow=e2e.yml --branch=main \
      --status=completed --limit="${list_limit}" \
      --json attempt,databaseId,conclusion,createdAt,url,displayTitle,headSha \
      > "${WORKDIR}/candidates.json" 2>"${ERRFILE}"; then
    die "listing completed e2e.yml runs on ${repo} main failed: $(<"${ERRFILE}")"
  fi
  # Fail closed on a malformed answer, like the repo's other gh consumers.
  if ! jq -e 'type == "array"' "${WORKDIR}/candidates.json" >/dev/null; then
    die "the run listing for ${repo} is not a JSON array"
  fi

  # Keep the most recent runs whose behaviour job ran; stop at the window.
  local kept=0 candidates=0 row dir rc rid
  while IFS= read -r row; do
    if (( kept >= runs )); then break; fi
    candidates=$(( candidates + 1 ))
    dir="${WORKDIR}/run-$(printf '%04d' "${candidates}")"
    rc=0
    scan_run "${repo}" "${row}" "${dir}" || rc=$?
    rid=$(jq -r '.databaseId' <<<"${row}")
    if (( rc == 2 )); then
      rm -rf "${dir}"
      say "run ${rid}: behaviour job did not run in any attempt — excluded from the window"
      continue
    elif (( rc != 0 )); then
      # Bash suspends errexit for scan_run on the left of ||, so a failing
      # command inside it that is not itself a die() call lands here: any
      # exit status other than the no-behaviour-job sentinel 2 must fail
      # the scan rather than keep a run whose evidence.json is missing or
      # incomplete.
      die "scanning run ${rid} failed with exit code ${rc}"
    fi
    kept=$(( kept + 1 ))
    printf '%s\n' "${dir}" >> "${WORKDIR}/kept-dirs.txt"
    say "scanned $(jq -r '"run \(.run_id): \(.conclusion // "unknown"), \(.attempts | length) attempt(s), \([.attempts[].markers[]] | length) marker(s)"' "${dir}/evidence.json") [${kept}/${runs}]"
  done < <(jq -c 'sort_by(.databaseId) | reverse | .[]' "${WORKDIR}/candidates.json")

  local runs_file="${WORKDIR}/runs.json"
  if [[ -s "${WORKDIR}/kept-dirs.txt" ]]; then
    while IFS= read -r dir; do
      cat "${dir}/evidence.json"
    done < "${WORKDIR}/kept-dirs.txt" | jq -s . > "${runs_file}"
  else
    printf '[]' > "${runs_file}"
  fi

  # The report is the stdout artifact; the table is human-facing (stderr).
  render_report "${repo}" "${runs}" < "${runs_file}" > "${WORKDIR}/report.json"
  render_table < "${WORKDIR}/report.json" >&2
  cat "${WORKDIR}/report.json"

  if (( INCOMPLETE )); then
    die "the report was emitted, but some behaviour-job logs could not be fetched (see the log_error entries) — the verdict may undercount"
  fi
}

# Allow sourcing for the paired test (scan-ratelimit-evidence_test.sh):
# the analysis layer is only callable when the script is executed directly.
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  main "$@"
fi
