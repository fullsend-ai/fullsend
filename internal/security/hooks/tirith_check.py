#!/usr/bin/env python3
"""Claude Code PreToolUse hook for Tirith terminal security scanning.

Intercepts Bash tool calls and runs them through the Tirith CLI for
command injection, unicode tricks, and exfiltration pattern detection.

Requires: tirith binary in PATH (baked into sandbox container image).

Protocol: reads JSON from stdin, writes JSON to stdout.
Exit codes: 0 = allow, 1 = block (with reason on stdout).

Fail-open by default. Set TIRITH_REQUIRED=1 to fail closed when tirith is
missing, times out, or errors (intended for sandbox where tirith is baked in).
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
from datetime import UTC, datetime

FINDINGS_PATH = "/tmp/workspace/.security/findings.jsonl"
TIRITH_FAIL_ON = os.environ.get("TIRITH_FAIL_ON", "high")
# When tirith is baked into the sandbox image, set TIRITH_REQUIRED=1 so that
# a missing binary is treated as a security failure (fail-closed) rather than
# silently skipped (fail-open).
TIRITH_REQUIRED = os.environ.get("TIRITH_REQUIRED", "") == "1"

# Map tirith severity to numeric for comparison
SEVERITY_LEVELS = {"low": 1, "medium": 2, "high": 3, "critical": 4}


def log_finding(name: str, severity: str, detail: str, action: str):
    trace_id = os.environ.get("FULLSEND_TRACE_ID", "")
    finding = {
        "trace_id": trace_id,
        "timestamp": datetime.now(UTC).isoformat(),
        "phase": "hook_pretool",
        "scanner": "tirith_check",
        "name": name,
        "severity": severity,
        "detail": detail,
        "action": action,
    }
    try:
        with open(FINDINGS_PATH, "a") as f:
            f.write(json.dumps(finding) + "\n")
    except OSError:
        pass


def severity_meets_threshold(severity: str, threshold: str) -> bool:
    sev_level = SEVERITY_LEVELS.get(severity.lower(), 0)
    thresh_level = SEVERITY_LEVELS.get(threshold.lower(), 3)
    return sev_level >= thresh_level


def check_command(command: str) -> tuple[bool, str]:
    """Run tirith check on a command. Returns (should_block, reason)."""
    try:
        result = subprocess.run(
            ["tirith", "check", "--json", "--non-interactive", "--shell", "posix", "--", command],
            capture_output=True,
            text=True,
            timeout=5,
        )
    except FileNotFoundError:
        if TIRITH_REQUIRED:
            reason = "tirith binary not found but TIRITH_REQUIRED=1 (expected in sandbox image)"
            log_finding("tirith_missing", "critical", reason, "block")
            return True, reason
        return False, ""
    except subprocess.TimeoutExpired:
        if TIRITH_REQUIRED:
            reason = "tirith timed out — blocking (TIRITH_REQUIRED=1)"
            log_finding("tirith_timeout", "critical", reason, "block")
            return True, reason
        return False, ""
    except Exception as e:
        if TIRITH_REQUIRED:
            sanitized_err = type(e).__name__
            reason = f"tirith error: {sanitized_err} — blocking (TIRITH_REQUIRED=1)"
            log_finding("tirith_error", "critical", reason, "block")
            return True, reason
        return False, ""

    if result.returncode == 0:
        return False, ""

    # Parse tirith JSON output
    try:
        findings = json.loads(result.stdout)
    except (json.JSONDecodeError, Exception):
        # Can't parse output — treat any non-zero exit as a block.
        if result.returncode != 0:
            reason = (
                f"Tirith blocked command (exit code {result.returncode}): {result.stderr.strip()}"
            )
            log_finding("tirith_block", "high", reason, "block")
            return True, reason
        return False, ""

    # Check findings against threshold
    if isinstance(findings, list):
        for finding in findings:
            severity = finding.get("severity", "medium")
            if severity_meets_threshold(severity, TIRITH_FAIL_ON):
                rule = finding.get("rule", "unknown")
                detail = finding.get("message", finding.get("detail", ""))
                reason = f"Tirith [{severity}] {rule}: {detail}"
                log_finding(rule, severity, reason, "block")
                return True, reason
            else:
                rule = finding.get("rule", "unknown")
                detail = finding.get("message", finding.get("detail", ""))
                log_finding(rule, severity, f"Tirith [{severity}] {rule}: {detail}", "warn")

    return False, ""


MAX_INPUT_BYTES = 10 * 1024 * 1024  # 10 MB


def main():
    try:
        raw = sys.stdin.read(MAX_INPUT_BYTES + 1)
        if len(raw) > MAX_INPUT_BYTES:
            # Oversized input — fail closed (pre-tool hook blocks).
            json.dump({"decision": "block", "reason": "Hook input exceeds 10 MB limit"}, sys.stdout)
            sys.exit(1)
        if not raw.strip():
            sys.exit(0)
        tool_input = json.loads(raw)
    except json.JSONDecodeError:
        json.dump(
            {"decision": "block", "reason": "Unparseable hook input (fail-closed)"}, sys.stdout
        )
        sys.exit(1)
    except Exception as e:
        json.dump({"decision": "block", "reason": f"Hook error (fail-closed): {e}"}, sys.stdout)
        sys.exit(1)

    tool_name = tool_input.get("tool_name", "")
    if tool_name != "Bash":
        sys.exit(0)

    command = tool_input.get("tool_input", {}).get("command", "")
    if not command:
        sys.exit(0)

    should_block, reason = check_command(command)

    if should_block:
        json.dump({"decision": "block", "reason": reason}, sys.stdout)
        sys.exit(1)

    sys.exit(0)


if __name__ == "__main__":
    main()
