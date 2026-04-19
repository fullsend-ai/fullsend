#!/usr/bin/env python3
"""
REST API server for sandboxed agent execution.

Exposes a single endpoint: POST /run-agent
The server manages sandbox lifecycle on the host — creating, bootstrapping,
running, and cleaning up OpenShell sandboxes for each agent invocation.

The triage agent (inside its own sandbox) calls this via curl.
The orchestrator calls it to launch the top-level triage agent.

Usage:
  python3 agent_runner_server.py --port 8082

Requires environment variables:
  AGENT_RUNNER_WORKING_DIR: path to the experiment directory
  AGENT_RUNNER_OWNER: GitHub org/owner
  AGENT_RUNNER_REPO_NAME: GitHub repo name
  AGENT_RUNNER_ISSUE_NUMBER: issue number being triaged
"""

import json
import os
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
from socketserver import ThreadingMixIn

# Add this directory to sys.path so we can import runner and sandbox
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from runner import AgentRunner  # noqa: E402


def make_http_handler(runner: AgentRunner) -> type:
    """Create an HTTP handler with the runner bound."""

    class AgentRunnerHandler(BaseHTTPRequestHandler):
        def do_GET(self):
            if self.path == "/health":
                self._send_json(200, {"status": "ok"})
                return
            self.send_error(404, f"Not found: {self.path}")

        def do_POST(self):
            # Only accept /run-agent
            if self.path != "/run-agent":
                self.send_error(404, f"Not found: {self.path}")
                return

            content_length = int(self.headers.get("Content-Length", 0))
            body = self.rfile.read(content_length)

            try:
                params = json.loads(body)
            except json.JSONDecodeError:
                self.send_error(400, "Invalid JSON")
                return

            agent_name = params.get("agent_name", "")
            prompt = params.get("prompt", "")
            stream = params.get("stream", False)

            if not agent_name or not prompt:
                response = {"error": "agent_name and prompt are required"}
                self._send_json(400, response)
                return

            exit_code, output = runner.run_agent(agent_name, prompt, stream=stream)

            response = {
                "exit_code": exit_code,
                "output": output,
            }
            status = 200 if exit_code == 0 else 500
            self._send_json(status, response)

        def _send_json(self, status: int, data: dict) -> None:
            body = json.dumps(data).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, format, *args):
            print(f"[agent-runner] {args[0]}", file=sys.stderr)

    return AgentRunnerHandler


def main() -> None:
    import argparse

    parser = argparse.ArgumentParser(description="REST server for sandboxed agent execution")
    parser.add_argument(
        "--port",
        type=int,
        default=8082,
        help="HTTP port (default: 8082)",
    )
    args = parser.parse_args()

    working_dir = os.environ.get("AGENT_RUNNER_WORKING_DIR")
    owner = os.environ.get("AGENT_RUNNER_OWNER")
    repo_name = os.environ.get("AGENT_RUNNER_REPO_NAME")
    issue_number = os.environ.get("AGENT_RUNNER_ISSUE_NUMBER")

    for name, val in [
        ("AGENT_RUNNER_WORKING_DIR", working_dir),
        ("AGENT_RUNNER_OWNER", owner),
        ("AGENT_RUNNER_REPO_NAME", repo_name),
        ("AGENT_RUNNER_ISSUE_NUMBER", issue_number),
    ]:
        if not val:
            print(f"Error: {name} not set", file=sys.stderr)
            sys.exit(1)

    assert working_dir is not None
    assert owner is not None
    assert repo_name is not None
    assert issue_number is not None

    runner = AgentRunner(
        working_dir=Path(working_dir),
        owner=owner,
        repo_name=repo_name,
        issue_number=int(issue_number),
    )

    class ThreadingHTTPServer(ThreadingMixIn, HTTPServer):
        daemon_threads = True

    handler = make_http_handler(runner)
    server = ThreadingHTTPServer(
        ("0.0.0.0", args.port),  # nosec B104
        handler,
    )
    print(
        f"Agent runner server listening on http://0.0.0.0:{args.port}/",
        file=sys.stderr,
    )
    sys.stderr.flush()
    server.serve_forever()


if __name__ == "__main__":
    main()
