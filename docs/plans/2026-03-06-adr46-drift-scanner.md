# ADR-0046 Drift Scanner Implementation Plan

> **Note:** This experiment has been moved to the [experiments repository](https://github.com/fullsend-ai/experiments/tree/main/adr46-scanner). File paths below reference the original `experiments/` directory in this repo.

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build a drift scanner that detects Tekton tasks using images that should be replaced by the task runner image per ADR-0046, and files GitHub issues reporting the drift.

**Architecture:** A Python CLI tool that clones/reads the build-definitions repo, parses Tekton task YAML files, compares step images against a known allowlist (task runner + explicitly exempted images like build-trusted-artifacts), and reports violations. Can optionally file GitHub issues via `gh` CLI. The scanner is config-driven: the allowlist and exemptions are declared in a YAML config file, not hard-coded.

**Tech Stack:** Python 3.12, PyYAML, pytest, `gh` CLI for issue filing

---

### Task 1: Project scaffolding

**Files:**
- Create: `experiments/adr46-scanner/pyproject.toml`
- Create: `experiments/adr46-scanner/scanner/__init__.py`
- Create: `experiments/adr46-scanner/scanner/cli.py`
- Create: `experiments/adr46-scanner/tests/__init__.py`

**Step 1: Create pyproject.toml**

```toml
[project]
name = "adr46-scanner"
version = "0.1.0"
description = "Detect Tekton tasks drifting from ADR-0046 (common task runner image)"
requires-python = ">=3.12"
dependencies = [
    "pyyaml>=6.0",
]

[project.optional-dependencies]
dev = [
    "pytest>=8.0",
]

[project.scripts]
adr46-scan = "scanner.cli:main"
```

**Step 2: Create empty module files**

Create `scanner/__init__.py` and `tests/__init__.py` as empty files.
Create `scanner/cli.py` with a placeholder:

```python
def main():
    raise SystemExit("Not implemented yet")
```

**Step 3: Verify the project installs**

Run: `cd experiments/adr46-scanner && pip install -e ".[dev]"`
Expected: installs successfully

**Step 4: Verify the CLI entry point exists**

Run: `adr46-scan`
Expected: exits with "Not implemented yet"

**Step 5: Commit**

```bash
git add experiments/adr46-scanner/
git commit -m "feat: scaffold adr46-scanner project"
```

---

### Task 2: Config file format and loader

**Files:**
- Create: `experiments/adr46-scanner/scanner/config.py`
- Create: `experiments/adr46-scanner/tests/test_config.py`
- Create: `experiments/adr46-scanner/config.yaml`

The config declares: the task runner image reference pattern, exempt image patterns (e.g., build-trusted-artifacts), and the target repo/directory to scan.

**Step 1: Write the failing tests**

```python
# tests/test_config.py
import pytest
from scanner.config import load_config


def test_load_config_from_file(tmp_path):
    config_file = tmp_path / "config.yaml"
    config_file.write_text(
        """\
task_runner_image: "quay.io/konflux-ci/task-runner"
exempt_images:
  - "quay.io/konflux-ci/build-trusted-artifacts"
scan_paths:
  - "task/"
"""
    )
    config = load_config(str(config_file))
    assert config.task_runner_image == "quay.io/konflux-ci/task-runner"
    assert "quay.io/konflux-ci/build-trusted-artifacts" in config.exempt_images
    assert "task/" in config.scan_paths


def test_load_config_missing_file():
    with pytest.raises(FileNotFoundError):
        load_config("/nonexistent/config.yaml")


def test_load_config_missing_required_field(tmp_path):
    config_file = tmp_path / "config.yaml"
    config_file.write_text("exempt_images: []\n")
    with pytest.raises(ValueError, match="task_runner_image"):
        load_config(str(config_file))
```

**Step 2: Run tests to verify they fail**

Run: `cd experiments/adr46-scanner && pytest tests/test_config.py -v`
Expected: FAIL — `scanner.config` does not exist

**Step 3: Write the implementation**

```python
# scanner/config.py
from dataclasses import dataclass, field
from pathlib import Path

import yaml


@dataclass
class ScannerConfig:
    task_runner_image: str
    exempt_images: list[str] = field(default_factory=list)
    scan_paths: list[str] = field(default_factory=lambda: ["task/"])


def load_config(path: str) -> ScannerConfig:
    config_path = Path(path)
    if not config_path.exists():
        raise FileNotFoundError(f"Config file not found: {path}")

    with open(config_path) as f:
        data = yaml.safe_load(f)

    if not data or "task_runner_image" not in data:
        raise ValueError("Config must include 'task_runner_image'")

    return ScannerConfig(
        task_runner_image=data["task_runner_image"],
        exempt_images=data.get("exempt_images", []),
        scan_paths=data.get("scan_paths", ["task/"]),
    )
```

**Step 4: Run tests to verify they pass**

Run: `cd experiments/adr46-scanner && pytest tests/test_config.py -v`
Expected: PASS (3 tests)

**Step 5: Create the default config file**

```yaml
# config.yaml
# ADR-0046 drift scanner configuration
# See: https://github.com/konflux-ci/architecture/blob/main/ADR/0046-common-task-runner-image.md

task_runner_image: "quay.io/konflux-ci/task-runner"

# Images that are explicitly exempt from the task runner requirement.
# The trusted artifacts image is carved out as an exception in ADR-0046.
exempt_images:
  - "quay.io/konflux-ci/build-trusted-artifacts"

# Paths within the build-definitions repo to scan for task YAML files.
scan_paths:
  - "task/"
```

**Step 6: Commit**

```bash
git add experiments/adr46-scanner/
git commit -m "feat: add config file format and loader for adr46 scanner"
```

---

### Task 3: Tekton task parser

**Files:**
- Create: `experiments/adr46-scanner/scanner/parser.py`
- Create: `experiments/adr46-scanner/tests/test_parser.py`
- Create: `experiments/adr46-scanner/tests/fixtures/`

Parse Tekton task YAML files and extract the step images.

**Step 1: Create a test fixture**

Create `tests/fixtures/sample-task.yaml` — a minimal Tekton task with multiple steps using different images:

```yaml
apiVersion: tekton.dev/v1
kind: Task
metadata:
  name: sample-task
spec:
  steps:
    - name: trusted-artifact
      image: quay.io/konflux-ci/build-trusted-artifacts:latest@sha256:abc123
    - name: do-work
      image: quay.io/konflux-ci/oras:latest@sha256:def456
    - name: report
      image: quay.io/konflux-ci/yq:latest@sha256:789abc
```

**Step 2: Write the failing tests**

```python
# tests/test_parser.py
from pathlib import Path

import pytest
from scanner.parser import parse_task, StepImage


FIXTURES = Path(__file__).parent / "fixtures"


def test_parse_task_extracts_steps():
    task = parse_task(FIXTURES / "sample-task.yaml")
    assert task.name == "sample-task"
    assert len(task.steps) == 3


def test_parse_task_extracts_image_repo():
    task = parse_task(FIXTURES / "sample-task.yaml")
    assert task.steps[0].image_repo == "quay.io/konflux-ci/build-trusted-artifacts"
    assert task.steps[1].image_repo == "quay.io/konflux-ci/oras"
    assert task.steps[2].image_repo == "quay.io/konflux-ci/yq"


def test_parse_task_preserves_full_image_ref():
    task = parse_task(FIXTURES / "sample-task.yaml")
    assert "sha256:abc123" in task.steps[0].full_ref


def test_parse_task_preserves_step_name():
    task = parse_task(FIXTURES / "sample-task.yaml")
    assert task.steps[0].name == "trusted-artifact"
    assert task.steps[1].name == "do-work"


def test_parse_non_task_yaml(tmp_path):
    non_task = tmp_path / "not-a-task.yaml"
    non_task.write_text("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: foo\n")
    assert parse_task(non_task) is None
```

**Step 3: Run tests to verify they fail**

Run: `cd experiments/adr46-scanner && pytest tests/test_parser.py -v`
Expected: FAIL — `scanner.parser` does not exist

**Step 4: Write the implementation**

```python
# scanner/parser.py
import re
from dataclasses import dataclass
from pathlib import Path

import yaml


@dataclass
class StepImage:
    name: str
    full_ref: str
    image_repo: str


@dataclass
class TektonTask:
    name: str
    file_path: Path
    steps: list[StepImage]


def _extract_repo(image_ref: str) -> str:
    """Extract the repository part of an image reference, stripping tag and digest."""
    # Remove digest
    repo = image_ref.split("@")[0]
    # Remove tag
    repo = re.split(r":[^/]", repo)[0]
    return repo


def parse_task(path: Path) -> TektonTask | None:
    with open(path) as f:
        doc = yaml.safe_load(f)

    if not doc or doc.get("kind") != "Task":
        return None

    name = doc.get("metadata", {}).get("name", path.stem)
    steps = []
    for step in doc.get("spec", {}).get("steps", []):
        image = step.get("image", "")
        steps.append(
            StepImage(
                name=step.get("name", ""),
                full_ref=image,
                image_repo=_extract_repo(image),
            )
        )

    return TektonTask(name=name, file_path=path, steps=steps)
```

**Step 5: Run tests to verify they pass**

Run: `cd experiments/adr46-scanner && pytest tests/test_parser.py -v`
Expected: PASS (5 tests)

**Step 6: Commit**

```bash
git add experiments/adr46-scanner/
git commit -m "feat: add Tekton task YAML parser for adr46 scanner"
```

---

### Task 4: Drift detector

**Files:**
- Create: `experiments/adr46-scanner/scanner/detector.py`
- Create: `experiments/adr46-scanner/tests/test_detector.py`

Compare step images against the config to find violations.

**Step 1: Write the failing tests**

```python
# tests/test_detector.py
from pathlib import Path

import pytest
from scanner.config import ScannerConfig
from scanner.detector import detect_drift
from scanner.parser import TektonTask, StepImage


@pytest.fixture
def config():
    return ScannerConfig(
        task_runner_image="quay.io/konflux-ci/task-runner",
        exempt_images=["quay.io/konflux-ci/build-trusted-artifacts"],
    )


def _make_task(steps):
    return TektonTask(name="test-task", file_path=Path("task/test/0.1/test.yaml"), steps=steps)


def test_no_drift_when_using_task_runner(config):
    task = _make_task([
        StepImage(name="work", full_ref="quay.io/konflux-ci/task-runner:1.0@sha256:abc", image_repo="quay.io/konflux-ci/task-runner"),
    ])
    violations = detect_drift(task, config)
    assert len(violations) == 0


def test_no_drift_for_exempt_image(config):
    task = _make_task([
        StepImage(name="ta", full_ref="quay.io/konflux-ci/build-trusted-artifacts:latest@sha256:abc", image_repo="quay.io/konflux-ci/build-trusted-artifacts"),
    ])
    violations = detect_drift(task, config)
    assert len(violations) == 0


def test_drift_detected_for_non_runner_image(config):
    task = _make_task([
        StepImage(name="pull", full_ref="quay.io/konflux-ci/oras:latest@sha256:abc", image_repo="quay.io/konflux-ci/oras"),
    ])
    violations = detect_drift(task, config)
    assert len(violations) == 1
    assert violations[0].step_name == "pull"
    assert violations[0].current_image == "quay.io/konflux-ci/oras"


def test_mixed_steps(config):
    task = _make_task([
        StepImage(name="ta", full_ref="quay.io/konflux-ci/build-trusted-artifacts:latest", image_repo="quay.io/konflux-ci/build-trusted-artifacts"),
        StepImage(name="work", full_ref="quay.io/konflux-ci/task-runner:1.0", image_repo="quay.io/konflux-ci/task-runner"),
        StepImage(name="pull", full_ref="quay.io/konflux-ci/oras:latest", image_repo="quay.io/konflux-ci/oras"),
        StepImage(name="report", full_ref="quay.io/konflux-ci/yq:latest", image_repo="quay.io/konflux-ci/yq"),
    ])
    violations = detect_drift(task, config)
    assert len(violations) == 2
    assert {v.step_name for v in violations} == {"pull", "report"}
```

**Step 2: Run tests to verify they fail**

Run: `cd experiments/adr46-scanner && pytest tests/test_detector.py -v`
Expected: FAIL — `scanner.detector` does not exist

**Step 3: Write the implementation**

```python
# scanner/detector.py
from dataclasses import dataclass

from scanner.config import ScannerConfig
from scanner.parser import TektonTask


@dataclass
class Violation:
    task_name: str
    task_file: str
    step_name: str
    current_image: str


def detect_drift(task: TektonTask, config: ScannerConfig) -> list[Violation]:
    violations = []
    for step in task.steps:
        if step.image_repo == config.task_runner_image:
            continue
        if step.image_repo in config.exempt_images:
            continue
        violations.append(
            Violation(
                task_name=task.name,
                task_file=str(task.file_path),
                step_name=step.name,
                current_image=step.image_repo,
            )
        )
    return violations
```

**Step 4: Run tests to verify they pass**

Run: `cd experiments/adr46-scanner && pytest tests/test_detector.py -v`
Expected: PASS (4 tests)

**Step 5: Commit**

```bash
git add experiments/adr46-scanner/
git commit -m "feat: add drift detector comparing step images to task runner"
```

---

### Task 5: Directory scanner

**Files:**
- Create: `experiments/adr46-scanner/scanner/scan.py`
- Create: `experiments/adr46-scanner/tests/test_scan.py`

Walk a directory tree, find Tekton task YAML files, parse them, and run drift detection.

**Step 1: Write the failing tests**

```python
# tests/test_scan.py
from pathlib import Path

from scanner.config import ScannerConfig
from scanner.scan import scan_directory


def _write_task(path, name, steps_yaml):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(
        f"apiVersion: tekton.dev/v1\nkind: Task\nmetadata:\n  name: {name}\nspec:\n  steps:\n{steps_yaml}"
    )


def test_scan_finds_violations(tmp_path):
    task_dir = tmp_path / "task" / "my-task" / "0.1"
    _write_task(
        task_dir / "my-task.yaml",
        "my-task",
        '    - name: work\n      image: quay.io/konflux-ci/oras:latest\n',
    )
    config = ScannerConfig(
        task_runner_image="quay.io/konflux-ci/task-runner",
        exempt_images=[],
        scan_paths=["task/"],
    )
    violations = scan_directory(tmp_path, config)
    assert len(violations) == 1
    assert violations[0].task_name == "my-task"


def test_scan_ignores_non_yaml(tmp_path):
    task_dir = tmp_path / "task"
    task_dir.mkdir(parents=True)
    (task_dir / "readme.md").write_text("# hello")
    config = ScannerConfig(
        task_runner_image="quay.io/konflux-ci/task-runner",
        exempt_images=[],
        scan_paths=["task/"],
    )
    violations = scan_directory(tmp_path, config)
    assert len(violations) == 0


def test_scan_skips_non_task_yaml(tmp_path):
    task_dir = tmp_path / "task"
    task_dir.mkdir(parents=True)
    (task_dir / "configmap.yaml").write_text(
        "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: foo\n"
    )
    config = ScannerConfig(
        task_runner_image="quay.io/konflux-ci/task-runner",
        exempt_images=[],
        scan_paths=["task/"],
    )
    violations = scan_directory(tmp_path, config)
    assert len(violations) == 0
```

**Step 2: Run tests to verify they fail**

Run: `cd experiments/adr46-scanner && pytest tests/test_scan.py -v`
Expected: FAIL — `scanner.scan` does not exist

**Step 3: Write the implementation**

```python
# scanner/scan.py
from pathlib import Path

from scanner.config import ScannerConfig
from scanner.detector import Violation, detect_drift
from scanner.parser import parse_task


def scan_directory(root: Path, config: ScannerConfig) -> list[Violation]:
    violations = []
    for scan_path in config.scan_paths:
        search_root = root / scan_path
        if not search_root.exists():
            continue
        for yaml_file in search_root.rglob("*.yaml"):
            task = parse_task(yaml_file)
            if task is None:
                continue
            violations.extend(detect_drift(task, config))
    return violations
```

**Step 4: Run tests to verify they pass**

Run: `cd experiments/adr46-scanner && pytest tests/test_scan.py -v`
Expected: PASS (3 tests)

**Step 5: Commit**

```bash
git add experiments/adr46-scanner/
git commit -m "feat: add directory scanner to walk task YAML files"
```

---

### Task 6: CLI and report output

**Files:**
- Modify: `experiments/adr46-scanner/scanner/cli.py`
- Create: `experiments/adr46-scanner/tests/test_cli.py`

Wire everything together into the CLI. Output a human-readable report to stdout. Support `--json` for machine-readable output.

**Step 1: Write the failing tests**

```python
# tests/test_cli.py
import json
import subprocess
import sys
from pathlib import Path

import pytest


def _write_task(path, name, steps_yaml):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(
        f"apiVersion: tekton.dev/v1\nkind: Task\nmetadata:\n  name: {name}\nspec:\n  steps:\n{steps_yaml}"
    )


def _write_config(path, runner_image="quay.io/konflux-ci/task-runner", exempt=None):
    path.write_text(
        f"task_runner_image: {runner_image}\n"
        f"exempt_images: {json.dumps(exempt or [])}\n"
        f"scan_paths: ['task/']\n"
    )


def test_cli_reports_violations(tmp_path):
    _write_config(tmp_path / "config.yaml")
    task_dir = tmp_path / "task" / "t" / "0.1"
    _write_task(task_dir / "t.yaml", "t", '    - name: s1\n      image: quay.io/konflux-ci/oras:latest\n')

    result = subprocess.run(
        [sys.executable, "-m", "scanner.cli", "--config", str(tmp_path / "config.yaml"), str(tmp_path)],
        capture_output=True, text=True,
    )
    assert result.returncode == 1
    assert "t" in result.stdout
    assert "oras" in result.stdout


def test_cli_clean_exit_no_violations(tmp_path):
    _write_config(tmp_path / "config.yaml")
    task_dir = tmp_path / "task" / "t" / "0.1"
    _write_task(
        task_dir / "t.yaml", "t",
        '    - name: s1\n      image: quay.io/konflux-ci/task-runner:1.0\n',
    )

    result = subprocess.run(
        [sys.executable, "-m", "scanner.cli", "--config", str(tmp_path / "config.yaml"), str(tmp_path)],
        capture_output=True, text=True,
    )
    assert result.returncode == 0


def test_cli_json_output(tmp_path):
    _write_config(tmp_path / "config.yaml")
    task_dir = tmp_path / "task" / "t" / "0.1"
    _write_task(task_dir / "t.yaml", "t", '    - name: s1\n      image: quay.io/konflux-ci/oras:latest\n')

    result = subprocess.run(
        [sys.executable, "-m", "scanner.cli", "--config", str(tmp_path / "config.yaml"), "--json", str(tmp_path)],
        capture_output=True, text=True,
    )
    data = json.loads(result.stdout)
    assert len(data) == 1
    assert data[0]["step_name"] == "s1"
```

**Step 2: Run tests to verify they fail**

Run: `cd experiments/adr46-scanner && pytest tests/test_cli.py -v`
Expected: FAIL — cli.py is just a placeholder

**Step 3: Write the implementation**

```python
# scanner/cli.py
import argparse
import json
import sys
from dataclasses import asdict
from pathlib import Path

from scanner.config import load_config
from scanner.scan import scan_directory


def main():
    parser = argparse.ArgumentParser(
        description="Scan Tekton tasks for ADR-0046 drift (non-task-runner images)",
    )
    parser.add_argument("repo_path", help="Path to the build-definitions repo (or similar)")
    parser.add_argument("--config", required=True, help="Path to scanner config YAML")
    parser.add_argument("--json", dest="json_output", action="store_true", help="Output as JSON")
    args = parser.parse_args()

    config = load_config(args.config)
    violations = scan_directory(Path(args.repo_path), config)

    if args.json_output:
        print(json.dumps([asdict(v) for v in violations], indent=2))
    else:
        if not violations:
            print("No ADR-0046 drift detected.")
        else:
            print(f"Found {len(violations)} step(s) not using the task runner image:\n")
            for v in violations:
                print(f"  Task: {v.task_name}")
                print(f"  File: {v.task_file}")
                print(f"  Step: {v.step_name}")
                print(f"  Image: {v.current_image}")
                print()

    raise SystemExit(1 if violations else 0)


if __name__ == "__main__":
    main()
```

Also add `__main__.py` so `python -m scanner.cli` works:

Create `experiments/adr46-scanner/scanner/__main__.py`:

```python
from scanner.cli import main

main()
```

**Step 4: Run tests to verify they pass**

Run: `cd experiments/adr46-scanner && pytest tests/test_cli.py -v`
Expected: PASS (3 tests)

**Step 5: Run all tests**

Run: `cd experiments/adr46-scanner && pytest -v`
Expected: PASS (all 15 tests)

**Step 6: Commit**

```bash
git add experiments/adr46-scanner/
git commit -m "feat: wire up CLI with human-readable and JSON output"
```

---

### Task 7: Integration test against real modelcar-oci-ta task

**Files:**
- Create: `experiments/adr46-scanner/tests/fixtures/modelcar-oci-ta.yaml`
- Create: `experiments/adr46-scanner/tests/test_modelcar.py`

Copy the real modelcar-oci-ta task YAML into fixtures and verify the scanner catches the expected violations.

**Step 1: Fetch the real task YAML**

Run: `gh api repos/konflux-ci/build-definitions/contents/task/modelcar-oci-ta/0.1/modelcar-oci-ta.yaml -q '.content' | base64 -d > experiments/adr46-scanner/tests/fixtures/modelcar-oci-ta.yaml`

**Step 2: Write the test**

```python
# tests/test_modelcar.py
from pathlib import Path

from scanner.config import ScannerConfig
from scanner.detector import detect_drift
from scanner.parser import parse_task


FIXTURES = Path(__file__).parent / "fixtures"


def test_modelcar_oci_ta_drift():
    """The modelcar-oci-ta task should have violations per ADR-0046.

    Expected violations (steps NOT using task runner and NOT exempt):
    - download-model-files (oras)
    - create-modelcar-base-image (release-service-utils)
    - copy-model-files (ubi9/python-311)
    - push-image (oras)
    - sbom-generate (mobster)
    - upload-sbom (appstudio-utils)
    - report-sbom-url (yq)

    The only exempt step is use-trusted-artifact (build-trusted-artifacts).
    """
    config = ScannerConfig(
        task_runner_image="quay.io/konflux-ci/task-runner",
        exempt_images=["quay.io/konflux-ci/build-trusted-artifacts"],
    )
    task = parse_task(FIXTURES / "modelcar-oci-ta.yaml")
    assert task is not None
    violations = detect_drift(task, config)

    violating_steps = {v.step_name for v in violations}
    assert "use-trusted-artifact" not in violating_steps, "TA step should be exempt"
    assert "download-model-files" in violating_steps
    assert "create-modelcar-base-image" in violating_steps
    assert "copy-model-files" in violating_steps
    assert "push-image" in violating_steps
    assert "sbom-generate" in violating_steps
    assert "upload-sbom" in violating_steps
    assert "report-sbom-url" in violating_steps
    assert len(violations) == 7
```

**Step 3: Run the test**

Run: `cd experiments/adr46-scanner && pytest tests/test_modelcar.py -v`
Expected: PASS

**Step 4: Run all tests**

Run: `cd experiments/adr46-scanner && pytest -v`
Expected: PASS (all 16 tests)

**Step 5: Commit**

```bash
git add experiments/adr46-scanner/
git commit -m "test: verify scanner catches all modelcar-oci-ta ADR-0046 violations"
```

---

### Task 8: Document the experiment

**Files:**
- Create: `docs/experiments/001-adr46-drift-scanner.md`

**Step 1: Write the experiment log**

Document: hypothesis (automated drift detection against ADRs is feasible), setup (Python scanner, ADR-0046, modelcar-oci-ta task), method, results (which violations were found), and analysis (what this tells us about the architectural invariants problem).

Include the output of running the scanner against the real task.

**Step 2: Commit**

```bash
git add docs/experiments/
git commit -m "docs: add experiment log for adr46 drift scanner PoC"
```

---

## Future work (not in this plan)

These are natural follow-ons but out of scope for the PoC:

1. **GitHub issue filing** — `adr46-scan --file-issues` that uses `gh issue create` to file one issue per task with violations, including which tools are needed and whether the task runner already has them.

2. **Drift fixer agent** — takes a filed issue as input, modifies the task YAML to use the task runner image, identifies missing tools, and proposes additions to the task-runner repo. Opens PRs on both repos.

3. **Broader ADR coverage** — generalize beyond ADR-0046 to other architectural invariants.

4. **CI integration** — run the scanner in build-definitions CI to prevent new drift from being introduced.

---

Plan complete and saved to `docs/plans/2026-03-06-adr46-drift-scanner.md`. Two execution options:

**1. Subagent-Driven (this session)** - I dispatch fresh subagent per task, review between tasks, fast iteration

**2. Parallel Session (separate)** - Open new session with executing-plans, batch execution with checkpoints

Which approach?
