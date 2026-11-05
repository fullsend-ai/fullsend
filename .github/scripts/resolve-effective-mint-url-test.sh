#!/usr/bin/env bash
# resolve-effective-mint-url-test.sh — Tests for resolve-effective-mint-url.sh
#
# Run from the repo root:
#   bash .github/scripts/resolve-effective-mint-url-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="${SCRIPT_DIR}/resolve-effective-mint-url.sh"
FAILURES=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

run_resolve() {
  local fallback="${1:-}"
  local output_file="${TMPDIR}/github-output.txt"
  local oldpwd="${PWD}"
  : >"${output_file}"
  cd "${TMPDIR}/repo"
  GITHUB_OUTPUT="${output_file}" FALLBACK_MINT_URL="${fallback}" bash "${SCRIPT}"
  cd "${oldpwd}"
  grep '^mint_url=' "${output_file}" | cut -d= -f2-
}

run_resolve_expect_fail() {
  local fallback="${1:-}"
  local name="${2}"
  local leak="${3:-}"
  local output_file="${TMPDIR}/github-output.txt"
  local stderr_file="${TMPDIR}/stderr.txt"
  local oldpwd="${PWD}"
  : >"${output_file}"
  : >"${stderr_file}"
  cd "${TMPDIR}/repo"
  if GITHUB_OUTPUT="${output_file}" FALLBACK_MINT_URL="${fallback}" bash "${SCRIPT}" 2>"${stderr_file}"; then
    cd "${oldpwd}"
    echo "FAIL: ${name}: expected non-zero exit"
    FAILURES=$((FAILURES + 1))
    return 0
  fi
  cd "${oldpwd}"
  if [[ -n "${leak}" ]] && grep -F -- "${leak}" "${stderr_file}" >/dev/null; then
    echo "FAIL: ${name}: stderr leaked ${leak@Q}"
    FAILURES=$((FAILURES + 1))
  fi
}

setup_repo() {
  rm -rf "${TMPDIR}/repo"
  mkdir -p "${TMPDIR}/repo/.fullsend"
  cd "${TMPDIR}/repo"
}

assert_eq() {
  local name="$1" want="$2" got="$3"
  if [[ "${want}" != "${got}" ]]; then
    echo "FAIL: ${name}: want ${want@Q}, got ${got@Q}"
    FAILURES=$((FAILURES + 1))
  fi
}

setup_repo
assert_eq "fallback only" "https://fallback.example" "$(run_resolve "https://fallback.example")"

setup_repo
printf 'version: "1"\nmint_url: https://overlay.example\n' >.fullsend/config.yaml
assert_eq "overlay wins" "https://overlay.example" "$(run_resolve "https://fallback.example")"

setup_repo
printf 'version: "1"\nmint_url: https://base.example\n' >.fullsend/config.base.yaml
assert_eq "base only" "https://base.example" "$(run_resolve "https://fallback.example")"

setup_repo
printf 'version: "1"\n' >.fullsend/config.yaml
printf 'version: "1"\nmint_url: https://base.example\n' >.fullsend/config.base.yaml
assert_eq "silent overlay uses base" "https://base.example" "$(run_resolve "https://fallback.example")"

setup_repo
printf 'version: "1"\nmint_url: https://overlay.example\n' >.fullsend/config.yaml
printf 'version: "1"\nmint_url: https://base.example\n' >.fullsend/config.base.yaml
assert_eq "overlay beats base" "https://overlay.example" "$(run_resolve "https://fallback.example")"

setup_repo
assert_eq "empty fallback" "" "$(run_resolve "")"

setup_repo
printf 'version: "1"\nmint_url: http://evil.example\n' >.fullsend/config.yaml
run_resolve_expect_fail "https://fallback.example" "reject http mint_url"

setup_repo
printf 'version: "1"\nmint_url: https://user:pass@mint.example\n' >.fullsend/config.yaml
run_resolve_expect_fail "https://fallback.example" "reject userinfo mint_url" "user:pass"

setup_repo
printf 'version: "1"\nmint_url: https://mint.example/a@b\n' >.fullsend/config.yaml
assert_eq "at-sign in path is not userinfo" "https://mint.example/a@b" "$(run_resolve "")"

setup_repo
printf 'version: "1"\nmint_url: HTTPS://mint.example\n' >.fullsend/config.yaml
assert_eq "uppercase scheme" "HTTPS://mint.example" "$(run_resolve "")"

setup_repo
assert_eq "localhost http fallback" "http://localhost:8080" "$(run_resolve "http://localhost:8080")"

setup_repo
assert_eq "loopback http fallback" "http://127.0.0.1:8080" "$(run_resolve "http://127.0.0.1:8080")"

setup_repo
printf 'version: "1"\nmint_url: http://localhost:8080\n' >.fullsend/config.yaml
run_resolve_expect_fail "" "reject localhost http from config file"

noyq_path=""
IFS=':' read -ra _path_parts <<<"${PATH}"
for _dir in "${_path_parts[@]}"; do
  if [[ -n "${_dir}" && -x "${_dir}/yq" ]]; then
    continue
  fi
  if [[ -z "${noyq_path}" ]]; then
    noyq_path="${_dir}"
  else
    noyq_path="${noyq_path}:${_dir}"
  fi
done
if [[ -z "${noyq_path}" ]]; then
  noyq_path="/usr/bin:/bin"
fi

run_resolve_without_yq() {
  local fallback="${1:-}"
  local output_file="${TMPDIR}/github-output.txt"
  local oldpwd="${PWD}"
  : >"${output_file}"
  cd "${TMPDIR}/repo"
  PATH="${noyq_path}" GITHUB_OUTPUT="${output_file}" FALLBACK_MINT_URL="${fallback}" bash "${SCRIPT}"
  cd "${oldpwd}"
  grep '^mint_url=' "${output_file}" | cut -d= -f2-
}

setup_repo
assert_eq "no yq without config files" "https://fallback.example" "$(run_resolve_without_yq "https://fallback.example")"

setup_repo
printf 'version: "1"\nmint_url: https://overlay.example\n' >.fullsend/config.yaml
PATH="${noyq_path}" run_resolve_expect_fail "https://fallback.example" "yq required when config files exist"

if [[ "${FAILURES}" -eq 0 ]]; then
  echo "All tests passed."
else
  echo "${FAILURES} test(s) failed."
  exit 1
fi
