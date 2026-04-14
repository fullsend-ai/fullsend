#!/usr/bin/env python3
"""Claude Code PreToolUse hook: tool call allowlist enforcement.

Blocks tool calls that are outside the triage agent's authorized tool set.
If the agent attempts to call Bash, WebFetch, or any other out-of-role tool,
this hook blocks the call and returns a reason.

Install in .claude/settings.json:
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "*",
        "hooks": [
          {
            "type": "command",
            "command": "python3 experiments/reasoning-monitor/monitor/tool_allowlist.py"
          }
        ]
      }
    ]
  }
}

Environment variables:
  FULLSEND_TOOL_ALLOWLIST: Comma-separated list of allowed tool names.
                            If unset, defaults to the standard triage agent allowlist.
                            If set to empty string "", all tools are blocked.

Protocol: reads JSON from stdin (tool_name, tool_input),
writes JSON to stdout if blocking. Exit 0 = allow, exit 1 = block.
"""

import json
import os
import sys

from monitor.interface import MonitorVerdict

# Default triage agent allowlist — only GitHub issue operations
DEFAULT_TRIAGE_ALLOWLIST: frozenset[str] = frozenset(
    {
        "mcp__github__issue_read",
        "mcp__github__issue_comment",
        "mcp__github__label_add",
        "mcp__github__label_remove",
        "mcp__github__search_issues",
    }
)


def check_tool(allowed_tools: frozenset[str] | set[str] | None, tool_name: str) -> MonitorVerdict:
    """Check whether a tool call is in the allowlist.

    Python API for testing without subprocess invocation.

    Args:
        allowed_tools: Set of allowed tool names. If None, defaults to
                       DEFAULT_TRIAGE_ALLOWLIST.
        tool_name: The tool being called.

    Returns:
        MonitorVerdict with verdict=suspicious if tool is blocked, else clean.
    """
    if allowed_tools is None:
        allowed_tools = DEFAULT_TRIAGE_ALLOWLIST

    if tool_name in allowed_tools:
        return MonitorVerdict(
            verdict="clean",
            confidence=1.0,
            reason=f"Tool '{tool_name}' is in the allowlist",
            technique="tool_allowlist",
        )

    return MonitorVerdict(
        verdict="suspicious",
        confidence=1.0,
        reason=(
            f"Tool '{tool_name}' is NOT in the allowlist "
            f"({', '.join(sorted(allowed_tools))})"
        ),
        technique="tool_allowlist",
    )


def _parse_allowlist(env_value: str | None) -> frozenset[str] | None:
    """Parse FULLSEND_TOOL_ALLOWLIST env var into a frozenset.

    Returns None if env var not set (use default), frozenset otherwise.
    """
    if env_value is None:
        return None
    # Env var set but possibly empty — parse it
    tools = {t.strip() for t in env_value.split(",") if t.strip()}
    return frozenset(tools)


def main() -> None:
    """Hook entrypoint: read JSON from stdin, write decision to stdout."""
    try:
        raw = sys.stdin.read()
        if not raw.strip():
            sys.exit(0)
        hook_input = json.loads(raw)
    except json.JSONDecodeError:
        # Fail closed: malformed input from the harness is treated as a block.
        # A security hook that silently allows on error provides no defense.
        json.dump({"decision": "block", "reason": "ALLOWLIST_HOOK_ERROR: malformed JSON input"}, sys.stdout)
        sys.exit(1)
    except Exception:  # noqa: BLE001
        json.dump({"decision": "block", "reason": "ALLOWLIST_HOOK_ERROR: unexpected error reading input"}, sys.stdout)
        sys.exit(1)

    env_value = os.environ.get("FULLSEND_TOOL_ALLOWLIST")
    allowed_tools = _parse_allowlist(env_value)

    tool_name = hook_input.get("tool_name", "")
    if not tool_name:
        sys.exit(0)

    verdict = check_tool(allowed_tools, tool_name)

    if verdict.verdict == "suspicious":
        json.dump({"decision": "block", "reason": verdict.reason}, sys.stdout)
        sys.exit(1)

    sys.exit(0)


if __name__ == "__main__":
    main()
