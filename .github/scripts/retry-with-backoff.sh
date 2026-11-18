#!/usr/bin/env bash
# Shared retry helpers for runtime dependency setup (install-podman.sh,
# install-openshell.sh).
#
# GitHub release CDN 5xx bursts can outlast a few seconds of retries
# (#9203, #7287), so these helpers use exponential backoff with a total
# recovery window of a few minutes. They only retry: they never skip or
# relax the checksum, version, or provenance checks the callers run on
# what was downloaded. Callers must verify integrity after the download
# and fail closed on a mismatch without retrying.
#
# Source this file; do not execute it.
#
# Tunables (environment, mainly for tests):
#   RETRY_MAX_ATTEMPTS   total attempts, default 6
#   RETRY_INITIAL_DELAY  first delay in seconds, default 10 (doubles each retry)
#   RETRY_MAX_DELAY      cap on a single delay in seconds, default 120
#
# With the defaults the delays are 10, 20, 40, 80, 120 seconds: about
# 4.5 minutes of recovery time before setup fails.

# Exit status an attempt returns to stop retrying (permanent failure).
RETRY_PERMANENT_STATUS=100

# retry_with_backoff <label> <command> [args...]
#
# Runs the command until it succeeds, it returns RETRY_PERMANENT_STATUS,
# or RETRY_MAX_ATTEMPTS is reached. <label> names the dependency, version
# and stage (e.g. "Podman v5.8.4 (amd64) download") and is used in every
# message. The command may set RETRY_FAILURE_CATEGORY to describe why an
# attempt failed; otherwise the category is its exit status.
retry_with_backoff() {
  local label="$1"
  shift
  local max_attempts="${RETRY_MAX_ATTEMPTS:-6}"
  local delay="${RETRY_INITIAL_DELAY:-10}"
  local max_delay="${RETRY_MAX_DELAY:-120}"
  local attempt=1 status category

  while true; do
    RETRY_FAILURE_CATEGORY=""
    status=0
    "$@" || status=$?
    if (( status == 0 )); then
      return 0
    fi
    category="${RETRY_FAILURE_CATEGORY:-exit-status-${status}}"
    if (( status == RETRY_PERMANENT_STATUS )); then
      echo "::error::${label} failed (category: ${category}); permanent failure, not retried" >&2
      return 1
    fi
    if (( attempt >= max_attempts )); then
      echo "::error::${label} failed after ${max_attempts} attempts (category: ${category})" >&2
      return 1
    fi
    echo "::warning::${label} attempt ${attempt}/${max_attempts} failed (category: ${category}), retrying in ${delay}s..." >&2
    sleep "${delay}"
    attempt=$((attempt + 1))
    delay=$((delay * 2))
    if (( delay > max_delay )); then
      delay="${max_delay}"
    fi
  done
}

# _download_attempt <url> <output-path>
#
# One download attempt. Classifies failures for retry_with_backoff:
# connection errors, timeouts, HTTP 408/429 and 5xx are transient; any
# other non-2xx response (e.g. 404) is permanent.
_download_attempt() {
  local url="$1" out="$2" http_code curl_status=0
  http_code="$(curl -sSL --connect-timeout 30 --max-time 600 \
    -o "${out}" -w '%{http_code}' "${url}")" || curl_status=$?
  if (( curl_status != 0 )); then
    RETRY_FAILURE_CATEGORY="network (curl exit ${curl_status}, HTTP ${http_code:-000})"
    return 1
  fi
  case "${http_code}" in
    2??)
      return 0
      ;;
    408 | 429 | 5??)
      RETRY_FAILURE_CATEGORY="transient HTTP ${http_code}"
      return 1
      ;;
    *)
      RETRY_FAILURE_CATEGORY="HTTP ${http_code}"
      return "${RETRY_PERMANENT_STATUS}"
      ;;
  esac
}

# download_with_retry <label> <url> <output-path>
#
# Downloads <url> to <output-path>, retrying transient failures with
# backoff. The caller must verify the file's checksum afterwards.
download_with_retry() {
  retry_with_backoff "$1" _download_attempt "$2" "$3"
}
