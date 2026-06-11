#!/usr/bin/env bash
# extract-transcript-error.sh — Extract errors from agent transcript JSONL files.
#
# Reads transcript JSONL files (Claude Code stream-json format) and extracts
# the final result event. If the result indicates an error, prints a summary
# suitable for GitHub Actions annotations or human consumption.
#
# Usage:
#   extract-transcript-error.sh <transcript-file-or-directory>
#
# When given a directory, processes all .jsonl files in it.
# When given a file, processes just that file.
#
# Exit codes:
#   0 — no errors found (or no transcript files)
#   1 — at least one transcript contains an error result
#   2 — usage error (bad arguments)
#
# This script can be used by:
#   - Post-scripts to surface errors in workflow logs
#   - The triage agent to extract errors from downloaded artifacts
#   - Operators debugging failed agent runs
#
# Example with artifact download:
#   gh run download <run-id> -n agent-transcripts -D /tmp/transcripts
#   extract-transcript-error.sh /tmp/transcripts/

set -euo pipefail

if [[ $# -lt 1 ]]; then
  echo "Usage: $0 <transcript-file-or-directory>" >&2
  exit 2
fi

TARGET="$1"
FOUND_ERROR=0
MAX_ERROR_LENGTH=2000

# extract_error processes a single JSONL file and prints any error found.
extract_error() {
  local file="$1"
  local basename
  basename="$(basename "$file")"

  # Find the last result line in the file.
  # Claude Code transcripts end with a result event.
  local last_result
  last_result="$(grep -E '"type"\s*:\s*"result"' "$file" | tail -1)" || true

  if [[ -z "$last_result" ]]; then
    return
  fi

  # Check if the result indicates an error.
  local is_error
  is_error="$(echo "$last_result" | jq -r '.is_error // false' 2>/dev/null)" || true

  if [[ "$is_error" != "true" ]]; then
    return
  fi

  FOUND_ERROR=1

  local error_msg
  error_msg="$(echo "$last_result" | jq -r '.result // "unknown error"' 2>/dev/null)" || error_msg="unknown error"

  local subtype
  subtype="$(echo "$last_result" | jq -r '.subtype // "unknown"' 2>/dev/null)" || subtype="unknown"

  # Truncate long error messages.
  if [[ ${#error_msg} -gt $MAX_ERROR_LENGTH ]]; then
    error_msg="${error_msg:0:$MAX_ERROR_LENGTH}... (truncated)"
  fi

  echo "--- Error in ${basename} ---"
  echo "Subtype: ${subtype}"
  echo "Message: ${error_msg}"
  echo ""

  # Emit GHA annotation if running in GitHub Actions.
  if [[ "${GITHUB_ACTIONS:-}" == "true" ]]; then
    # Sanitize for GHA: replace :: and URL-encoded newlines to prevent command injection.
    local safe_msg="${error_msg//::/ :}"
    safe_msg="${safe_msg//%0A/ }"
    safe_msg="${safe_msg//%0a/ }"
    safe_msg="${safe_msg//%0D/ }"
    safe_msg="${safe_msg//%0d/ }"
    echo "::error title=Agent Error (${basename})::${safe_msg}"
  fi
}

if [[ -d "$TARGET" ]]; then
  # Process all JSONL files in the directory.
  for f in "$TARGET"/*.jsonl; do
    [[ -f "$f" ]] || continue
    extract_error "$f"
  done
elif [[ -f "$TARGET" ]]; then
  extract_error "$TARGET"
else
  echo "Error: $TARGET is not a file or directory" >&2
  exit 2
fi

if [[ $FOUND_ERROR -eq 1 ]]; then
  exit 1
fi

exit 0
