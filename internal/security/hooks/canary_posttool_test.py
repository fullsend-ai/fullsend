"""Tests for canary_posttool.py PostToolUse hook."""

from __future__ import annotations

import json
import os
import subprocess
import sys

HOOK_PATH = os.path.join(os.path.dirname(__file__), "canary_posttool.py")


def _run_hook(stdin_data: str, env_extra: dict[str, str] | None = None) -> tuple[int, str]:
    env = {k: v for k, v in os.environ.items() if k != "FULLSEND_CANARY_TOKEN"}
    env.update(env_extra or {})
    result = subprocess.run(
        [sys.executable, HOOK_PATH],
        input=stdin_data,
        capture_output=True,
        text=True,
        env=env,
    )
    return result.returncode, result.stdout


def test_no_canary_configured_allows_all():
    env = {k: v for k, v in os.environ.items() if k != "FULLSEND_CANARY_TOKEN"}
    result = subprocess.run(
        [sys.executable, HOOK_PATH],
        input=json.dumps({"tool_name": "Bash", "tool_result": "some output"}),
        capture_output=True,
        text=True,
        env=env,
    )
    assert result.returncode == 0
    assert result.stdout == ""


def test_canary_not_present_allows():
    code, stdout = _run_hook(
        json.dumps({"tool_name": "Bash", "tool_result": "normal output"}),
        {"FULLSEND_CANARY_TOKEN": "SECRET_CANARY_xyz"},
    )
    assert code == 0
    assert stdout == ""


def test_canary_leaked_blocks():
    code, stdout = _run_hook(
        json.dumps({"tool_name": "Bash", "tool_result": "found SECRET_CANARY_xyz in output"}),
        {"FULLSEND_CANARY_TOKEN": "SECRET_CANARY_xyz"},
    )
    assert code == 1
    response = json.loads(stdout)
    assert response["decision"] == "block"
    assert "CANARY_LEAKED" in response["reason"]


def test_canary_in_json_tool_result_blocks():
    code, stdout = _run_hook(
        json.dumps(
            {"tool_name": "mcp__github__issue_read", "tool_result": {"body": "SECRET_CANARY_xyz"}}
        ),
        {"FULLSEND_CANARY_TOKEN": "SECRET_CANARY_xyz"},
    )
    assert code == 1
    response = json.loads(stdout)
    assert response["decision"] == "block"


def test_malformed_json_fails_closed():
    code, stdout = _run_hook(
        "not valid json{{{",
        {"FULLSEND_CANARY_TOKEN": "SECRET_CANARY_xyz"},
    )
    assert code == 1
    response = json.loads(stdout)
    assert response["decision"] == "block"
    assert "malformed" in response["reason"].lower()


def test_case_insensitive_canary_blocks():
    code, stdout = _run_hook(
        json.dumps({"tool_name": "Bash", "tool_result": "leaked secret_canary_XYZ in output"}),
        {"FULLSEND_CANARY_TOKEN": "SECRET_CANARY_xyz"},
    )
    assert code == 1
    response = json.loads(stdout)
    assert response["decision"] == "block"
    assert "CANARY_LEAKED" in response["reason"]


def test_empty_stdin_allows():
    code, stdout = _run_hook(
        "",
        {"FULLSEND_CANARY_TOKEN": "SECRET_CANARY_xyz"},
    )
    assert code == 0
    assert stdout == ""
