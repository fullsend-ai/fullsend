#!/usr/bin/env bash
set -euo pipefail

# Keep this projection byte-for-byte aligned with pullRequestDiffHash in
# internal/cli/postreview.go. The Go parity test executes this script directly.
# Tree-mode maps are passed as files so large repositories do not hit the
# operating system's command-line argument limit.
base_modes_file=${1:?base tree modes JSON file is required}
head_modes_file=${2:?head tree modes JSON file is required}

if ! hash_input=$(jq -s -c --slurpfile base_modes "${base_modes_file}" --slurpfile head_modes "${head_modes_file}" '
  if length == 0 or any(.[]; .patch == null or .patch == "") then
    empty
  else
    sort_by(.filename, .previous_filename, .status)
    | map(. + {old_mode: ($base_modes[0][(if .status == "added" then "" elif (.previous_filename // "") == "" then .filename else .previous_filename end)] // ""), new_mode: ($head_modes[0][(if .status == "removed" then "" else .filename end)] // "")})
    | if any(.[]; (.status == "added" and .new_mode == "") or (.status == "removed" and .old_mode == "") or ((.status != "added" and .status != "removed") and (.old_mode == "" or .new_mode == ""))) then empty else map({filename: .filename, previous_filename: .previous_filename, status: .status, old_mode: .old_mode, new_mode: .new_mode, patch: .patch}) end
  end'); then
  exit 0
fi

if [[ -z "${hash_input}" ]]; then
  exit 0
fi

# Match Go encoding/json with HTML escaping disabled: jq escapes DEL while Go
# emits it literally, and Go escapes U+2028/U+2029 in JSON strings.
if ! hash_input=$(printf '%s' "${hash_input}" | perl -CS -pe 's/(?<!\\)(?:\\\\)*\K\\u007f/\x{007f}/g; s/\x{2028}/\\u2028/g; s/\x{2029}/\\u2029/g'); then
  exit 0
fi

printf '%s' "${hash_input}" | sha256sum | awk '{print $1}'
