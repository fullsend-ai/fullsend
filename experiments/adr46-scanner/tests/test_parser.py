from pathlib import Path

from scanner.parser import parse_task

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
