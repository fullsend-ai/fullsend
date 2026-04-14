#!/usr/bin/env python3
"""Claude Code PostToolUse hook: canary token leak detection.

Checks whether a pre-injected canary token appears in tool results.
If the canary leaks into tool output, it means the agent is exfiltrating
the system prompt or secrets, or poisoned content echoed the token.

Install in .claude/settings.json:
{
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "*",
        "hooks": [
          {
            "type": "command",
            "command": "python3 experiments/reasoning-monitor/monitor/canary_hook.py"
          }
        ]
      }
    ]
  }
}

Environment variables:
  FULLSEND_CANARY_TOKEN: The canary string to watch for in tool results.
                         If unset, the hook is a no-op (allow all).

Protocol: reads JSON from stdin (tool_name, tool_input, tool_result),
writes JSON to stdout if blocking. Exit 0 = allow, exit 1 = block.
"""

import json
import os
import sys

from monitor.interface import MonitorVerdict


def check_canary(canary: str, tool_result: str) -> MonitorVerdict:
    """Check whether the canary token appears in a tool result.

    Python API for testing without subprocess invocation.

    Args:
        canary: The canary token to search for.
        tool_result: The tool result content to check.

    Returns:
        MonitorVerdict with verdict=suspicious if canary found, else clean.
    """
    if canary and canary in tool_result:
        return MonitorVerdict(
            verdict="suspicious",
            confidence=1.0,
            reason=f"CANARY_LEAKED: canary token found in tool result",
            technique="canary",
        )
    return MonitorVerdict(
        verdict="clean",
        confidence=1.0,
        reason="Canary token not present in tool result",
        technique="canary",
    )


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
        json.dump({"decision": "block", "reason": "CANARY_HOOK_ERROR: malformed JSON input"}, sys.stdout)
        sys.exit(1)
    except Exception:  # noqa: BLE001
        json.dump({"decision": "block", "reason": "CANARY_HOOK_ERROR: unexpected error reading input"}, sys.stdout)
        sys.exit(1)

    canary = os.environ.get("FULLSEND_CANARY_TOKEN", "")
    if not canary:
        # No canary configured — nothing to check
        sys.exit(0)

    tool_result = hook_input.get("tool_result", "")
    if not isinstance(tool_result, str):
        tool_result = json.dumps(tool_result)

    verdict = check_canary(canary, tool_result)

    if verdict.verdict == "suspicious":
        json.dump({"decision": "block", "reason": verdict.reason}, sys.stdout)
        sys.exit(1)

    sys.exit(0)


if __name__ == "__main__":
    main()
