#!/usr/bin/env python3
"""Claude Code PreToolUse hook for Tirith terminal security scanning.

Intercepts Bash tool calls and runs them through the Tirith CLI for
command injection, unicode tricks, and exfiltration pattern detection.

Requires: tirith binary in PATH (baked into sandbox container image).

Protocol: reads JSON from stdin, writes JSON to stdout.
Exit codes: 0 = allow, 1 = block (with reason on stdout).

Fail-open by default. Set TIRITH_REQUIRED=1 to fail closed when tirith is
missing, times out, or errors (intended for sandbox where tirith is baked in).

That fail-open covers only a tirith that produced no result. Once it has come back
non-zero, every path blocks unless tirith itself says allow or warn — output
this hook cannot read blocks, and so does any unexpected error, because Claude
Code reads a bare exit 1 with no stdout as *non*-blocking, making a traceback
equivalent to an allow.

analysis_incomplete blocks too. Tirith emits it when it cannot resolve what a
command will execute, which is the same signal for an unparsed POSIX idiom and
for a deliberately obfuscated payload, so it cannot be downgraded. The block
reason names the rewrite instead: see "Tirith: accepted shell dialect" in
docs/contributing/runtime-implementation.md.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import traceback
from datetime import UTC, datetime

FINDINGS_PATH = "/sandbox/workspace/.security/findings.jsonl"
TIRITH_FAIL_ON = os.environ.get("TIRITH_FAIL_ON", "high")
# When tirith is baked into the sandbox image, set TIRITH_REQUIRED=1 so that
# a missing binary is treated as a security failure (fail-closed) rather than
# silently skipped (fail-open).
TIRITH_REQUIRED = os.environ.get("TIRITH_REQUIRED", "") == "1"

# Map tirith severity to numeric for comparison
SEVERITY_LEVELS = {"low": 1, "medium": 2, "high": 3, "critical": 4}

# Appended when analysis_incomplete alone caused the block, so the agent gets a
# rewrite instead of "could not analyse". Syntax only: this reaches the caller
# whose command was just blocked, so it must never name the credential rewrite
# that clears the sensitive-upload rule. Every rewrite below is asserted not to
# unblock an attack shape. Mirrors the dialect table in
# docs/contributing/runtime-implementation.md.
DIALECT_HINT = (
    " — tirith could not analyse this command, so it is blocked rather than trusted. "
    'Rewrite it in a form tirith parses: `test -n "$X"` instead of `[ -n "$X" ]` or '
    "`[[ ... ]]`; two-step arithmetic (`n=$(cmd); y=$(( n - 1 ))`) instead of `$( )` "
    "inside `$(( ))`; in a `case`, only the first arm may be a glob. "
    "See docs/contributing/runtime-implementation.md, 'Tirith: accepted shell dialect'."
)


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
    thresh_level = SEVERITY_LEVELS.get(threshold.lower(), 3)
    sev_level = SEVERITY_LEVELS.get(severity.lower(), thresh_level)
    return sev_level >= thresh_level


def check_command(command: str) -> tuple[bool, str]:
    """Run tirith check on a command. Returns (should_block, reason)."""
    try:
        result = subprocess.run(
            ["tirith", "check", "--json", "--non-interactive", "--shell", "posix", "--", command],
            capture_output=True,
            text=True,
            # Without this a non-UTF-8 byte in a finding raises into the
            # fail-open handler below, discarding a real threat report.
            errors="replace",
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

    # Parse tirith JSON output.
    # v0.3.x returns {"action": "block", "findings": [...], ...}
    # v0.2.x returned a flat list of findings.
    try:
        raw = json.loads(result.stdout)
    except json.JSONDecodeError:
        stderr_snippet = result.stderr.strip()[:500]
        reason = f"Tirith blocked command (exit code {result.returncode}): {stderr_snippet}"
        log_finding("tirith_block", "high", reason, "block")
        return True, reason

    if isinstance(raw, dict):
        findings = raw.get("findings", [])
        # dict.get's default only covers an absent key, not a present null.
        # An unreadable action is not an allow: "" routes to the fail-closed tail.
        raw_action = raw.get("action", "")
        action = raw_action.lower().strip() if isinstance(raw_action, str) else ""
    elif isinstance(raw, list):
        findings = raw
        action = ""
    else:
        reason = (
            f"Tirith blocked command (exit code {result.returncode}): "
            f"top-level output is {type(raw).__name__}, expected an object or a list"
        )
        log_finding("tirith_malformed_output", "high", reason, "block")
        return True, reason

    # Renovate bumps TIRITH_VERSION on its own, so a release that restructures
    # "findings" has to fail closed rather than run the loop over nothing.
    if not isinstance(findings, list):
        reason = (
            f"Tirith blocked command (exit code {result.returncode}): "
            f"findings is {type(findings).__name__}, expected a list"
        )
        log_finding("tirith_malformed_output", "high", reason, "block")
        return True, reason

    # Tracked apart so a confirmed threat is reported even when an
    # analysis_incomplete finding precedes it in the array.
    threat_reason = ""
    incomplete_reason = ""
    saw_analysis_incomplete = False
    for finding in findings:
        # Skipping an unreadable entry would let the response reach the allow
        # paths below with nothing examined.
        if not isinstance(finding, dict):
            reason = (
                f"Tirith blocked command (exit code {result.returncode}): "
                f"finding entry is {type(finding).__name__}, expected an object"
            )
            log_finding("tirith_malformed_output", "high", reason, "block")
            return True, reason
        severity = finding.get("severity", "medium")
        if not isinstance(severity, str):
            # A severity we cannot compare must not default to "medium".
            reason = (
                f"Tirith blocked command (exit code {result.returncode}): "
                f"severity is {type(severity).__name__}, expected a string"
            )
            log_finding("tirith_malformed_output", "high", reason, "block")
            return True, reason
        rule = finding.get("rule_id", finding.get("rule", "unknown"))
        detail = finding.get("title", finding.get("message", finding.get("detail", "")))
        msg = f"Tirith [{severity}] {rule}: {detail}"

        incomplete = rule == "analysis_incomplete"
        if incomplete:
            saw_analysis_incomplete = True

        if severity_meets_threshold(severity, TIRITH_FAIL_ON):
            log_finding(rule, severity, msg, "block")
            if incomplete:
                if not incomplete_reason:
                    incomplete_reason = msg
            elif not threat_reason:
                threat_reason = msg
        else:
            log_finding(rule, severity, msg, "warn")

    block_reason = threat_reason or incomplete_reason

    # Keyed on what caused the block, not on the raw finding list, so an
    # unrelated below-threshold finding does not suppress the guidance.
    only_analysis_incomplete = saw_analysis_incomplete and not threat_reason

    if block_reason:
        if only_analysis_incomplete:
            block_reason += DIALECT_HINT
        return True, block_reason

    # v0.3.x: honour the top-level action field even when no individual
    # finding met the severity threshold.
    if action == "block":
        reason = "Tirith action=block (no individual finding met threshold)"
        if only_analysis_incomplete:
            reason += DIALECT_HINT
        log_finding("tirith_action_block", "high", reason, "block")
        return True, reason

    if action in ("allow", "warn"):
        return False, ""

    # Fall back to non-zero exit code: tirith signalled a problem but output
    # didn't contain a parseable block reason — block anyway.
    reason = f"Tirith blocked command (exit code {result.returncode})"
    log_finding("tirith_block", "high", reason, "block")
    return True, reason


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

    # Wrapped because an escaping traceback prints no decision, which the
    # runner reads as an allow. SystemExit is a BaseException, so the sys.exit
    # calls below pass through the handler untouched.
    try:
        tool_name = tool_input.get("tool_name", "")
        if tool_name != "Bash":
            sys.exit(0)

        tool_args = tool_input.get("tool_input", {})
        command = tool_args.get("command", "") if isinstance(tool_args, dict) else ""
        # Typed before the emptiness check, so a falsy non-string ({}, null,
        # false, 0, []) blocks instead of passing as "no command to scan".
        if not isinstance(command, str):
            reason = f"Hook input command is {type(command).__name__}, expected a string"
            log_finding("hook_input_malformed", "critical", reason, "block")
            json.dump({"decision": "block", "reason": reason}, sys.stdout)
            sys.exit(1)
        if not command:
            sys.exit(0)

        should_block, reason = check_command(command)
    except Exception as e:
        # stdout is the decision channel, so the traceback goes to stderr.
        traceback.print_exc(file=sys.stderr)
        reason = f"Hook error (fail-closed): {type(e).__name__}"
        log_finding("hook_error", "critical", reason, "block")
        json.dump({"decision": "block", "reason": reason}, sys.stdout)
        sys.exit(1)

    if should_block:
        json.dump({"decision": "block", "reason": reason}, sys.stdout)
        sys.exit(1)

    sys.exit(0)


if __name__ == "__main__":
    main()
