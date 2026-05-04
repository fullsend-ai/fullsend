"""Tests for context_suppress_posttool.py hook."""

from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path

HOOK_SCRIPT = str(Path(__file__).parent / "context_suppress_posttool.py")


def run_hook(hook_input: dict) -> dict | None:
    """Run the hook script with the given input and return parsed output or None."""
    result = subprocess.run(
        [sys.executable, HOOK_SCRIPT],
        input=json.dumps(hook_input),
        capture_output=True,
        text=True,
        timeout=10,
    )
    assert result.returncode == 0, f"Hook exited non-zero: {result.stderr}"
    if not result.stdout.strip():
        return None
    return json.loads(result.stdout)


def make_input(command: str, tool_result: str) -> dict:
    return {
        "tool_name": "Bash",
        "tool_input": {"command": command},
        "tool_result": tool_result,
    }


# --- scan-secrets ---


class TestScanSecrets:
    def test_no_findings(self):
        out = run_hook(make_input("scan-secrets foo.go bar.go", "No leaks found\n"))
        assert out is not None
        assert out["tool_result"] == "scan-secrets: passed (no findings)"

    def test_empty_output_passthrough(self):
        out = run_hook(make_input("scan-secrets foo.go", ""))
        assert out is None  # empty output → passthrough (scanner may have crashed)

    def test_failure_passthrough(self):
        out = run_hook(
            make_input(
                "scan-secrets foo.go",
                "Exit code 1\nSecret detected in foo.go:12\n",
            )
        )
        assert out is None  # exit code prefix → passthrough


# --- gitleaks ---


class TestGitleaks:
    def test_no_leaks(self):
        out = run_hook(make_input("gitleaks detect --source .", "no leaks found\n"))
        assert out is not None
        assert "passed" in out["tool_result"]

    def test_empty_output_passthrough(self):
        out = run_hook(make_input("gitleaks detect --source .", ""))
        assert out is None  # empty output → passthrough (scanner may have crashed)


# --- pre-commit ---


class TestPreCommit:
    def test_all_passed(self):
        output = (
            "check yaml...............Passed\n"
            "end-of-file-fixer........Passed\n"
            "trailing-whitespace......Passed\n"
            "detect-private-key.......Passed\n"
            "gitleaks.................Passed\n"
        )
        out = run_hook(make_input("pre-commit run --files foo.go", output))
        assert out is not None
        assert "all 5 hooks passed" in out["tool_result"]

    def test_all_passed_with_skipped(self):
        output = (
            "check yaml...............Passed\n"
            "hadolint-docker..........Skipped\n"
            "trailing-whitespace......Passed\n"
        )
        out = run_hook(make_input("pre-commit run --files foo.go", output))
        assert out is not None
        assert "all 3 hooks passed" in out["tool_result"]

    def test_auto_fix_only(self):
        output = (
            "check yaml...............Passed\n"
            "end-of-file-fixer........Fixed\n"
            "Fixing foo.go\n"
            "trailing-whitespace......Fixed\n"
            "Fixing bar.go\n"
            "detect-private-key.......Passed\n"
        )
        out = run_hook(make_input("pre-commit run --files foo.go bar.go", output))
        assert out is not None
        assert "auto-fixed" in out["tool_result"]
        assert "bar.go" in out["tool_result"]
        assert "foo.go" in out["tool_result"]
        assert "re-stage" in out["tool_result"]

    def test_real_errors(self):
        output = (
            "check yaml...............Passed\n"
            "golangci-lint............Failed\n"
            "foo.go:12:5: unused variable\n"
        )
        out = run_hook(make_input("pre-commit run --files foo.go", output))
        assert out is None  # errors → passthrough

    def test_mixed_autofix_and_errors(self):
        output = (
            "end-of-file-fixer........Fixed\n"
            "Fixing foo.go\n"
            "golangci-lint............Failed\n"
            "bar.go:5:1: syntax error\n"
        )
        out = run_hook(make_input("pre-commit run --files foo.go bar.go", output))
        assert out is None  # mixed → passthrough

    def test_empty_output(self):
        out = run_hook(make_input("pre-commit run --files foo.go", ""))
        assert out is not None
        assert "passed" in out["tool_result"]

    def test_failure_exit_code(self):
        out = run_hook(
            make_input(
                "pre-commit run --files foo.go",
                "Exit code 1\ngolangci-lint............Failed\nerror details\n",
            )
        )
        assert out is None  # exit code prefix → passthrough


# --- go test ---


class TestGoTest:
    def test_all_pass(self):
        output = (
            "ok  \tgithub.com/org/repo/internal/foo\t0.123s\n"
            "ok  \tgithub.com/org/repo/internal/bar\t1.456s\n"
            "ok  \tgithub.com/org/repo/internal/baz\t0.789s\n"
        )
        out = run_hook(make_input("go test ./internal/...", output))
        assert out is not None
        assert "3 packages passed" in out["tool_result"]
        assert "2.4s" in out["tool_result"]

    def test_failure(self):
        output = (
            "ok  \tgithub.com/org/repo/internal/foo\t0.123s\n"
            "FAIL\tgithub.com/org/repo/internal/bar\t0.456s\n"
        )
        out = run_hook(make_input("go test ./...", output))
        assert out is None  # FAIL line → passthrough

    def test_empty_output_passthrough(self):
        out = run_hook(make_input("go test ./...", ""))
        assert out is None  # empty output → passthrough (runner may have crashed)


# --- pytest ---


class TestPytest:
    def test_all_pass(self):
        output = (
            "tests/test_foo.py ...\n"
            "tests/test_bar.py ....\n"
            "================ 7 passed in 1.23s ================\n"
        )
        out = run_hook(make_input("pytest tests/", output))
        assert out is not None
        assert "7 passed" in out["tool_result"]
        assert "1.23s" in out["tool_result"]

    def test_failure(self):
        output = (
            "tests/test_foo.py .F.\n================ 2 passed, 1 failed in 0.5s ================\n"
        )
        out = run_hook(make_input("pytest", output))
        assert out is None  # failure → passthrough

    def test_empty_output_passthrough(self):
        out = run_hook(make_input("pytest tests/", ""))
        assert out is None  # empty output → passthrough (runner may have crashed)


# --- npm test ---


class TestNpmTest:
    def test_pass(self):
        out = run_hook(make_input("npm test", "  42 passing (3s)\n"))
        assert out is not None
        assert "passed" in out["tool_result"]

    def test_failure(self):
        out = run_hook(make_input("npm test", "  1 failing\n  Error: expected 1 to equal 2\n"))
        assert out is None

    def test_error_in_test_name_still_passes(self):
        output = "  should render error-boundary component\n  42 passing (3s)\n"
        out = run_hook(make_input("npm test", output))
        assert out is not None
        assert "passed" in out["tool_result"]


# --- make test ---


class TestMakeTest:
    def test_pass(self):
        output = "go test ./...\nok all tests\n"
        out = run_hook(make_input("make test", output))
        assert out is not None
        assert "passed" in out["tool_result"]

    def test_failure(self):
        output = "go test ./...\nFAIL error in tests\n"
        out = run_hook(make_input("make test", output))
        assert out is None

    def test_ok_word_boundary(self):
        output = "checking token validation\nlookup table ready\n"
        out = run_hook(make_input("make test", output))
        assert out is None  # "token"/"lookup" contain "ok" but are not the word "ok"

    def test_pass_word_boundary(self):
        output = "bypass check completed\npassword hashing verified\n"
        out = run_hook(make_input("make test", output))
        assert out is None  # "bypass"/"password" contain "pass" but are not the word "pass"


# --- go vet / go build ---


class TestGoVetBuild:
    def test_go_vet_clean(self):
        out = run_hook(make_input("go vet ./...", ""))
        assert out is not None
        assert out["tool_result"] == "go vet: clean"

    def test_go_vet_with_errors(self):
        out = run_hook(make_input("go vet ./...", "foo.go:12: unreachable code\n"))
        assert out is None

    def test_go_build_clean(self):
        out = run_hook(make_input("go build ./...", ""))
        assert out is not None
        assert out["tool_result"] == "go build: clean"

    def test_go_build_with_errors(self):
        out = run_hook(make_input("go build ./...", "foo.go:5:3: undefined: bar\n"))
        assert out is None


# --- linters ---


class TestLinters:
    def test_golangci_lint_clean(self):
        out = run_hook(make_input("golangci-lint run ./...", ""))
        assert out is not None
        assert "golangci-lint: clean" in out["tool_result"]

    def test_golangci_lint_errors(self):
        out = run_hook(make_input("golangci-lint run", "foo.go:5: error: unused\n"))
        assert out is None

    def test_eslint_clean(self):
        out = run_hook(make_input("eslint src/", ""))
        assert out is not None
        assert "eslint: clean" in out["tool_result"]

    def test_ruff_check_clean(self):
        out = run_hook(make_input("ruff check .", ""))
        assert out is not None
        assert "ruff: clean" in out["tool_result"]

    def test_ruff_format_clean(self):
        out = run_hook(make_input("ruff format --check .", ""))
        assert out is not None
        assert "ruff-format: clean" in out["tool_result"]

    def test_make_lint_clean(self):
        out = run_hook(make_input("make lint", ""))
        assert out is not None
        assert "lint: clean" in out["tool_result"]

    def test_make_lint_nonempty_passthrough(self):
        out = run_hook(make_input("make lint", "all checks passed\n"))
        assert out is None

    def test_make_lint_failure(self):
        out = run_hook(make_input("make lint", "golangci-lint: error in foo.go\n"))
        assert out is None


# --- gitlint ---


class TestGitlint:
    def test_pass(self):
        out = run_hook(make_input("gitlint --commit HEAD", ""))
        assert out is not None
        assert out["tool_result"] == "gitlint: passed"

    def test_failure(self):
        out = run_hook(make_input("gitlint --commit HEAD", "1: T1 Title exceeds max length\n"))
        assert out is None


# --- passthrough cases ---


class TestPassthrough:
    def test_non_bash_tool(self):
        hook_input = {
            "tool_name": "Read",
            "tool_input": {"file_path": "/tmp/foo.go"},
            "tool_result": "package main\n",
        }
        out = run_hook(hook_input)
        assert out is None

    def test_non_verification_command(self):
        out = run_hook(make_input("git diff --stat", "foo.go | 5 ++---\n"))
        assert out is None

    def test_cat_command(self):
        out = run_hook(make_input("cat foo.go", "package main\n"))
        assert out is None

    def test_ls_command(self):
        out = run_hook(make_input("ls -la", "total 42\ndrwxr-xr-x ...\n"))
        assert out is None

    def test_empty_input(self):
        result = subprocess.run(
            [sys.executable, HOOK_SCRIPT],
            input="",
            capture_output=True,
            text=True,
            timeout=10,
        )
        assert result.returncode == 0
        assert result.stdout.strip() == ""

    def test_invalid_json(self):
        result = subprocess.run(
            [sys.executable, HOOK_SCRIPT],
            input="not json",
            capture_output=True,
            text=True,
            timeout=10,
        )
        assert result.returncode == 0
        assert result.stdout.strip() == ""

    def test_exit_code_prefix_always_passthrough(self):
        out = run_hook(make_input("go test ./...", "Exit code 2\nFAIL something\n"))
        assert out is None
