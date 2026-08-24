"""Tests for tool_allowlist_pretool.py PreToolUse hook."""

from __future__ import annotations

import importlib.util
import io
import json
import os
import subprocess
import sys

import pytest

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


def test_unnormalized_name_blocked_with_diagnostic():
    """A case variant of a canonical allowlisted name is blocked with ALLOWLIST_HOOK_ERROR."""
    code, stdout = _run_hook(
        json.dumps({"tool_name": "bash"}),
        {"FULLSEND_TOOL_ALLOWLIST": "Bash,Read,Write"},
    )
    assert code == 1
    response = json.loads(stdout)
    assert response["decision"] == "block"
    assert "ALLOWLIST_HOOK_ERROR" in response["reason"]
    assert "'bash'" in response["reason"]
    assert "'Bash'" in response["reason"]
    assert "runtime adapter must translate" in response["reason"]


def test_uppercase_variant_blocked_with_diagnostic():
    """ALL-CAPS variant also gets the diagnostic."""
    code, stdout = _run_hook(
        json.dumps({"tool_name": "BASH"}),
        {"FULLSEND_TOOL_ALLOWLIST": "Bash,Read,Write"},
    )
    assert code == 1
    response = json.loads(stdout)
    assert "ALLOWLIST_HOOK_ERROR" in response["reason"]
    assert "'Bash'" in response["reason"]


def test_legacy_name_variant_names_adapter():
    """pi's `ls` reaching the hook untranslated is an adapter gap against legacy `LS`."""
    code, stdout = _run_hook(
        json.dumps({"tool_name": "ls"}),
        {"FULLSEND_TOOL_ALLOWLIST": "Read,LS"},
    )
    assert code == 1
    response = json.loads(stdout)
    assert "ALLOWLIST_HOOK_ERROR" in response["reason"]
    assert "is not the legacy Claude name the allowlist uses (expected 'LS')" in response["reason"]
    assert "canonical" not in response["reason"]
    assert "runtime adapter must translate" in response["reason"]


def test_unnormalized_allowlist_entry_names_allowlist():
    """Canonical tool vs lowercase allowlist: the allowlist is the un-normalized side."""
    code, stdout = _run_hook(
        json.dumps({"tool_name": "Bash"}),
        {"FULLSEND_TOOL_ALLOWLIST": "bash,read"},
    )
    assert code == 1
    response = json.loads(stdout)
    assert response["decision"] == "block"
    assert "ALLOWLIST_HOOK_ERROR" in response["reason"]
    assert "FULLSEND_TOOL_ALLOWLIST entry 'bash'" in response["reason"]
    assert "expected canonical name 'Bash'" in response["reason"]
    assert "adapter" not in response["reason"]


def test_unnormalized_allowlist_entry_for_legacy_name():
    """A legacy Claude name is still the known side, but not called canonical."""
    code, stdout = _run_hook(
        json.dumps({"tool_name": "Task"}),
        {"FULLSEND_TOOL_ALLOWLIST": "TASK"},
    )
    assert code == 1
    response = json.loads(stdout)
    assert "FULLSEND_TOOL_ALLOWLIST entry 'TASK'" in response["reason"]
    assert "expected legacy name 'Task'" in response["reason"]


def test_unknown_case_variant_blames_neither_side():
    """Neither spelling is Claude vocabulary: no adapter or allowlist is blamed."""
    code, stdout = _run_hook(
        json.dumps({"tool_name": "FOO"}),
        {"FULLSEND_TOOL_ALLOWLIST": "foo"},
    )
    assert code == 1
    response = json.loads(stdout)
    assert "ALLOWLIST_HOOK_ERROR" in response["reason"]
    assert "neither is a Claude tool name" in response["reason"]


def test_mcp_case_variant_is_plain_block():
    """MCP names are matched verbatim; a case variant is a different tool, not a gap."""
    code, stdout = _run_hook(
        json.dumps({"tool_name": "mcp__github__Issue_Read"}),
        {"FULLSEND_TOOL_ALLOWLIST": "mcp__github__issue_read"},
    )
    assert code == 1
    response = json.loads(stdout)
    assert "ALLOWLIST_HOOK_ERROR" not in response["reason"]
    assert "NOT in the allowlist" in response["reason"]


def test_no_case_match_keeps_tool_blocked():
    """A tool with no case-insensitive match uses the standard tool_blocked path."""
    code, stdout = _run_hook(
        json.dumps({"tool_name": "WebFetch"}),
        {"FULLSEND_TOOL_ALLOWLIST": "Bash,Read,Write"},
    )
    assert code == 1
    response = json.loads(stdout)
    assert response["decision"] == "block"
    assert "ALLOWLIST_HOOK_ERROR" not in response["reason"]
    assert "NOT in the allowlist" in response["reason"]


def test_canonical_name_still_allowed():
    """Exact match is still allowed — no false positive from case matching."""
    code, _stdout = _run_hook(
        json.dumps({"tool_name": "Bash"}),
        {"FULLSEND_TOOL_ALLOWLIST": "Bash,Read,Write"},
    )
    assert code == 0


def test_non_string_tool_name_blocks_with_json():
    """A non-string tool_name must not escape as a traceback: block with the JSON contract."""
    for value in (123, True, ["Bash"], {"name": "Bash"}):
        code, stdout = _run_hook(
            json.dumps({"tool_name": value}),
            {"FULLSEND_TOOL_ALLOWLIST": "Bash,Read,Write"},
        )
        assert code == 1, value
        response = json.loads(stdout)
        assert response["decision"] == "block"
        assert "not a string" in response["reason"]


def test_non_object_payload_blocks_with_json():
    """Valid JSON that is not an object must block with the contract, not traceback."""
    for raw in ("[]", "null", "true", "false", "123", '"Bash"'):
        code, stdout = _run_hook(raw, {"FULLSEND_TOOL_ALLOWLIST": "Bash"})
        assert code == 1, raw
        response = json.loads(stdout)
        assert response["decision"] == "block"
        assert "malformed" in response["reason"]


def test_case_fold_collision_prefers_known_name():
    """With `bash` and `Bash` both allowlisted, the diagnostic names the Claude one."""
    code, stdout = _run_hook(
        json.dumps({"tool_name": "BASH"}),
        {"FULLSEND_TOOL_ALLOWLIST": "bash,Bash"},
    )
    assert code == 1
    response = json.loads(stdout)
    assert "expected 'Bash'" in response["reason"]
    assert "runtime adapter must translate" in response["reason"]


def _load_hook_module():
    spec = importlib.util.spec_from_file_location("tool_allowlist_pretool", HOOK_PATH)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _run_in_process(
    monkeypatch, tmp_path, tool_name: str, allowlist: str
) -> tuple[int, dict, list]:
    module = _load_hook_module()
    findings = tmp_path / "findings.jsonl"
    monkeypatch.setattr(module, "FINDINGS_PATH", str(findings))
    monkeypatch.setenv("FULLSEND_TOOL_ALLOWLIST", allowlist)
    monkeypatch.setattr(sys, "stdin", io.StringIO(json.dumps({"tool_name": tool_name})))
    out = io.StringIO()
    monkeypatch.setattr(sys, "stdout", out)
    with pytest.raises(SystemExit) as exc:
        module.main()
    logged = []
    if findings.exists():
        logged = [json.loads(line) for line in findings.read_text().splitlines() if line]
    return int(exc.value.code or 0), json.loads(out.getvalue() or "{}"), logged


def test_unnormalized_finding_is_logged(monkeypatch, tmp_path):
    """The adapter-gap path logs tool_name_unnormalized (high, block), not tool_blocked."""
    code, response, logged = _run_in_process(monkeypatch, tmp_path, "bash", "Bash,Read")
    assert code == 1
    assert response["decision"] == "block"
    assert [f["name"] for f in logged] == ["tool_name_unnormalized"]
    assert logged[0]["severity"] == "high"
    assert logged[0]["action"] == "block"
    assert logged[0]["scanner"] == "tool_allowlist_pretool"
    assert "'Bash'" in logged[0]["detail"]


def test_unnormalized_allowlist_entry_finding_is_logged(monkeypatch, tmp_path):
    code, _response, logged = _run_in_process(monkeypatch, tmp_path, "Bash", "bash,read")
    assert code == 1
    assert [f["name"] for f in logged] == ["allowlist_entry_unnormalized"]
    assert logged[0]["severity"] == "high"
    assert logged[0]["action"] == "block"


def test_case_collision_finding_is_logged(monkeypatch, tmp_path):
    """Neither spelling is Claude vocabulary: a distinct finding, not an adapter gap."""
    code, _response, logged = _run_in_process(monkeypatch, tmp_path, "FOO", "foo")
    assert code == 1
    assert [f["name"] for f in logged] == ["tool_name_case_collision"]
    assert logged[0]["severity"] == "high"


def test_tool_blocked_finding_unchanged(monkeypatch, tmp_path):
    """A genuinely forbidden tool keeps the critical tool_blocked finding."""
    code, _response, logged = _run_in_process(monkeypatch, tmp_path, "WebFetch", "Bash,Read")
    assert code == 1
    assert [f["name"] for f in logged] == ["tool_blocked"]
    assert logged[0]["severity"] == "critical"


def test_allowed_tool_logs_nothing(monkeypatch, tmp_path):
    code, _response, logged = _run_in_process(monkeypatch, tmp_path, "Bash", "Bash,Read")
    assert code == 0
    assert logged == []


def test_vocabulary_sets_are_disjoint_and_well_formed():
    module = _load_hook_module()
    assert module.CANONICAL_TOOLS
    assert not (module.CANONICAL_TOOLS & module.LEGACY_TOOLS)
    for name in module.CANONICAL_TOOLS | module.LEGACY_TOOLS:
        assert name.strip() == name and name
        assert "(" not in name and "," not in name, name
        assert not name.startswith(module.MCP_PREFIX), name
