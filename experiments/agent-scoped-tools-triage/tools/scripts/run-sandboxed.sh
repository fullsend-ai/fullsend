#!/bin/bash
# Runs a subagent in an OpenShell sandbox.
#
# Usage: run-sandboxed.sh <agent-name> <prompt>
#
# Two modes of operation:
# 1. If SANDBOX_EXECUTOR_URL is set: delegates to the host-side executor
#    HTTP service, which creates and manages the sandbox. Used when running
#    inside a sandbox (the triage agent can't create sandboxes directly).
# 2. Otherwise: creates the sandbox directly using openshell CLI.
#    Used when running on the host.
#
# Expects MCP_CONFIG_PATH in env (set by launcher.py).

set -euo pipefail

AGENT_NAME="$1"
PROMPT="$2"

# --- Mode 1: Delegate to executor (when running inside a sandbox) ---

if [[ -n "${SANDBOX_EXECUTOR_URL:-}" ]]; then
    echo "[run-sandboxed] Delegating '${AGENT_NAME}' to executor at ${SANDBOX_EXECUTOR_URL}" >&2

    RESPONSE=$(curl -s --max-time 300 -X POST \
        "${SANDBOX_EXECUTOR_URL}/run/${AGENT_NAME}" \
        --data-raw "$PROMPT")

    # Parse JSON response
    EXIT_CODE=$(echo "$RESPONSE" | python3 -c "import sys,json; print(json.load(sys.stdin).get('exit_code', 1))" 2>/dev/null || echo 1)
    OUTPUT=$(echo "$RESPONSE" | python3 -c "import sys,json; print(json.load(sys.stdin).get('output', ''))" 2>/dev/null || echo "$RESPONSE")

    echo "$OUTPUT"
    exit "$EXIT_CODE"
fi

# --- Mode 2: Direct sandbox creation (when running on the host) ---

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BASE_DIR="$(dirname "$(dirname "$SCRIPT_DIR")")"
AGENT_FILE="${BASE_DIR}/agents/${AGENT_NAME}.md"

if [[ ! -f "$AGENT_FILE" ]]; then
    echo "Error: agent definition not found: $AGENT_FILE" >&2
    exit 1
fi

# Read the sandbox field from agent frontmatter
POLICY=$(sed -n '/^---$/,/^---$/{ /^sandbox:/{ s/^sandbox: *//; p; q; } }' "$AGENT_FILE")

if [[ -z "$POLICY" ]]; then
    echo "Error: no sandbox policy defined for agent '${AGENT_NAME}'" >&2
    exit 1
fi

POLICY_TEMPLATE="${BASE_DIR}/${POLICY}"

if [[ ! -f "$POLICY_TEMPLATE" ]]; then
    echo "Error: policy template not found: ${POLICY_TEMPLATE}" >&2
    exit 1
fi

# Substitute runtime values into policy template.
OWNER="${REPO%%/*}"
REPO_NAME="${REPO##*/}"
POLICY_PATH="/tmp/policy-${AGENT_NAME}-$$.yaml"
sed -e "s/{{OWNER}}/${OWNER}/g" \
    -e "s/{{REPO_NAME}}/${REPO_NAME}/g" \
    -e "s/{{ISSUE_NUMBER}}/${ISSUE_NUMBER}/g" \
    "$POLICY_TEMPLATE" > "$POLICY_PATH"

if ! command -v openshell &> /dev/null; then
    echo "Error: OpenShell is not installed" >&2
    exit 1
fi

if ! openshell status &>/dev/null; then
    echo "Error: OpenShell gateway is not reachable" >&2
    exit 1
fi

SANDBOX_NAME="triage-${AGENT_NAME}-$$"
SSH_CONFIG="/tmp/openshell-ssh-${SANDBOX_NAME}.config"
WORKSPACE="/tmp/workspace"

cleanup() {
    openshell sandbox delete "$SANDBOX_NAME" &>/dev/null || true
    rm -f "$SSH_CONFIG" "$POLICY_PATH"
}
trap cleanup EXIT

echo "[run-sandboxed] Running '${AGENT_NAME}' in sandbox: ${POLICY}" >&2

# 1. Create a persistent sandbox
if ! timeout 30 openshell sandbox create \
    --name "$SANDBOX_NAME" \
    --keep \
    --no-auto-providers \
    --no-tty </dev/null 2>/tmp/openshell-err-$$.log; then

    if ! openshell sandbox get "$SANDBOX_NAME" &>/dev/null; then
        echo "[run-sandboxed] OpenShell sandbox create failed:" >&2
        cat /tmp/openshell-err-$$.log >&2 2>/dev/null
        rm -f /tmp/openshell-err-$$.log
        exit 1
    fi
fi
rm -f /tmp/openshell-err-$$.log

# 2. Apply the custom policy
POLICY_APPLIED=false
for attempt in 1 2 3; do
    if openshell policy set "$SANDBOX_NAME" --policy "$POLICY_PATH" --wait 2>&1; then
        POLICY_APPLIED=true
        break
    fi
    echo "[run-sandboxed] Policy set attempt $attempt failed, retrying in 3s..." >&2
    sleep 3
done

if [[ "$POLICY_APPLIED" != "true" ]]; then
    echo "Error: policy set failed after 3 attempts" >&2
    exit 1
fi

# 3. Get SSH config
openshell sandbox ssh-config "$SANDBOX_NAME" > "$SSH_CONFIG"

# 4. Copy MCP config into the sandbox
SANDBOX_MCP_CONFIG="/tmp/mcp_config.json"
scp -F "$SSH_CONFIG" "$MCP_CONFIG_PATH" "openshell-${SANDBOX_NAME}:${SANDBOX_MCP_CONFIG}"

# 5. Copy claude binary into the sandbox
CLAUDE_BIN=$(command -v claude 2>/dev/null || true)
if [[ -z "$CLAUDE_BIN" ]]; then
    echo "Error: claude CLI not found in PATH" >&2
    exit 1
fi
ssh -F "$SSH_CONFIG" "openshell-${SANDBOX_NAME}" "mkdir -p ${WORKSPACE}/bin"
scp -F "$SSH_CONFIG" "$CLAUDE_BIN" "openshell-${SANDBOX_NAME}:${WORKSPACE}/bin/claude"
ssh -F "$SSH_CONFIG" "openshell-${SANDBOX_NAME}" "chmod +x ${WORKSPACE}/bin/claude"

# 6. Set up agent workspace with .claude/ directory structure
ssh -F "$SSH_CONFIG" "openshell-${SANDBOX_NAME}" \
    "mkdir -p ${WORKSPACE}/.claude/agents ${WORKSPACE}/.claude/skills"

if [[ -d "${BASE_DIR}/.claude/agents" ]]; then
    scp -F "$SSH_CONFIG" -r "${BASE_DIR}/.claude/agents" \
        "openshell-${SANDBOX_NAME}:${WORKSPACE}/.claude/"
fi

if [[ -d "${BASE_DIR}/.claude/skills" ]]; then
    for skill_dir in "${BASE_DIR}/.claude/skills"/*/; do
        if [[ -d "$skill_dir" ]]; then
            scp -F "$SSH_CONFIG" -r "$skill_dir" \
                "openshell-${SANDBOX_NAME}:${WORKSPACE}/.claude/skills/"
        fi
    done
fi

echo "[run-sandboxed] '${AGENT_NAME}' sandbox ready, running agent via SSH" >&2

# 7. Run the claude agent inside the sandbox via SSH
ssh -F "$SSH_CONFIG" "openshell-${SANDBOX_NAME}" \
    "cd ${WORKSPACE} && export PATH=${WORKSPACE}/bin:\$PATH && claude --print --agent '${AGENT_NAME}' --mcp-config '${SANDBOX_MCP_CONFIG}' --strict-mcp-config --dangerously-skip-permissions '${PROMPT}'"
