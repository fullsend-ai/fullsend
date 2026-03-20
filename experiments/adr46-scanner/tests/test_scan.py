from scanner.config import ScannerConfig
from scanner.scan import scan_directory


def _write_task(path, name, steps_yaml):
    path.parent.mkdir(parents=True, exist_ok=True)
    content = (
        f"apiVersion: tekton.dev/v1\nkind: Task\nmetadata:\n"
        f"  name: {name}\nspec:\n  steps:\n{steps_yaml}"
    )
    path.write_text(content)


def test_scan_finds_violations(tmp_path):
    task_dir = tmp_path / "task" / "my-task" / "0.1"
    _write_task(
        task_dir / "my-task.yaml",
        "my-task",
        "    - name: work\n      image: quay.io/konflux-ci/oras:latest\n",
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
