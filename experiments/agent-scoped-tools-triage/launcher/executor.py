"""Subagent executor: runs subagents in sandboxed environments on the host.

The triage agent (inside its own sandbox) calls this executor via HTTP.
The executor creates a sandbox for the requested subagent, runs it,
and returns the output. This avoids nested sandbox creation issues
(gateway auth not available from inside a sandbox).
"""

import json
import os
import re
import subprocess
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

from . import EXECUTOR_PORT, SANDBOX_CLAUDE_CONFIG, SANDBOX_WORKSPACE
from .auth import bootstrap_vertex_creds
from .sandbox import (
    apply_policy,
    create_sandbox,
    delete_sandbox,
    extract_transcripts,
    get_ssh_config,
    render_policy,
)


def discover_agents(working_dir: Path) -> dict[str, str | None]:
    """Read agent definitions and return {name: policy_relative_path}."""
    agents = {}
    for agent_file in (working_dir / "agents").glob("*.md"):
        name = agent_file.stem
        with open(agent_file) as f:
            content = f.read()
        match = re.search(r"^sandbox:\s*(.+)$", content, re.MULTILINE)
        policy = match.group(1).strip() if match else None
        agents[name] = policy
    return agents


def bootstrap_agent_sandbox(
    ssh_config_path: str,
    sandbox_name: str,
    working_dir: Path,
    mcp_config_path: str,
) -> None:
    """Copy claude binary, agent files, skills, and MCP config into a sandbox."""
    import shutil

    from .sandbox import sandbox_scp, sandbox_ssh

    def scp(local, remote):
        sandbox_scp(ssh_config_path, sandbox_name, local, remote)

    def ssh(cmd):
        return sandbox_ssh(ssh_config_path, sandbox_name, cmd)

    claude_bin = shutil.which("claude")
    if not claude_bin:
        raise RuntimeError("claude binary not found in PATH")

    # Create workspace structure
    ssh(
        f"mkdir -p {SANDBOX_WORKSPACE}/.claude/agents "
        f"{SANDBOX_WORKSPACE}/.claude/skills "
        f"{SANDBOX_WORKSPACE}/bin"
    )

    # Copy claude binary
    scp(claude_bin, f"{SANDBOX_WORKSPACE}/bin/claude")
    ssh(f"chmod +x {SANDBOX_WORKSPACE}/bin/claude")

    # Copy agent definitions
    for agent_file in (working_dir / "agents").glob("*.md"):
        scp(str(agent_file), f"{SANDBOX_WORKSPACE}/.claude/agents/")

    # Copy skills
    for skill_dir in (working_dir / "skills").iterdir():
        if skill_dir.is_dir():
            scp(str(skill_dir), f"{SANDBOX_WORKSPACE}/.claude/skills/")

    # Copy MCP config
    scp(mcp_config_path, f"{SANDBOX_WORKSPACE}/mcp_config.json")


class SubagentExecutor:
    """Runs subagents in sandboxed environments on the host."""

    def __init__(
        self, working_dir: Path, mcp_config_path: str, owner: str, repo_name: str, issue_number: int
    ):
        self.working_dir = working_dir
        self.mcp_config_path = mcp_config_path
        self.owner = owner
        self.repo_name = repo_name
        self.issue_number = issue_number
        self.agents = discover_agents(working_dir)

    def run_agent(self, agent_name: str, prompt: str) -> tuple[int, str]:
        """Run an agent in a fresh sandbox. Returns (exit_code, output)."""
        if agent_name not in self.agents:
            return 1, f"Unknown agent: {agent_name}"
        if agent_name == "triage":
            return 1, "Cannot run triage agent as a subagent"

        policy_rel = self.agents[agent_name]
        if not policy_rel:
            return 1, f"No sandbox policy defined for agent '{agent_name}'"

        policy_template = self.working_dir / policy_rel
        if not policy_template.exists():
            return 1, f"Policy template not found: {policy_template}"

        sandbox_name = f"sub-{agent_name}-{os.getpid()}-{int(time.time())}"
        ssh_config_path = f"/tmp/openshell-ssh-{sandbox_name}.config"  # nosec B108
        policy_path = None

        try:
            # 1. Render policy
            policy_path = render_policy(
                policy_template,
                self.owner,
                self.repo_name,
                self.issue_number,
            )

            # 2. Create sandbox
            print(f"[executor] Creating sandbox for '{agent_name}'...", file=sys.stderr)
            create_sandbox(sandbox_name)

            # 3. Apply policy
            print(f"[executor] Applying policy for '{agent_name}'...", file=sys.stderr)
            apply_policy(sandbox_name, policy_path)

            # 4. Get SSH config
            ssh_config = get_ssh_config(sandbox_name)
            with open(ssh_config_path, "w") as f:
                f.write(ssh_config)

            # 5. Bootstrap sandbox
            print(f"[executor] Bootstrapping '{agent_name}'...", file=sys.stderr)
            bootstrap_agent_sandbox(
                ssh_config_path,
                sandbox_name,
                self.working_dir,
                self.mcp_config_path,
            )

            # 5b. Copy Vertex AI credentials
            vertex_exports = bootstrap_vertex_creds(ssh_config_path, sandbox_name)

            # 6. Run agent
            print(f"[executor] Running '{agent_name}'...", file=sys.stderr)
            mcp_config = f"{SANDBOX_WORKSPACE}/mcp_config.json"
            result = subprocess.run(
                [  # nosec B607
                    "ssh",
                    "-F",
                    ssh_config_path,
                    f"openshell-{sandbox_name}",
                    f"cd {SANDBOX_WORKSPACE} && "
                    f"export PATH={SANDBOX_WORKSPACE}/bin:$PATH && "
                    f"export CLAUDE_CONFIG_DIR={SANDBOX_CLAUDE_CONFIG} && "
                    f"{vertex_exports}"
                    f"claude --print --agent '{agent_name}' "
                    f"--mcp-config '{mcp_config}' "
                    f"--strict-mcp-config --dangerously-skip-permissions "
                    f"'{prompt}'",
                ],
                capture_output=True,
                text=True,
                timeout=300,
            )
            print(
                f"[executor] '{agent_name}' exited with code {result.returncode}", file=sys.stderr
            )
            return result.returncode, result.stdout

        except Exception as e:
            return 1, f"Error running '{agent_name}': {e}"
        finally:
            # Extract transcripts before deleting the sandbox
            if os.path.exists(ssh_config_path):
                try:
                    extract_transcripts(ssh_config_path, sandbox_name, agent_name)
                except Exception as e:
                    print(
                        f"[executor] Failed to extract transcripts for '{agent_name}': {e}",
                        file=sys.stderr,
                    )
            delete_sandbox(sandbox_name)
            if policy_path and os.path.exists(policy_path):
                os.unlink(policy_path)
            if os.path.exists(ssh_config_path):
                os.unlink(ssh_config_path)


def _make_handler(executor: SubagentExecutor) -> type:
    """Create an HTTP handler for the subagent executor."""

    class ExecutorHandler(BaseHTTPRequestHandler):
        def do_POST(self):
            # URL format: /run/<agent-name>
            path_match = re.match(r"^/run/([a-zA-Z0-9_-]+)$", self.path)
            if not path_match:
                self.send_error(404, "Use POST /run/<agent-name>")
                return

            agent_name = path_match.group(1)
            content_length = int(self.headers.get("Content-Length", 0))
            prompt = self.rfile.read(content_length).decode("utf-8")

            if not prompt:
                self.send_error(400, "Prompt is required in request body")
                return

            exit_code, output = executor.run_agent(agent_name, prompt)

            response = json.dumps(
                {
                    "exit_code": exit_code,
                    "output": output,
                }
            ).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(response)))
            self.end_headers()
            self.wfile.write(response)

        def log_message(self, format, *args):
            print(f"[executor] {args[0]}", file=sys.stderr)

    return ExecutorHandler


def start_executor(executor: SubagentExecutor) -> HTTPServer:
    """Start the subagent executor HTTP server in a background thread."""
    handler = _make_handler(executor)
    server = HTTPServer(("0.0.0.0", EXECUTOR_PORT), handler)  # nosec B104
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    print(f"[executor] Listening on http://0.0.0.0:{EXECUTOR_PORT}/", file=sys.stderr)
    return server
