#!/usr/bin/env python3
# /// script
# requires-python = ">=3.12"
# dependencies = ["pyyaml>=6.0"]
# ///
"""Skill evaluation framework.

Runs eval cases against Claude Code and OpenCode, using LLM-as-judge grading
and mutation testing to verify skill robustness.
"""

from __future__ import annotations

import os
import shutil
import subprocess
import sys
from dataclasses import dataclass, field
from pathlib import Path

import yaml

REPO_ROOT = Path(__file__).resolve().parent.parent.parent
SKILLS_DIR = REPO_ROOT / "skills"
WORKSPACE_DIR = REPO_ROOT / ".eval-workspace"
IS_TTY = sys.stdout.isatty() and not os.environ.get("CI")


# --- Data types ---


@dataclass
class ModelConfig:
    mutation: str
    runner: str
    judge: str


@dataclass
class EvalCase:
    id: str
    title: str
    prompt: str
    expected: str
    mutations: int
    threshold: float


@dataclass
class MutationResult:
    mutation: int
    prompt: str
    expected: str
    passed: bool | None  # None = skipped
    evidence: str


@dataclass
class CaseResult:
    eval_id: str
    agent: str
    configuration: str
    results: list[MutationResult] = field(default_factory=list)

    @property
    def total(self) -> int:
        return sum(1 for r in self.results if r.passed is not None)

    @property
    def passed(self) -> int:
        return sum(1 for r in self.results if r.passed is True)

    @property
    def failed(self) -> int:
        return sum(1 for r in self.results if r.passed is False)

    @property
    def skipped(self) -> int:
        return sum(1 for r in self.results if r.passed is None)

    @property
    def pass_rate(self) -> float:
        return self.passed / self.total if self.total > 0 else 0.0


# --- Model name translation ---


def model_for_agent(agent: str, opencode_model: str) -> str:
    """Translate an OpenCode-style model name for a specific agent CLI.

    Model names follow the schema at https://models.dev/model-schema.json#/$defs/Model
    using 'provider/model@version' format
    (e.g. 'google-vertex-anthropic/claude-haiku-4-5@default').
    Claude Code uses just the model name (e.g. 'claude-haiku-4-5').
    """
    if agent == "claude":
        # Strip provider prefix and @version suffix
        model = opencode_model.split("/", 1)[-1]
        model = model.split("@", 1)[0]
        return model
    return opencode_model


# --- Runner ---


def run_agent(
    agent: str,
    prompt: str,
    skill_content: str | None = None,
    model: str | None = None,
) -> str | None:
    """Run prompt through an agent CLI. Returns output or None if unavailable."""
    full_prompt = f"{skill_content}\n\n---\n\n{prompt}" if skill_content else prompt

    if agent == "claude":
        if not shutil.which("claude"):
            return None
        env = {k: v for k, v in os.environ.items() if k != "CLAUDECODE"}
        cmd = ["claude", "-p", full_prompt, "--allowedTools", "Read"]
        if model:
            cmd.extend(["--model", model_for_agent("claude", model)])
        return _run_subprocess(cmd, env=env)
    elif agent == "opencode":
        if not shutil.which("opencode"):
            return None
        # TODO: OpenCode lacks --allowedTools equivalent. Add sandboxing
        # config when available. For now, runs without tool restrictions.
        cmd = ["opencode", "run", "-q"]
        if model:
            cmd.extend(["-m", model])
        cmd.append(full_prompt)
        return _run_subprocess(cmd)
    return None


def _run_subprocess(
    cmd: list[str], env: dict[str, str] | None = None
) -> str | None:
    """Run a subprocess, killing it immediately on KeyboardInterrupt."""
    try:
        proc = subprocess.Popen(
            cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, env=env
        )
        stdout, _ = proc.communicate(timeout=120)
        return stdout
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait()
        return None
    except KeyboardInterrupt:
        proc.kill()
        proc.wait()
        raise
    except FileNotFoundError:
        return None


# --- Mutator ---

MUTATION_PROMPT = """You are generating a variant of a test case for an AI skill evaluation.

Rephrase both the user prompt and the expected behavior description below.
Change the wording, phrasing, and specific details (names, scenarios,
error descriptions) while preserving the core intent and the behaviors
being tested. The variant should test the same capability but with
different surface-level details.

## Original prompt
{prompt}

## Original expected behavior
{expected}

Respond with ONLY a YAML object (no markdown fences, no extra text):
prompt: "the rephrased user prompt"
expected: "the rephrased expected behavior"
"""


def generate_mutations(
    prompt: str, expected: str, count: int, agent: str, model: str | None = None
) -> list[tuple[str, str]]:
    """Generate mutated prompt/expected pairs via LLM. Falls back to originals."""
    mutations: list[tuple[str, str]] = []
    for i in range(count):
        if IS_TTY:
            print(f"    Mutating... {i + 1}/{count}", end="\r", flush=True)
        else:
            print(f"    Mutating... {i + 1}/{count}")
        mutation_request = MUTATION_PROMPT.format(prompt=prompt, expected=expected)
        output = run_agent(agent, mutation_request, model=model)
        if output:
            parsed = parse_yaml_from_output(output)
            if parsed and "prompt" in parsed and "expected" in parsed:
                mutations.append((parsed["prompt"], parsed["expected"]))
                continue
        print("  WARNING: mutation generation failed, using original", file=sys.stderr)
        mutations.append((prompt, expected))
    if IS_TTY:
        print(f"    Mutated {count} variants" + " " * 20)
    return mutations


# --- Grader ---

GRADING_PROMPT = """You are a strict evaluator for an AI coding agent. You are judging
whether the agent's actual output demonstrates the expected behaviors.

## Expected behavior
{expected}

## Actual agent output
{output}

Does the actual output satisfy the expected behavior? Be strict but fair.
The output does not need to match word-for-word — it needs to demonstrate
the described behaviors and meet the described criteria.

Respond with ONLY a YAML object (no markdown fences, no extra text):
pass: true or false
evidence: "specific quotes or explanation of what is missing"
"""


def grade_output(
    agent: str, expected: str, output: str, model: str | None = None
) -> tuple[bool | None, str]:
    """Grade agent output against expected behavior. Returns (passed, evidence)."""
    grading_request = GRADING_PROMPT.format(expected=expected, output=output)
    response = run_agent(agent, grading_request, model=model)
    if not response:
        return None, "grading call failed"
    parsed = parse_yaml_from_output(response)
    if parsed and "pass" in parsed:
        return bool(parsed["pass"]), str(parsed.get("evidence", ""))
    return None, f"could not parse grading response: {response[:200]}"


# --- YAML parsing helper ---


def parse_yaml_from_output(text: str) -> dict | None:  # type: ignore[type-arg]
    """Parse YAML from LLM output, stripping markdown fences if present."""
    text = text.strip()
    # Strip markdown code fences
    if text.startswith("```"):
        lines = text.split("\n")
        lines = lines[1:]  # remove opening fence
        if lines and lines[-1].strip() == "```":
            lines = lines[:-1]
        text = "\n".join(lines)
    try:
        data = yaml.safe_load(text)
        if isinstance(data, dict):
            return data
    except yaml.YAMLError:
        pass
    # Try to find YAML in the text (LLM may add preamble)
    for marker in ["pass:", "prompt:"]:
        idx = text.find(marker)
        if idx >= 0:
            try:
                data = yaml.safe_load(text[idx:])
                if isinstance(data, dict):
                    return data
            except yaml.YAMLError:
                pass
    return None


# --- Eval loading ---


def load_evals(skill_name: str) -> tuple[list[EvalCase], float, ModelConfig]:
    """Load eval definitions for a skill. Returns (cases, default_threshold, models)."""
    evals_file = SKILLS_DIR / skill_name / "evals.yaml"
    if not evals_file.exists():
        print(f"ERROR: {evals_file} not found", file=sys.stderr)
        sys.exit(1)
    with open(evals_file) as f:
        data = yaml.safe_load(f)
    default_threshold = float(data.get("threshold", 0.9))
    models_raw = data.get("models", {})
    models = ModelConfig(
        mutation=models_raw.get("mutation", ""),
        runner=models_raw.get("runner", ""),
        judge=models_raw.get("judge", ""),
    )
    cases = []
    for e in data["evals"]:
        cases.append(
            EvalCase(
                id=e["id"],
                title=e["title"],
                prompt=e["prompt"],
                expected=e["expected"],
                mutations=int(e.get("mutations", 5)),
                threshold=float(e.get("threshold", default_threshold)),
            )
        )
    return cases, default_threshold, models


def load_skill_content(skill_name: str) -> str:
    """Load SKILL.md content for a skill."""
    skill_file = SKILLS_DIR / skill_name / "SKILL.md"
    with open(skill_file) as f:
        return f.read()


# --- Core eval runner ---


def build_variants(
    case: EvalCase, available_agents: list[str], model: str | None = None
) -> list[tuple[int, str, str]]:
    """Generate the original + mutated variants for a case. Done once, shared by all runs.

    Prefers opencode for mutation generation, falls back to claude.
    """
    variants: list[tuple[int, str, str]] = [(0, case.prompt, case.expected)]
    if case.mutations > 0:
        # Prefer opencode for mutations, fall back to claude
        mutation_agent = (
            "opencode" if "opencode" in available_agents else available_agents[0]
        )
        print(f"  Generating {case.mutations} mutations via {mutation_agent}...")
        mutations = generate_mutations(
            case.prompt, case.expected, case.mutations, mutation_agent, model=model
        )
        for i, (p, e) in enumerate(mutations, start=1):
            variants.append((i, p, e))
    return variants


def run_eval_case(
    case: EvalCase,
    agent: str,
    skill_content: str | None,
    configuration: str,
    variants: list[tuple[int, str, str]],
    models: ModelConfig | None = None,
) -> CaseResult:
    """Run a single eval case with pre-generated variants against one agent."""
    result = CaseResult(eval_id=case.id, agent=agent, configuration=configuration)
    runner_model = (models.runner or None) if models else None
    judge_model = (models.judge or None) if models else None

    total = len(variants)
    for step, (mut_idx, prompt, expected) in enumerate(variants, start=1):
        label = "original" if mut_idx == 0 else f"mutation {mut_idx}"
        if IS_TTY:
            print(f"    [{step}/{total}] {label}: running...", end="\r", flush=True)
        output = run_agent(agent, prompt, skill_content, model=runner_model)
        if output is None:
            result.results.append(
                MutationResult(mut_idx, prompt, expected, None, "agent not available")
            )
            status = "SKIP"
        else:
            if IS_TTY:
                print(f"    [{step}/{total}] {label}: grading...", end="\r", flush=True)
            passed, evidence = grade_output(agent, expected, output, model=judge_model)
            result.results.append(
                MutationResult(mut_idx, prompt, expected, passed, evidence)
            )
            status = "PASS" if passed else ("FAIL" if passed is False else "SKIP")
        pct = int(result.pass_rate * 100) if result.total > 0 else 0
        line = (
            f"    [{step}/{total}] {label:<16s} {status}  "
            f"({result.passed}/{result.total} = {pct}%)"
        )
        if IS_TTY:
            print(f"\r{line}" + " " * 10)
        else:
            print(line)

    return result


# --- Reporting ---


def save_grading_yaml(result: CaseResult, threshold: float, output_dir: Path) -> None:
    """Write grading.yaml for a case result."""
    output_dir.mkdir(parents=True, exist_ok=True)
    filename = f"{result.eval_id}_{result.agent}_{result.configuration}.yaml"
    data = {
        "eval_id": result.eval_id,
        "agent": result.agent,
        "configuration": result.configuration,
        "mutations": [
            {
                "mutation": r.mutation,
                "prompt": r.prompt,
                "expected": r.expected,
                "passed": r.passed,
                "evidence": r.evidence,
            }
            for r in result.results
        ],
        "summary": {
            "total": result.total,
            "passed": result.passed,
            "failed": result.failed,
            "skipped": result.skipped,
            "pass_rate": round(result.pass_rate, 3),
            "threshold": threshold,
            "met_threshold": result.pass_rate >= threshold,
        },
    }
    with open(output_dir / filename, "w") as f:
        yaml.dump(data, f, default_flow_style=False, sort_keys=False)


def print_report(
    skill_name: str,
    cases: list[EvalCase],
    all_results: dict[str, dict[str, dict[str, CaseResult]]],
    agents: list[str],
) -> bool:
    """Print summary report. Returns True if all cases pass."""
    print(f"\n=== Skill Eval Results: {skill_name} ===\n")
    all_pass = True
    failures: list[str] = []

    for case in cases:
        print(f"  {case.id} (threshold: {case.threshold:.2f})")
        case_pass = True
        for agent in agents:
            agent_results = all_results.get(case.id, {}).get(agent)
            if not agent_results:
                print(f"    {agent}: (skipped - not installed)")
                continue
            ws = agent_results.get("with_skill")
            wo = agent_results.get("without_skill")
            if ws:
                pct = int(ws.pass_rate * 100)
                status = "PASS" if ws.pass_rate >= case.threshold else "FAIL"
                print(f"    {agent}:")
                print(f"      with_skill:    {ws.passed}/{ws.total} passed ({pct}%) -- {status}")
                if status == "FAIL":
                    case_pass = False
                    failures.append(
                        f"{case.id}: {agent} with_skill below threshold "
                        f"({pct}% < {int(case.threshold * 100)}%)"
                    )
            if wo:
                wo_pct = int(wo.pass_rate * 100)
                print(f"      without_skill: {wo.passed}/{wo.total} passed ({wo_pct}%)")
                if ws and ws.total > 0 and wo.total > 0:
                    delta = int((ws.pass_rate - wo.pass_rate) * 100)
                    sign = "+" if delta >= 0 else ""
                    print(f"      delta: {sign}{delta}%")

        if not case_pass:
            all_pass = False
        print()

    passing = [c for c in cases if all(
        all_results.get(c.id, {}).get(a, {}).get("with_skill", None) is None
        or all_results[c.id][a]["with_skill"].pass_rate >= c.threshold
        for a in agents
    )]
    print("  Summary:")
    print(f"    Total cases:    {len(cases)}")
    print(f"    Cases passing:  {len(passing)} ({', '.join(c.id for c in passing) or 'none'})")
    if failures:
        print(f"    Cases failing:  {len(cases) - len(passing)}")
        for f in failures:
            print(f"      {f}")
    print(f"\n=== {'PASSED' if all_pass else 'FAILED'} ===")
    return all_pass


# --- Main ---


def discover_skills(skill_filter: str | None = None) -> list[str]:
    """Find skills that have evals defined."""
    skills: list[str] = []
    if skill_filter:
        evals_file = SKILLS_DIR / skill_filter / "evals.yaml"
        if evals_file.exists():
            skills.append(skill_filter)
        else:
            print(f"ERROR: No evals found for skill '{skill_filter}'", file=sys.stderr)
            sys.exit(1)
    else:
        for skill_dir in sorted(SKILLS_DIR.iterdir()):
            if (skill_dir / "evals.yaml").exists():
                skills.append(skill_dir.name)
    return skills


def main() -> int:
    skill_filter = sys.argv[1] if len(sys.argv) > 1 else None
    skills = discover_skills(skill_filter)
    if not skills:
        print("No skills with evals found.")
        return 0

    agents = ["claude", "opencode"]
    available_agents = [a for a in agents if shutil.which(a if a == "claude" else a)]
    if not available_agents:
        print("ERROR: Neither claude nor opencode is installed.", file=sys.stderr)
        return 1

    WORKSPACE_DIR.mkdir(parents=True, exist_ok=True)
    overall_pass = True

    for skill_name in skills:
        cases, _, models = load_evals(skill_name)
        skill_content = load_skill_content(skill_name)
        output_dir = WORKSPACE_DIR / skill_name

        # {case_id: {agent: {config: CaseResult}}}
        all_results: dict[str, dict[str, dict[str, CaseResult]]] = {}

        for case in cases:
            print(f"\n--- {skill_name}/{case.id}: {case.title} ---")
            all_results.setdefault(case.id, {})

            mutation_model = (models.mutation or None) if models else None
            variants = build_variants(case, available_agents, model=mutation_model)

            for agent in agents:
                if agent not in available_agents:
                    print(f"  [{agent}] skipped (not installed)")
                    continue
                all_results[case.id].setdefault(agent, {})

                for config in ["with_skill", "without_skill"]:
                    sc = skill_content if config == "with_skill" else None
                    print(f"  [{agent}] {config}:")
                    result = run_eval_case(
                        case, agent, sc, config, variants, models
                    )
                    all_results[case.id][agent][config] = result
                    save_grading_yaml(result, case.threshold, output_dir)

        skill_pass = print_report(skill_name, cases, all_results, agents)
        if not skill_pass:
            overall_pass = False

    return 0 if overall_pass else 1


if __name__ == "__main__":
    try:
        sys.exit(main())
    except KeyboardInterrupt:
        print("\n\nInterrupted.", file=sys.stderr)
        sys.exit(130)
