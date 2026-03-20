import json
import subprocess
import sys


def _write_task(path, name, steps_yaml):
    path.parent.mkdir(parents=True, exist_ok=True)
    content = (
        f"apiVersion: tekton.dev/v1\nkind: Task\nmetadata:\n"
        f"  name: {name}\nspec:\n  steps:\n{steps_yaml}"
    )
    path.write_text(content)


def _write_config(path, runner_image="quay.io/konflux-ci/task-runner", exempt=None):
    path.write_text(
        f"task_runner_image: {runner_image}\n"
        f"exempt_images: {json.dumps(exempt or [])}\n"
        f"scan_paths: ['task/']\n"
    )


def test_cli_reports_violations(tmp_path):
    _write_config(tmp_path / "config.yaml")
    task_dir = tmp_path / "task" / "t" / "0.1"
    _write_task(
        task_dir / "t.yaml",
        "t",
        "    - name: s1\n      image: quay.io/konflux-ci/oras:latest\n",
    )

    result = subprocess.run(
        [
            sys.executable,
            "-m",
            "scanner.cli",
            "--config",
            str(tmp_path / "config.yaml"),
            str(tmp_path),
        ],
        capture_output=True,
        text=True,
        cwd="/home/rbean/code/fullsend/experiments/adr46-scanner",
    )
    assert result.returncode == 1
    assert "t" in result.stdout
    assert "oras" in result.stdout


def test_cli_clean_exit_no_violations(tmp_path):
    _write_config(tmp_path / "config.yaml")
    task_dir = tmp_path / "task" / "t" / "0.1"
    _write_task(
        task_dir / "t.yaml",
        "t",
        "    - name: s1\n      image: quay.io/konflux-ci/task-runner:1.0\n",
    )

    result = subprocess.run(
        [
            sys.executable,
            "-m",
            "scanner.cli",
            "--config",
            str(tmp_path / "config.yaml"),
            str(tmp_path),
        ],
        capture_output=True,
        text=True,
        cwd="/home/rbean/code/fullsend/experiments/adr46-scanner",
    )
    assert result.returncode == 0


def test_cli_json_output(tmp_path):
    _write_config(tmp_path / "config.yaml")
    task_dir = tmp_path / "task" / "t" / "0.1"
    _write_task(
        task_dir / "t.yaml",
        "t",
        "    - name: s1\n      image: quay.io/konflux-ci/oras:latest\n",
    )

    result = subprocess.run(
        [
            sys.executable,
            "-m",
            "scanner.cli",
            "--config",
            str(tmp_path / "config.yaml"),
            "--json",
            str(tmp_path),
        ],
        capture_output=True,
        text=True,
        cwd="/home/rbean/code/fullsend/experiments/adr46-scanner",
    )
    data = json.loads(result.stdout)
    assert len(data) == 1
    assert data[0]["step_name"] == "s1"
