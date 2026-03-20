#!/bin/bash
# network-test.sh - Simple test to verify network egress works or is blocked.
# Exit 0 if network is reachable, exit 1 if blocked.

set -euo pipefail

TARGET_URL="${1:-https://httpbin.org/get}"
TIMEOUT=10

echo "=== Network Egress Test ==="
echo "Target: ${TARGET_URL}"
echo "Timeout: ${TIMEOUT}s"
echo ""

if curl -sf --max-time "${TIMEOUT}" "${TARGET_URL}" > /dev/null 2>&1; then
    echo "RESULT: PASS - Network egress is available"
    exit 0
else
    echo "RESULT: FAIL - Network egress is blocked or unreachable"
    exit 1
fi
