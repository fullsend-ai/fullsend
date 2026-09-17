#!/usr/bin/env bash
# review-handoff-test.sh — Tests for review-handoff.sh and the maintainer
# workflow patch that inlines the same skip.
#
# Run from the repo root:
#   bash .github/scripts/review-handoff-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
# shellcheck source=review-handoff.sh
source "${SCRIPT_DIR}/review-handoff.sh"

FAILURES=0

pass() { echo "PASS: $1"; }
fail() {
  echo "FAIL: $1"
  FAILURES=$((FAILURES + 1))
}

assert_skip() {
  local name="$1" labels="$2"
  if should_skip_automatic_review_handoff "${labels}"; then
    pass "${name}"
  else
    fail "${name} — expected skip for labels '${labels}'"
  fi
}

assert_dispatch_label() {
  local name="$1" labels="$2"
  if should_skip_automatic_review_handoff "${labels}"; then
    fail "${name} — expected dispatch for labels '${labels}'"
  else
    pass "${name}"
  fi
}

assert_route() {
  local name="$1" action="$2" labels="$3" expect="$4"
  local rc=0
  github_like_should_dispatch_review "${action}" "${labels}" || rc=$?
  if [[ "${expect}" == "dispatch" && "${rc}" -eq 0 ]]; then
    pass "${name}"
  elif [[ "${expect}" == "skip" && "${rc}" -ne 0 ]]; then
    pass "${name}"
  else
    fail "${name} — action=${action} labels='${labels}' expect=${expect} rc=${rc}"
  fi
}

echo "=== review-handoff helper ==="

assert_dispatch_label "empty labels dispatch" ""
assert_dispatch_label "ready-for-review only dispatches" "ready-for-review"
assert_dispatch_label "unrelated labels dispatch" "bug,ready-to-code"
assert_skip "provenance present skips" "ready-for-review,fullsend-auto-review-handoff"
assert_skip "provenance first skips" "fullsend-auto-review-handoff,ready-for-review"
assert_skip "provenance only skips" "fullsend-auto-review-handoff"
assert_dispatch_label "substring is not provenance" "not-fullsend-auto-review-handoff,ready-for-review"
assert_dispatch_label "prefix is not provenance" "fullsend-auto-review-handoff-extra,ready-for-review"

# Actor identity is not an input of the helper. A bot-looking CSV must
# not skip without the provenance label.
assert_dispatch_label "bot username in labels is not provenance" "fullsend-ai-coder[bot],ready-for-review"

echo "=== github-like routing scenarios ==="

HANDOFF="fullsend-auto-review-handoff,ready-for-review"
EXPLICIT="ready-for-review"

assert_route "opened dispatches" "opened" "${HANDOFF}" dispatch
assert_route "automatic labeled skips" "labeled" "${HANDOFF}" skip
assert_route "explicit labeled dispatches" "labeled" "${EXPLICIT}" dispatch
assert_route "synchronize after handoff dispatches" "synchronize" "" dispatch
assert_route "fix-agent push (synchronize) dispatches" "synchronize" "ready-for-review" dispatch
assert_route "slash-review same SHA dispatches" "slash-review" "${HANDOFF}" dispatch
assert_route "ready_for_review event dispatches" "ready_for_review" "" dispatch
assert_route "closed does not dispatch review" "closed" "" skip

count_pair() {
  local first_action="$1" first_labels="$2"
  local second_action="$3" second_labels="$4"
  local n=0 rc
  rc=0
  github_like_should_dispatch_review "${first_action}" "${first_labels}" || rc=$?
  [[ "${rc}" -eq 0 ]] && n=$((n + 1))
  rc=0
  github_like_should_dispatch_review "${second_action}" "${second_labels}" || rc=$?
  [[ "${rc}" -eq 0 ]] && n=$((n + 1))
  echo "${n}"
}

got=$(count_pair opened "${HANDOFF}" labeled "${HANDOFF}")
if [[ "${got}" == "1" ]]; then
  pass "opened then automatic labeled: one review"
else
  fail "opened then automatic labeled: got ${got}, want 1"
fi

got=$(count_pair labeled "${HANDOFF}" opened "${HANDOFF}")
if [[ "${got}" == "1" ]]; then
  pass "automatic labeled then opened: one review"
else
  fail "automatic labeled then opened: got ${got}, want 1"
fi

got=$(count_pair opened "" labeled "${EXPLICIT}")
if [[ "${got}" == "2" ]]; then
  pass "opened then explicit labeled: two reviews"
else
  fail "opened then explicit labeled: got ${got}, want 2"
fi

echo "=== concurrent-ish pair (background) ==="

concurrent_count() {
  local a1="$1" l1="$2" a2="$3" l2="$4"
  local tmp
  tmp="$(mktemp -d)"
  (
    rc=0
    github_like_should_dispatch_review "${a1}" "${l1}" || rc=$?
    [[ "${rc}" -eq 0 ]] && echo 1 >"${tmp}/1"
  ) &
  (
    rc=0
    github_like_should_dispatch_review "${a2}" "${l2}" || rc=$?
    [[ "${rc}" -eq 0 ]] && echo 1 >"${tmp}/2"
  ) &
  wait
  local n=0
  [[ -f "${tmp}/1" ]] && n=$((n + 1))
  [[ -f "${tmp}/2" ]] && n=$((n + 1))
  rm -rf "${tmp}"
  echo "${n}"
}

i=0
while [[ "${i}" -lt 16 ]]; do
  if [[ $((i % 2)) -eq 0 ]]; then
    got=$(concurrent_count opened "${HANDOFF}" labeled "${HANDOFF}")
  else
    got=$(concurrent_count labeled "${HANDOFF}" opened "${HANDOFF}")
  fi
  if [[ "${got}" != "1" ]]; then
    fail "concurrent automatic pair iteration ${i}: got ${got}, want 1"
    break
  fi
  i=$((i + 1))
done
if [[ "${i}" -eq 16 ]]; then
  pass "concurrent automatic pair (16 iterations, both orders)"
fi

i=0
while [[ "${i}" -lt 16 ]]; do
  got=$(concurrent_count opened "" labeled "${EXPLICIT}")
  if [[ "${got}" != "2" ]]; then
    fail "concurrent explicit pair iteration ${i}: got ${got}, want 2"
    break
  fi
  i=$((i + 1))
done
if [[ "${i}" -eq 16 ]]; then
  pass "concurrent explicit same-revision pair (16 iterations)"
fi

echo "=== maintainer patch ==="

PATCH="${REPO_ROOT}/docs/contributing/patches/7384-review-handoff-dedup.patch"
DISPATCH="${REPO_ROOT}/.github/workflows/reusable-dispatch.yml"
SCAFFOLD="${REPO_ROOT}/internal/scaffold/fullsend-repo/.github/workflows/dispatch.yml"

if [[ ! -f "${PATCH}" ]]; then
  fail "patch file missing: ${PATCH}"
else
  pass "patch file present"
fi

assert_patched_routing() {
  local file="$1" name="$2"
  local s
  s="$(cat "${file}")"
  if [[ "${s}" != *"fullsend-auto-review-handoff"* ]]; then
    fail "${name} missing provenance skip"
    return
  fi
  if [[ "${s}" != *'[[ "${PR_USER_LOGIN}" =~ \[bot\]$ ]] || is_event_actor_authorized "${PR_USER_LOGIN}" triage'* ]]; then
    fail "${name} lost bot authorization exemption on opened|synchronize|ready_for_review"
    return
  fi
  if ! grep -A6 'TRIGGERING_LABEL}" == "ready-for-review"' "${file}" | grep -q 'fullsend-auto-review-handoff'; then
    fail "${name} ready-for-review labeled path missing provenance skip"
    return
  fi
  if grep -A8 'pull_request_target' "${file}" | grep -A20 'labeled)' | grep -q '\[bot\]'; then
    fail "${name} labeled review path must not use bot-name suppression"
    return
  fi
  pass "${name} patched routing"
}

if grep -q 'fullsend-auto-review-handoff' "${DISPATCH}"; then
  echo "Workflow files already contain provenance skip (patch applied)."
  assert_patched_routing "${DISPATCH}" "reusable-dispatch.yml"
  assert_patched_routing "${SCAFFOLD}" "scaffold dispatch.yml"
else
  if git -C "${REPO_ROOT}" apply --check "${PATCH}"; then
    pass "git apply --check"
  else
    fail "git apply --check failed"
  fi

  tmp="$(mktemp -d)"
  mkdir -p "${tmp}/.github/workflows" \
    "${tmp}/internal/scaffold/fullsend-repo/.github/workflows"
  cp "${DISPATCH}" "${tmp}/.github/workflows/reusable-dispatch.yml"
  cp "${SCAFFOLD}" "${tmp}/internal/scaffold/fullsend-repo/.github/workflows/dispatch.yml"
  apply_rc=0
  git -C "${tmp}" apply "${PATCH}" || apply_rc=$?
  if [[ "${apply_rc}" -eq 0 ]]; then
    pass "git apply on temp copies"
    assert_patched_routing "${tmp}/.github/workflows/reusable-dispatch.yml" "patched reusable-dispatch.yml"
    assert_patched_routing "${tmp}/internal/scaffold/fullsend-repo/.github/workflows/dispatch.yml" "patched scaffold dispatch.yml"
    if grep -q 'has_label "fullsend-auto-review-handoff" "${PR_LABELS}"' \
        "${tmp}/.github/workflows/reusable-dispatch.yml" \
      && grep -q 'has_label "fullsend-auto-review-handoff" "${ISSUE_LABELS}"' \
        "${tmp}/.github/workflows/reusable-dispatch.yml"; then
      pass "reusable-dispatch uses has_label on PR_LABELS and ISSUE_LABELS"
    else
      fail "reusable-dispatch patched skip missing has_label on both label CSVs"
    fi
    if grep -q 'has_label "fullsend-auto-review-handoff" "${PR_LABELS}"' \
      "${tmp}/internal/scaffold/fullsend-repo/.github/workflows/dispatch.yml"; then
      pass "scaffold dispatch uses has_label on PR_LABELS"
    else
      fail "scaffold dispatch patched skip missing has_label"
    fi
  else
    fail "git apply on temp copies failed"
  fi
  rm -rf "${tmp}"
fi

echo "=== results ==="
if [[ "${FAILURES}" -ne 0 ]]; then
  echo "${FAILURES} failure(s)"
  exit 1
fi
echo "All review-handoff tests passed"
