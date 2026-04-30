#!/usr/bin/env python3
"""Tests for process-fix-result.py."""

import io
import json
import os
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(__file__))

from importlib.util import module_from_spec, spec_from_file_location

spec = spec_from_file_location(
    "process_fix_result",
    os.path.join(os.path.dirname(__file__), "process-fix-result.py"),
)
assert spec is not None and spec.loader is not None
mod = module_from_spec(spec)
spec.loader.exec_module(mod)

build_summary_body = mod.build_summary_body
main = mod.main


class TestBuildSummaryBody(unittest.TestCase):
    def test_basic_summary_with_fixes(self):
        data = {
            "summary": "Fixed 2 issues.",
            "trigger_source": "bot",
            "iteration": 3,
            "tests_passed": True,
            "actions": [
                {
                    "type": "fix",
                    "finding": "missing nil check",
                    "path": "pkg/handler.go",
                    "description": "Added nil check",
                },
                {
                    "type": "fix",
                    "finding": "unused import",
                    "path": "pkg/util.go",
                    "description": "Removed unused import",
                },
            ],
        }
        body = build_summary_body(data)
        self.assertIn("iteration 3", body)
        self.assertIn("bot-triggered", body)
        self.assertIn("Fixed 2 issues.", body)
        self.assertIn("Fixed (2)", body)
        self.assertIn("missing nil check", body)
        self.assertIn("`pkg/handler.go`", body)
        self.assertIn("**Tests:** passed", body)
        self.assertIn("fullsend", body)

    def test_disagreements(self):
        data = {
            "summary": "Disagreed with 1 finding.",
            "tests_passed": True,
            "actions": [
                {
                    "type": "disagree",
                    "finding": "refactor to strategy pattern",
                    "reason": "Out of scope for this PR",
                },
            ],
        }
        body = build_summary_body(data)
        self.assertIn("Disagreed (1)", body)
        self.assertIn("refactor to strategy pattern", body)
        self.assertIn("Out of scope", body)

    def test_failed_tests(self):
        data = {"summary": "Partial fix.", "tests_passed": False, "actions": []}
        body = build_summary_body(data)
        self.assertIn("**Tests:** **failed**", body)

    def test_decision_points(self):
        data = {
            "summary": "Done.",
            "tests_passed": True,
            "actions": [],
            "decision_points": [
                {
                    "description": "Chose approach A",
                    "alternatives": ["B", "C"],
                    "rationale": "Simpler",
                }
            ],
        }
        body = build_summary_body(data)
        self.assertIn("Decision points", body)
        self.assertIn("Chose approach A", body)
        self.assertIn("B, C", body)
        self.assertIn("Simpler", body)

    def test_strategy_change_rendered(self):
        data = {
            "summary": "Done.",
            "tests_passed": True,
            "actions": [{"type": "fix", "finding": "bug", "description": "Fixed"}],
            "strategy_change": "Switched from inline fix to extract-method approach",
        }
        body = build_summary_body(data)
        self.assertIn("Strategy change:", body)
        self.assertIn("extract-method approach", body)

    def test_strategy_change_omitted_when_empty(self):
        data = {"summary": "Done.", "tests_passed": True, "actions": []}
        body = build_summary_body(data)
        self.assertNotIn("Strategy change:", body)

    def test_no_decision_points(self):
        data = {"summary": "Done.", "tests_passed": True, "actions": []}
        body = build_summary_body(data)
        self.assertNotIn("Decision points", body)

    def test_defaults_for_missing_fields(self):
        data = {}
        body = build_summary_body(data)
        self.assertIn("Fix agent completed.", body)
        self.assertIn("unknown-triggered", body)
        self.assertIn("iteration 1", body)

    def test_mixed_fix_and_disagree(self):
        data = {
            "summary": "Addressed 2 of 3.",
            "trigger_source": "bot",
            "iteration": 1,
            "tests_passed": True,
            "actions": [
                {"type": "fix", "finding": "bug A", "description": "Fixed A"},
                {"type": "fix", "finding": "bug B", "description": "Fixed B"},
                {"type": "disagree", "finding": "refactor C", "reason": "Out of scope"},
            ],
        }
        body = build_summary_body(data)
        self.assertIn("Fixed (2)", body)
        self.assertIn("Disagreed (1)", body)

    def test_no_actions(self):
        data = {
            "summary": "Nothing to fix.",
            "tests_passed": True,
            "actions": [],
        }
        body = build_summary_body(data)
        self.assertNotIn("Fixed (", body)
        self.assertNotIn("Disagreed (", body)

    def test_fix_without_path(self):
        data = {
            "summary": "Fixed.",
            "tests_passed": True,
            "actions": [
                {"type": "fix", "finding": "typo in docs", "description": "Fixed typo"},
            ],
        }
        body = build_summary_body(data)
        self.assertIn("**typo in docs**:", body)
        self.assertNotIn("(`", body)


post_summary = mod.post_summary
MAX_COMMENT_LENGTH = mod.MAX_COMMENT_LENGTH


class TestCommentTruncation(unittest.TestCase):
    def test_long_body_truncated(self):
        data = {
            "summary": "x" * 40000,
            "tests_passed": True,
            "actions": [],
        }
        body = build_summary_body(data)
        captured = io.StringIO()
        sys.stdout = captured
        post_summary("org/repo", "1", body, dry_run=True)
        sys.stdout = sys.__stdout__
        output = captured.getvalue()
        self.assertIn("[dry-run]", output)

    def test_short_body_not_truncated(self):
        data = {
            "summary": "Short.",
            "tests_passed": True,
            "actions": [],
        }
        body = build_summary_body(data)
        self.assertLess(len(body), MAX_COMMENT_LENGTH)


class TestUnknownActionType(unittest.TestCase):
    def test_unknown_type_logged(self):
        data = {
            "pr_number": 1,
            "trigger_source": "bot",
            "actions": [
                {"type": "exfiltrate", "finding": "sneaky"},
                {"type": "fix", "finding": "real fix", "description": "Fixed"},
            ],
            "summary": "Done.",
            "tests_passed": True,
            "files_changed": [],
        }
        with tempfile.NamedTemporaryFile(mode="w", suffix=".json", delete=False) as f:
            json.dump(data, f)
            f.flush()
            try:
                captured = io.StringIO()
                sys.stderr = captured
                result = main([f.name, "org/repo", "1", "--dry-run"])
                sys.stderr = sys.__stderr__
                self.assertEqual(result, 0)
                self.assertIn("Unknown action type 'exfiltrate'", captured.getvalue())
            finally:
                os.unlink(f.name)


class TestPostSummaryFailure(unittest.TestCase):
    def test_returns_2_when_comment_post_fails(self):
        data = {
            "pr_number": 42,
            "trigger_source": "bot",
            "actions": [
                {"type": "fix", "finding": "nil check", "description": "Fixed"},
            ],
            "summary": "All good.",
            "tests_passed": True,
            "files_changed": ["foo.go"],
        }
        with tempfile.NamedTemporaryFile(mode="w", suffix=".json", delete=False) as f:
            json.dump(data, f)
            f.flush()
            try:
                with patch(
                    "subprocess.run",
                    side_effect=subprocess.CalledProcessError(1, "gh", stderr="API error"),
                ):
                    result = main([f.name, "org/repo", "42"])
                self.assertEqual(result, 2)
            finally:
                os.unlink(f.name)


class TestMain(unittest.TestCase):
    def test_missing_args(self):
        self.assertEqual(main([]), 1)
        self.assertEqual(main(["file.json"]), 1)

    def test_nonexistent_file(self):
        self.assertEqual(main(["/nonexistent.json", "org/repo", "42"]), 1)

    def test_invalid_json(self):
        with tempfile.NamedTemporaryFile(mode="w", suffix=".json", delete=False) as f:
            f.write("not json")
            f.flush()
            try:
                self.assertEqual(main([f.name, "org/repo", "42"]), 1)
            finally:
                os.unlink(f.name)

    def test_valid_dry_run(self):
        data = {
            "pr_number": 42,
            "trigger_source": "bot",
            "actions": [
                {"type": "fix", "finding": "nil check", "description": "Fixed"},
            ],
            "summary": "All good.",
            "tests_passed": True,
            "files_changed": ["foo.go"],
        }
        with tempfile.NamedTemporaryFile(mode="w", suffix=".json", delete=False) as f:
            json.dump(data, f)
            f.flush()
            try:
                self.assertEqual(main([f.name, "org/repo", "42", "--dry-run"]), 0)
            finally:
                os.unlink(f.name)

    def test_empty_actions_dry_run(self):
        data = {
            "pr_number": 10,
            "trigger_source": "human",
            "actions": [],
            "summary": "Nothing to do.",
            "tests_passed": True,
            "files_changed": [],
        }
        with tempfile.NamedTemporaryFile(mode="w", suffix=".json", delete=False) as f:
            json.dump(data, f)
            f.flush()
            try:
                self.assertEqual(main([f.name, "org/repo", "10", "--dry-run"]), 0)
            finally:
                os.unlink(f.name)


if __name__ == "__main__":
    unittest.main()
