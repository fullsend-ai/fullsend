#!/usr/bin/env python3
# /// script
# requires-python = ">=3.12"
# dependencies = ["pyyaml>=6.0"]
# ///
"""Tests for the skill evaluation framework."""

from __future__ import annotations

import io
import os
import tempfile
import unittest
from collections.abc import Sequence
from pathlib import Path
from unittest.mock import MagicMock, patch

import yaml
from run import (
    AgentEmptyOutput,
    AgentNotAvailable,
    AgentTimeout,
    CaseResult,
    EvalCase,
    MutationResult,
    build_variants,
    check_required_env,
    discover_skills,
    grade_output,
    load_cached_variants,
    load_evals,
    model_for_agent,
    parse_yaml_from_output,
    print_report,
    run_agent,
    save_cached_variant,
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
        assert threshold == 0.8
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
        assert case.threshold == 0.8

    @patch.dict(os.environ, {}, clear=False)
    def test_models_loaded(self) -> None:
        os.environ.pop("EVAL_MODEL_MUTATION", None)
        os.environ.pop("EVAL_MODEL_RUNNER", None)
        os.environ.pop("EVAL_MODEL_JUDGE", None)
        _cases, _threshold, models = load_evals("filing-issues")
        assert models.mutation == "google-vertex-anthropic/claude-haiku-4-5@default"
        assert models.runner == "google-vertex-anthropic/claude-haiku-4-5@default"
        assert models.judge == "google-vertex-anthropic/claude-haiku-4-5@default"

    @patch.dict(os.environ, {
        "EVAL_MODEL_MUTATION": "google-vertex/gemini-2.5-flash@default",
        "EVAL_MODEL_RUNNER": "google-vertex/gemini-2.5-pro@default",
        "EVAL_MODEL_JUDGE": "google-vertex/gemini-2.5-flash@default",
    })
    def test_env_vars_override_models(self) -> None:
        _cases, _threshold, models = load_evals("filing-issues")
        assert models.mutation == "google-vertex/gemini-2.5-flash@default"
        assert models.runner == "google-vertex/gemini-2.5-pro@default"
        assert models.judge == "google-vertex/gemini-2.5-flash@default"

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
        self, eval_id: str, agent: str, config: str, pass_values: Sequence[bool | None]
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


def _mock_popen(stdout: str = "output") -> MagicMock:
    """Create a mock Popen instance that returns stdout from communicate()."""
    mock_proc = MagicMock()
    mock_proc.communicate.return_value = (stdout, "")
    mock_proc.kill = MagicMock()
    mock_proc.wait = MagicMock()
    return mock_proc


class TestRunAgent(unittest.TestCase):
    @patch("run.shutil.which", return_value=None)
    def test_claude_not_installed(self, _mock_which: object) -> None:
        with self.assertRaises(AgentNotAvailable):
            run_agent("claude", "hello")

    @patch("run.shutil.which", return_value=None)
    def test_opencode_not_installed(self, _mock_which: object) -> None:
        with self.assertRaises(AgentNotAvailable):
            run_agent("opencode", "hello")

    @patch("run.shutil.which", return_value="/usr/bin/unknown-agent")
    def test_unknown_agent(self, _mock_which: object) -> None:
        with self.assertRaises(ValueError):
            run_agent("unknown-agent", "hello")

    @patch("run.subprocess.Popen")
    @patch("run.shutil.which", return_value="/usr/bin/claude")
    def test_claude_invocation(self, _mock_which: MagicMock, mock_popen: MagicMock) -> None:
        mock_popen.return_value = _mock_popen("agent output")

        output = run_agent("claude", "test prompt")
        assert output == "agent output"
        mock_popen.assert_called_once()
        call_args = mock_popen.call_args
        cmd = call_args[0][0]
        assert cmd == ["claude", "-p", "--allowedTools", "Read"]
        env = call_args[1]["env"]
        assert "CLAUDECODE" not in env
        # Prompt is passed via stdin
        stdin_input = mock_popen.return_value.communicate.call_args[1].get("input")
        assert stdin_input == "test prompt"

    @patch("run.subprocess.Popen")
    @patch("run.shutil.which", return_value="/usr/bin/claude")
    def test_claude_with_skill_prepends_content(
        self, _mock_which: MagicMock, mock_popen: MagicMock
    ) -> None:
        mock_popen.return_value = _mock_popen()

        run_agent("claude", "my prompt", skill_content="# Skill instructions")
        # Prompt with skill content is passed via stdin, not as CLI arg
        stdin_input = mock_popen.return_value.communicate.call_args[1].get("input")
        assert stdin_input is not None
        assert stdin_input.startswith("# Skill instructions")
        assert "my prompt" in stdin_input

    @patch("run.subprocess.Popen")
    @patch("run.shutil.which", return_value="/usr/bin/claude")
    def test_claude_timeout_raises(
        self, _mock_which: object, mock_popen: object
    ) -> None:
        import subprocess
        from unittest.mock import MagicMock

        mock_proc = _mock_popen()
        assert isinstance(mock_proc, MagicMock)
        mock_proc.communicate.side_effect = subprocess.TimeoutExpired("claude", 120)
        assert isinstance(mock_popen, MagicMock)
        mock_popen.return_value = mock_proc

        with self.assertRaises(AgentTimeout):
            run_agent("claude", "slow prompt")
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

    @patch("run.run_agent", side_effect=AgentEmptyOutput("", 1))
    def test_agent_error(self, _mock: object) -> None:
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
    @patch("run.load_cached_variants", return_value=None)
    @patch("run.save_cached_variant")
    def test_zero_mutations_returns_only_original(self, _save: object, _load: object) -> None:
        case = EvalCase("c1", "Case", "prompt", "expected", 0, 0.9)
        variants = build_variants("skill", case, ["claude"], "ehash", "shash")
        assert len(variants) == 1
        assert variants[0] == (0, "prompt", "expected")

    @patch("run.load_cached_variants", return_value=None)
    @patch("run.save_cached_variant")
    @patch("run.generate_mutations", return_value=[("m1", "e1"), ("m2", "e2")])
    def test_mutations_appended_after_original(
        self, _gen: object, _save: object, _load: object
    ) -> None:
        case = EvalCase("c1", "Case", "prompt", "expected", 2, 0.9)
        variants = build_variants("skill", case, ["claude"], "ehash", "shash")
        assert len(variants) == 3
        assert variants[0] == (0, "prompt", "expected")
        assert variants[1] == (1, "m1", "e1")
        assert variants[2] == (2, "m2", "e2")

    @patch("run.load_cached_variants", return_value=None)
    @patch("run.save_cached_variant")
    @patch("run.generate_mutations", return_value=[("m1", "e1")])
    def test_prefers_opencode_for_mutations(
        self, mock_gen: object, _save: object, _load: object
    ) -> None:
        from unittest.mock import MagicMock

        assert isinstance(mock_gen, MagicMock)
        case = EvalCase("c1", "Case", "prompt", "expected", 1, 0.9)
        build_variants("skill", case, ["claude", "opencode"], "ehash", "shash")
        assert mock_gen.call_args[0][3] == "opencode"

    @patch("run.load_cached_variants", return_value=None)
    @patch("run.save_cached_variant")
    @patch("run.generate_mutations", return_value=[("m1", "e1")])
    def test_falls_back_to_claude_if_no_opencode(
        self, mock_gen: object, _save: object, _load: object
    ) -> None:
        from unittest.mock import MagicMock

        assert isinstance(mock_gen, MagicMock)
        case = EvalCase("c1", "Case", "prompt", "expected", 1, 0.9)
        build_variants("skill", case, ["claude"], "ehash", "shash")
        assert mock_gen.call_args[0][3] == "claude"

    def test_uses_cache_when_valid(self) -> None:
        cached = [(0, "p", "e"), (1, "m1", "e1")]
        with patch("run.load_cached_variants", return_value=cached):
            case = EvalCase("c1", "Case", "p", "e", 1, 0.9)
            variants = build_variants("skill", case, ["claude"], "ehash", "shash")
        assert variants == cached

    @patch("run.load_cached_variants", return_value=None)
    @patch("run.generate_mutations", return_value=[("m1", "e1")])
    def test_saves_each_variant_to_cache_on_miss(self, _gen: object, _load: object) -> None:
        from unittest.mock import MagicMock

        with patch("run.save_cached_variant") as mock_save:
            assert isinstance(mock_save, MagicMock)
            case = EvalCase("c1", "Case", "prompt", "expected", 1, 0.9)
            build_variants("skill", case, ["claude"], "ehash", "shash")
            # Should be called twice: once for original, once for mutation
            assert mock_save.call_count == 2
            # First call: original
            args0 = mock_save.call_args_list[0][0]
            assert args0[:3] == ("skill", "c1", 0)
            # Second call: mutation 1
            args1 = mock_save.call_args_list[1][0]
            assert args1[:3] == ("skill", "c1", 1)


# --- mutation cache ---


class TestMutationCache(unittest.TestCase):
    @staticmethod
    def _save_all(
        skill: str,
        case_id: str,
        variants: list[tuple[int, str, str]],
        evals_hash: str,
        skill_hash: str,
    ) -> None:
        for mut_idx, prompt, expected in variants:
            save_cached_variant(skill, case_id, mut_idx, prompt, expected, evals_hash, skill_hash)

    def test_roundtrip(self) -> None:
        variants = [(0, "original prompt", "original expected"), (1, "m1", "e1")]
        with tempfile.TemporaryDirectory() as tmpdir, patch("run.SKILLS_DIR", Path(tmpdir)):
            self._save_all("test-skill", "test-case", variants, "ehash", "shash")
            case = EvalCase("test-case", "Test", "original prompt", "original expected", 1, 0.9)
            loaded = load_cached_variants("test-skill", case, "ehash", "shash")
        assert loaded == variants

    def test_cache_miss_on_evals_hash_change(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir, patch("run.SKILLS_DIR", Path(tmpdir)):
            self._save_all("test-skill", "test-case", [(0, "p", "e")], "old-hash", "shash")
            case = EvalCase("test-case", "Test", "p", "e", 0, 0.9)
            loaded = load_cached_variants("test-skill", case, "new-hash", "shash")
        assert loaded is None

    def test_cache_miss_on_skill_hash_change(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir, patch("run.SKILLS_DIR", Path(tmpdir)):
            self._save_all("test-skill", "test-case", [(0, "p", "e")], "ehash", "old-hash")
            case = EvalCase("test-case", "Test", "p", "e", 0, 0.9)
            loaded = load_cached_variants("test-skill", case, "ehash", "new-hash")
        assert loaded is None

    def test_cache_miss_on_missing_dir(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir, patch("run.SKILLS_DIR", Path(tmpdir)):
            case = EvalCase("c1", "Test", "p", "e", 0, 0.9)
            loaded = load_cached_variants("skill", case, "ehash", "shash")
        assert loaded is None

    def test_cache_miss_on_missing_file(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir, patch("run.SKILLS_DIR", Path(tmpdir)):
            # Save only original, but case expects 1 mutation too
            self._save_all("test-skill", "test-case", [(0, "p", "e")], "ehash", "shash")
            case = EvalCase("test-case", "Test", "p", "e", 1, 0.9)
            loaded = load_cached_variants("test-skill", case, "ehash", "shash")
        assert loaded is None


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


class TestCheckRequiredEnv(unittest.TestCase):
    @patch.dict("os.environ", {
        "CLAUDE_CODE_USE_VERTEX": "1",
        "GOOGLE_APPLICATION_CREDENTIALS": "/path/to/creds.json",
    })
    def test_all_vars_set_returns_no_errors(self) -> None:
        errors = check_required_env(["claude"])
        assert errors == []

    @patch.dict("os.environ", {}, clear=True)
    def test_missing_vars_returns_errors(self) -> None:
        errors = check_required_env(["claude"])
        assert len(errors) == 2
        assert any("CLAUDE_CODE_USE_VERTEX" in e for e in errors)
        assert any("GOOGLE_APPLICATION_CREDENTIALS" in e for e in errors)

    @patch.dict("os.environ", {"CLAUDE_CODE_USE_VERTEX": "1"}, clear=True)
    def test_partial_vars_returns_missing(self) -> None:
        errors = check_required_env(["claude"])
        assert len(errors) == 1
        assert "GOOGLE_APPLICATION_CREDENTIALS" in errors[0]

    def test_unknown_agent_returns_no_errors(self) -> None:
        errors = check_required_env(["some-future-agent"])
        assert errors == []


if __name__ == "__main__":
    unittest.main()
