#!/usr/bin/env bash
# redact-behaviour-artifacts-test.sh — Tests for redact-behaviour-artifacts.sh
#
# Run from repo root: bash scripts/redact-behaviour-artifacts-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REDACT_SCRIPT="${SCRIPT_DIR}/redact-behaviour-artifacts.sh"
FAILURES=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

# Build secret-like literals at runtime so pre-commit detect-private-key and secret
# scanners do not see contiguous token/PEM patterns in the repository.
fake_ghp_token() {
  printf '%s%s' "gh" "p_${1}"
}

fake_pem_block() {
  local kind="$1"
  local payload="$2"
  printf '%s\n' \
    "$(printf '%s %s %s-----' '-----BEGIN' "${kind}" 'PRIVATE KEY')" \
    "${payload}" \
    "$(printf '%s %s %s-----' '-----END' "${kind}" 'PRIVATE KEY')"
}

fake_pgp_block() {
  local payload="$1"
  printf '%s\n' \
    "$(printf '%s %s %s %s-----' '-----BEGIN' 'PGP' 'PRIVATE KEY' 'BLOCK')" \
    "${payload}" \
    "$(printf '%s %s %s %s-----' '-----END' 'PGP' 'PRIVATE KEY' 'BLOCK')"
}

run_test() {
  local test_name="$1"
  local must_not_contain="$2"
  local must_contain="${3:-}"

  local actual
  actual="$(<"${TMPDIR}/artifact.log")"

  if [ -n "${must_not_contain}" ] && echo "${actual}" | grep -qF "${must_not_contain}"; then
    echo "FAIL: ${test_name}"
    echo "  sanitized output still contains: '${must_not_contain}'"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if [ -n "${must_contain}" ] && ! echo "${actual}" | grep -qF "${must_contain}"; then
    echo "FAIL: ${test_name}"
    echo "  expected to find: '${must_contain}'"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

run_redaction() {
  rm -rf "${TMPDIR}/artifacts"
  mkdir -p "${TMPDIR}/artifacts"
  cp "${TMPDIR}/artifact.log" "${TMPDIR}/artifacts/artifact.log"
  ARTIFACT_DIR="${TMPDIR}/artifacts" bash "${REDACT_SCRIPT}" >/dev/null
  cp "${TMPDIR}/artifacts/artifact.log" "${TMPDIR}/artifact.log"
}

run_redaction_on_tree() {
  ARTIFACT_DIR="${TMPDIR}/artifacts" bash "${REDACT_SCRIPT}" >/dev/null
}

echo "==> PEM block redaction"
cat >"${TMPDIR}/artifact.log" <<EOF
before
$(fake_pem_block "RSA" "MIIEowIBAAKCAQEAfake")
after
EOF
run_redaction
run_test "redacts-rsa-pem-block" "MIIEowIBAAKCAQEAfake" "[REDACTED PRIVATE KEY]"

cat >"${TMPDIR}/artifact.log" <<EOF
$(fake_pgp_block "lQOYBCA")
EOF
run_redaction
run_test "redacts-pgp-pem-block" "lQOYBCA" "[REDACTED PRIVATE KEY]"

echo "==> Token pattern redaction"
TOKEN="$(fake_ghp_token "abcdefghijklmnopqrstuvwxyz1234567890")"
cat >"${TMPDIR}/artifact.log" <<EOF
auth failed with ${TOKEN}
EOF
run_redaction
run_test "redacts-ghp-token" "${TOKEN}"

cat >"${TMPDIR}/artifact.log" <<EOF
remote: https://x-access-token:ghp_secret@github.com/org/repo.git
EOF
run_redaction
run_test "redacts-access-token-url" "ghp_secret"

cat >"${TMPDIR}/artifact.log" <<'EOF'
Authorization: Bearer abcdefghij+/=TOKENVALUE
keep sentinel line two
EOF
run_redaction
run_test "redacts-bearer-base64-chars" "abcdefghij+/=TOKENVALUE" "keep sentinel line two"

echo "==> Literal secret redaction"
cat >"${TMPDIR}/artifact.log" <<'EOF'
dumped literal-secret-pem-value in log
Normal log line: assertion failed at step 3
EOF
mkdir -p "${TMPDIR}/artifacts"
cp "${TMPDIR}/artifact.log" "${TMPDIR}/artifacts/artifact.log"
ARTIFACT_DIR="${TMPDIR}/artifacts" TEST_CODER_PEM="literal-secret-pem-value" bash "${REDACT_SCRIPT}" >/dev/null
cp "${TMPDIR}/artifacts/artifact.log" "${TMPDIR}/artifact.log"
run_test "redacts-literal-env-secret" "literal-secret-pem-value" "Normal log line: assertion failed at step 3"

cat >"${TMPDIR}/artifact.log" <<'EOF'
dumped literal-secret\value in log
EOF
mkdir -p "${TMPDIR}/artifacts"
cp "${TMPDIR}/artifact.log" "${TMPDIR}/artifacts/artifact.log"
ARTIFACT_DIR="${TMPDIR}/artifacts" TEST_CODER_PEM='literal-secret\value' bash "${REDACT_SCRIPT}" >/dev/null
cp "${TMPDIR}/artifacts/artifact.log" "${TMPDIR}/artifact.log"
run_test "redacts-literal-backslash-secret" 'literal-secret\value'

echo "==> Compressed artifact redaction"
rm -rf "${TMPDIR}/artifacts"
mkdir -p "${TMPDIR}/artifacts"
TOKEN="$(fake_ghp_token "abcdefghijklmnopqrstuvwxyz1234567890")"
printf 'token=%s\n' "${TOKEN}" >"${TMPDIR}/artifacts/secret.log"
gzip -c "${TMPDIR}/artifacts/secret.log" >"${TMPDIR}/artifacts/secret.log.gz"
rm -f "${TMPDIR}/artifacts/secret.log"
run_redaction_on_tree
gunzip -c "${TMPDIR}/artifacts/secret.log.gz" >"${TMPDIR}/artifact.log"
run_test "redacts-gzip-log" "${TOKEN}"

rm -rf "${TMPDIR}/artifacts"
mkdir -p "${TMPDIR}/artifacts/inner"
printf 'leaked literal-secret-pem-value here\n' >"${TMPDIR}/artifacts/inner/nested.log"
(
  cd "${TMPDIR}/artifacts/inner"
  zip -qr "../bundle.zip" nested.log
)
rm -rf "${TMPDIR}/artifacts/inner"
ARTIFACT_DIR="${TMPDIR}/artifacts" TEST_CODER_PEM="literal-secret-pem-value" bash "${REDACT_SCRIPT}" >/dev/null
mkdir -p "${TMPDIR}/unzipped"
unzip -q "${TMPDIR}/artifacts/bundle.zip" -d "${TMPDIR}/unzipped"
cp "${TMPDIR}/unzipped/nested.log" "${TMPDIR}/artifact.log"
run_test "redacts-zip-nested-log" "literal-secret-pem-value"

rm -rf "${TMPDIR}/artifacts" "${TMPDIR}/unzipped"
mkdir -p "${TMPDIR}/artifacts/inner"
printf 'leaked literal-secret-pem-value here\n' >"${TMPDIR}/artifacts/inner/nested.log"
tar -czf "${TMPDIR}/artifacts/bundle.tar.gz" -C "${TMPDIR}/artifacts/inner" nested.log
rm -rf "${TMPDIR}/artifacts/inner"
ARTIFACT_DIR="${TMPDIR}/artifacts" TEST_CODER_PEM="literal-secret-pem-value" bash "${REDACT_SCRIPT}" >/dev/null
mkdir -p "${TMPDIR}/unzipped"
tar -xzf "${TMPDIR}/artifacts/bundle.tar.gz" -C "${TMPDIR}/unzipped"
cp "${TMPDIR}/unzipped/nested.log" "${TMPDIR}/artifact.log"
run_test "redacts-tar-gz-nested-log" "literal-secret-pem-value"

echo "==> Encrypted/opaque artifact handling"
rm -rf "${TMPDIR}/artifacts" "${TMPDIR}/unzipped"
mkdir -p "${TMPDIR}/artifacts"
printf 'opaque-binary-secret' >"${TMPDIR}/artifacts/payload.gpg"
run_redaction_on_tree
cp "${TMPDIR}/artifacts/payload.gpg" "${TMPDIR}/artifact.log"
run_test "stubs-encrypted-artifact" "opaque-binary-secret" "[REDACTED OPAQUE CONTENT]"

rm -rf "${TMPDIR}/artifacts"
mkdir -p "${TMPDIR}/artifacts"
printf 'GIF89a' >"${TMPDIR}/artifacts/exfil.gif"
printf '%s' "$(printf 'fake-secret-payload' | base64)" >>"${TMPDIR}/artifacts/exfil.gif"
run_redaction_on_tree
cp "${TMPDIR}/artifacts/exfil.gif" "${TMPDIR}/artifact.log"
run_test "stubs-fake-media-artifact" "fake-secret-payload" "[REDACTED OPAQUE CONTENT]"

echo "==> Adversarial and edge-case handling"
rm -rf "${TMPDIR}/artifacts" "${TMPDIR}/unzipped"
mkdir -p "${TMPDIR}/artifacts"
PEM_BODY="MIIEowIBAAKCAQEAfakebodyline"
cat >"${TMPDIR}/artifacts/artifact.log" <<EOF
keep this sentinel line
${PEM_BODY}
EOF
ARTIFACT_DIR="${TMPDIR}/artifacts" TEST_CODER_PEM="$(fake_pem_block "RSA" "${PEM_BODY}")" bash "${REDACT_SCRIPT}" >/dev/null
cp "${TMPDIR}/artifacts/artifact.log" "${TMPDIR}/artifact.log"
run_test "redacts-multiline-pem-body-line" "${PEM_BODY}" "keep this sentinel line"

rm -rf "${TMPDIR}/artifacts"
mkdir -p "${TMPDIR}/artifacts"
printf '{"key":"%s"}\n' "$(fake_pem_block "RSA" "inlinejsonfake" | tr '\n' ' ')" >"${TMPDIR}/artifacts/embedded.json"
printf 'keep sentinel after json pem\n' >>"${TMPDIR}/artifacts/embedded.json"
run_redaction_on_tree
cp "${TMPDIR}/artifacts/embedded.json" "${TMPDIR}/artifact.log"
run_test "redacts-one-line-json-pem" "inlinejsonfake" "keep sentinel after json pem"

rm -rf "${TMPDIR}/artifacts"
mkdir -p "${TMPDIR}/artifacts"
printf 'root:%s:0:0:root:/root:/bin/bash\n' 'x' >"${TMPDIR}/passwd-sample"
ln -sf "${TMPDIR}/passwd-sample" "${TMPDIR}/artifacts/leak.log"
run_redaction_on_tree
cp "${TMPDIR}/artifacts/leak.log" "${TMPDIR}/artifact.log"
run_test "stubs-top-level-symlink" "root:x:0:0" "[REDACTED OPAQUE CONTENT]"

rm -rf "${TMPDIR}/artifacts" "${TMPDIR}/unzipped"
mkdir -p "${TMPDIR}/artifacts/inner"
printf 'leaked literal-secret-pem-value here\nkeep zip sentinel\n' >"${TMPDIR}/artifacts/inner/nested.log"
(
  cd "${TMPDIR}/artifacts/inner"
  zip -qr "../bundle.zip" nested.log
)
rm -rf "${TMPDIR}/artifacts/inner"
(
  cd "${TMPDIR}"
  ARTIFACT_DIR=artifacts TEST_CODER_PEM="literal-secret-pem-value" bash "${REDACT_SCRIPT}" >/dev/null
)
mkdir -p "${TMPDIR}/unzipped"
unzip -q "${TMPDIR}/artifacts/bundle.zip" -d "${TMPDIR}/unzipped"
cp "${TMPDIR}/unzipped/nested.log" "${TMPDIR}/artifact.log"
run_test "redacts-zip-with-relative-artifact-dir" "literal-secret-pem-value" "keep zip sentinel"

rm -rf "${TMPDIR}/artifacts"
mkdir -p "${TMPDIR}/artifacts"
printf 'keep readable sentinel\n' >"${TMPDIR}/artifacts/ok.log"
printf 'secret-in-unreadable\n' >"${TMPDIR}/artifacts/blocked.log"
chmod 000 "${TMPDIR}/artifacts/blocked.log"
ARTIFACT_DIR="${TMPDIR}/artifacts" bash "${REDACT_SCRIPT}" >/dev/null
chmod 644 "${TMPDIR}/artifacts/blocked.log"
cp "${TMPDIR}/artifacts/ok.log" "${TMPDIR}/artifact.log"
run_test "continues-after-unreadable-file" "secret-in-unreadable" "keep readable sentinel"
grep -q '\[REDACTED OPAQUE CONTENT\]' "${TMPDIR}/artifacts/blocked.log" || {
  echo "FAIL: stubs-unreadable-file"
  echo "  expected blocked.log to be stubbed"
  FAILURES=$((FAILURES + 1))
}
[ "${FAILURES}" -eq 0 ] && echo "PASS: stubs-unreadable-file"

rm -rf "${TMPDIR}/artifacts"
mkdir -p "${TMPDIR}/artifacts"
printf 'before\0after\nkeep nul sentinel\n' >"${TMPDIR}/artifacts/binary.log"
run_redaction_on_tree
cp "${TMPDIR}/artifacts/binary.log" "${TMPDIR}/artifact.log"
run_test "stubs-nul-byte-log" $'before\0after' "[REDACTED OPAQUE CONTENT]"

rm -rf "${TMPDIR}/artifacts"
mkdir -p "${TMPDIR}/artifacts"
python3 - <<'PY' "${TMPDIR}/artifacts/oversize.gz"
import gzip, pathlib, sys
path = pathlib.Path(sys.argv[1])
with gzip.open(path, "wb") as handle:
    handle.write(b"x" * (11 * 1024 * 1024))
PY
run_redaction_on_tree
cp "${TMPDIR}/artifacts/oversize.gz" "${TMPDIR}/artifact.log"
run_test "stubs-gzip-bomb" "xxxxxxxx" "[REDACTED OPAQUE CONTENT]"

echo ""
if [ "${FAILURES}" -gt 0 ]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All redact-behaviour-artifacts tests passed"
