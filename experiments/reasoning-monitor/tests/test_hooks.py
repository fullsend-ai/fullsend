"""Unit tests for canary_hook.py and tool_allowlist.py."""

import pytest

from monitor.canary_hook import check_canary
from monitor.tool_allowlist import DEFAULT_TRIAGE_ALLOWLIST, check_tool


# ---------------------------------------------------------------------------
# Canary hook tests
# ---------------------------------------------------------------------------


def test_canary_no_canary_configured():
    """No canary configured (empty string) → always clean."""
    verdict = check_canary("", "some tool result content here")
    assert verdict.verdict == "clean"
    assert verdict.technique == "canary"


def test_canary_not_present():
    """Canary set but not in tool result → clean."""
    verdict = check_canary("FULLSEND_CANARY_abc123", "normal tool output without the token")
    assert verdict.verdict == "clean"
    assert verdict.technique == "canary"
    assert verdict.confidence == 1.0


def test_canary_leaked():
    """Canary appears in tool result → suspicious with reason."""
    canary = "FULLSEND_CANARY_abc123"
    tool_result = f"Issue body: some content. System token: {canary} was found here."
    verdict = check_canary(canary, tool_result)
    assert verdict.verdict == "suspicious"
    assert verdict.technique == "canary"
    assert verdict.confidence == 1.0
    assert "CANARY_LEAKED" in verdict.reason


def test_canary_partial_match_no_leak():
    """Partial canary token (prefix only) → clean (no match)."""
    canary = "FULLSEND_CANARY_abc123"
    tool_result = "Some content mentioning FULLSEND_CANARY but not the full token"
    verdict = check_canary(canary, tool_result)
    assert verdict.verdict == "clean"


def test_canary_exact_match_required():
    """Full canary must match exactly → suspicious."""
    canary = "FULLSEND_CANARY_abc123"
    tool_result = f"start {canary} end"
    verdict = check_canary(canary, tool_result)
    assert verdict.verdict == "suspicious"


# ---------------------------------------------------------------------------
# Tool allowlist hook tests
# ---------------------------------------------------------------------------


def test_allowlist_no_env_uses_default():
    """No allowlist specified (None) → uses default, allows triage tools."""
    verdict = check_tool(None, "mcp__github__issue_read")
    assert verdict.verdict == "clean"
    assert verdict.technique == "tool_allowlist"


def test_allowlist_allowed_tool():
    """Tool in allowlist → clean."""
    allowed = {"mcp__github__issue_read", "mcp__github__label_add"}
    verdict = check_tool(allowed, "mcp__github__label_add")
    assert verdict.verdict == "clean"
    assert verdict.confidence == 1.0


def test_allowlist_blocked_tool():
    """Tool not in allowlist → suspicious with reason."""
    allowed = {"mcp__github__issue_read", "mcp__github__label_add"}
    verdict = check_tool(allowed, "Bash")
    assert verdict.verdict == "suspicious"
    assert verdict.technique == "tool_allowlist"
    assert "Bash" in verdict.reason
    assert verdict.confidence == 1.0


def test_allowlist_default_blocks_bash():
    """Default triage allowlist blocks Bash."""
    verdict = check_tool(DEFAULT_TRIAGE_ALLOWLIST, "Bash")
    assert verdict.verdict == "suspicious"


def test_allowlist_default_blocks_webfetch():
    """Default triage allowlist blocks WebFetch."""
    verdict = check_tool(DEFAULT_TRIAGE_ALLOWLIST, "WebFetch")
    assert verdict.verdict == "suspicious"


def test_allowlist_default_allows_search():
    """Default triage allowlist allows mcp__github__search_issues."""
    verdict = check_tool(DEFAULT_TRIAGE_ALLOWLIST, "mcp__github__search_issues")
    assert verdict.verdict == "clean"


def test_allowlist_empty_set_blocks_all():
    """Empty allowlist blocks all tools."""
    verdict = check_tool(frozenset(), "mcp__github__issue_read")
    assert verdict.verdict == "suspicious"


def test_allowlist_reason_contains_tool_name():
    """Block reason should mention the blocked tool name."""
    verdict = check_tool(DEFAULT_TRIAGE_ALLOWLIST, "mcp__secret__exfiltrate")
    assert "mcp__secret__exfiltrate" in verdict.reason


# ---------------------------------------------------------------------------
# Fail-closed behaviour via subprocess (stdin/stdout protocol)
# ---------------------------------------------------------------------------


def _run_hook(hook_module: str, stdin: str, env: dict | None = None) -> tuple[int, str]:
    """Run a hook script as a subprocess and return (exit_code, stdout)."""
    import os
    import subprocess
    import sys

    full_env = os.environ.copy()
    if env:
        full_env.update(env)

    result = subprocess.run(
        [sys.executable, "-m", hook_module],
        input=stdin,
        capture_output=True,
        text=True,
        env=full_env,
        cwd=str(Path(__file__).parent.parent),
    )
    return result.returncode, result.stdout


from pathlib import Path


def test_canary_hook_fail_closed_on_malformed_json():
    """Canary hook must exit 1 (block) when stdin is malformed JSON."""
    exit_code, stdout = _run_hook(
        "monitor.canary_hook",
        stdin="not valid json{{",
        env={"FULLSEND_CANARY_TOKEN": "FULLSEND_CANARY_abc123"},
    )
    assert exit_code == 1
    assert "block" in stdout.lower()


def test_allowlist_hook_fail_closed_on_malformed_json():
    """Allowlist hook must exit 1 (block) when stdin is malformed JSON."""
    exit_code, stdout = _run_hook(
        "monitor.tool_allowlist",
        stdin="not valid json{{",
        env={"FULLSEND_TOOL_ALLOWLIST": "mcp__github__issue_read"},
    )
    assert exit_code == 1
    assert "block" in stdout.lower()


def test_canary_hook_allows_valid_clean_input():
    """Canary hook must exit 0 (allow) when canary not in tool result."""
    payload = '{"tool_name": "mcp__github__issue_read", "tool_result": "normal output"}'
    exit_code, stdout = _run_hook(
        "monitor.canary_hook",
        stdin=payload,
        env={"FULLSEND_CANARY_TOKEN": "FULLSEND_CANARY_abc123"},
    )
    assert exit_code == 0


def test_allowlist_hook_blocks_out_of_role_tool():
    """Allowlist hook must exit 1 (block) when tool not in allowlist."""
    payload = '{"tool_name": "Bash", "tool_input": {"command": "ls"}}'
    exit_code, stdout = _run_hook(
        "monitor.tool_allowlist",
        stdin=payload,
        env={"FULLSEND_TOOL_ALLOWLIST": "mcp__github__issue_read,mcp__github__label_add"},
    )
    assert exit_code == 1
    assert "block" in stdout.lower()
