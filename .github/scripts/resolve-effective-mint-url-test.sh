#!/usr/bin/env bash
# resolve-effective-mint-url-test.sh — Tests for resolve-effective-mint-url.sh
#
# Run from the repo root:
#   bash .github/scripts/resolve-effective-mint-url-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="${SCRIPT_DIR}/resolve-effective-mint-url.sh"
BASH_ABS="$(command -v bash)"
FAILURES=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

run_resolve() {
  local fallback="${1:-}"
  local output_file="${TMPDIR}/github-output.txt"
  local stderr_file="${TMPDIR}/resolve-stderr.txt"
  local oldpwd="${PWD}"
  : >"${output_file}"
  : >"${stderr_file}"
  cd "${TMPDIR}/repo"
  GITHUB_OUTPUT="${output_file}" FALLBACK_MINT_URL="${fallback}" "${BASH_ABS}" "${SCRIPT}" 2>"${stderr_file}"
  cd "${oldpwd}"
  grep '^mint_url=' "${output_file}" | cut -d= -f2-
}

run_resolve_stderr() {
  local fallback="${1:-}"
  local output_file="${TMPDIR}/github-output.txt"
  local stderr_file="${TMPDIR}/resolve-stderr.txt"
  local oldpwd="${PWD}"
  : >"${output_file}"
  : >"${stderr_file}"
  cd "${TMPDIR}/repo"
  GITHUB_OUTPUT="${output_file}" FALLBACK_MINT_URL="${fallback}" "${BASH_ABS}" "${SCRIPT}" 2>"${stderr_file}"
  cd "${oldpwd}"
  cat "${stderr_file}"
}

assert_notice_contains() {
  local name="$1" want="$2" stderr="$3"
  if ! grep -F -- "${want}" <<<"${stderr}" >/dev/null; then
    echo "FAIL: ${name}: expected notice containing ${want@Q}, stderr=${stderr@Q}"
    FAILURES=$((FAILURES + 1))
  fi
}

assert_no_notice_override() {
  local name="$1" stderr="$2"
  if grep -F -- "overrides FULLSEND_MINT_URL" <<<"${stderr}" >/dev/null; then
    echo "FAIL: ${name}: unexpected override notice in stderr=${stderr@Q}"
    FAILURES=$((FAILURES + 1))
  fi
}

run_resolve_expect_fail() {
  local fallback="${1:-}"
  local name="${2}"
  local expected="${3}"
  local leak="${4:-}"
  local output_file="${TMPDIR}/github-output.txt"
  local stderr_file="${TMPDIR}/stderr.txt"
  local oldpwd="${PWD}"
  local got
  local script_path="${RESOLVE_PATH:-${PATH}}"
  : >"${output_file}"
  : >"${stderr_file}"
  cd "${TMPDIR}/repo"
  if PATH="${script_path}" GITHUB_OUTPUT="${output_file}" FALLBACK_MINT_URL="${fallback}" "${BASH_ABS}" "${SCRIPT}" 2>"${stderr_file}"; then
    cd "${oldpwd}"
    echo "FAIL: ${name}: expected non-zero exit"
    FAILURES=$((FAILURES + 1))
    return 0
  fi
  cd "${oldpwd}"
  got="$(cat "${stderr_file}")"
  if ! grep -F -- "${expected}" "${stderr_file}" >/dev/null; then
    echo "FAIL: ${name}: expected ${expected@Q} in stderr, got ${got@Q}"
    FAILURES=$((FAILURES + 1))
  fi
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
assert_notice_contains "overlay override notice" "mint_url from .fullsend config overrides FULLSEND_MINT_URL" "$(run_resolve_stderr "https://fallback.example")"

setup_repo
printf 'version: "1"\nmint_url: https://same.example\n' >.fullsend/config.yaml
assert_eq "file matches fallback" "https://same.example" "$(run_resolve "https://same.example")"
assert_no_notice_override "no notice when values match" "$(run_resolve_stderr "https://same.example")"

setup_repo
assert_no_notice_override "no notice for fallback only" "$(run_resolve_stderr "https://fallback.example")"

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
run_resolve_expect_fail "https://fallback.example" "reject http mint_url" "::error::mint_url must be a valid HTTPS URL"

setup_repo
printf 'version: "1"\nmint_url: https://user:pass@mint.example\n' >.fullsend/config.yaml
run_resolve_expect_fail "https://fallback.example" "reject userinfo mint_url" "::error::mint_url must not contain userinfo" "user:pass"

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
run_resolve_expect_fail "" "reject localhost http from config file" "::error::mint_url must be a valid HTTPS URL"

setup_repo
run_resolve_expect_fail "http://evil.example" "reject non-loopback http fallback" "::error::mint_url must be a valid HTTPS URL"

setup_repo
run_resolve_expect_fail "https://u:p@host" "reject userinfo fallback" "::error::mint_url must not contain userinfo" "u:p"

setup_repo
run_resolve_expect_fail $'https://mint.example\nsmuggled' "reject LF in fallback" "::error::mint_url must not contain CR or LF"

setup_repo
printf 'mint_url: [unclosed\n' >.fullsend/config.yaml
run_resolve_expect_fail "https://fallback.example" "reject unparsable overlay" "::error::failed to read mint_url from .fullsend/config.yaml"

setup_repo
printf 'version: "1"\n' >.fullsend/config.yaml
printf 'mint_url: [unclosed\n' >.fullsend/config.base.yaml
run_resolve_expect_fail "https://fallback.example" "reject unparsable base under silent overlay" "::error::failed to read mint_url from .fullsend/config.base.yaml"

# Isolate PATH so yq is missing without dropping bash/tr from system
# directories that also contain yq (for example /usr/bin on merged-/usr).
noyq_bin="${TMPDIR}/noyq-bin"
mkdir -p "${noyq_bin}"
for _cmd in bash tr grep cut cat; do
  _src="$(command -v "${_cmd}")"
  if [[ -z "${_src}" || ! -x "${_src}" ]]; then
    echo "FAIL: required tool ${_cmd} not found on PATH"
    exit 1
  fi
  ln -s "${_src}" "${noyq_bin}/${_cmd}"
done
if PATH="${noyq_bin}" command -v yq >/dev/null 2>&1; then
  echo "FAIL: isolated PATH still finds yq"
  exit 1
fi

run_resolve_without_yq() {
  local fallback="${1:-}"
  local output_file="${TMPDIR}/github-output.txt"
  local oldpwd="${PWD}"
  : >"${output_file}"
  cd "${TMPDIR}/repo"
  PATH="${noyq_bin}" GITHUB_OUTPUT="${output_file}" FALLBACK_MINT_URL="${fallback}" "${BASH_ABS}" "${SCRIPT}"
  cd "${oldpwd}"
  grep '^mint_url=' "${output_file}" | cut -d= -f2-
}

setup_repo
assert_eq "no yq without config files" "https://fallback.example" "$(run_resolve_without_yq "https://fallback.example")"

setup_repo
printf 'version: "1"\nmint_url: https://overlay.example\n' >.fullsend/config.yaml
RESOLVE_PATH="${noyq_bin}" run_resolve_expect_fail "https://fallback.example" "yq required when config files exist" "::error::yq is required to resolve mint_url from config"

if [[ "${FAILURES}" -eq 0 ]]; then
  echo "All tests passed."
else
  echo "${FAILURES} test(s) failed."
  exit 1
fi
