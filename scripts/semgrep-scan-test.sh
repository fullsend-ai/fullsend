#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/semgrep-scan-test.XXXXXX")"
trap 'rm -rf "$TEST_ROOT"' EXIT

FIXTURE_ROOT="${TEST_ROOT}/repo"
STUB_BIN="${TEST_ROOT}/bin"
mkdir -p "${FIXTURE_ROOT}/scripts" "${FIXTURE_ROOT}/tools/semgrep" \
	"${FIXTURE_ROOT}/src" "${STUB_BIN}"
cp "${REPO_ROOT}/scripts/semgrep-scan" "${FIXTURE_ROOT}/scripts/semgrep-scan"
chmod +x "${FIXTURE_ROOT}/scripts/semgrep-scan"
printf '%s\n' 'https://example.invalid/semgrep-rules.yml' \
	> "${FIXTURE_ROOT}/scripts/semgrep-ruleset-url"
printf '%s\n' 'package main' > "${FIXTURE_ROOT}/src/main.go"
printf '%s\n' 'rules: []' > "${TEST_ROOT}/valid-rules.yml"
printf '%s\n' 'rules: [not-the-pinned-rules]' > "${TEST_ROOT}/wrong-rules.yml"
EXPECTED_SHA="$(sha256sum "${TEST_ROOT}/valid-rules.yml" | cut -d ' ' -f 1)"
printf '%s\n' "$EXPECTED_SHA" > "${FIXTURE_ROOT}/scripts/semgrep-ruleset.sha256"

cat > "${STUB_BIN}/curl" <<'CURL_STUB'
#!/usr/bin/env bash
set -euo pipefail
: "${CURL_CALLS:?}"
printf '%s\n' called >> "$CURL_CALLS"
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
printf '%s\n' "$PWD" > "$UV_CWD_FILE"
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
	local ci_value="$5"
	local uv_prefix="$6"

	env \
		CI="$ci_value" \
		GITHUB_ACTIONS=false \
		XDG_CACHE_HOME="$cache_dir" \
		CURL_MODE="$curl_mode" \
		CURL_PAYLOAD="$curl_payload" \
		CURL_CALLS="$curl_calls" \
		UV_CWD_FILE="${uv_prefix}.cwd" \
		UV_ARGS_FILE="${uv_prefix}.args" \
		PATH="${STUB_BIN}:${PATH}" \
		"${FIXTURE_ROOT}/scripts/semgrep-scan" \
		--sarif-output report.sarif src/main.go
}

GOOD_CACHE="${TEST_ROOT}/cache-good"
GOOD_CALLS="${TEST_ROOT}/good-curl-calls"
GOOD_UV="${TEST_ROOT}/good-uv"
run_scan "$GOOD_CACHE" payload "${TEST_ROOT}/valid-rules.yml" \
	"$GOOD_CALLS" false "$GOOD_UV" || fail "valid pinned ruleset did not scan"
[[ -f "${GOOD_CACHE}/fullsend/semgrep/${EXPECTED_SHA}/security-audit.yml" ]] || \
	fail "verified ruleset was not cached under its SHA"

run_scan "$GOOD_CACHE" fail "${TEST_ROOT}/valid-rules.yml" \
	"$GOOD_CALLS" false "$GOOD_UV" || fail "cached ruleset did not scan offline"
[[ "$(wc -l < "$GOOD_CALLS")" -eq 1 ]] || fail "cached ruleset triggered another download"
[[ "$(cat "${GOOD_UV}.cwd")" == "${GOOD_CACHE}/fullsend/semgrep/${EXPECTED_SHA}" ]] || \
	fail "Semgrep did not run from the stable SHA-keyed ruleset directory"
mapfile -t UV_ARGS < "${GOOD_UV}.args"
found_sarif_path=false
for ((i = 0; i + 1 < ${#UV_ARGS[@]}; i++)); do
	if [[ "${UV_ARGS[i]}" == --sarif-output && "${UV_ARGS[i + 1]}" == "${FIXTURE_ROOT}/report.sarif" ]]; then
		found_sarif_path=true
	fi
done
[[ "$found_sarif_path" == true ]] || fail "relative SARIF output path was not rooted at the repository"
[[ "${UV_ARGS[-1]}" == "${FIXTURE_ROOT}/src/main.go" ]] || \
	fail "relative target path was not rooted at the repository"
pass "verified cache reuse and relative output/target path resolution"

LOCAL_OFFLINE_CACHE="${TEST_ROOT}/cache-local-offline"
LOCAL_OFFLINE_CALLS="${TEST_ROOT}/local-offline-curl-calls"
if output="$(run_scan "$LOCAL_OFFLINE_CACHE" fail "${TEST_ROOT}/valid-rules.yml" \
	"$LOCAL_OFFLINE_CALLS" false "${TEST_ROOT}/local-offline-uv" 2>&1)"; then
	[[ "$output" == *"warning:"* && "$output" == *"skipping the local scan"* ]] || \
		fail "local network failure did not warn and skip"
else
	fail "local network failure returned nonzero"
fi
[[ ! -e "${TEST_ROOT}/local-offline-uv.cwd" ]] || fail "local offline scan invoked Semgrep"
pass "local network failure warns and skips"

CI_OFFLINE_CACHE="${TEST_ROOT}/cache-ci-offline"
if output="$(run_scan "$CI_OFFLINE_CACHE" fail "${TEST_ROOT}/valid-rules.yml" \
	"${TEST_ROOT}/ci-offline-curl-calls" true "${TEST_ROOT}/ci-offline-uv" 2>&1)"; then
	fail "CI network failure returned success"
fi
[[ "$output" == *"failing the CI scan"* ]] || fail "CI network failure did not report fail-closed behavior"
[[ ! -e "${TEST_ROOT}/ci-offline-uv.cwd" ]] || fail "CI offline scan invoked Semgrep"
pass "CI network failure fails closed"

for mode in local ci; do
	mismatch_cache="${TEST_ROOT}/cache-mismatch-${mode}"
	ci_value=false
	[[ "$mode" == ci ]] && ci_value=true
	if output="$(run_scan "$mismatch_cache" payload "${TEST_ROOT}/wrong-rules.yml" \
		"${TEST_ROOT}/mismatch-${mode}-curl-calls" "$ci_value" \
		"${TEST_ROOT}/mismatch-${mode}-uv" 2>&1)"; then
		fail "${mode} hash mismatch returned success"
	fi
	[[ "$output" == *"does not match the pin"* ]] || fail "${mode} hash mismatch was not reported"
	[[ ! -e "${TEST_ROOT}/mismatch-${mode}-uv.cwd" ]] || \
		fail "${mode} hash mismatch invoked Semgrep"
done
pass "hash mismatches fail in both local runs and CI"
