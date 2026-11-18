#!/usr/bin/env bash
# retry-with-backoff-test.sh — Tests for retry-with-backoff.sh and the
# runtime dependency installers that use it (install-podman.sh,
# install-openshell.sh). curl, sleep and the privileged commands are
# stubbed on PATH; nothing is downloaded or installed.
#
# Run from the repo root:
#   bash .github/scripts/retry-with-backoff-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FAILURES=0

# Pinned versions, read from the scripts so the tests follow version bumps.
PODMAN_TAG="$(sed -n 's/^PODMAN_STATIC_TAG="\(.*\)"$/\1/p' "${SCRIPT_DIR}/install-podman.sh")"
OPENSHELL_VERSION="$(GITHUB_ENV="" bash -c 'source "$1"; echo "${OPENSHELL_VERSION}"' _ "${SCRIPT_DIR}/openshell-version.sh")"

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

pass() { printf 'PASS: %s\n' "$1"; }
fail() {
  printf 'FAIL: %s\n' "$1" >&2
  FAILURES=$((FAILURES + 1))
}

# setup_stubs <curl responses...>
#
# Creates a fresh stub directory and exports STUB_DIR. Each curl call
# consumes the next response (the last one repeats):
#   http:<code>     write a payload to -o and print <code> (for -w)
#   exit:<status>   print HTTP 000 and exit with <status>
#   stdout:<text>   print <text> on stdout and exit 0
setup_stubs() {
  STUB_DIR="${TMPDIR}/stubs.$((++STUB_SEQ))"
  export STUB_DIR
  mkdir -p "${STUB_DIR}/bin" "${STUB_DIR}/home"
  printf '%s\n' "$@" > "${STUB_DIR}/curl.responses"
  : > "${STUB_DIR}/curl.log"
  : > "${STUB_DIR}/sleep.log"
  : > "${STUB_DIR}/sudo.log"

  cat > "${STUB_DIR}/bin/curl" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "${STUB_DIR}/curl.log"
n="$(wc -l < "${STUB_DIR}/curl.log")"
resp="$(sed -n "${n}p" "${STUB_DIR}/curl.responses")"
if [[ -z "${resp}" ]]; then
  resp="$(tail -n 1 "${STUB_DIR}/curl.responses")"
fi
out="" prev=""
for a in "$@"; do
  if [[ "${prev}" == "-o" ]]; then out="${a}"; fi
  prev="${a}"
done
case "${resp}" in
  http:*)
    if [[ -n "${out}" ]]; then echo "stub payload" > "${out}"; fi
    printf '%s' "${resp#http:}"
    ;;
  exit:*)
    printf '000'
    exit "${resp#exit:}"
    ;;
  stdout:*)
    printf '%s\n' "${resp#stdout:}"
    ;;
esac
EOF

  cat > "${STUB_DIR}/bin/sleep" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$1" >> "${STUB_DIR}/sleep.log"
EOF

  cat > "${STUB_DIR}/bin/sudo" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "${STUB_DIR}/sudo.log"
exit 1
EOF

  cat > "${STUB_DIR}/bin/uname" <<'EOF'
#!/usr/bin/env bash
echo x86_64
EOF

  cat > "${STUB_DIR}/bin/openshell" <<'EOF'
#!/usr/bin/env bash
echo "openshell 0.1.4"
EOF

  for cmd in journalctl systemctl; do
    printf '#!/usr/bin/env bash\nexit 0\n' > "${STUB_DIR}/bin/${cmd}"
  done
  chmod +x "${STUB_DIR}/bin/"*
}
STUB_SEQ=0

curl_calls() { wc -l < "${STUB_DIR}/curl.log" | tr -d ' '; }
sleeps() { paste -sd' ' "${STUB_DIR}/sleep.log"; }

# run_download <label> — runs download_with_retry in a subshell with the
# stubs on PATH; sets RC and OUT (combined output).
run_download() {
  RC=0
  OUT="$(PATH="${STUB_DIR}/bin:${PATH}" bash -c '
    set -euo pipefail
    source "$1"
    download_with_retry "$2" "https://example.invalid/dep.tar.gz" "$3"
  ' _ "${SCRIPT_DIR}/retry-with-backoff.sh" "$1" "${STUB_DIR}/out" 2>&1)" || RC=$?
}

# --- download_with_retry ---------------------------------------------------

setup_stubs http:504 http:504 http:200
run_download "Dep v1 download"
if [[ "${RC}" -eq 0 && "$(curl_calls)" -eq 3 && "$(sleeps)" == "10 20" ]] \
  && [[ "$(cat "${STUB_DIR}/out")" == "stub payload" ]]; then
  pass "transient 504s are retried with growing delays, then succeed"
else
  fail "transient 504 recovery (rc=${RC}, calls=$(curl_calls), sleeps='$(sleeps)'): ${OUT}"
fi

setup_stubs http:504
run_download "Dep v1 download"
if [[ "${RC}" -ne 0 && "$(curl_calls)" -eq 6 && "$(sleeps)" == "10 20 40 80 120" ]] \
  && grep -q '::error::Dep v1 download failed after 6 attempts (category: transient HTTP 504)' <<< "${OUT}"; then
  pass "repeated 504s fail after 6 attempts naming dependency and category"
else
  fail "repeated 504 exhaustion (rc=${RC}, calls=$(curl_calls), sleeps='$(sleeps)'): ${OUT}"
fi

setup_stubs exit:28 http:200
run_download "Dep v1 download"
if [[ "${RC}" -eq 0 && "$(curl_calls)" -eq 2 ]] \
  && grep -q 'category: network (curl exit 28' <<< "${OUT}"; then
  pass "curl timeouts are retried as network failures"
else
  fail "network retry (rc=${RC}, calls=$(curl_calls)): ${OUT}"
fi

setup_stubs http:429 http:200
run_download "Dep v1 download"
if [[ "${RC}" -eq 0 && "$(curl_calls)" -eq 2 ]]; then
  pass "HTTP 429 is retried"
else
  fail "HTTP 429 retry (rc=${RC}, calls=$(curl_calls)): ${OUT}"
fi

setup_stubs http:404
run_download "Dep v1 download"
if [[ "${RC}" -ne 0 && "$(curl_calls)" -eq 1 && -z "$(sleeps)" ]] \
  && grep -q 'category: HTTP 404); permanent failure, not retried' <<< "${OUT}"; then
  pass "HTTP 404 fails immediately without retrying"
else
  fail "HTTP 404 permanent failure (rc=${RC}, calls=$(curl_calls)): ${OUT}"
fi

# --- retry_with_backoff tunables -------------------------------------------

setup_stubs http:200
RC=0
OUT="$(PATH="${STUB_DIR}/bin:${PATH}" RETRY_MAX_ATTEMPTS=4 RETRY_INITIAL_DELAY=3 RETRY_MAX_DELAY=5 bash -c '
  set -euo pipefail
  source "$1"
  retry_with_backoff "Generic step" false
' _ "${SCRIPT_DIR}/retry-with-backoff.sh" 2>&1)" || RC=$?
if [[ "${RC}" -ne 0 && "$(sleeps)" == "3 5 5" ]] \
  && grep -q 'Generic step failed after 4 attempts (category: exit-status-1)' <<< "${OUT}"; then
  pass "delays double up to the configured cap"
else
  fail "delay cap (rc=${RC}, sleeps='$(sleeps)'): ${OUT}"
fi

# --- install-podman.sh -----------------------------------------------------

run_podman() {
  RC=0
  OUT="$(PATH="${STUB_DIR}/bin:${PATH}" "$@" bash "${SCRIPT_DIR}/install-podman.sh" 2>&1)" || RC=$?
}

# The stub payload never matches the pinned checksum.
setup_stubs http:200
run_podman env
if [[ "${RC}" -ne 0 && "$(curl_calls)" -eq 1 && ! -s "${STUB_DIR}/sudo.log" ]] \
  && grep -q "stage 'verify checksum' (category: integrity); not retried" <<< "${OUT}"; then
  pass "podman checksum mismatch fails closed without retry or extraction"
else
  fail "podman checksum mismatch (rc=${RC}, calls=$(curl_calls), sudo='$(cat "${STUB_DIR}/sudo.log")'): ${OUT}"
fi

setup_stubs http:504
run_podman env RETRY_MAX_ATTEMPTS=3
if [[ "${RC}" -ne 0 && "$(curl_calls)" -eq 3 && ! -s "${STUB_DIR}/sudo.log" ]] \
  && grep -qF "::error::Podman ${PODMAN_TAG} (amd64) download failed after 3 attempts (category: transient HTTP 504)" <<< "${OUT}" \
  && grep -q "Podman setup failed at stage 'download'" <<< "${OUT}"; then
  pass "podman download exhaustion names dependency, version, stage and category"
else
  fail "podman download exhaustion (rc=${RC}, calls=$(curl_calls)): ${OUT}"
fi

# --- install-openshell.sh --------------------------------------------------

run_openshell() {
  RC=0
  OUT="$(PATH="${STUB_DIR}/bin:${PATH}" HOME="${STUB_DIR}/home" GITHUB_ENV="" \
    bash "${SCRIPT_DIR}/install-openshell.sh" 2>&1)" || RC=$?
}

# curl output is piped into the real sh: "exit N" simulates the installer.
setup_stubs exit:22 "stdout:exit 3" "stdout:exit 0"
run_openshell
if [[ "${RC}" -eq 0 && "$(curl_calls)" -eq 3 && "$(sleeps)" == "10 20" ]] \
  && grep -q 'category: install.sh download (curl exit 22)' <<< "${OUT}" \
  && grep -q 'category: installer (package download, checksum or install; exit 3)' <<< "${OUT}"; then
  pass "openshell install retries download and installer failures, then succeeds"
else
  fail "openshell recovery (rc=${RC}, calls=$(curl_calls), sleeps='$(sleeps)'): ${OUT}"
fi

setup_stubs "stdout:exit 1"
RC=0
OUT="$(PATH="${STUB_DIR}/bin:${PATH}" HOME="${STUB_DIR}/home" GITHUB_ENV="" RETRY_MAX_ATTEMPTS=2 \
  bash "${SCRIPT_DIR}/install-openshell.sh" 2>&1)" || RC=$?
if [[ "${RC}" -ne 0 && "$(curl_calls)" -eq 2 ]] \
  && grep -qF "::error::OpenShell v${OPENSHELL_VERSION} install failed after 2 attempts" <<< "${OUT}" \
  && grep -q "OpenShell setup failed at stage 'install'" <<< "${OUT}"; then
  pass "openshell install exhaustion names dependency, version and stage"
else
  fail "openshell exhaustion (rc=${RC}, calls=$(curl_calls)): ${OUT}"
fi

if [[ "${FAILURES}" -gt 0 ]]; then
  printf '\n%d test(s) failed\n' "${FAILURES}" >&2
  exit 1
fi
printf '\nAll tests passed\n'
