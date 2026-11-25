#!/usr/bin/env bash
# Called by Renovate postUpgradeTasks after a kubernetes/release bump in
# scripts/gather-release-notes.sh. Downloads the linux release-notes
# asset for the bumped version, checks it against the sha256 digest
# GitHub recorded for the asset, and only then updates
# RELEASE_NOTES_SHA256 in the script.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib-checksums.sh"

FILE="scripts/gather-release-notes.sh"
ASSET="release-notes-amd64-linux"

url=$(grep -oE "https://github\.com/kubernetes/release/releases/download/v[0-9.]+/${ASSET}" "${FILE}") \
  || die "no ${ASSET} download URL in ${FILE}"
VERSION="${url##*/v}"
VERSION="${VERSION%%/*}"

artifact=$(fetch_cached "${url}") || die "failed to download ${url}"
computed=$(sha256sum "${artifact}" | cut -d' ' -f1)
[[ "${computed}" =~ ${SHA_RE} ]] || die "computed checksum is not a sha256 hex digest: '${computed}'"

published=$(github_asset_digest kubernetes/release "v${VERSION}" "${ASSET}") \
  || die "could not get the published digest for ${url}"
[[ "${published}" =~ ${SHA_RE} ]] || die "published digest for ${url} is not a sha256 hex digest: '${published}'"
[[ "${published}" == "${computed}" ]] || die "checksum mismatch for ${url}: published ${published}, downloaded ${computed}"

count=$(grep -c '^  local RELEASE_NOTES_SHA256=' "${FILE}" || true)
if [[ "${count}" != "1" ]]; then
  die "expected exactly one 'local RELEASE_NOTES_SHA256=' line in ${FILE}, found ${count}"
fi
sed -i "s/^  local RELEASE_NOTES_SHA256=.*/  local RELEASE_NOTES_SHA256=${computed}/" "${FILE}"
grep -q "local RELEASE_NOTES_SHA256=${computed}" "${FILE}" || die "failed to update RELEASE_NOTES_SHA256 in ${FILE}"

echo "updated RELEASE_NOTES_SHA256 to ${computed} (kubernetes/release v${VERSION})"
