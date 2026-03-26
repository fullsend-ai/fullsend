#!/usr/bin/env python3
# /// script
# requires-python = ">=3.12"
# dependencies = ["pyyaml>=6.0"]
# ///
"""Tests for the skill evaluation framework."""

from __future__ import annotations

import io
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import yaml
from run import (
    CaseResult,
    EvalCase,
    MutationResult,
    build_variants,
    discover_skills,
    grade_output,
    load_evals,
    model_for_agent,
    parse_yaml_from_output,
    print_report,
    run_agent,
    save_grading_yaml,
)

# --- parse_yaml_from_output ---


class TestParseYamlFromOutput(unittest.TestCase):
    def test_plain_yaml(self) -> None:
        text = 'pass: true\nevidence: "it works"'
        result = parse_yaml_from_output(text)
        assert result is not None
        assert result["pass"] is True
        assert result["evidence"] == "it works"

    def test_yaml_in_markdown_fences(self) -> None:
        text = '```yaml\npass: false\nevidence: "missing"\n```'
        result = parse_yaml_from_output(text)
        assert result is not None
        assert result["pass"] is False

    def test_yaml_with_preamble(self) -> None:
        text = 'Here is my evaluation:\n\npass: true\nevidence: "looks good"'
        result = parse_yaml_from_output(text)
        assert result is not None
        assert result["pass"] is True

    def test_prompt_yaml_with_preamble(self) -> None:
        text = 'Here is the variant:\n\nprompt: "new prompt"\nexpected: "new expected"'
        result = parse_yaml_from_output(text)
        assert result is not None
        assert result["prompt"] == "new prompt"
        assert result["expected"] == "new expected"

    def test_returns_none_for_garbage(self) -> None:
        assert parse_yaml_from_output("this is not yaml at all") is None

    def test_returns_none_for_non_dict(self) -> None:
        assert parse_yaml_from_output("- item1\n- item2") is None

    def test_strips_surrounding_whitespace(self) -> None:
        text = '  \n\npass: true\nevidence: "ok"\n\n  '
        result = parse_yaml_from_output(text)
        assert result is not None
        assert result["pass"] is True

    def test_fenced_block_with_language_tag(self) -> None:
        text = '```yml\nprompt: "hi"\nexpected: "bye"\n```'
        result = parse_yaml_from_output(text)
        assert result is not None
        assert result["prompt"] == "hi"

    def test_bare_fences_no_closing(self) -> None:
        text = '```\npass: true\nevidence: "no close"'
        result = parse_yaml_from_output(text)
        assert result is not None
        assert result["pass"] is True


# --- CaseResult ---


class TestCaseResult(unittest.TestCase):
    def _make_result(self, pass_values: list[bool | None]) -> CaseResult:
        cr = CaseResult(eval_id="test", agent="claude", configuration="with_skill")
        for i, v in enumerate(pass_values):
            cr.results.append(MutationResult(i, "p", "e", v, "ev"))
        return cr

    def test_all_pass(self) -> None:
        cr = self._make_result([True, True, True])
        assert cr.total == 3
        assert cr.passed == 3
        assert cr.failed == 0
        assert cr.skipped == 0
        assert cr.pass_rate == 1.0

    def test_all_fail(self) -> None:
        cr = self._make_result([False, False])
        assert cr.total == 2
        assert cr.passed == 0
        assert cr.pass_rate == 0.0

    def test_mixed(self) -> None:
        cr = self._make_result([True, False, True, None])
        assert cr.total == 3
        assert cr.passed == 2
        assert cr.failed == 1
        assert cr.skipped == 1
        assert abs(cr.pass_rate - 2 / 3) < 0.001

    def test_all_skipped(self) -> None:
        cr = self._make_result([None, None])
        assert cr.total == 0
        assert cr.pass_rate == 0.0

    def test_empty(self) -> None:
        cr = CaseResult(eval_id="test", agent="claude", configuration="with_skill")
        assert cr.total == 0
        assert cr.pass_rate == 0.0


# --- load_evals ---


class TestLoadEvals(unittest.TestCase):
    def test_loads_filing_issues_evals(self) -> None:
        cases, threshold, models = load_evals("filing-issues")
        assert threshold == 0.9
        assert len(cases) == 6
        ids = [c.id for c in cases]
        assert "asks-clarifying-questions" in ids
        assert "searches-for-duplicates" in ids
        assert "no-solution-in-body" in ids
        assert "user-approval-gate" in ids
        assert "label-discovery" in ids
        assert "identifies-target-repo" in ids

    def test_case_fields(self) -> None:
        cases, _, _models = load_evals("filing-issues")
        case = next(c for c in cases if c.id == "asks-clarifying-questions")
        assert case.title != ""
        assert case.prompt != ""
        assert case.expected != ""
        assert case.mutations == 5
        assert case.threshold == 0.9

    def test_models_loaded(self) -> None:
        _cases, _threshold, models = load_evals("filing-issues")
        assert models.mutation == "google-vertex-anthropic/claude-haiku-4-5@default"
        assert models.runner == "google-vertex-anthropic/claude-haiku-4-5@default"
        assert models.judge == "google-vertex-anthropic/claude-haiku-4-5@default"

    def test_nonexistent_skill_exits(self) -> None:
        with self.assertRaises(SystemExit):
            load_evals("nonexistent-skill")


# --- discover_skills ---


class TestDiscoverSkills(unittest.TestCase):
    def test_discovers_filing_issues(self) -> None:
        skills = discover_skills()
        assert "filing-issues" in skills

    def test_filter_valid_skill(self) -> None:
        skills = discover_skills("filing-issues")
        assert skills == ["filing-issues"]

    def test_filter_invalid_skill_exits(self) -> None:
        with self.assertRaises(SystemExit):
            discover_skills("no-such-skill")


# --- save_grading_yaml ---


class TestSaveGradingYaml(unittest.TestCase):
    def test_writes_valid_yaml(self) -> None:
        cr = CaseResult(eval_id="test-case", agent="claude", configuration="with_skill")
        cr.results.append(MutationResult(0, "prompt", "expected", True, "good"))
        cr.results.append(MutationResult(1, "prompt2", "expected2", False, "bad"))

        with tempfile.TemporaryDirectory() as tmpdir:
            output_dir = Path(tmpdir)
            save_grading_yaml(cr, 0.9, output_dir)

            outfile = output_dir / "test-case_claude_with_skill.yaml"
            assert outfile.exists()

            with open(outfile) as f:
                data = yaml.safe_load(f)

            assert data["eval_id"] == "test-case"
            assert data["agent"] == "claude"
            assert data["configuration"] == "with_skill"
            assert len(data["mutations"]) == 2
            assert data["mutations"][0]["passed"] is True
            assert data["mutations"][1]["passed"] is False
            assert data["summary"]["total"] == 2
            assert data["summary"]["passed"] == 1
            assert data["summary"]["failed"] == 1
            assert data["summary"]["skipped"] == 0
            assert data["summary"]["pass_rate"] == 0.5
            assert data["summary"]["threshold"] == 0.9
            assert data["summary"]["met_threshold"] is False


# --- print_report ---


class TestPrintReport(unittest.TestCase):
    def _make_case_result(
        self, eval_id: str, agent: str, config: str, pass_values: list[bool | None]
    ) -> CaseResult:
        cr = CaseResult(eval_id=eval_id, agent=agent, configuration=config)
        for i, v in enumerate(pass_values):
            cr.results.append(MutationResult(i, "p", "e", v, "ev"))
        return cr

    def test_all_pass_returns_true(self) -> None:
        cases = [EvalCase("c1", "Case 1", "p", "e", 0, 0.8)]
        results: dict[str, dict[str, dict[str, CaseResult]]] = {
            "c1": {
                "claude": {
                    "with_skill": self._make_case_result("c1", "claude", "with_skill", [True] * 5),
                    "without_skill": self._make_case_result(
                        "c1", "claude", "without_skill", [True, False]
                    ),
                },
            },
        }
        with patch("sys.stdout", new_callable=io.StringIO) as mock_out:
            ok = print_report("test-skill", cases, results, ["claude"])
        assert ok is True
        output = mock_out.getvalue()
        assert "PASS" in output
        assert "PASSED" in output

    def test_below_threshold_returns_false(self) -> None:
        cases = [EvalCase("c1", "Case 1", "p", "e", 0, 0.9)]
        results: dict[str, dict[str, dict[str, CaseResult]]] = {
            "c1": {
                "claude": {
                    "with_skill": self._make_case_result(
                        "c1", "claude", "with_skill", [True, False, False]
                    ),
                    "without_skill": self._make_case_result(
                        "c1", "claude", "without_skill", [False, False]
                    ),
                },
            },
        }
        with patch("sys.stdout", new_callable=io.StringIO) as mock_out:
            ok = print_report("test-skill", cases, results, ["claude"])
        assert ok is False
        output = mock_out.getvalue()
        assert "FAIL" in output
        assert "FAILED" in output

    def test_skipped_agent_still_passes(self) -> None:
        cases = [EvalCase("c1", "Case 1", "p", "e", 0, 0.9)]
        results: dict[str, dict[str, dict[str, CaseResult]]] = {
            "c1": {
                "claude": {
                    "with_skill": self._make_case_result("c1", "claude", "with_skill", [True] * 6),
                    "without_skill": self._make_case_result(
                        "c1", "claude", "without_skill", [False] * 6
                    ),
                },
            },
        }
        with patch("sys.stdout", new_callable=io.StringIO):
            ok = print_report("test-skill", cases, results, ["claude", "opencode"])
        assert ok is True

    def test_delta_shown_in_output(self) -> None:
        cases = [EvalCase("c1", "Case 1", "p", "e", 0, 0.5)]
        results: dict[str, dict[str, dict[str, CaseResult]]] = {
            "c1": {
                "claude": {
                    "with_skill": self._make_case_result(
                        "c1", "claude", "with_skill", [True, True, False]
                    ),
                    "without_skill": self._make_case_result(
                        "c1", "claude", "without_skill", [True, False, False]
                    ),
                },
            },
        }
        with patch("sys.stdout", new_callable=io.StringIO) as mock_out:
            print_report("test-skill", cases, results, ["claude"])
        output = mock_out.getvalue()
        assert "delta:" in output


# --- run_agent ---


def _mock_popen(stdout: str = "output") -> object:
    """Create a mock Popen instance that returns stdout from communicate()."""
    from unittest.mock import MagicMock

    mock_proc = MagicMock()
    mock_proc.communicate.return_value = (stdout, "")
    mock_proc.kill = MagicMock()
    mock_proc.wait = MagicMock()
    return mock_proc


class TestRunAgent(unittest.TestCase):
    @patch("run.shutil.which", return_value=None)
    def test_claude_not_installed(self, _mock_which: object) -> None:
        assert run_agent("claude", "hello") is None

    @patch("run.shutil.which", return_value=None)
    def test_opencode_not_installed(self, _mock_which: object) -> None:
        assert run_agent("opencode", "hello") is None

    def test_unknown_agent(self) -> None:
        assert run_agent("unknown-agent", "hello") is None

    @patch("run.subprocess.Popen")
    @patch("run.shutil.which", return_value="/usr/bin/claude")
    def test_claude_invocation(self, _mock_which: object, mock_popen: object) -> None:
        from unittest.mock import MagicMock

        assert isinstance(mock_popen, MagicMock)
        mock_popen.return_value = _mock_popen("agent output")

        output = run_agent("claude", "test prompt")
        assert output == "agent output"
        mock_popen.assert_called_once()
        call_args = mock_popen.call_args
        cmd = call_args[0][0]
        assert cmd == ["claude", "-p", "test prompt", "--allowedTools", "Read"]
        env = call_args[1]["env"]
        assert "CLAUDECODE" not in env

    @patch("run.subprocess.Popen")
    @patch("run.shutil.which", return_value="/usr/bin/claude")
    def test_claude_with_skill_prepends_content(
        self, _mock_which: object, mock_popen: object
    ) -> None:
        from unittest.mock import MagicMock

        assert isinstance(mock_popen, MagicMock)
        mock_popen.return_value = _mock_popen()

        run_agent("claude", "my prompt", skill_content="# Skill instructions")
        call_args = mock_popen.call_args
        prompt_arg = call_args[0][0][2]  # claude -p <prompt>
        assert prompt_arg.startswith("# Skill instructions")
        assert "my prompt" in prompt_arg

    @patch("run.subprocess.Popen")
    @patch("run.shutil.which", return_value="/usr/bin/claude")
    def test_claude_timeout_returns_none(
        self, _mock_which: object, mock_popen: object
    ) -> None:
        import subprocess
        from unittest.mock import MagicMock

        mock_proc = _mock_popen()
        assert isinstance(mock_proc, MagicMock)
        mock_proc.communicate.side_effect = subprocess.TimeoutExpired("claude", 120)
        assert isinstance(mock_popen, MagicMock)
        mock_popen.return_value = mock_proc

        result = run_agent("claude", "slow prompt")
        assert result is None
        mock_proc.kill.assert_called_once()

    @patch("run.subprocess.Popen")
    @patch("run.shutil.which", return_value="/usr/bin/claude")
    def test_claude_keyboard_interrupt_kills_and_raises(
        self, _mock_which: object, mock_popen: object
    ) -> None:
        from unittest.mock import MagicMock

        mock_proc = _mock_popen()
        assert isinstance(mock_proc, MagicMock)
        mock_proc.communicate.side_effect = KeyboardInterrupt
        assert isinstance(mock_popen, MagicMock)
        mock_popen.return_value = mock_proc

        with self.assertRaises(KeyboardInterrupt):
            run_agent("claude", "prompt")
        mock_proc.kill.assert_called_once()

    @patch("run.subprocess.Popen")
    @patch("run.shutil.which", return_value="/usr/bin/opencode")
    def test_opencode_invocation(self, _mock_which: object, mock_popen: object) -> None:
        from unittest.mock import MagicMock

        assert isinstance(mock_popen, MagicMock)
        mock_popen.return_value = _mock_popen("opencode output")

        output = run_agent("opencode", "test prompt")
        assert output == "opencode output"
        mock_popen.assert_called_once()
        cmd = mock_popen.call_args[0][0]
        assert cmd == ["opencode", "run", "-q", "test prompt"]


# --- grade_output ---


class TestGradeOutput(unittest.TestCase):
    @patch("run.run_agent", return_value='pass: true\nevidence: "good"')
    def test_passing_grade(self, _mock: object) -> None:
        passed, evidence = grade_output("claude", "should ask questions", "it asked questions")
        assert passed is True
        assert evidence == "good"

    @patch("run.run_agent", return_value='pass: false\nevidence: "missing behavior"')
    def test_failing_grade(self, _mock: object) -> None:
        passed, evidence = grade_output("claude", "should ask questions", "it filed immediately")
        assert passed is False

    @patch("run.run_agent", return_value=None)
    def test_unavailable_agent(self, _mock: object) -> None:
        passed, evidence = grade_output("claude", "expected", "output")
        assert passed is None
        assert "grading call failed" in evidence

    @patch("run.run_agent", return_value="totally unparseable garbage")
    def test_unparseable_response(self, _mock: object) -> None:
        passed, evidence = grade_output("claude", "expected", "output")
        assert passed is None
        assert "could not parse" in evidence


# --- evals.yaml schema validation ---


class TestEvalsYamlSchema(unittest.TestCase):
    def test_all_eval_ids_are_unique(self) -> None:
        cases, _, _models = load_evals("filing-issues")
        ids = [c.id for c in cases]
        assert len(ids) == len(set(ids)), f"Duplicate eval IDs: {ids}"

    def test_all_eval_ids_are_kebab_case(self) -> None:
        import re

        cases, _, _models = load_evals("filing-issues")
        for c in cases:
            assert re.match(
                r"^[a-z][a-z0-9]*(-[a-z0-9]+)*$", c.id
            ), f"ID '{c.id}' is not kebab-case"

    def test_thresholds_in_valid_range(self) -> None:
        cases, threshold, _models = load_evals("filing-issues")
        assert 0.0 <= threshold <= 1.0
        for c in cases:
            assert 0.0 <= c.threshold <= 1.0, f"Case '{c.id}' threshold out of range"

    def test_mutations_positive(self) -> None:
        cases, _, _models = load_evals("filing-issues")
        for c in cases:
            assert c.mutations >= 0, f"Case '{c.id}' has negative mutations"

    def test_prompts_and_expected_nonempty(self) -> None:
        cases, _, _models = load_evals("filing-issues")
        for c in cases:
            assert c.prompt.strip(), f"Case '{c.id}' has empty prompt"
            assert c.expected.strip(), f"Case '{c.id}' has empty expected"


# --- build_variants ---


class TestBuildVariants(unittest.TestCase):
    def test_zero_mutations_returns_only_original(self) -> None:
        case = EvalCase("c1", "Case", "prompt", "expected", 0, 0.9)
        variants = build_variants(case, ["claude"])
        assert len(variants) == 1
        assert variants[0] == (0, "prompt", "expected")

    @patch("run.generate_mutations", return_value=[("m1", "e1"), ("m2", "e2")])
    def test_mutations_appended_after_original(self, _mock: object) -> None:
        case = EvalCase("c1", "Case", "prompt", "expected", 2, 0.9)
        variants = build_variants(case, ["claude"])
        assert len(variants) == 3
        assert variants[0] == (0, "prompt", "expected")
        assert variants[1] == (1, "m1", "e1")
        assert variants[2] == (2, "m2", "e2")

    @patch("run.generate_mutations", return_value=[("m1", "e1")])
    def test_prefers_opencode_for_mutations(self, mock_gen: object) -> None:
        from unittest.mock import MagicMock

        assert isinstance(mock_gen, MagicMock)
        case = EvalCase("c1", "Case", "prompt", "expected", 1, 0.9)
        build_variants(case, ["claude", "opencode"])
        # Second positional arg to generate_mutations is the agent
        assert mock_gen.call_args[0][3] == "opencode"

    @patch("run.generate_mutations", return_value=[("m1", "e1")])
    def test_falls_back_to_claude_if_no_opencode(self, mock_gen: object) -> None:
        from unittest.mock import MagicMock

        assert isinstance(mock_gen, MagicMock)
        case = EvalCase("c1", "Case", "prompt", "expected", 1, 0.9)
        build_variants(case, ["claude"])
        assert mock_gen.call_args[0][3] == "claude"


# --- model_for_agent ---


class TestModelForAgent(unittest.TestCase):
    def test_claude_strips_provider_and_version(self) -> None:
        result = model_for_agent("claude", "google-vertex-anthropic/claude-haiku-4-5@default")
        assert result == "claude-haiku-4-5"

    def test_claude_strips_provider_only(self) -> None:
        assert model_for_agent("claude", "anthropic/claude-sonnet-4") == "claude-sonnet-4"

    def test_claude_handles_no_prefix(self) -> None:
        assert model_for_agent("claude", "claude-haiku-4-5") == "claude-haiku-4-5"

    def test_claude_handles_no_version(self) -> None:
        result = model_for_agent("claude", "google-vertex-anthropic/claude-opus-4")
        assert result == "claude-opus-4"

    def test_opencode_passes_through(self) -> None:
        model = "google-vertex-anthropic/claude-haiku-4-5@default"
        assert model_for_agent("opencode", model) == model


# --- run_agent with model ---


class TestRunAgentModel(unittest.TestCase):
    @patch("run.subprocess.Popen")
    @patch("run.shutil.which", return_value="/usr/bin/claude")
    def test_claude_passes_model_flag(self, _mock_which: object, mock_popen: object) -> None:
        from unittest.mock import MagicMock

        assert isinstance(mock_popen, MagicMock)
        mock_popen.return_value = _mock_popen()

        run_agent("claude", "prompt", model="google-vertex-anthropic/claude-haiku-4-5@default")
        cmd = mock_popen.call_args[0][0]
        assert "--model" in cmd
        assert "claude-haiku-4-5" in cmd
        # Provider prefix and @version should be stripped for claude
        assert "google-vertex-anthropic" not in " ".join(cmd)
        assert "@default" not in " ".join(cmd)

    @patch("run.subprocess.Popen")
    @patch("run.shutil.which", return_value="/usr/bin/claude")
    def test_claude_no_model_flag_when_none(
        self, _mock_which: object, mock_popen: object
    ) -> None:
        from unittest.mock import MagicMock

        assert isinstance(mock_popen, MagicMock)
        mock_popen.return_value = _mock_popen()

        run_agent("claude", "prompt")
        cmd = mock_popen.call_args[0][0]
        assert "--model" not in cmd

    @patch("run.subprocess.Popen")
    @patch("run.shutil.which", return_value="/usr/bin/opencode")
    def test_opencode_passes_model_flag(
        self, _mock_which: object, mock_popen: object
    ) -> None:
        from unittest.mock import MagicMock

        assert isinstance(mock_popen, MagicMock)
        mock_popen.return_value = _mock_popen()

        run_agent("opencode", "prompt", model="google-vertex-anthropic/claude-haiku-4-5@default")
        cmd = mock_popen.call_args[0][0]
        assert "-m" in cmd
        assert "google-vertex-anthropic/claude-haiku-4-5@default" in cmd

    @patch("run.subprocess.Popen")
    @patch("run.shutil.which", return_value="/usr/bin/opencode")
    def test_opencode_no_model_flag_when_none(
        self, _mock_which: object, mock_popen: object
    ) -> None:
        from unittest.mock import MagicMock

        assert isinstance(mock_popen, MagicMock)
        mock_popen.return_value = _mock_popen()

        run_agent("opencode", "prompt")
        cmd = mock_popen.call_args[0][0]
        assert "-m" not in cmd


if __name__ == "__main__":
    unittest.main()
