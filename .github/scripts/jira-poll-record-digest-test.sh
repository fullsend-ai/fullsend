#!/usr/bin/env bash
# jira-poll-record-digest-test.sh — digest and workflow-contract tests for the
# Jira poll batch check.
#
# The batch check is only evidence while its stand-in workflows hand the
# batch over the way the real ones do. The contract cases below fail when the
# real hand-off changes and the stand-ins do not follow.
#
# Run from the repo root:
#   bash .github/scripts/jira-poll-record-digest-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="${SCRIPT_DIR}/jira-poll-record-digest.sh"
WORKFLOWS="${SCRIPT_DIR}/../workflows"
TEMPLATE="${SCRIPT_DIR}/../../internal/scaffold/fullsend-repo/templates/jira-poll-per-repo.yaml"

fail() { echo "FAIL: $*" >&2; exit 1; }
contains() { grep -Fq -- "$2" "$1" || fail "${1} does not contain ${2}"; }
digest() { printf '%s' "$1" | "${SCRIPT}"; }

# The digest is of the content, not of how the JSON is written.
want="$(digest '{"agent":"triage","event_payload":{"body":"a<b 語","n":7}}')"
[[ "${want}" =~ ^[0-9a-f]{64}$ ]] || fail "digest is not a SHA-256: ${want}"

reordered="$(digest '{
  "event_payload": {
    "n": 7,
    "body": "a<b 語"
  },
  "agent": "triage"
}')"
[[ "${reordered}" == "${want}" ]] || fail "key order and whitespace changed the digest"

escaped="$(digest '{"agent":"triage","event_payload":{"body":"a<b 語","n":7}}')"
[[ "${escaped}" == "${want}" ]] || fail "escaping a character changed the digest"

changed="$(digest '{"agent":"triage","event_payload":{"body":"a<b 語","n":8}}')"
[[ "${changed}" != "${want}" ]] || fail "a changed value kept the digest"

truncated="$(digest '{"agent":"triage","event_payload":{"body":"a<b ","n":7}}')"
[[ "${truncated}" != "${want}" ]] || fail "a shortened string kept the digest"

if digest '{"agent":' >/dev/null 2>&1; then
  fail "input that is not JSON produced a digest"
fi

# The poll stand-in builds the matrix output with the real workflow's step.
matrix_step() {
  awk '
    /^      - name: Build dispatch matrix$/ { on = 1 }
    on && seen && /^      - / { exit }
    on {
      seen = 1
      if ($0 ~ /^[[:space:]]*$/) { blank = blank $0 "\n"; next }
      printf "%s%s\n", blank, $0
      blank = ""
    }
  ' "$1"
}
real_step="$(matrix_step "${WORKFLOWS}/reusable-poll-jira.yml")"
[[ -n "${real_step}" ]] || fail "reusable-poll-jira.yml has no Build dispatch matrix step"
[[ "$(matrix_step "${WORKFLOWS}/jira-poll-batch-check-poll.yml")" == "${real_step}" ]] ||
  fail "the Build dispatch matrix step of jira-poll-batch-check-poll.yml differs from reusable-poll-jira.yml"

# Both return the matrix as the poll job's output.
for workflow in reusable-poll-jira.yml jira-poll-batch-check-poll.yml; do
  contains "${WORKFLOWS}/${workflow}" 'value: ${{ jobs.poll.outputs.matrix }}'
  contains "${WORKFLOWS}/${workflow}" 'matrix: ${{ steps.matrix.outputs.matrix }}'
done

# The caller hands the matrix on under the managed caller's condition.
condition() { grep -F 'fromJSON(needs.poll.outputs.matrix)' "$1" | sed 's/^[[:space:]]*//'; }
real_condition="$(condition "${TEMPLATE}")"
[[ -n "${real_condition}" ]] || fail "jira-poll-per-repo.yaml has no dispatch condition"
[[ "$(condition "${WORKFLOWS}/jira-poll-batch-check.yml")" == "${real_condition}" ]] ||
  fail "the dispatch condition of jira-poll-batch-check.yml differs from jira-poll-per-repo.yaml"
for caller in "${TEMPLATE}" "${WORKFLOWS}/jira-poll-batch-check.yml"; do
  contains "${caller}" 'matrix: ${{ needs.poll.outputs.matrix }}'
done

# The real dispatch workflow still takes the matrix as a string input and
# makes one job of each record, which the dispatch stand-in copies.
contains "${WORKFLOWS}/reusable-dispatch.yml" 'matrix: ${{ fromJSON(inputs.matrix != '"''"' && inputs.matrix || '
contains "${WORKFLOWS}/reusable-dispatch.yml" 'EVENT_PAYLOAD: ${{ toJSON(matrix.event_payload) }}'
contains "${WORKFLOWS}/jira-poll-batch-check-dispatch.yml" 'matrix: ${{ fromJSON(inputs.matrix) }}'
contains "${WORKFLOWS}/jira-poll-batch-check-dispatch.yml" 'RECORD: ${{ toJSON(matrix) }}'

echo "jira poll record digest and workflow-contract tests passed"
