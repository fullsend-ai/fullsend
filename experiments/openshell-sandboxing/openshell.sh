#!/bin/bash
# openshell.sh - A sandboxed shell wrapper using bubblewrap (bwrap).
#
# This script wraps command execution inside a bubblewrap sandbox with
# configurable network access. It demonstrates how agent tool execution
# can be restricted at the OS level.
#
# Usage:
#   ./openshell.sh --network=allow -- <command> [args...]
#   ./openshell.sh --network=deny  -- <command> [args...]
#
# Network modes:
#   allow - Share the host network namespace (egress permitted)
#   deny  - Create a new, empty network namespace (egress blocked)

set -euo pipefail

NETWORK_MODE="deny"  # Default to deny (secure by default)

# Parse arguments
while [[ $# -gt 0 ]]; do
    case "$1" in
        --network=allow)
            NETWORK_MODE="allow"
            shift
            ;;
        --network=deny)
            NETWORK_MODE="deny"
            shift
            ;;
        --)
            shift
            break
            ;;
        --help|-h)
            echo "Usage: $0 [--network=allow|deny] -- <command> [args...]"
            echo ""
            echo "Options:"
            echo "  --network=allow   Share host network (egress permitted)"
            echo "  --network=deny    Isolate network namespace (egress blocked)"
            echo ""
            echo "Examples:"
            echo "  $0 --network=allow -- curl https://httpbin.org/get"
            echo "  $0 --network=deny  -- curl https://httpbin.org/get"
            echo "  $0 --network=allow -- opencode run 'fetch https://example.com'"
            exit 0
            ;;
        *)
            echo "Unknown option: $1" >&2
            echo "Use --help for usage information" >&2
            exit 1
            ;;
    esac
done

if [[ $# -eq 0 ]]; then
    echo "Error: No command specified. Use -- before command." >&2
    echo "Usage: $0 [--network=allow|deny] -- <command> [args...]" >&2
    exit 1
fi

echo "=== OpenShell Sandbox ==="
echo "Network mode: ${NETWORK_MODE}"
echo "Command: $*"
echo "========================="
echo ""

# Build bwrap arguments
BWRAP_ARGS=(
    # Bind the root filesystem read-only
    --ro-bind / /
    # Bind /proc for process info; use dev-bind for /dev to avoid
    # permission issues with devpts in unprivileged containers
    --proc /proc
    --dev-bind /dev /dev
    # Bind common writable locations
    --bind /tmp /tmp
    --bind /var/tmp /var/tmp
    # Bind the workspace read-write so the agent can modify files
    --bind /workspaces /workspaces
    # Bind home directory for config access
    --bind /home /home
    # Keep the current working directory
    --chdir "$(pwd)"
    # Die with parent
    --die-with-parent
)

# Network configuration
if [[ "${NETWORK_MODE}" == "deny" ]]; then
    BWRAP_ARGS+=(--unshare-net)
    echo "[sandbox] Network namespace: isolated (no egress)"
elif [[ "${NETWORK_MODE}" == "allow" ]]; then
    BWRAP_ARGS+=(--share-net)
    echo "[sandbox] Network namespace: shared (egress permitted)"
fi

echo "[sandbox] Starting sandboxed execution..."
echo ""

# Execute the command inside the sandbox
exec bwrap "${BWRAP_ARGS[@]}" -- "$@"
