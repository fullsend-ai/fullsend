#!/usr/bin/env bash
# Thin wrapper — logic lives in gather.py (linted + unit-tested).
# Usage: bash skills/user-forum-whats-new/scripts/gather.sh --since YYYY-MM-DD [--until YYYY-MM-DD]
#
# Requires Python 3.11+ (datetime.UTC) and IANA tz data (system tzdb or
# the tzdata package) for America/New_York window math.
#
# Dates are forum Tuesdays in America/New_York. --since is 08:00 ET that
# morning; --until is end of that day ET, or now (UTC) when until is today.
# Default --until is today's date in America/New_York.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec python3 "${SCRIPT_DIR}/gather.py" "$@"
