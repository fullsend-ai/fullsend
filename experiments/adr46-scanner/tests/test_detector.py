from pathlib import Path

import pytest
from scanner.config import ScannerConfig
from scanner.detector import detect_drift
from scanner.parser import StepImage, TektonTask


@pytest.fixture
def config():
    return ScannerConfig(
        task_runner_image="quay.io/konflux-ci/task-runner",
        exempt_images=["quay.io/konflux-ci/build-trusted-artifacts"],
    )


def _make_task(steps):
    return TektonTask(name="test-task", file_path=Path("task/test/0.1/test.yaml"), steps=steps)


def test_no_drift_when_using_task_runner(config):
    task = _make_task(
        [
            StepImage(
                name="work",
                full_ref="quay.io/konflux-ci/task-runner:1.0@sha256:abc",
                image_repo="quay.io/konflux-ci/task-runner",
            ),
        ]
    )
    violations = detect_drift(task, config)
    assert len(violations) == 0


def test_no_drift_for_exempt_image(config):
    task = _make_task(
        [
            StepImage(
                name="ta",
                full_ref="quay.io/konflux-ci/build-trusted-artifacts:latest@sha256:abc",
                image_repo="quay.io/konflux-ci/build-trusted-artifacts",
            ),
        ]
    )
    violations = detect_drift(task, config)
    assert len(violations) == 0


def test_drift_detected_for_non_runner_image(config):
    task = _make_task(
        [
            StepImage(
                name="pull",
                full_ref="quay.io/konflux-ci/oras:latest@sha256:abc",
                image_repo="quay.io/konflux-ci/oras",
            ),
        ]
    )
    violations = detect_drift(task, config)
    assert len(violations) == 1
    assert violations[0].step_name == "pull"
    assert violations[0].current_image == "quay.io/konflux-ci/oras"


def test_mixed_steps(config):
    task = _make_task(
        [
            StepImage(
                name="ta",
                full_ref="quay.io/konflux-ci/build-trusted-artifacts:latest",
                image_repo="quay.io/konflux-ci/build-trusted-artifacts",
            ),
            StepImage(
                name="work",
                full_ref="quay.io/konflux-ci/task-runner:1.0",
                image_repo="quay.io/konflux-ci/task-runner",
            ),
            StepImage(
                name="pull",
                full_ref="quay.io/konflux-ci/oras:latest",
                image_repo="quay.io/konflux-ci/oras",
            ),
            StepImage(
                name="report",
                full_ref="quay.io/konflux-ci/yq:latest",
                image_repo="quay.io/konflux-ci/yq",
            ),
        ]
    )
    violations = detect_drift(task, config)
    assert len(violations) == 2
    assert {v.step_name for v in violations} == {"pull", "report"}
