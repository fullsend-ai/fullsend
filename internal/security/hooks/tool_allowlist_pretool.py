#!/usr/bin/env python3
"""Claude Code PreToolUse hook: tool call allowlist enforcement.

Blocks tool calls outside the agent's authorized tool set. If the agent
attempts to call Bash, WebFetch, or any other out-of-role tool, this
hook blocks the call.

Protocol: reads JSON from stdin (tool_name, tool_input),
writes JSON to stdout if blocking. Exit 0 = allow, exit 1 = block.

Environment variables:
  FULLSEND_TOOL_ALLOWLIST: Comma-separated list of allowed tool names.
                            Required when this hook is enabled.
                            If unset, all tools are blocked (fail-closed).
                            If set to empty string "", all tools are blocked.
"""

from __future__ import annotations

import json
import os
import sys
from datetime import UTC, datetime

FINDINGS_PATH = "/sandbox/workspace/.security/findings.jsonl"
MAX_INPUT_BYTES = 10 * 1024 * 1024  # 10 MB

_ERR_MALFORMED = '{"decision":"block","reason":"ALLOWLIST_HOOK_ERROR: malformed JSON input"}'
_ERR_UNEXPECTED = (
    '{"decision":"block","reason":"ALLOWLIST_HOOK_ERROR: unexpected error reading input"}'
)
_ERR_OVERSIZED = '{"decision":"block","reason":"ALLOWLIST_HOOK_ERROR: input exceeds 10 MB limit"}'


def log_finding(name: str, severity: str, detail: str, action: str) -> None:
    trace_id = os.environ.get("FULLSEND_TRACE_ID", "")
    finding = {
        "trace_id": trace_id,
        "timestamp": datetime.now(UTC).isoformat(),
        "phase": "hook_pretool",
        "scanner": "tool_allowlist_pretool",
        "name": name,
        "severity": severity,
        "detail": detail,
        "action": action,
    }
    try:
        os.makedirs(os.path.dirname(FINDINGS_PATH), exist_ok=True)
        with open(FINDINGS_PATH, "a") as f:
            f.write(json.dumps(finding) + "\n")
    except OSError:
        pass


# Canonical Claude Code tool names — the hook vocabulary (ADR 0090, #608).
# Mirror of security.CanonicalClaudeTools (internal/security/canonical_tools.go);
# TestToolAllowlistHook_VocabularyMatchesGo fails if the two drift. Verified
# 2026-08-23 against the live tools reference (latest release, 2.1.241 then;
# no tool changes in the CHANGELOG since the pinned 2.1.234).
CANONICAL_TOOLS = frozenset(
    {
        "Agent",
        "Artifact",
        "AskUserQuestion",
        "Bash",
        "CronCreate",
        "CronDelete",
        "CronList",
        "Edit",
        "EndConversation",
        "EnterPlanMode",
        "EnterWorktree",
        "ExitPlanMode",
        "ExitWorktree",
        "Glob",
        "Grep",
        "ListAgents",
        "ListMcpResourcesTool",
        "LSP",
        "Monitor",
        "NotebookEdit",
        "PowerShell",
        "PushNotification",
        "Read",
        "ReadMcpResourceTool",
        "RemoteTrigger",
        "ReportFindings",
        "ScheduleWakeup",
        "SendMessage",
        "SendUserFile",
        "ShareOnboardingGuide",
        "Skill",
        "TaskCreate",
        "TaskGet",
        "TaskList",
        "TaskOutput",
        "TaskStop",
        "TaskUpdate",
        "TodoWrite",
        "ToolSearch",
        "WaitForMcpServers",
        "WebFetch",
        "WebSearch",
        "Workflow",
        "Write",
    }
)

# Names Claude Code no longer exposes but which agent `tools:` frontmatter and
# runtime adapters (pi: ls -> LS, MultiEdit -> edit) still use. Mirror of
# security.LegacyClaudeTools.
LEGACY_TOOLS = frozenset(
    {
        "LS",
        "MultiEdit",
        "NotebookRead",
        "Task",
        "TodoRead",
    }
)

MCP_PREFIX = "mcp__"


def _is_known_tool(name: str) -> bool:
    return name in CANONICAL_TOOLS or name in LEGACY_TOOLS


def _find_case_match(tool_name: str, allowed: frozenset[str]) -> str | None:
    """Return the allowlisted name that equals tool_name case-insensitively.

    None when there is no such entry, or when the name is an MCP tool
    (``mcp__<server>__<tool>``): MCP names are not canonical vocabulary and are
    matched verbatim, so a case variant is a different tool, not an
    un-normalized one. When several entries fold to the same value, a known
    Claude tool name wins; otherwise the first in sorted order.
    """
    folded = tool_name.casefold()
    if folded.startswith(MCP_PREFIX):
        return None
    candidates = sorted(name for name in allowed if name.casefold() == folded)
    if not candidates:
        return None
    for name in candidates:
        if _is_known_tool(name):
            return name
    return candidates[0]


def _unnormalized_diagnosis(tool_name: str, match: str) -> tuple[str, str, str]:
    """Classify a case-variant collision: (finding name, detail, block reason).

    The allowlist is exact-match, so the call is blocked either way; the
    diagnosis only says *which side* is not Claude vocabulary:
    tool_name_unnormalized (the runtime adapter did not translate),
    allowlist_entry_unnormalized (the allowlist is written in the wrong
    case) or tool_name_case_collision (neither spelling is a Claude tool).
    """
    if _is_known_tool(match):
        if match in CANONICAL_TOOLS:
            what = f"is not canonical Claude vocabulary (expected '{match}')"
        else:
            what = f"is not the legacy Claude name the allowlist uses (expected '{match}')"
        return (
            "tool_name_unnormalized",
            f"Tool '{tool_name}' is a case variant of Claude tool '{match}'",
            f"ALLOWLIST_HOOK_ERROR: tool name '{tool_name}' {what}; "
            "the runtime adapter must translate it",
        )
    if _is_known_tool(tool_name):
        kind = "canonical" if tool_name in CANONICAL_TOOLS else "legacy"
        return (
            "allowlist_entry_unnormalized",
            f"Allowlist entry '{match}' is a case variant of Claude tool '{tool_name}'",
            f"ALLOWLIST_HOOK_ERROR: FULLSEND_TOOL_ALLOWLIST entry '{match}' is not "
            f"Claude vocabulary (expected {kind} name '{tool_name}'); fix the allowlist",
        )
    return (
        "tool_name_case_collision",
        f"Tool '{tool_name}' is a case variant of allowlisted '{match}'; neither is a Claude tool",
        f"ALLOWLIST_HOOK_ERROR: tool name '{tool_name}' is a case variant of "
        f"allowlisted '{match}'; neither is a Claude tool name and tool names "
        "are case-sensitive",
    )


def _parse_allowlist(env_value: str | None) -> frozenset[str]:
    if env_value is None:
        return frozenset()
    tools = {t.strip() for t in env_value.split(",") if t.strip()}
    return frozenset(tools)


def main() -> None:
    try:
        raw = sys.stdin.read(MAX_INPUT_BYTES + 1)
        if len(raw) > MAX_INPUT_BYTES:
            sys.stdout.write(_ERR_OVERSIZED)
            sys.exit(1)
        if not raw.strip():
            sys.exit(0)
        hook_input = json.loads(raw)
    except json.JSONDecodeError:
        sys.stdout.write(_ERR_MALFORMED)
        sys.exit(1)
    except Exception:  # noqa: BLE001
        sys.stdout.write(_ERR_UNEXPECTED)
        sys.exit(1)

    if not isinstance(hook_input, dict):
        # Valid JSON that is not an object ([] / null / 123) is malformed for
        # this protocol; keep the JSON block contract rather than tracebacking.
        sys.stdout.write(_ERR_MALFORMED)
        sys.exit(1)

    tool_name = hook_input.get("tool_name", "")
    if not isinstance(tool_name, str) or not tool_name:
        json.dump(
            {"decision": "block", "reason": "Tool name is empty, missing or not a string"},
            sys.stdout,
        )
        sys.exit(1)

    env_value = os.environ.get("FULLSEND_TOOL_ALLOWLIST")
    allowed_tools = _parse_allowlist(env_value)

    if tool_name in allowed_tools:
        sys.exit(0)

    # A case variant of an allowlisted name is still blocked (exact match,
    # fail-closed), but reported as a normalization gap — an adapter that did
    # not translate to Claude vocabulary, or an allowlist entry that is not
    # Claude vocabulary — instead of as a forbidden tool (ADR 0090, #608).
    case_match = _find_case_match(tool_name, allowed_tools)
    if case_match is not None:
        finding, detail, reason = _unnormalized_diagnosis(tool_name, case_match)
        log_finding(finding, "high", detail, "block")
        json.dump({"decision": "block", "reason": reason}, sys.stdout)
        sys.exit(1)

    log_finding("tool_blocked", "critical", f"Tool '{tool_name}' blocked by allowlist", "block")
    reason = f"Tool '{tool_name}' is NOT in the allowlist"
    json.dump({"decision": "block", "reason": reason}, sys.stdout)
    sys.exit(1)


if __name__ == "__main__":
    main()
