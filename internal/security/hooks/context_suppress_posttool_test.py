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


def make_input(command: str, tool_result: str, *, key: str = "tool_result") -> dict:
    return {
        "tool_name": "Bash",
        "tool_input": {"command": command},
        key: tool_result,
    }


# --- scan-secrets ---


class TestScanSecrets:
    def test_no_findings(self):
        out = run_hook(make_input("scan-secrets foo.go bar.go", "No leaks found\n"))
        assert out is not None
        assert out["tool_result"] == "scan-secrets: passed (no findings)"
        assert out["hookSpecificOutput"]["updatedToolOutput"] == out["tool_result"]

    def test_tool_response_payload(self):
        out = run_hook(
            make_input("scan-secrets foo.go bar.go", "No leaks found\n", key="tool_response")
        )
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

    def test_empty_output_is_not_a_pass(self):
        # A hook whose interpreter is missing from PATH prints nothing, and
        # Claude Code's Bash result carries no exit code: silence is not proof.
        out = run_hook(make_input("pre-commit run --files foo.go", ""))
        assert out is None

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


# --- silence is not evidence ---


class TestSilenceIsNotEvidence:
    """Tools whose clean run prints nothing are never condensed: an empty
    result already costs no context, and a silent failure (missing
    interpreter, wrong PATH) would otherwise be reported as clean."""

    def test_go_vet_and_build_pass_through(self):
        assert run_hook(make_input("go vet ./...", "")) is None
        assert run_hook(make_input("go build ./...", "")) is None
        assert run_hook(make_input("go vet ./...", "foo.go:12: unreachable code\n")) is None

    def test_linters_pass_through(self):
        for cmd in (
            "golangci-lint run ./...",
            "eslint src/",
            "ruff check .",
            "ruff format --check .",
            "make lint",
        ):
            assert run_hook(make_input(cmd, "")) is None, cmd
        assert run_hook(make_input("make lint", "all checks passed\n")) is None

    def test_gitlint_passes_through(self):
        assert run_hook(make_input("gitlint --commit HEAD", "")) is None
        assert (
            run_hook(make_input("gitlint --commit HEAD", "1: T1 Title exceeds max length\n"))
            is None
        )


# --- mention is not invocation ---


class TestMentionIsNotInvocation:
    def test_grep_for_a_tool_name_keeps_its_hits(self):
        out = run_hook(make_input("grep -n scan-secrets hooks.py", "12: scan-secrets\n"))
        assert out is None

    def test_runner_prefixes_still_dispatch(self):
        import context_suppress_posttool as cs

        assert cs.select_summarizer("uvx pytest -q") is cs.suppress_pytest
        assert cs.select_summarizer("python3 -m pytest") is cs.suppress_pytest
        assert cs.select_summarizer("./bin/go test ./...") is cs.suppress_go_test
        assert cs.select_summarizer("pnpm run test") is cs.suppress_npm_test
        assert cs.select_summarizer("echo go test ./...") is None
        assert cs.select_summarizer("cat go-test.log") is None


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

    def test_interrupted_bash_object_passthrough(self):
        out = run_hook(
            {
                "tool_name": "Bash",
                "tool_input": {"command": "go test ./..."},
                "tool_response": {
                    "stdout": "ok\tgithub.com/fullsend-ai/fullsend\t0.1s",
                    "stderr": "",
                    "interrupted": True,
                    "isImage": False,
                },
            }
        )
        assert out is None

    def test_bash_object_success_clears_stderr(self):
        out = run_hook(
            {
                "tool_name": "Bash",
                "tool_input": {"command": "go test ./..."},
                "tool_response": {
                    "stdout": "ok\tgithub.com/fullsend-ai/fullsend\t0.12s\n",
                    "stderr": "warning: verbose compiler noise\n",
                    "interrupted": False,
                    "isImage": False,
                },
            }
        )
        assert out is not None
        updated = out["hookSpecificOutput"]["updatedToolOutput"]
        assert "packages passed" in updated["stdout"]
        assert updated["stderr"] == ""
        assert updated["interrupted"] is False


# --- command shapes ---


class TestCommandShapes:
    """One summary can only speak for one verification command."""

    GO_OK = "ok  \tgithub.com/org/repo/internal/foo\t0.5s\n"

    def test_two_suites_not_condensed(self):
        out = run_hook(
            make_input("uvx pytest -q; go test ./...", "3 failed, 2 passed in 1.2s\n" + self.GO_OK)
        )
        assert out is None

    def test_setup_prefix_still_condensed(self):
        out = run_hook(make_input("cd /r && GOFLAGS=-mod=mod go test ./... 2>&1", self.GO_OK))
        assert out is not None
        assert "packages passed" in out["tool_result"]

    def test_pipeline_not_condensed(self):
        assert run_hook(make_input("go test ./... | tail -5", self.GO_OK)) is None

    def test_exit_status_echo_not_condensed(self):
        assert run_hook(make_input("go test ./...; echo EXIT=$?", self.GO_OK + "EXIT=0\n")) is None

    def test_substitution_not_condensed(self):
        assert run_hook(make_input("go test $(go list ./...)", self.GO_OK)) is None

    def test_failure_count_never_condensed(self):
        out = run_hook(make_input("go test ./...", self.GO_OK + "3 failed in 1s\n"))
        assert out is None

    def test_panic_never_condensed(self):
        out = run_hook(make_input("go test ./...", "panic: boom\n" + self.GO_OK))
        assert out is None

    def test_pytest_quiet_summary(self):
        out = run_hook(make_input("pytest -q", "....\n4 passed in 0.31s\n"))
        assert out is not None
        assert out["tool_result"] == "tests: 4 passed (0.31s)"

    def test_select_summarizer(self):
        import context_suppress_posttool as cs

        assert cs.select_summarizer("cd x && go test ./...") is cs.suppress_go_test
        assert cs.select_summarizer("export A=1; go test ./...") is cs.suppress_go_test
        assert cs.select_summarizer("pytest; go test ./...") is None
        assert cs.select_summarizer("go test ./... || true") is None
        assert cs.select_summarizer("ls") is None


class TestPytestQuietFailure:
    def test_summarizer_itself_refuses_quiet_failure(self):
        import context_suppress_posttool as cs

        assert cs.suppress_pytest("3 failed, 2 passed in 1.2s\n") is None
        assert cs.suppress_pytest("2 passed in 1.2s\n") == "tests: 2 passed (1.2s)"


class TestCommandShapesRoundTwo:
    GO_OK = "ok  \tgithub.com/org/repo/internal/foo\t0.5s\n"

    def test_quoted_pipe_is_not_a_pipeline(self):
        out = run_hook(make_input("go test ./... -run 'TestA|TestB'", self.GO_OK))
        assert out is not None

    def test_comment_and_continuation(self):
        assert run_hook(make_input("# run the suite\ngo test ./...", self.GO_OK)) is not None
        assert run_hook(make_input("go test \\\n  ./...", self.GO_OK)) is not None

    def test_two_tools_still_not_condensed(self):
        assert run_hook(make_input("go test ./... && go vet ./...", self.GO_OK)) is None

    def test_pytest_summary_echoes_all_counts(self):
        out = run_hook(make_input("pytest -q", "2 passed, 1 xfailed in 0.3s\n"))
        assert out is not None
        assert out["tool_result"] == "tests: 2 passed, 1 xfailed (0.3s)"


class TestQuotedRegions:
    GO_OK = "ok  \tgithub.com/org/repo/internal/foo\t0.5s\n"

    def test_escaped_quote_does_not_swallow_a_pipe(self):
        assert run_hook(make_input('go test ./... -run "A\\\\" | tee "log"', self.GO_OK)) is None

    def test_double_quoted_pipe_is_not_a_pipeline(self):
        assert run_hook(make_input('go test ./... -run "A|B"', self.GO_OK)) is not None

    def test_lowercase_error_line_not_condensed(self):
        assert (
            run_hook(make_input("pytest -q", "5 passed in 0.1s\nerror: something broke\n")) is None
        )
