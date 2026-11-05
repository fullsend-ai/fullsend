#!/usr/bin/env bash
# update-go-checksums-test.sh — Tests for update-go-checksums.sh
#
# Run from the repo root:
#   bash scripts/renovate/update-go-checksums-test.sh
#
# Each case copies the real code and runner Containerfiles into a scratch
# directory, rewrites GO_VERSION as a Renovate bump would, and runs the
# script there with a mock curl that serves a fixture go.dev release index.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
SCRIPT="${SCRIPT_DIR}/update-go-checksums.sh"
FAILURES=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

NEW_VERSION="1.99.3"
NEW_AMD64="1111111111111111111111111111111111111111111111111111111111111111"
NEW_ARM64="2222222222222222222222222222222222222222222222222222222222222222"
OTHER_SHA="3333333333333333333333333333333333333333333333333333333333333333"

# Fixture shaped like https://go.dev/dl/?mode=json&include=all. Decoys for
# other versions, OSes, and kinds must not be picked.
FIXTURE="${TMPDIR}/releases.json"
cat > "${FIXTURE}" <<EOF
[
  {
    "version": "go${NEW_VERSION}",
    "stable": true,
    "files": [
      {"filename": "go${NEW_VERSION}.src.tar.gz", "os": "", "arch": "", "version": "go${NEW_VERSION}", "sha256": "${OTHER_SHA}", "kind": "source"},
      {"filename": "go${NEW_VERSION}.darwin-arm64.tar.gz", "os": "darwin", "arch": "arm64", "version": "go${NEW_VERSION}", "sha256": "${OTHER_SHA}", "kind": "archive"},
      {"filename": "go${NEW_VERSION}.linux-amd64.tar.gz", "os": "linux", "arch": "amd64", "version": "go${NEW_VERSION}", "sha256": "${NEW_AMD64}", "kind": "archive"},
      {"filename": "go${NEW_VERSION}.linux-arm64.tar.gz", "os": "linux", "arch": "arm64", "version": "go${NEW_VERSION}", "sha256": "${NEW_ARM64}", "kind": "archive"}
    ]
  },
  {
    "version": "go1.99.2",
    "stable": true,
    "files": [
      {"filename": "go1.99.2.linux-amd64.tar.gz", "os": "linux", "arch": "amd64", "version": "go1.99.2", "sha256": "${OTHER_SHA}", "kind": "archive"},
      {"filename": "go1.99.2.linux-arm64.tar.gz", "os": "linux", "arch": "arm64", "version": "go1.99.2", "sha256": "${OTHER_SHA}", "kind": "archive"}
    ]
  },
  {
    "version": "go1.99.4",
    "stable": true,
    "files": [
      {"filename": "go1.99.4.linux-amd64.tar.gz", "os": "linux", "arch": "amd64", "version": "go1.99.4", "sha256": "not-a-sha", "kind": "archive"},
      {"filename": "go1.99.4.linux-arm64.tar.gz", "os": "linux", "arch": "arm64", "version": "go1.99.4", "sha256": "${NEW_ARM64}", "kind": "archive"}
    ]
  }
]
EOF

# build_mock_curl writes a curl stub that copies $1 to the -o target, or
# fails when $1 is "fail". It rejects any URL other than the go.dev index.
build_mock_curl() {
  local body="$1"
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
    -*) shift ;;
    *) url="\$1"; shift ;;
  esac
done
if [[ "\${url}" != "https://go.dev/dl/?mode=json&include=all" ]]; then
  echo "mock curl: unexpected URL: \${url}" >&2
  exit 1
fi
if [[ '${body}' == "fail" ]]; then
  echo "curl: (22) The requested URL returned error: 500" >&2
  exit 22
fi
cp '${body}' "\${out}"
MOCKEOF
  chmod +x "${mock_bin}/curl"
  echo "${mock_bin}"
}

# setup_workdir creates a scratch tree with both Containerfiles bumped to
# the given GO_VERSION values (stale checksums left in place).
#   $1 — GO_VERSION for images/code/Containerfile
#   $2 — GO_VERSION for images/runner/Containerfile
setup_workdir() {
  local code_ver="$1" runner_ver="$2"
  local wd="${TMPDIR}/work"
  rm -rf "${wd}"
  mkdir -p "${wd}/images/code" "${wd}/images/runner"
  cp "${REPO_ROOT}/images/code/Containerfile" "${wd}/images/code/Containerfile"
  cp "${REPO_ROOT}/images/runner/Containerfile" "${wd}/images/runner/Containerfile"
  sed -i "s/^ARG GO_VERSION=.*/ARG GO_VERSION=${code_ver}/" "${wd}/images/code/Containerfile"
  sed -i "s/^ARG GO_VERSION=.*/ARG GO_VERSION=${runner_ver}/" "${wd}/images/runner/Containerfile"
  echo "${wd}"
}

snapshot() {
  cat "$1/images/code/Containerfile" "$1/images/runner/Containerfile"
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
# that neither Containerfile was modified.
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
    fail "${name}: Containerfiles were modified on failure"
  else
    pass "${name}"
  fi
}

# --- Happy path: both files get the published checksums ---
MOCK=$(build_mock_curl "${FIXTURE}")
WD=$(setup_workdir "${NEW_VERSION}" "${NEW_VERSION}")
before_code=$(cat "${WD}/images/code/Containerfile")
rc=0
output=$(run_script "${WD}" "${MOCK}") || rc=$?
if [[ "${rc}" -ne 0 ]]; then
  fail "updates checksums: exit ${rc} (output: ${output})"
else
  ok=1
  for f in images/code/Containerfile images/runner/Containerfile; do
    if ! grep -qx "ARG GO_SHA256_AMD64=${NEW_AMD64}" "${WD}/${f}" \
      || ! grep -qx "ARG GO_SHA256_ARM64=${NEW_ARM64}" "${WD}/${f}"; then
      fail "updates checksums: ${f} not updated"
      ok=0
    fi
  done
  # Only the two checksum lines may change.
  changed=$(diff <(echo "${before_code}") "${WD}/images/code/Containerfile" | grep -c '^>' || true)
  if [[ "${changed}" != "2" ]]; then
    fail "updates checksums: expected 2 changed lines in code Containerfile, got ${changed}"
    ok=0
  fi
  if [[ "${ok}" -eq 1 ]]; then
    pass "updates checksums in both Containerfiles"
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
  fail "second run is a no-op: files changed"
else
  pass "second run is a no-op"
fi

# --- Failure cases: non-zero exit and no file changes ---
WD=$(setup_workdir "1.98.0" "1.98.0")
expect_failure "unknown version" "${WD}" "${MOCK}" "no valid linux-amd64 archive sha256 for go1.98.0"

WD=$(setup_workdir "1.99.4" "1.99.4")
expect_failure "malformed checksum in index" "${WD}" "${MOCK}" "no valid linux-amd64 archive sha256 for go1.99.4"

WD=$(setup_workdir "${NEW_VERSION}" "1.99.2")
expect_failure "GO_VERSION mismatch between files" "${WD}" "${MOCK}" "GO_VERSION mismatch"

WD=$(setup_workdir "${NEW_VERSION}" "${NEW_VERSION}")
sed -i '/^ARG GO_SHA256_ARM64=/d' "${WD}/images/runner/Containerfile"
expect_failure "missing ARG line" "${WD}" "${MOCK}" "expected exactly one 'ARG GO_SHA256_ARM64=' line in images/runner/Containerfile, found 0"

WD=$(setup_workdir "${NEW_VERSION}" "${NEW_VERSION}")
sed -i '/^ARG GO_VERSION=/d' "${WD}/images/code/Containerfile"
expect_failure "missing GO_VERSION" "${WD}" "${MOCK}" "could not extract GO_VERSION from images/code/Containerfile"

WD=$(setup_workdir "${NEW_VERSION}" "${NEW_VERSION}")
expect_failure "fetch failure" "${WD}" "$(build_mock_curl fail)" "failed to fetch"

echo
if [[ "${FAILURES}" -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All tests passed"
