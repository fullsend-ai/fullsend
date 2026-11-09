#!/usr/bin/env bash
# update-fd-checksums-test.sh — Tests for update-fd-checksums.sh
#
# Run from the repo root:
#   bash scripts/renovate/update-fd-checksums-test.sh
#
# Each case copies the real sandbox Containerfile into a scratch directory,
# rewrites FD_VERSION as a Renovate bump would, and runs the script there
# with a mock curl that serves fixture tarballs for the fd release URLs.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
SCRIPT="${SCRIPT_DIR}/update-fd-checksums.sh"
FAILURES=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

NEW_VERSION="99.3.1"

# Fixture tarballs: arbitrary distinct bytes. The script hashes whatever it
# downloads, so the expected checksums are computed here.
FIXTURES="${TMPDIR}/fixtures"
mkdir -p "${FIXTURES}"
echo "amd64 tarball bytes" > "${FIXTURES}/fd-v${NEW_VERSION}-x86_64-unknown-linux-musl.tar.gz"
echo "arm64 tarball bytes" > "${FIXTURES}/fd-v${NEW_VERSION}-aarch64-unknown-linux-musl.tar.gz"
NEW_AMD64=$(sha256sum "${FIXTURES}/fd-v${NEW_VERSION}-x86_64-unknown-linux-musl.tar.gz" | awk '{print $1}')
NEW_ARM64=$(sha256sum "${FIXTURES}/fd-v${NEW_VERSION}-aarch64-unknown-linux-musl.tar.gz" | awk '{print $1}')

# build_mock_curl writes a curl stub that serves files from ${FIXTURES} by
# the basename of fd release URLs under the expected prefix, and fails on
# anything else (or on everything when $1 is "fail").
build_mock_curl() {
  local mode="$1"
  local mock_bin="${TMPDIR}/bin"
  rm -rf "${mock_bin}"
  mkdir -p "${mock_bin}"
  cat > "${mock_bin}/curl" <<MOCKEOF
#!/usr/bin/env bash
out=""
url=""
while [[ \$# -gt 0 ]]; do
  case "\$1" in
    -o) out="\$2"; shift 2 ;;
    --retry|--retry-delay) shift 2 ;;
    -*) shift ;;
    *) url="\$1"; shift ;;
  esac
done
if [[ '${mode}' == "fail" ]]; then
  echo "curl: (22) The requested URL returned error: 404" >&2
  exit 22
fi
prefix="https://github.com/sharkdp/fd/releases/download/v"
if [[ "\${url}" != "\${prefix}"* ]]; then
  echo "mock curl: unexpected URL: \${url}" >&2
  exit 1
fi
file="${FIXTURES}/\$(basename "\${url}")"
if [[ ! -f "\${file}" ]]; then
  echo "curl: (22) The requested URL returned error: 404" >&2
  exit 22
fi
cp "\${file}" "\${out}"
MOCKEOF
  chmod +x "${mock_bin}/curl"
  echo "${mock_bin}"
}

# setup_workdir creates a scratch tree with the sandbox Containerfile bumped
# to FD_VERSION=$1 (stale checksums left in place).
setup_workdir() {
  local ver="$1"
  local wd="${TMPDIR}/work"
  rm -rf "${wd}"
  mkdir -p "${wd}/images/sandbox"
  cp "${REPO_ROOT}/images/sandbox/Containerfile" "${wd}/images/sandbox/Containerfile"
  sed -i "s/^ARG FD_VERSION=.*/ARG FD_VERSION=${ver}/" "${wd}/images/sandbox/Containerfile"
  echo "${wd}"
}

snapshot() {
  cat "$1/images/sandbox/Containerfile"
}

# run_script runs the script in $1 with mock curl dir $2 on PATH.
# Prints the combined output; returns the script's exit code.
run_script() {
  local wd="$1" mock_bin="$2"
  (cd "${wd}" && PATH="${mock_bin}:${PATH}" bash "${SCRIPT}" 2>&1)
}

pass() { echo "PASS: $1"; }
fail() { echo "FAIL: $1"; FAILURES=$((FAILURES + 1)); }

# expect_failure asserts a non-zero exit, an expected output substring, and
# that the Containerfile was not modified.
#   $1 — test name, $2 — workdir, $3 — mock bin, $4 — expected substring
expect_failure() {
  local name="$1" wd="$2" mock_bin="$3" want="$4"
  local before after output rc=0
  before=$(snapshot "${wd}")
  output=$(run_script "${wd}" "${mock_bin}") || rc=$?
  after=$(snapshot "${wd}")
  if [[ "${rc}" -eq 0 ]]; then
    fail "${name}: expected non-zero exit, got 0 (output: ${output})"
  elif [[ "${output}" != *"${want}"* ]]; then
    fail "${name}: output missing '${want}' (output: ${output})"
  elif [[ "${before}" != "${after}" ]]; then
    fail "${name}: Containerfile was modified on failure"
  else
    pass "${name}"
  fi
}

# --- Happy path: checksums are computed from the downloaded tarballs ---
MOCK=$(build_mock_curl serve)
WD=$(setup_workdir "${NEW_VERSION}")
before_file=$(cat "${WD}/images/sandbox/Containerfile")
rc=0
output=$(run_script "${WD}" "${MOCK}") || rc=$?
if [[ "${rc}" -ne 0 ]]; then
  fail "updates checksums: exit ${rc} (output: ${output})"
else
  ok=1
  f="${WD}/images/sandbox/Containerfile"
  if ! grep -qx "ARG FD_SHA256_AMD64=${NEW_AMD64}" "${f}" \
    || ! grep -qx "ARG FD_SHA256_ARM64=${NEW_ARM64}" "${f}"; then
    fail "updates checksums: Containerfile not updated"
    ok=0
  fi
  # Only the two checksum lines may change.
  changed=$(diff <(echo "${before_file}") "${f}" | grep -c '^>' || true)
  if [[ "${changed}" != "2" ]]; then
    fail "updates checksums: expected 2 changed lines, got ${changed}"
    ok=0
  fi
  if [[ "${ok}" -eq 1 ]]; then
    pass "updates FD_SHA256 ARGs from downloaded tarballs"
  fi
fi

# --- Second run is a no-op ---
before=$(snapshot "${WD}")
rc=0
output=$(run_script "${WD}" "${MOCK}") || rc=$?
after=$(snapshot "${WD}")
if [[ "${rc}" -ne 0 ]]; then
  fail "second run is a no-op: exit ${rc} (output: ${output})"
elif [[ "${before}" != "${after}" ]]; then
  fail "second run is a no-op: file changed"
else
  pass "second run is a no-op"
fi

# --- Failure cases: non-zero exit and no file changes ---
WD=$(setup_workdir "98.0.0")
expect_failure "unknown version (404)" "${WD}" "${MOCK}" "failed to fetch https://github.com/sharkdp/fd/releases/download/v98.0.0/"

WD=$(setup_workdir "${NEW_VERSION}")
expect_failure "fetch failure" "${WD}" "$(build_mock_curl fail)" "failed to fetch"

MOCK=$(build_mock_curl serve)
WD=$(setup_workdir "${NEW_VERSION}")
sed -i '/^ARG FD_SHA256_ARM64=/d' "${WD}/images/sandbox/Containerfile"
expect_failure "missing ARG line" "${WD}" "${MOCK}" "expected exactly one 'ARG FD_SHA256_ARM64=' line in images/sandbox/Containerfile, found 0"

WD=$(setup_workdir "${NEW_VERSION}")
sed -i '/^ARG FD_VERSION=/d' "${WD}/images/sandbox/Containerfile"
expect_failure "missing FD_VERSION" "${WD}" "${MOCK}" "could not extract FD_VERSION from images/sandbox/Containerfile"

WD=$(setup_workdir "not-a-version")
expect_failure "invalid FD_VERSION" "${WD}" "${MOCK}" "FD_VERSION is not a valid semver"

echo
if [[ "${FAILURES}" -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All tests passed"
