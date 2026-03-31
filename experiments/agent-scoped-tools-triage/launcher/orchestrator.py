"""Orchestrator: starts REST servers and launches the triage agent via the agent runner."""

import json
import os
import shutil
import subprocess
import sys
import time
import urllib.request
from pathlib import Path

from . import AGENT_RUNNER_PORT, GH_SERVER_PORT


def _wait_for_server(port: int, label: str, timeout: int = 10) -> None:
    """Wait for a REST server to respond on the given port."""
    for _ in range(timeout * 2):
        try:
            req = urllib.request.Request(
                f"http://localhost:{port}/health",
                method="GET",
            )
            urllib.request.urlopen(req, timeout=1)  # nosec B310
            return
        except Exception:
            time.sleep(0.5)
    raise RuntimeError(f"{label} failed to start on port {port}")


def _start_gh_server(token: str, repo: str, working_dir: Path) -> subprocess.Popen:
    """Start the GitHub REST server as a background HTTP process."""
    server_path = working_dir / "tools" / "gh-server" / "gh_server.py"
    env = {
        **os.environ,
        "GH_TOKEN": token,
        "GH_ALLOWED_REPO": repo,
    }
    process = subprocess.Popen(
        [  # nosec B607
            "python3",
            str(server_path),
            "--port",
            str(GH_SERVER_PORT),
        ],
        env=env,
        stdout=subprocess.DEVNULL,
        stderr=sys.stderr,
    )
    _wait_for_server(GH_SERVER_PORT, "GitHub REST server")
    return process


def _start_agent_runner_server(
    working_dir: Path,
    owner: str,
    repo_name: str,
    issue_number: int,
) -> subprocess.Popen:
    """Start the agent runner REST server as a background HTTP process."""
    server_path = working_dir / "tools" / "agent-runner" / "agent_runner_server.py"
    env = {
        **os.environ,
        "AGENT_RUNNER_WORKING_DIR": str(working_dir),
        "AGENT_RUNNER_OWNER": owner,
        "AGENT_RUNNER_REPO_NAME": repo_name,
        "AGENT_RUNNER_ISSUE_NUMBER": str(issue_number),
    }
    process = subprocess.Popen(
        [  # nosec B607
            "python3",
            str(server_path),
            "--port",
            str(AGENT_RUNNER_PORT),
        ],
        env=env,
        stdout=subprocess.DEVNULL,
        stderr=sys.stderr,
    )
    _wait_for_server(AGENT_RUNNER_PORT, "Agent runner server")
    return process


def launch_agent(
    token: str,
    repo: str,
    issue_number: int,
    working_dir: Path,
) -> None:
    """Launch the top-level triage agent in an OpenShell sandbox."""

    owner, repo_name = repo.split("/", 1)
    prompt = f"Triage issue #{issue_number} in {repo}."

    # Check prerequisites
    if not shutil.which("openshell"):
        print("Error: OpenShell is not installed", file=sys.stderr)
        sys.exit(1)
    result = subprocess.run(
        ["openshell", "status"],  # nosec B607
        capture_output=True,
        timeout=10,
    )
    if result.returncode != 0:
        print(
            "Error: OpenShell gateway is not running",
            file=sys.stderr,
        )
        sys.exit(1)

    gh_server_process = None
    runner_process = None

    def cleanup():
        if runner_process:
            runner_process.terminate()
            runner_process.wait(timeout=5)
        if gh_server_process:
            gh_server_process.terminate()
            gh_server_process.wait(timeout=5)

    try:
        # 1. Start GitHub REST server
        gh_server_process = _start_gh_server(token, repo, working_dir)

        # 2. Start agent runner REST server
        runner_process = _start_agent_runner_server(
            working_dir,
            owner,
            repo_name,
            issue_number,
        )

        # --- Log setup ---
        print(f"Triage: {repo}#{issue_number}")
        print(
            f"  GH server: http://host.docker.internal:{GH_SERVER_PORT}/"
            f" (pid {gh_server_process.pid})"
        )
        print(
            f"  Runner:    http://host.docker.internal"
            f":{AGENT_RUNNER_PORT}/"
            f" (pid {runner_process.pid})"
        )
        print("---")
        sys.stdout.flush()

        # 3. Run triage agent via the agent runner REST server
        #    (same sandbox lifecycle as subagents — the server
        #    is the single entry point for all agent execution)
        request_body = {
            "agent_name": "triage",
            "prompt": prompt,
            "stream": True,
        }
        req = urllib.request.Request(
            f"http://localhost:{AGENT_RUNNER_PORT}/run-agent",
            data=json.dumps(request_body).encode(),
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        resp = urllib.request.urlopen(req, timeout=660)  # nosec B310
        result = json.loads(resp.read().decode())

        if result.get("exit_code", 1) != 0:
            print(f"\nAgent failed: {result.get('output', '')}", file=sys.stderr)
            sys.exit(1)
    finally:
        cleanup()
