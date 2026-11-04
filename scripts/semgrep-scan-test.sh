#!/usr/bin/env bash
# semgrep-scan-test.sh — Tests for semgrep-scan
# Run from the repo root: bash scripts/semgrep-scan-test.sh
set -euo pipefail

REPO_ROOT="$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_ROOT="$(CDPATH='' cd -- "$(mktemp -d "${TMPDIR:-/tmp}/semgrep-scan-test.XXXXXX")" && pwd -P)"
trap 'rm -rf "$TEST_ROOT"' EXIT

FIXTURE_ROOT="${TEST_ROOT}/repo"
STUB_BIN="${TEST_ROOT}/bin"
mkdir -p "${FIXTURE_ROOT}/scripts" "${FIXTURE_ROOT}/tools/semgrep" \
	"${FIXTURE_ROOT}/src" "${STUB_BIN}"
cp "${REPO_ROOT}/scripts/semgrep-scan" "${FIXTURE_ROOT}/scripts/semgrep-scan"
chmod +x "${FIXTURE_ROOT}/scripts/semgrep-scan"
TEST_COMMIT=0123456789abcdef0123456789abcdef01234567
printf '%s\n' "$TEST_COMMIT" > "${FIXTURE_ROOT}/scripts/semgrep-ruleset-commit"
printf '%s\n' '# Test fixture membership' 'go/lang/security/audit/example.yaml' \
	> "${FIXTURE_ROOT}/scripts/semgrep-ruleset-files.txt"
printf '%s\n' 'package main' > "${FIXTURE_ROOT}/src/main.go"
SOURCE_ROOT="${TEST_ROOT}/upstream"
SOURCE_DIR="${SOURCE_ROOT}/semgrep-rules-${TEST_COMMIT}"
mkdir -p "${SOURCE_DIR}/go/lang/security/audit" \
	"${SOURCE_DIR}/go/lang/security/vuln"
printf '%s\n' 'rules: []' > "${SOURCE_DIR}/go/lang/security/audit/example.yaml"
printf '%s\n' 'rules: []' > "${SOURCE_DIR}/go/lang/security/audit/example.test.yml"
printf '%s\n' 'rules: []' > "${SOURCE_DIR}/go/lang/security/vuln/outside-scope.yaml"
tar -czf "${TEST_ROOT}/valid-rules.tar.gz" -C "$SOURCE_ROOT" "semgrep-rules-${TEST_COMMIT}"
cp "${TEST_ROOT}/valid-rules.tar.gz" "${TEST_ROOT}/wrong-rules.tar.gz"
printf '%s\n' 'tamper' >> "${TEST_ROOT}/wrong-rules.tar.gz"
if command -v sha256sum >/dev/null 2>&1; then
	EXPECTED_SHA="$(sha256sum "${TEST_ROOT}/valid-rules.tar.gz" | cut -d ' ' -f 1)"
else
	EXPECTED_SHA="$(shasum -a 256 "${TEST_ROOT}/valid-rules.tar.gz" | cut -d ' ' -f 1)"
fi
printf '%s\n' "$EXPECTED_SHA" > "${FIXTURE_ROOT}/scripts/semgrep-ruleset.sha256"

cat > "${STUB_BIN}/curl" <<'CURL_STUB'
#!/usr/bin/env bash
set -euo pipefail
: "${CURL_CALLS:?}"
: "${CURL_ARGS_FILE:?}"
printf '%s\n' called >> "$CURL_CALLS"
printf '%s\n' "$@" > "$CURL_ARGS_FILE"
output=
while (($#)); do
	if [[ "$1" == --output ]]; then
		output="$2"
		shift 2
	else
		shift
	fi
done
if [[ "${CURL_MODE:-payload}" == fail ]]; then
	exit 22
fi
cp "$CURL_PAYLOAD" "$output"
CURL_STUB

cat > "${STUB_BIN}/uv" <<'UV_STUB'
#!/usr/bin/env bash
set -euo pipefail
pwd -P > "$UV_CWD_FILE"
printf '%s\n' "$@" > "$UV_ARGS_FILE"
UV_STUB
chmod +x "${STUB_BIN}/curl" "${STUB_BIN}/uv"

fail() {
	echo "FAIL: $1" >&2
	exit 1
}

pass() {
	echo "PASS: $1"
}

run_scan() {
	local cache_dir="$1"
	local curl_mode="$2"
	local curl_payload="$3"
	local curl_calls="$4"
	local required_value="$5"
	local uv_prefix="$6"

	env \
		CI=true \
		GITHUB_ACTIONS=true \
		SEMGREP_REQUIRED="$required_value" \
		XDG_CACHE_HOME="$cache_dir" \
		CURL_MODE="$curl_mode" \
		CURL_PAYLOAD="$curl_payload" \
		CURL_CALLS="$curl_calls" \
		CURL_ARGS_FILE="${uv_prefix}.curl-args" \
		UV_CWD_FILE="${uv_prefix}.cwd" \
		UV_ARGS_FILE="${uv_prefix}.args" \
		PATH="${STUB_BIN}:${PATH}" \
		"${FIXTURE_ROOT}/scripts/semgrep-scan" \
		--sarif-output report.sarif src/main.go
}

GOOD_CACHE="${TEST_ROOT}/cache-good"
GOOD_CALLS="${TEST_ROOT}/good-curl-calls"
GOOD_UV="${TEST_ROOT}/good-uv"
run_scan "$GOOD_CACHE" payload "${TEST_ROOT}/valid-rules.tar.gz" \
	"$GOOD_CALLS" 0 "$GOOD_UV" || fail "valid pinned ruleset did not scan"
[[ -f "${GOOD_CACHE}/fullsend/semgrep/${TEST_COMMIT}-${EXPECTED_SHA}.tar.gz" ]] || \
	fail "verified upstream archive was not cached under its commit and SHA"
grep -Fq "https://codeload.github.com/semgrep/semgrep-rules/tar.gz/${TEST_COMMIT}" \
	"${GOOD_UV}.curl-args" || fail "download did not use the pinned commit URL"

run_scan "$GOOD_CACHE" fail "${TEST_ROOT}/valid-rules.tar.gz" \
	"$GOOD_CALLS" 0 "$GOOD_UV" || fail "cached ruleset did not scan offline"
[[ "$(wc -l < "$GOOD_CALLS")" -eq 1 ]] || fail "cached ruleset triggered another download"
RULES_CWD="$(cat "${GOOD_UV}.cwd")"
case "$RULES_CWD" in
"${GOOD_CACHE}/fullsend/semgrep/.security-audit-rules."*) ;;
*) fail "Semgrep did not run from a temporary extracted rules directory" ;;
esac
UV_ARGS=()
while IFS= read -r arg; do
	UV_ARGS[${#UV_ARGS[@]}]="$arg"
done < "${GOOD_UV}.args"
found_audit_rule=false
found_audit_test=false
found_out_of_scope_rule=false
found_absolute_config=false
for ((i = 0; i + 1 < ${#UV_ARGS[@]}; i++)); do
	if [[ "${UV_ARGS[i]}" == --config ]]; then
		case "${UV_ARGS[i + 1]}" in
		go/lang/security/audit/example.yaml) found_audit_rule=true ;;
		/*) found_absolute_config=true ;;
		*.test.yml) found_audit_test=true ;;
		go/lang/security/vuln/*) found_out_of_scope_rule=true ;;
		esac
	fi
done
[[ "$found_audit_rule" == true ]] || fail "security/audit rule was not passed to Semgrep"
[[ "$found_absolute_config" == false ]] || fail "Semgrep config path was absolute"
[[ "$found_audit_test" == false ]] || fail "upstream test fixture was passed to Semgrep"
[[ "$found_out_of_scope_rule" == false ]] || fail "out-of-scope rule was passed to Semgrep"
found_strict=false
found_error=false
for arg in "${UV_ARGS[@]}"; do
	[[ "$arg" == --strict ]] && found_strict=true
	[[ "$arg" == --error ]] && found_error=true
done
[[ "$found_strict" == true ]] || fail "--strict was not passed to Semgrep"
[[ "$found_error" == true ]] || fail "--error was not passed to Semgrep"
found_sarif_path=false
for ((i = 0; i + 1 < ${#UV_ARGS[@]}; i++)); do
	if [[ "${UV_ARGS[i]}" == --sarif-output && "${UV_ARGS[i + 1]}" == "${FIXTURE_ROOT}/report.sarif" ]]; then
		found_sarif_path=true
	fi
done
[[ "$found_sarif_path" == true ]] || fail "relative SARIF output path was not rooted at the repository"
last_arg_index=$((${#UV_ARGS[@]} - 1))
[[ "${UV_ARGS[$last_arg_index]}" == "${FIXTURE_ROOT}/src/main.go" ]] || \
	fail "relative target path was not rooted at the repository"
pass "verified cache reuse and relative output/target path resolution"

expect_manifest_rejected() {
	local name="$1"
	local manifest_entry="$2"
	local cache_dir="${TEST_ROOT}/cache-manifest-${name}"
	local uv_prefix="${TEST_ROOT}/manifest-${name}-uv"
	local output
	printf '%s\n' "$manifest_entry" > "${FIXTURE_ROOT}/scripts/semgrep-ruleset-files.txt"
	if output="$(run_scan "$cache_dir" payload "${TEST_ROOT}/valid-rules.tar.gz" \
		"${TEST_ROOT}/manifest-${name}-curl-calls" 1 "$uv_prefix" 2>&1)"; then
		fail "${name} allowlist entry returned success"
	fi
	[[ "$output" == *"refusing to scan with an unverified ruleset"* ]] || \
		fail "${name} allowlist entry was not rejected as invalid"
	[[ ! -e "${uv_prefix}.cwd" ]] || fail "${name} allowlist entry invoked Semgrep"
}

expect_manifest_rejected traversal '../go/lang/security/audit/example.yaml'
expect_manifest_rejected out-of-scope 'go/lang/security/vuln/outside-scope.yaml'
expect_manifest_rejected upstream-test 'go/lang/security/audit/example.test.yml'
expect_manifest_rejected missing-file 'go/lang/security/audit/missing.yaml'
expect_manifest_rejected comment-only '# no source files selected'
printf '%s\n' 'go/lang/security/audit/example.yaml' \
	> "${FIXTURE_ROOT}/scripts/semgrep-ruleset-files.txt"
pass "allowlist rejects traversal, out-of-scope, test, missing, and empty entries"

LOCAL_OFFLINE_CACHE="${TEST_ROOT}/cache-local-offline"
LOCAL_OFFLINE_CALLS="${TEST_ROOT}/local-offline-curl-calls"
if output="$(run_scan "$LOCAL_OFFLINE_CACHE" fail "${TEST_ROOT}/valid-rules.tar.gz" \
	"$LOCAL_OFFLINE_CALLS" 0 "${TEST_ROOT}/local-offline-uv" 2>&1)"; then
	[[ "$output" == *"warning:"* && "$output" == *"skipping this scan"* ]] || \
		fail "local network failure did not warn and skip"
else
	fail "local network failure returned nonzero"
fi
[[ ! -e "${TEST_ROOT}/local-offline-uv.cwd" ]] || fail "local offline scan invoked Semgrep"
pass "local network failure warns and skips"

CI_OFFLINE_CACHE="${TEST_ROOT}/cache-ci-offline"
if output="$(run_scan "$CI_OFFLINE_CACHE" fail "${TEST_ROOT}/valid-rules.tar.gz" \
	"${TEST_ROOT}/ci-offline-curl-calls" 1 "${TEST_ROOT}/ci-offline-uv" 2>&1)"; then
	fail "CI network failure returned success"
fi
[[ "$output" == *"failing the required scan"* ]] || fail "required network failure did not report fail-closed behavior"
[[ ! -e "${TEST_ROOT}/ci-offline-uv.cwd" ]] || fail "CI offline scan invoked Semgrep"
pass "CI network failure fails closed"

for mode in local ci; do
	mismatch_cache="${TEST_ROOT}/cache-mismatch-${mode}"
	ci_value=0
	[[ "$mode" == ci ]] && ci_value=1
	if output="$(run_scan "$mismatch_cache" payload "${TEST_ROOT}/wrong-rules.tar.gz" \
		"${TEST_ROOT}/mismatch-${mode}-curl-calls" "$ci_value" \
		"${TEST_ROOT}/mismatch-${mode}-uv" 2>&1)"; then
		fail "${mode} hash mismatch returned success"
	fi
	[[ "$output" == *"does not match the pin"* ]] || fail "${mode} hash mismatch was not reported"
	[[ ! -e "${TEST_ROOT}/mismatch-${mode}-uv.cwd" ]] || \
		fail "${mode} hash mismatch invoked Semgrep"
done
pass "hash mismatches fail in both local runs and CI"
