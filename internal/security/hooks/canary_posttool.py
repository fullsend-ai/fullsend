#!/usr/bin/env python3
"""Claude Code PostToolUse hook: canary token leak detection.

Checks whether a pre-injected canary token appears in tool results.
If the canary leaks, the agent is exfiltrating the system prompt or
secrets, or poisoned content echoed the token back.

This hook blocks (exit 1) on canary detection because a canary leak
is a definitive exfiltration signal requiring immediate termination.

Protocol: reads JSON from stdin (``tool_response`` preferred, ``tool_result``
fallback). Exit 0 = allow, exit 1 = block.

Under Claude Code, ``decision: "block"`` only appends ``reason`` next to the
tool result — Claude still sees the original output. This hook therefore also
emits ``hookSpecificOutput.updatedToolOutput`` with the canary redacted so the
leaked token is not left in context.

Environment variables:
  FULLSEND_CANARY_TOKEN: The canary string to watch for in tool results.
                         If unset, the hook is a no-op (allow all).

Known limitations:
  - Plain substring match only. Encoded representations (base64,
    URL-encoding, hex escaping) will evade detection.
"""

from __future__ import annotations

import json
import os
import sys
from datetime import UTC, datetime

import hook_io

FINDINGS_PATH = "/sandbox/workspace/.security/findings.jsonl"
MAX_INPUT_BYTES = 10 * 1024 * 1024  # 10 MB

_ERR_MALFORMED = '{"decision":"block","reason":"CANARY_HOOK_ERROR: malformed JSON input"}'
_ERR_UNEXPECTED = (
    '{"decision":"block","reason":"CANARY_HOOK_ERROR: unexpected error reading input"}'
)
_ERR_OVERSIZED = '{"decision":"block","reason":"CANARY_HOOK_ERROR: input exceeds 10 MB limit"}'
_ERR_BLOCK = (
    '{"decision":"block","continue":false,'
    '"reason":"CANARY_HOOK_ERROR: canary leak detected; output withheld"}'
)


def log_finding(name: str, severity: str, detail: str, action: str) -> None:
    trace_id = os.environ.get("FULLSEND_TRACE_ID", "")
    finding = {
        "trace_id": trace_id,
        "timestamp": datetime.now(UTC).isoformat(),
        "phase": "hook_posttool",
        "scanner": "canary_posttool",
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

    original = hook_io.payload(hook_input)
    if not hook_io.contains_canary(original, canary):
        sys.exit(0)

    tool_name = hook_input.get("tool_name", "unknown")
    reason = f"CANARY_LEAKED: canary token found in {tool_name} result"
    log_finding("canary_leak", "critical", reason, "block")
    # Block for adapters; also replace output so Claude Code does not keep the
    # leaked token (decision:block alone does not hide PostToolUse output) and
    # set ``continue: false``, the field that actually halts the session.
    # Fails closed: output is withheld unless redaction provably succeeded, and
    # exit 1 is unconditional.
    try:
        blocked = hook_io.redact_canary(original, canary)
        if hook_io.contains_canary(blocked, canary):
            blocked = None
    except Exception:  # noqa: BLE001
        blocked = None
    try:
        hook_io.emit_block(reason, blocked, stop=True)
    except Exception:  # noqa: BLE001
        sys.stdout.write(_ERR_BLOCK)
    sys.exit(1)


if __name__ == "__main__":
    main()
