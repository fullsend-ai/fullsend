"""OpenShell sandbox lifecycle: create, delete, policy, SSH, SCP, transcripts."""

import os
import subprocess
import sys
import tempfile
import time
from pathlib import Path

from . import LOG_DIR, SANDBOX_CLAUDE_CONFIG


def create_sandbox(name: str) -> None:
    """Create a persistent OpenShell sandbox and wait for it to be ready."""
    result = subprocess.run(
        [  # nosec B607
            "timeout",
            "60",
            "openshell",
            "sandbox",
            "create",
            "--name",
            name,
            "--keep",
            "--no-auto-providers",
            "--no-tty",
        ],
        stdin=subprocess.DEVNULL,
        stderr=subprocess.PIPE,
        timeout=65,
    )
    # timeout exits 124 — sandbox create may exit non-zero after the
    # interactive shell is killed. Check if the sandbox actually exists.
    if result.returncode not in (0, 124):
        check = subprocess.run(
            ["openshell", "sandbox", "get", name],  # nosec B607
            capture_output=True,
            timeout=10,
        )
        if check.returncode != 0:
            raise RuntimeError(f"Sandbox create failed: {result.stderr.decode()}")

    # Wait for sandbox to be fully ready (image pull can take a while)
    for _ in range(30):
        check = subprocess.run(
            ["openshell", "sandbox", "get", name],  # nosec B607
            capture_output=True,
            text=True,
            timeout=10,
        )
        if check.returncode == 0 and "Ready" in check.stdout:
            return
        time.sleep(2)
    raise RuntimeError(f"Sandbox '{name}' not ready after 60s")


def apply_policy(sandbox_name: str, policy_path: str) -> None:
    """Apply a policy to a sandbox, retrying up to 3 times."""
    for attempt in range(1, 4):
        result = subprocess.run(
            [  # nosec B607
                "openshell",
                "policy",
                "set",
                sandbox_name,
                "--policy",
                policy_path,
                "--wait",
            ],
            capture_output=True,
            text=True,
            timeout=30,
        )
        if result.returncode == 0:
            return
        print(f"  Policy attempt {attempt} failed, retrying in 3s...", file=sys.stderr)
        time.sleep(3)
    raise RuntimeError("Policy set failed after 3 attempts")


def get_ssh_config(sandbox_name: str) -> str:
    """Get SSH config for a sandbox, return as string."""
    result = subprocess.run(
        ["openshell", "sandbox", "ssh-config", sandbox_name],  # nosec B607
        capture_output=True,
        text=True,
        timeout=10,
        check=True,
    )
    return result.stdout


def sandbox_scp(ssh_config_path: str, sandbox_name: str, local: str, remote: str) -> None:
    """Copy a file or directory into a sandbox."""
    subprocess.run(
        [  # nosec B607
            "scp",
            "-F",
            ssh_config_path,
            "-r",
            str(local),
            f"openshell-{sandbox_name}:{remote}",
        ],
        check=True,
        timeout=60,
    )


def sandbox_ssh(
    ssh_config_path: str, sandbox_name: str, cmd: str, timeout: int = 30
) -> subprocess.CompletedProcess:
    """Run a command inside a sandbox."""
    return subprocess.run(
        ["ssh", "-F", ssh_config_path, f"openshell-{sandbox_name}", cmd],  # nosec B607
        capture_output=True,
        text=True,
        timeout=timeout,
    )


def extract_transcripts(
    ssh_config_path: str,
    sandbox_name: str,
    agent_name: str,
) -> None:
    """Copy Claude transcript files out of a sandbox before it's deleted."""
    os.makedirs(LOG_DIR, exist_ok=True)

    # Find transcript files in the known CLAUDE_CONFIG_DIR path
    result = sandbox_ssh(
        ssh_config_path,
        sandbox_name,
        f"find {SANDBOX_CLAUDE_CONFIG} -name '*.jsonl' 2>/dev/null || true",
        timeout=10,
    )
    files = [f.strip() for f in result.stdout.strip().split("\n") if f.strip()]
    if not files:
        print(f"  [{agent_name}] No transcripts found")
        return

    for remote_path in files:
        local_name = f"{agent_name}-{os.path.basename(remote_path)}"
        local_path = os.path.join(LOG_DIR, local_name)
        try:
            subprocess.run(
                [  # nosec B607
                    "scp",
                    "-F",
                    ssh_config_path,
                    "-r",
                    f"openshell-{sandbox_name}:{remote_path}",
                    local_path,
                ],
                check=True,
                timeout=30,
                capture_output=True,
            )
            print(f"  [{agent_name}] Saved transcript: {local_name}")
        except Exception as e:
            print(f"  [{agent_name}] Failed to copy transcript: {e}", file=sys.stderr)


def render_policy(
    template_path: Path,
    owner: str,
    repo_name: str,
    issue_number: int,
) -> str:
    """Render a policy template and return the temp file path."""
    with open(template_path) as f:
        content = f.read()
    content = (
        content.replace("{{OWNER}}", owner)
        .replace("{{REPO_NAME}}", repo_name)
        .replace("{{ISSUE_NUMBER}}", str(issue_number))
    )
    with tempfile.NamedTemporaryFile(
        mode="w",
        prefix="policy_",
        suffix=".yaml",
        delete=False,
    ) as tmp:
        tmp.write(content)
        return tmp.name


def delete_sandbox(name: str) -> None:
    """Delete a sandbox, ignoring errors."""
    subprocess.run(
        ["openshell", "sandbox", "delete", name],  # nosec B607
        capture_output=True,
        timeout=10,
    )
