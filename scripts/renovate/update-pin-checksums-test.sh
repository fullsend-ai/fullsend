#!/usr/bin/env bash
# update-pin-checksums-test.sh — Tests for lib-checksums.sh and the
# update-*-checksum(s).sh wrappers built on it.
#
# Run from the repo root:
#   bash scripts/renovate/update-pin-checksums-test.sh
#
# Each case copies the real Containerfiles into a scratch directory,
# rewrites the pin's VERSION ARG as a Renovate bump would, and runs the
# wrapper there with a mock curl that serves fixture artifacts and
# published checksums from a URL map. Any URL not in the map fails, so a
# wrapper that downloads from the wrong place fails its case.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
FAILURES=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

MOCK_BIN="${TMPDIR}/bin"
URLMAP="${TMPDIR}/urlmap"
FIXTURES="${TMPDIR}/fixtures"
mkdir -p "${MOCK_BIN}" "${FIXTURES}"

# Mock curl: copies the fixture mapped to the URL to the -o target, or fails
# like curl -f on a 404 when the URL is unmapped.
cat > "${MOCK_BIN}/curl" <<MOCKEOF
#!/usr/bin/env bash
out=""
url=""
while [[ \$# -gt 0 ]]; do
  case "\$1" in
    -o) out="\$2"; shift 2 ;;
    -H|--retry|--retry-delay) shift 2 ;;
    -*) shift ;;
    *) url="\$1"; shift ;;
  esac
done
src=\$(awk -F '\t' -v u="\${url}" '\$1 == u { print \$2; exit }' '${URLMAP}')
if [[ -z "\${src}" ]]; then
  echo "curl: (22) The requested URL returned error: 404 (\${url})" >&2
  exit 22
fi
cp "\${src}" "\${out}"
MOCKEOF
chmod +x "${MOCK_BIN}/curl"

pass() { echo "PASS: $1"; }
fail() { echo "FAIL: $1"; FAILURES=$((FAILURES + 1)); }

reset_urls() { : > "${URLMAP}"; }

# serve <url> <file> — map <url> to <file> for the mock curl.
serve() { printf '%s\t%s\n' "$1" "$2" >> "${URLMAP}"; }

# unserve <url> — drop <url> from the map (simulates a missing asset).
unserve() {
  awk -F '\t' -v u="$1" '$1 != u' "${URLMAP}" > "${URLMAP}.new"
  mv "${URLMAP}.new" "${URLMAP}"
}

# make_artifact <label> — write a fixture artifact, print its path.
make_artifact() {
  local path
  path="${FIXTURES}/$(printf '%s' "$1" | tr -c 'A-Za-z0-9._-' '_')"
  printf 'fixture artifact %s\n' "$1" > "${path}"
  echo "${path}"
}

sha_of() { sha256sum "$1" | cut -d' ' -f1; }

# setup_workdir <PIN> <version> <file>... — scratch tree with the real
# Containerfiles and <PIN>_VERSION set to <version> in each listed file.
setup_workdir() {
  local pin="$1" version="$2"
  shift 2
  local wd="${TMPDIR}/work"
  rm -rf "${wd}"
  mkdir -p "${wd}/images/code" "${wd}/images/runner" "${wd}/images/sandbox"
  local img
  for img in code runner sandbox; do
    cp "${REPO_ROOT}/images/${img}/Containerfile" "${wd}/images/${img}/Containerfile"
  done
  local f
  for f in "$@"; do
    sed -i "s/^ARG ${pin}_VERSION=.*/ARG ${pin}_VERSION=${version}/" "${wd}/${f}"
  done
  echo "${wd}"
}

snapshot() {
  cat "$1/images/code/Containerfile" "$1/images/runner/Containerfile" "$1/images/sandbox/Containerfile"
}

# run_script <workdir> <script> — prints combined output, returns exit code.
run_script() {
  (cd "$1" && env -u GITHUB_TOKEN PATH="${MOCK_BIN}:${PATH}" bash "${SCRIPT_DIR}/$2" 2>&1)
}

# expect_failure <name> <workdir> <script> <expected substring>
expect_failure() {
  local name="$1" wd="$2" script="$3" want="$4"
  local before after output rc=0
  before=$(snapshot "${wd}")
  output=$(run_script "${wd}" "${script}") || rc=$?
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

# --- Published-checksum fixture builders -----------------------------------
# Each takes the artifact URLs and their sha256s (parallel arrays URLS/SHAS)
# plus an optional override sha that replaces the first published value
# (to provoke a mismatch).

# publish_checksums_file <checksums-url> <override> — goreleaser-style
# checksums.txt with decoy entries for other assets.
publish_checksums_file() {
  local url="$1" override="$2" path="${FIXTURES}/checksums-$RANDOM.txt" i sha
  {
    echo "3333333333333333333333333333333333333333333333333333333333333333  decoy_darwin_arm64.tar.gz"
    for i in "${!URLS[@]}"; do
      sha="${SHAS[$i]}"
      [[ "${i}" -eq 0 && -n "${override}" ]] && sha="${override}"
      echo "${sha}  ${URLS[$i]##*/}"
    done
    echo "4444444444444444444444444444444444444444444444444444444444444444  ${URLS[0]##*/}.sbom.json"
  } > "${path}"
  serve "${url}" "${path}"
}

# publish_sha256_assets <override> — one <asset>.sha256 file per artifact.
publish_sha256_assets() {
  local override="$1" i sha path
  for i in "${!URLS[@]}"; do
    sha="${SHAS[$i]}"
    [[ "${i}" -eq 0 && -n "${override}" ]] && sha="${override}"
    path="${FIXTURES}/sha256-$RANDOM-${i}"
    echo "${sha}  ${URLS[$i]##*/}" > "${path}"
    serve "${URLS[$i]}.sha256" "${path}"
  done
}

# publish_github_digests <repo> <tag> <override> — GitHub release JSON with
# per-asset digests (plus a decoy asset).
publish_github_digests() {
  local repo="$1" tag="$2" override="$3" i sha assets="" path="${FIXTURES}/release-$RANDOM.json"
  for i in "${!URLS[@]}"; do
    sha="${SHAS[$i]}"
    [[ "${i}" -eq 0 && -n "${override}" ]] && sha="${override}"
    assets+="{\"name\": \"${URLS[$i]##*/}\", \"digest\": \"sha256:${sha}\"},"
  done
  assets+='{"name": "decoy-darwin.tar.gz", "digest": "sha256:3333333333333333333333333333333333333333333333333333333333333333"}'
  echo "{\"tag_name\": \"${tag}\", \"assets\": [${assets}]}" > "${path}"
  serve "https://api.github.com/repos/${repo}/releases/tags/${tag}" "${path}"
}

# publish_release_notes <repo> <tag> <override> — release JSON whose body
# carries the "For pinned/vendored consumers" table the pi extensions use.
publish_release_notes() {
  local repo="$1" tag="$2" override="$3" sha="${SHAS[0]}" path="${FIXTURES}/notes-$RANDOM.json"
  [[ -n "${override}" ]] && sha="${override}"
  jq -n --arg tag "${tag}" --arg body "$(printf '%s\r\n' \
    '**For pinned/vendored consumers**' '' '| | |' '|---|---|' \
    "| Tarball | \`${URLS[0]}\` |" "| SHA256 | \`${sha}\` |" '' \
    '## What'"'"'s Changed' '* fix: something')" \
    '{tag_name: $tag, body: $body, assets: []}' > "${path}"
  serve "https://api.github.com/repos/${repo}/releases/tags/${tag}" "${path}"
}

WRONG_SHA="5555555555555555555555555555555555555555555555555555555555555555"

# --- Case table -------------------------------------------------------------
# script | PIN | mode | files (space-separated) | version | amd64/single URL |
# arm64 URL | published source
#
# Published source:
#   checksums:<url>         sha256sum-style file covering both arches
#   sha256                  <artifact-url>.sha256 per arch
#   digest:<repo>:<tag>     GitHub release asset digests
#   notes:<repo>:<tag>      SHA256 row in GitHub release notes
#   none                    upstream publishes nothing; computed hash used
CASES=(
  "update-pi-anthropic-vertex-checksum.sh|PI_ANTHROPIC_VERTEX|single|images/sandbox/Containerfile|9.8.7|https://github.com/fullsend-ai/pi-anthropic-vertex/archive/refs/tags/v9.8.7.tar.gz||notes:fullsend-ai/pi-anthropic-vertex:v9.8.7"
  "update-pi-xai-vertex-checksum.sh|PI_XAI_VERTEX|single|images/sandbox/Containerfile|9.8.7|https://github.com/fullsend-ai/pi-xai-vertex/archive/refs/tags/v9.8.7.tar.gz||notes:fullsend-ai/pi-xai-vertex:v9.8.7"
  "update-pi-inference-gateway-checksum.sh|PI_INFERENCE_GATEWAY|single|images/sandbox/Containerfile|9.8.7|https://github.com/fullsend-ai/pi-inference-gateway/archive/refs/tags/v9.8.7.tar.gz||notes:fullsend-ai/pi-inference-gateway:v9.8.7"
  "update-ripgrep-checksums.sh|RIPGREP|multi|images/sandbox/Containerfile|99.1.0|https://github.com/BurntSushi/ripgrep/releases/download/99.1.0/ripgrep-99.1.0-x86_64-unknown-linux-musl.tar.gz|https://github.com/BurntSushi/ripgrep/releases/download/99.1.0/ripgrep-99.1.0-aarch64-unknown-linux-musl.tar.gz|sha256"
  "update-fd-checksums.sh|FD|multi|images/sandbox/Containerfile|99.1.0|https://github.com/sharkdp/fd/releases/download/v99.1.0/fd-v99.1.0-x86_64-unknown-linux-musl.tar.gz|https://github.com/sharkdp/fd/releases/download/v99.1.0/fd-v99.1.0-aarch64-unknown-linux-musl.tar.gz|digest:sharkdp/fd:v99.1.0"
  "update-gcloud-checksums.sh|GCLOUD|multi|images/runner/Containerfile|999.0.0|https://dl.google.com/dl/cloudsdk/channels/rapid/downloads/google-cloud-cli-999.0.0-linux-x86_64.tar.gz|https://dl.google.com/dl/cloudsdk/channels/rapid/downloads/google-cloud-cli-999.0.0-linux-arm.tar.gz|none"
  "update-gh-checksums.sh|GH|multi|images/runner/Containerfile|9.1.0|https://github.com/cli/cli/releases/download/v9.1.0/gh_9.1.0_linux_amd64.tar.gz|https://github.com/cli/cli/releases/download/v9.1.0/gh_9.1.0_linux_arm64.tar.gz|checksums:https://github.com/cli/cli/releases/download/v9.1.0/gh_9.1.0_checksums.txt"
  "update-gitleaks-checksums.sh|GITLEAKS|multi|images/sandbox/Containerfile images/runner/Containerfile|9.1.0|https://github.com/gitleaks/gitleaks/releases/download/v9.1.0/gitleaks_9.1.0_linux_x64.tar.gz|https://github.com/gitleaks/gitleaks/releases/download/v9.1.0/gitleaks_9.1.0_linux_arm64.tar.gz|checksums:https://github.com/gitleaks/gitleaks/releases/download/v9.1.0/gitleaks_9.1.0_checksums.txt"
  "update-gitsign-checksums.sh|GITSIGN|multi|images/code/Containerfile|9.1.0|https://github.com/sigstore/gitsign/releases/download/v9.1.0/gitsign_9.1.0_linux_amd64|https://github.com/sigstore/gitsign/releases/download/v9.1.0/gitsign_9.1.0_linux_arm64|checksums:https://github.com/sigstore/gitsign/releases/download/v9.1.0/checksums.txt"
  "update-lychee-checksums.sh|LYCHEE|multi|images/code/Containerfile|9.1.0|https://github.com/lycheeverse/lychee/releases/download/lychee-v9.1.0/lychee-x86_64-unknown-linux-gnu.tar.gz|https://github.com/lycheeverse/lychee/releases/download/lychee-v9.1.0/lychee-aarch64-unknown-linux-gnu.tar.gz|sha256"
  "update-opencode-checksums.sh|OPENCODE|multi|images/sandbox/Containerfile|9.1.0|https://github.com/anomalyco/opencode/releases/download/v9.1.0/opencode-linux-x64.tar.gz|https://github.com/anomalyco/opencode/releases/download/v9.1.0/opencode-linux-arm64.tar.gz|digest:anomalyco/opencode:v9.1.0"
  "update-ort-checksums.sh|ORT|multi|images/runner/Containerfile|9.1.0|https://github.com/microsoft/onnxruntime/releases/download/v9.1.0/onnxruntime-linux-x64-9.1.0.tgz|https://github.com/microsoft/onnxruntime/releases/download/v9.1.0/onnxruntime-linux-aarch64-9.1.0.tgz|digest:microsoft/onnxruntime:v9.1.0"
)

# publish <source> <override> — serve the published checksums for URLS/SHAS.
publish() {
  local src="$1" override="$2" repo tag
  case "${src}" in
    checksums:*) publish_checksums_file "${src#checksums:}" "${override}" ;;
    sha256) publish_sha256_assets "${override}" ;;
    digest:*)
      IFS=: read -r _ repo tag <<< "${src}"
      publish_github_digests "${repo}" "${tag}" "${override}"
      ;;
    notes:*)
      IFS=: read -r _ repo tag <<< "${src}"
      publish_release_notes "${repo}" "${tag}" "${override}"
      ;;
    none) ;;
    *) echo "unknown published source: ${src}" >&2; exit 1 ;;
  esac
}

for case_line in "${CASES[@]}"; do
  IFS='|' read -r script pin mode files_str version url_a url_b source <<< "${case_line}"
  read -r -a files <<< "${files_str}"

  if [[ "${mode}" == "single" ]]; then
    URLS=("${url_a}")
    NAMES=("${pin}_SHA256")
  else
    URLS=("${url_a}" "${url_b}")
    NAMES=("${pin}_SHA256_AMD64" "${pin}_SHA256_ARM64")
  fi
  SHAS=()
  reset_urls
  for i in "${!URLS[@]}"; do
    artifact=$(make_artifact "${URLS[$i]}")
    SHAS+=("$(sha_of "${artifact}")")
    serve "${URLS[$i]}" "${artifact}"
  done
  publish "${source}" ""

  # --- Happy path: every listed file gets the new checksums, nothing else ---
  WD=$(setup_workdir "${pin}" "${version}" "${files[@]}")
  before=$(snapshot "${WD}")
  rc=0
  output=$(run_script "${WD}" "${script}") || rc=$?
  if [[ "${rc}" -ne 0 ]]; then
    fail "${script}: updates checksums: exit ${rc} (output: ${output})"
  else
    ok=1
    for f in "${files[@]}"; do
      for i in "${!NAMES[@]}"; do
        if ! grep -qx "ARG ${NAMES[$i]}=${SHAS[$i]}" "${WD}/${f}"; then
          fail "${script}: ${NAMES[$i]} not updated in ${f}"
          ok=0
        fi
      done
    done
    changed=$(diff <(echo "${before}") <(snapshot "${WD}") | grep -c '^>' || true)
    want=$(( ${#NAMES[@]} * ${#files[@]} ))
    if [[ "${changed}" != "${want}" ]]; then
      fail "${script}: expected ${want} changed lines, got ${changed}"
      ok=0
    fi
    [[ "${ok}" -eq 1 ]] && pass "${script}: updates checksums"
  fi

  # --- Re-running is a no-op ---
  before=$(snapshot "${WD}")
  rc=0
  output=$(run_script "${WD}" "${script}") || rc=$?
  if [[ "${rc}" -ne 0 ]]; then
    fail "${script}: second run: exit ${rc} (output: ${output})"
  elif [[ "${before}" != "$(snapshot "${WD}")" ]]; then
    fail "${script}: second run changed files"
  else
    pass "${script}: second run is a no-op"
  fi

  # --- Missing artifact (last arch): fails, nothing written ---
  WD=$(setup_workdir "${pin}" "${version}" "${files[@]}")
  unserve "${URLS[-1]}"
  expect_failure "${script}: missing artifact" "${WD}" "${script}" "failed to fetch ${URLS[-1]}"
  serve "${URLS[-1]}" "$(make_artifact "${URLS[-1]}")"

  # --- Published checksum differs from the download: fails, nothing written ---
  if [[ "${source}" != "none" ]]; then
    reset_urls
    for i in "${!URLS[@]}"; do
      serve "${URLS[$i]}" "$(make_artifact "${URLS[$i]}")"
    done
    publish "${source}" "${WRONG_SHA}"
    WD=$(setup_workdir "${pin}" "${version}" "${files[@]}")
    expect_failure "${script}: published checksum mismatch" "${WD}" "${script}" "checksum mismatch for ${URLS[0]}"
  fi

  # --- Malformed version: fails before any download ---
  WD=$(setup_workdir "${pin}" "1.2.3;rm" "${files[@]}")
  expect_failure "${script}: invalid version" "${WD}" "${script}" "${pin}_VERSION in ${files[0]} is not a valid semver"
done

# --- Pin-specific and library cases ----------------------------------------

# gitleaks is pinned in two images; a version split between them fails.
WD=$(setup_workdir GITLEAKS 9.1.0 images/sandbox/Containerfile)
sed -i "s/^ARG GITLEAKS_VERSION=.*/ARG GITLEAKS_VERSION=9.0.0/" "${WD}/images/runner/Containerfile"
expect_failure "gitleaks: version mismatch between images" "${WD}" update-gitleaks-checksums.sh "GITLEAKS_VERSION mismatch"

# A missing SHA ARG line fails before anything is written.
WD=$(setup_workdir RIPGREP 99.1.0 images/sandbox/Containerfile)
sed -i '/^ARG RIPGREP_SHA256_ARM64=/d' "${WD}/images/sandbox/Containerfile"
expect_failure "missing SHA ARG line" "${WD}" update-ripgrep-checksums.sh "expected exactly one 'ARG RIPGREP_SHA256_ARM64=' line in images/sandbox/Containerfile, found 0"

# ALLOW_UNPUBLISHED in the environment does not switch off verification:
# a release with no published checksum still fails.
reset_urls
URLS=("https://github.com/BurntSushi/ripgrep/releases/download/99.1.0/ripgrep-99.1.0-x86_64-unknown-linux-musl.tar.gz"
      "https://github.com/BurntSushi/ripgrep/releases/download/99.1.0/ripgrep-99.1.0-aarch64-unknown-linux-musl.tar.gz")
for u in "${URLS[@]}"; do serve "${u}" "$(make_artifact "${u}")"; done
WD=$(setup_workdir RIPGREP 99.1.0 images/sandbox/Containerfile)
before=$(snapshot "${WD}")
rc=0
output=$(cd "${WD}" && ALLOW_UNPUBLISHED=1 PATH="${MOCK_BIN}:${PATH}" bash "${SCRIPT_DIR}/update-ripgrep-checksums.sh" 2>&1) || rc=$?
if [[ "${rc}" -ne 0 && "${output}" == *"failed to fetch ${URLS[0]}.sha256"* && "${before}" == "$(snapshot "${WD}")" ]]; then
  pass "ALLOW_UNPUBLISHED from the environment is ignored"
else
  fail "ALLOW_UNPUBLISHED from the environment is ignored (exit ${rc}, output: ${output})"
fi

# Release notes without a SHA256 row fail.
reset_urls
URL="https://github.com/fullsend-ai/pi-xai-vertex/archive/refs/tags/v9.8.7.tar.gz"
serve "${URL}" "$(make_artifact "${URL}")"
NOTES="${FIXTURES}/notes-empty.json"
printf '%s\n' '{"tag_name": "v9.8.7", "body": "## What'"'"'s Changed\r\n* nothing pinned here", "assets": []}' > "${NOTES}"
serve "https://api.github.com/repos/fullsend-ai/pi-xai-vertex/releases/tags/v9.8.7" "${NOTES}"
WD=$(setup_workdir PI_XAI_VERTEX 9.8.7 images/sandbox/Containerfile)
expect_failure "release notes without SHA256" "${WD}" update-pi-xai-vertex-checksum.sh "no SHA256 row in the fullsend-ai/pi-xai-vertex v9.8.7 release notes"

# A tag with no GitHub release fails (API 404).
unserve "https://api.github.com/repos/fullsend-ai/pi-xai-vertex/releases/tags/v9.8.7"
WD=$(setup_workdir PI_XAI_VERTEX 9.8.7 images/sandbox/Containerfile)
expect_failure "tag without a release" "${WD}" update-pi-xai-vertex-checksum.sh "failed to fetch https://api.github.com/repos/fullsend-ai/pi-xai-vertex/releases/tags/v9.8.7"

# An asset with no recorded digest fails.
reset_urls
URLS=("https://github.com/sharkdp/fd/releases/download/v99.1.0/fd-v99.1.0-x86_64-unknown-linux-musl.tar.gz"
      "https://github.com/sharkdp/fd/releases/download/v99.1.0/fd-v99.1.0-aarch64-unknown-linux-musl.tar.gz")
for u in "${URLS[@]}"; do serve "${u}" "$(make_artifact "${u}")"; done
NODIGEST="${FIXTURES}/release-nodigest.json"
echo "{\"tag_name\": \"v99.1.0\", \"assets\": [{\"name\": \"${URLS[0]##*/}\", \"digest\": null}, {\"name\": \"${URLS[1]##*/}\", \"digest\": null}]}" > "${NODIGEST}"
serve "https://api.github.com/repos/sharkdp/fd/releases/tags/v99.1.0" "${NODIGEST}"
WD=$(setup_workdir FD 99.1.0 images/sandbox/Containerfile)
expect_failure "release asset without digest" "${WD}" update-fd-checksums.sh "no sha256 digest for asset ${URLS[0]##*/}"

# checksum_file_sha accepts "<hex> *<name>", path-prefixed names, CRLF and
# bare-digest files, and rejects files with no entry for the asset.
lib_out=$(
  cd "${TMPDIR}" && PATH="${MOCK_BIN}:${PATH}" bash -c '
    set -euo pipefail
    source "$1/lib-checksums.sh"
    fx="$2"
    A=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    B=BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB
    printf "%s *dist/tool.tar.gz\r\n%s  other.tar.gz\r\n" "$A" "$B" > "$fx/binary-mode"
    printf "%s\n" "$B" > "$fx/bare"
    printf "%s  other.tar.gz\n" "$A" > "$fx/absent"
    printf "%s\t%s\n" https://x/binary-mode "$fx/binary-mode" https://x/bare "$fx/bare" https://x/absent "$fx/absent" > "$3"
    echo "binary-mode=$(checksum_file_sha https://x/binary-mode tool.tar.gz)"
    echo "bare=$(checksum_file_sha https://x/bare tool.tar.gz)"
    if checksum_file_sha https://x/absent tool.tar.gz 2>/dev/null; then echo "absent=accepted"; else echo "absent=rejected"; fi
  ' _ "${SCRIPT_DIR}" "${FIXTURES}" "${URLMAP}" 2>&1
) || true
if [[ "${lib_out}" == *"binary-mode=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"* \
  && "${lib_out}" == *"bare=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"* \
  && "${lib_out}" == *"absent=rejected"* ]]; then
  pass "checksum_file_sha parses checksum file variants"
else
  fail "checksum_file_sha parses checksum file variants (output: ${lib_out})"
fi

# The library refuses to run as a script.
rc=0
lib_direct=$(bash "${SCRIPT_DIR}/lib-checksums.sh" 2>&1) || rc=$?
if [[ "${rc}" -ne 0 && "${lib_direct}" == *"is a library"* ]]; then
  pass "lib-checksums.sh refuses direct execution"
else
  fail "lib-checksums.sh refuses direct execution (exit ${rc}, output: ${lib_direct})"
fi

echo
if [[ "${FAILURES}" -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All tests passed"
