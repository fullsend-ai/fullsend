#!/usr/bin/env python3
"""Claude Code PostToolUse hook for context suppression.

Intercepts Bash tool results from verification commands (scan-secrets,
pre-commit, go test, linters, etc.) and replaces verbose success output
with compact one-line summaries. Failures pass through unchanged so the
agent can act on them.

Principle: success is silent, failure is loud.

Protocol: reads JSON from stdin, writes JSON to stdout with compacted
tool_result when suppression applies. Exit code 0 always (never blocks).
"""

from __future__ import annotations

import json
import os
import re
import sys
from datetime import UTC, datetime

FINDINGS_PATH = "/tmp/workspace/.security/findings.jsonl"
MAX_INPUT_BYTES = 10 * 1024 * 1024  # 10 MB

# --- Command pattern matchers ---

_SCAN_SECRETS_RE = re.compile(r"\bscan-secrets\b")
_GITLEAKS_RE = re.compile(r"\bgitleaks\s+detect\b")
_PRECOMMIT_RE = re.compile(r"\bpre-commit\s+run\b")
_GO_TEST_RE = re.compile(r"\bgo\s+test\b")
_MAKE_TEST_RE = re.compile(r"\bmake\s+(?:test|check)\b")
_NPM_TEST_RE = re.compile(r"\bnpm\s+test\b")
_PYTEST_RE = re.compile(r"\bpytest\b")
_GO_VET_RE = re.compile(r"\bgo\s+vet\b")
_GO_BUILD_RE = re.compile(r"\bgo\s+build\b")
_MAKE_LINT_RE = re.compile(r"\bmake\s+lint\b")
_GOLANGCI_RE = re.compile(r"\bgolangci-lint\s+run\b")
_ESLINT_RE = re.compile(r"\beslint\b")
_RUFF_RE = re.compile(r"\bruff\s+(?:check|format)\b")
_RUFF_FORMAT_RE = re.compile(r"\bruff\s+format\b")
_GITLINT_RE = re.compile(r"\bgitlint\b")

# pre-commit output patterns
_PRECOMMIT_HOOK_LINE_RE = re.compile(
    r"^(.+?)\.{3,}\s*(Passed|Failed|Skipped|Fixed)\s*$", re.MULTILINE
)
_PRECOMMIT_FIXING_RE = re.compile(r"^(?:Fixing|Fixed)\s+(.+?)\.?$", re.MULTILINE)
_PRECOMMIT_FILE_CHANGED_RE = re.compile(
    r"^(?:reformatted|would reformat|fixed)\s+(.+?)$",
    re.MULTILINE | re.IGNORECASE,
)

# go test output patterns
_GO_TEST_OK_RE = re.compile(r"^ok\s+\S+\s+([\d.]+)s", re.MULTILINE)
_GO_TEST_FAIL_RE = re.compile(r"^FAIL\s+", re.MULTILINE)

# pytest output patterns
_PYTEST_SUMMARY_RE = re.compile(r"=+\s+(\d+)\s+passed.*?in\s+([\d.]+)s\s+=+", re.MULTILINE)
_PYTEST_FAIL_RE = re.compile(r"=+\s+.*?(\d+)\s+failed", re.MULTILINE)


def log_suppression(command: str, summary: str) -> None:
    trace_id = os.environ.get("FULLSEND_TRACE_ID", "")
    finding = {
        "trace_id": trace_id,
        "timestamp": datetime.now(UTC).isoformat(),
        "phase": "hook_posttool",
        "scanner": "context_suppress_posttool",
        "name": "context_suppressed",
        "severity": "info",
        "detail": f"Suppressed output for: {command[:80]}",
        "summary": summary,
        "action": "suppress",
    }
    try:
        os.makedirs(os.path.dirname(FINDINGS_PATH), exist_ok=True)
        with open(FINDINGS_PATH, "a") as f:
            f.write(json.dumps(finding) + "\n")
    except OSError:
        pass


def suppress_scan_secrets(output: str) -> str | None:
    if not output.strip():
        return "scan-secrets: passed (no findings)"
    lower = output.lower()
    if "no leaks" in lower or "no secrets" in lower or "0 findings" in lower:
        return "scan-secrets: passed (no findings)"
    return None


def suppress_gitleaks(output: str) -> str | None:
    lower = output.lower()
    if not output.strip() or "no leaks" in lower:
        return "gitleaks: passed (no leaks detected)"
    return None


def suppress_precommit(output: str) -> str | None:
    hook_results = _PRECOMMIT_HOOK_LINE_RE.findall(output)
    if not hook_results:
        if not output.strip():
            return "pre-commit: passed"
        return None

    statuses = [status for _, status in hook_results]
    failed = [name.strip() for name, status in hook_results if status == "Failed"]
    passed_or_skipped = all(s in ("Passed", "Skipped") for s in statuses)

    if passed_or_skipped:
        return f"pre-commit: all {len(hook_results)} hooks passed"

    fixing_files = _PRECOMMIT_FIXING_RE.findall(output)
    reformatted = _PRECOMMIT_FILE_CHANGED_RE.findall(output)
    auto_fixed_files = sorted(set(fixing_files + reformatted))

    if auto_fixed_files and not failed:
        file_list = ", ".join(auto_fixed_files[:10])
        suffix = f" (+{len(auto_fixed_files) - 10} more)" if len(auto_fixed_files) > 10 else ""
        return (
            f"pre-commit: auto-fixed [{file_list}{suffix}]"
            " \u2014 re-stage modified files before commit"
        )

    # Mixed auto-fix + errors, or pure errors: pass through full output
    return None


def suppress_go_test(output: str) -> str | None:
    if _GO_TEST_FAIL_RE.search(output):
        return None

    ok_matches = _GO_TEST_OK_RE.findall(output)
    if not ok_matches:
        if not output.strip():
            return "tests: passed"
        return None

    total_time = sum(float(t) for t in ok_matches)
    pkg_count = len(ok_matches)
    return f"tests: {pkg_count} packages passed ({total_time:.1f}s)"


def suppress_pytest(output: str) -> str | None:
    if _PYTEST_FAIL_RE.search(output):
        return None

    match = _PYTEST_SUMMARY_RE.search(output)
    if match:
        passed = match.group(1)
        duration = match.group(2)
        return f"tests: {passed} passed ({duration}s)"

    if not output.strip():
        return "tests: passed"
    return None


def suppress_npm_test(output: str) -> str | None:
    lower = output.lower()
    if "fail" in lower or "error" in lower:
        return None
    if "passing" in lower or "tests passed" in lower or not output.strip():
        return "tests: passed"
    return None


def suppress_make_test(output: str) -> str | None:
    lower = output.lower()
    if "fail" in lower or "error" in lower:
        return None
    return "tests: passed"


def suppress_go_vet(output: str) -> str | None:
    if not output.strip():
        return "go vet: clean"
    return None


def suppress_go_build(output: str) -> str | None:
    if not output.strip():
        return "go build: clean"
    return None


def suppress_linter(name: str, output: str) -> str | None:
    if not output.strip():
        return f"{name}: clean"
    lower = output.lower()
    if "error" in lower or "fail" in lower or "warning" in lower:
        return None
    return f"{name}: passed"


def suppress_gitlint(output: str) -> str | None:
    if not output.strip():
        return "gitlint: passed"
    return None


def try_suppress(command: str, output: str) -> str | None:
    if _SCAN_SECRETS_RE.search(command):
        return suppress_scan_secrets(output)

    if _GITLEAKS_RE.search(command):
        return suppress_gitleaks(output)

    if _PRECOMMIT_RE.search(command):
        return suppress_precommit(output)

    if _GO_TEST_RE.search(command):
        return suppress_go_test(output)

    if _PYTEST_RE.search(command):
        return suppress_pytest(output)

    if _NPM_TEST_RE.search(command):
        return suppress_npm_test(output)

    if _MAKE_TEST_RE.search(command):
        return suppress_make_test(output)

    if _GO_VET_RE.search(command):
        return suppress_go_vet(output)

    if _GO_BUILD_RE.search(command):
        return suppress_go_build(output)

    if _GOLANGCI_RE.search(command):
        return suppress_linter("golangci-lint", output)

    if _ESLINT_RE.search(command):
        return suppress_linter("eslint", output)

    if _RUFF_FORMAT_RE.search(command):
        return suppress_linter("ruff-format", output)

    if _RUFF_RE.search(command):
        return suppress_linter("ruff", output)

    if _MAKE_LINT_RE.search(command):
        return suppress_linter("lint", output)

    if _GITLINT_RE.search(command):
        return suppress_gitlint(output)

    return None


def main() -> None:
    try:
        raw = sys.stdin.read(MAX_INPUT_BYTES + 1)
        if len(raw) > MAX_INPUT_BYTES:
            raw = raw[:MAX_INPUT_BYTES]
        if not raw.strip():
            sys.exit(0)
        hook_input = json.loads(raw)
    except (json.JSONDecodeError, Exception):
        sys.exit(0)

    tool_name = hook_input.get("tool_name", "")
    if tool_name != "Bash":
        sys.exit(0)

    tool_input = hook_input.get("tool_input", {})
    if isinstance(tool_input, str):
        try:
            tool_input = json.loads(tool_input)
        except (json.JSONDecodeError, Exception):
            sys.exit(0)

    command = tool_input.get("command", "")
    if not command:
        sys.exit(0)

    tool_result = hook_input.get("tool_result", "")
    if not isinstance(tool_result, str):
        sys.exit(0)

    # Non-zero exit code: always pass through full output for agent to act on.
    if tool_result.startswith("Exit code"):
        sys.exit(0)

    summary = try_suppress(command, tool_result)
    if summary is None:
        sys.exit(0)

    log_suppression(command, summary)
    json.dump({"tool_result": summary}, sys.stdout)
    sys.exit(0)


if __name__ == "__main__":
    main()
