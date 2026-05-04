"""Tests for tool_allowlist_pretool.py PreToolUse hook."""

from __future__ import annotations

import json
import os
import subprocess
import sys

HOOK_PATH = os.path.join(os.path.dirname(__file__), "tool_allowlist_pretool.py")


def _run_hook(stdin_data: str, env_extra: dict[str, str] | None = None) -> tuple[int, str]:
    env = {k: v for k, v in os.environ.items() if k != "FULLSEND_TOOL_ALLOWLIST"}
    env.update(env_extra or {})
    result = subprocess.run(
        [sys.executable, HOOK_PATH],
        input=stdin_data,
        capture_output=True,
        text=True,
        env=env,
    )
    return result.returncode, result.stdout


def test_unset_allowlist_blocks_all():
    code, stdout = _run_hook(
        json.dumps({"tool_name": "mcp__github__issue_read"}),
    )
    assert code == 1
    response = json.loads(stdout)
    assert response["decision"] == "block"


def test_custom_allowlist_allows_listed_tool():
    code, _stdout = _run_hook(
        json.dumps({"tool_name": "Bash"}),
        {"FULLSEND_TOOL_ALLOWLIST": "Bash,Read,Write"},
    )
    assert code == 0


def test_custom_allowlist_blocks_unlisted_tool():
    code, stdout = _run_hook(
        json.dumps({"tool_name": "WebFetch"}),
        {"FULLSEND_TOOL_ALLOWLIST": "Bash,Read,Write"},
    )
    assert code == 1
    response = json.loads(stdout)
    assert response["decision"] == "block"


def test_empty_allowlist_blocks_all():
    code, _stdout = _run_hook(
        json.dumps({"tool_name": "mcp__github__issue_read"}),
        {"FULLSEND_TOOL_ALLOWLIST": ""},
    )
    assert code == 1


def test_malformed_json_fails_closed():
    code, stdout = _run_hook(
        "not valid json{{{",
    )
    assert code == 1
    response = json.loads(stdout)
    assert response["decision"] == "block"
    assert "malformed" in response["reason"].lower()


def test_empty_stdin_allows():
    code, _stdout = _run_hook("")
    assert code == 0


def test_empty_tool_name_blocks():
    code, stdout = _run_hook(
        json.dumps({"tool_name": ""}),
    )
    assert code == 1
    response = json.loads(stdout)
    assert response["decision"] == "block"


def test_missing_tool_name_blocks():
    code, stdout = _run_hook(
        json.dumps({"tool_input": {"command": "ls"}}),
    )
    assert code == 1
    response = json.loads(stdout)
    assert response["decision"] == "block"
