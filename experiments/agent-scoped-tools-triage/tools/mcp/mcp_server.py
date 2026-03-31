#!/usr/bin/env python3
"""
MCP server that exposes scoped GitHub tools for the triage agent.

The server holds the GitHub token internally — the agent never sees it.
Each tool enforces its own constraints (input validation, credential
scanning, output sanitization) before making any API call.

Supports two transport modes:
- stdio (default): reads JSON-RPC from stdin, writes to stdout
- http: runs an HTTP server for streamable-http MCP transport

Usage:
  python3 mcp_server.py                    # stdio mode
  python3 mcp_server.py --http --port 8080 # HTTP mode
"""

import argparse
import json
import os
import re
import subprocess
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer


def gh(args: list[str], token: str) -> subprocess.CompletedProcess:
    """Run a gh CLI command with the given token."""
    env = {**os.environ, "GH_TOKEN": token}
    return subprocess.run(
        ["gh", *args],  # nosec B607
        env=env,
        capture_output=True,
        text=True,
        timeout=30,
    )


# --- Tool implementations ---


def read_issue(params: dict, token: str, allowed_repo: str) -> dict:
    """Read an issue's title, body, labels, and author."""
    repo = params.get("repo", "")
    issue_number = params.get("issue_number")

    if repo != allowed_repo:
        return {"error": f"Access denied: can only read issues in {allowed_repo}"}

    result = gh(
        ["issue", "view", str(issue_number), "--repo", repo, "--json", "title,body,labels,author"],
        token,
    )

    if result.returncode != 0:
        return {"error": result.stderr.strip()}

    data = json.loads(result.stdout)

    # Sanitize: strip HTML comments from body
    if "body" in data and data["body"]:
        data["body"] = re.sub(r"<!--.*?-->", "", data["body"], flags=re.DOTALL)

    return data


def list_issues(params: dict, token: str, allowed_repo: str) -> dict:
    """Search issues in the repository."""
    repo = params.get("repo", "")
    query = params.get("query", "")

    if repo != allowed_repo:
        return {"error": f"Access denied: can only list issues in {allowed_repo}"}

    args = ["issue", "list", "--repo", repo, "--json", "number,title,labels,state", "--limit", "20"]
    if query:
        args.extend(["--search", query])

    result = gh(args, token)

    if result.returncode != 0:
        return {"error": result.stderr.strip()}

    return json.loads(result.stdout)


def comment_issue(params: dict, token: str, allowed_repo: str) -> dict:
    """Add a comment to an issue with validation."""
    repo = params.get("repo", "")
    issue_number = params.get("issue_number")
    body = params.get("body", "")

    if repo != allowed_repo:
        return {"error": f"Access denied: can only comment in {allowed_repo}"}

    # Constraint: truncate long comments
    if len(body) > 4096:
        body = body[:4096] + "\n\n[truncated by tool: exceeded 4096 character limit]"

    # Constraint: scan for credential patterns
    credential_patterns = [
        r"ghp_[a-zA-Z0-9]{36}",
        r"ghs_[a-zA-Z0-9]+",
        r"github_pat_",
        r"-----BEGIN .* KEY-----",
        r"sk-[a-zA-Z0-9]{20,}",
    ]
    for pattern in credential_patterns:
        if re.search(pattern, body, re.IGNORECASE):
            return {"error": "Comment body appears to contain credentials. Refusing to post."}

    result = gh(
        ["issue", "comment", str(issue_number), "--repo", repo, "--body", body],
        token,
    )

    if result.returncode != 0:
        return {"error": result.stderr.strip()}

    return {"status": "ok", "output": result.stdout.strip()}


def add_label(params: dict, token: str, allowed_repo: str) -> dict:
    """Add labels to an issue."""
    repo = params.get("repo", "")
    issue_number = params.get("issue_number")
    labels = params.get("labels", "")

    if repo != allowed_repo:
        return {"error": f"Access denied: can only label issues in {allowed_repo}"}

    result = gh(
        ["issue", "edit", str(issue_number), "--repo", repo, "--add-label", labels],
        token,
    )

    if result.returncode != 0:
        return {"error": result.stderr.strip()}

    # Return updated labels
    view_result = gh(
        ["issue", "view", str(issue_number), "--repo", repo, "--json", "labels"],
        token,
    )

    if view_result.returncode != 0:
        return {"status": "ok", "labels": labels}

    return json.loads(view_result.stdout)


# --- MCP protocol ---

TOOLS = {
    "read_issue": {
        "description": "Read the title, body, labels, and author of a single issue",
        "inputSchema": {
            "type": "object",
            "properties": {
                "issue_number": {
                    "type": "integer",
                    "description": "Issue number",
                },
                "repo": {
                    "type": "string",
                    "description": "Repository in org/repo format",
                },
            },
            "required": ["issue_number", "repo"],
        },
        "handler": read_issue,
    },
    "list_issues": {
        "description": "Search issues in the repository, useful for duplicate detection",
        "inputSchema": {
            "type": "object",
            "properties": {
                "repo": {
                    "type": "string",
                    "description": "Repository in org/repo format",
                },
                "query": {
                    "type": "string",
                    "description": "Search terms to filter issues",
                },
            },
            "required": ["repo"],
        },
        "handler": list_issues,
    },
    "comment_issue": {
        "description": (
            "Add a comment to an issue. "
            "Body is validated for credentials and truncated at 4096 chars."
        ),
        "inputSchema": {
            "type": "object",
            "properties": {
                "issue_number": {
                    "type": "integer",
                    "description": "Issue number",
                },
                "repo": {
                    "type": "string",
                    "description": "Repository in org/repo format",
                },
                "body": {
                    "type": "string",
                    "description": "Comment body in markdown",
                },
            },
            "required": ["issue_number", "repo", "body"],
        },
        "handler": comment_issue,
    },
    "add_label": {
        "description": "Add one or more labels to an issue. Can only add, not remove.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "issue_number": {
                    "type": "integer",
                    "description": "Issue number",
                },
                "repo": {
                    "type": "string",
                    "description": "Repository in org/repo format",
                },
                "labels": {
                    "type": "string",
                    "description": "Comma-separated list of labels to add",
                },
            },
            "required": ["issue_number", "repo", "labels"],
        },
        "handler": add_label,
    },
}


def handle_request(request: dict, token: str, allowed_repo: str) -> dict:
    """Handle a single JSON-RPC request."""
    method = request.get("method", "")
    req_id = request.get("id")
    params = request.get("params", {})

    if method == "initialize":
        return {
            "jsonrpc": "2.0",
            "id": req_id,
            "result": {
                "protocolVersion": "2024-11-05",
                "capabilities": {"tools": {"listChanged": False}},
                "serverInfo": {
                    "name": "fullsend-triage-tools",
                    "version": "0.1.0",
                },
            },
        }

    if method == "notifications/initialized":
        return None  # notification, no response

    if method == "tools/list":
        tools_list = []
        for name, tool in TOOLS.items():
            tools_list.append(
                {
                    "name": name,
                    "description": tool["description"],
                    "inputSchema": tool["inputSchema"],
                }
            )
        return {
            "jsonrpc": "2.0",
            "id": req_id,
            "result": {"tools": tools_list},
        }

    if method == "tools/call":
        tool_name = params.get("name", "")
        arguments = params.get("arguments", {})

        if tool_name not in TOOLS:
            return {
                "jsonrpc": "2.0",
                "id": req_id,
                "result": {
                    "content": [{"type": "text", "text": f"Unknown tool: {tool_name}"}],
                    "isError": True,
                },
            }

        handler = TOOLS[tool_name]["handler"]
        result = handler(arguments, token, allowed_repo)

        return {
            "jsonrpc": "2.0",
            "id": req_id,
            "result": {
                "content": [{"type": "text", "text": json.dumps(result, indent=2)}],
                "isError": "error" in result,
            },
        }

    return {
        "jsonrpc": "2.0",
        "id": req_id,
        "error": {"code": -32601, "message": f"Unknown method: {method}"},
    }


def make_http_handler(token: str, allowed_repo: str) -> type:
    """Create an HTTP request handler class with the token and repo bound."""

    class MCPHandler(BaseHTTPRequestHandler):
        def do_POST(self):
            content_length = int(self.headers.get("Content-Length", 0))
            body = self.rfile.read(content_length)

            try:
                request = json.loads(body)
            except json.JSONDecodeError:
                self.send_error(400, "Invalid JSON")
                return

            response = handle_request(request, token, allowed_repo)

            if response is None:
                self.send_response(204)
                self.end_headers()
                return

            response_body = json.dumps(response).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(response_body)))
            self.end_headers()
            self.wfile.write(response_body)

        def log_message(self, format, *args):
            print(f"[mcp-server] {args[0]}", file=sys.stderr)

    return MCPHandler


def run_stdio(token: str, allowed_repo: str) -> None:
    """Run MCP server over stdio."""
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue

        try:
            request = json.loads(line)
        except json.JSONDecodeError:
            continue

        response = handle_request(request, token, allowed_repo)

        if response is not None:
            sys.stdout.write(json.dumps(response) + "\n")
            sys.stdout.flush()


def run_http(token: str, allowed_repo: str, port: int) -> None:
    """Run MCP server over HTTP (streamable-http transport)."""
    handler = make_http_handler(token, allowed_repo)
    server = HTTPServer(("0.0.0.0", port), handler)  # nosec B104
    print(f"MCP server listening on http://0.0.0.0:{port}/", file=sys.stderr)
    sys.stderr.flush()
    server.serve_forever()


def main() -> None:
    parser = argparse.ArgumentParser(description="MCP server for GitHub triage tools")
    parser.add_argument("--http", action="store_true", help="Run as HTTP server")
    parser.add_argument("--port", type=int, default=8081, help="HTTP port (default: 8081)")
    args = parser.parse_args()

    token = os.environ.get("MCP_GH_TOKEN")
    allowed_repo = os.environ.get("MCP_ALLOWED_REPO")

    if not token:
        print("Error: MCP_GH_TOKEN not set", file=sys.stderr)
        sys.exit(1)
    if not allowed_repo:
        print("Error: MCP_ALLOWED_REPO not set", file=sys.stderr)
        sys.exit(1)

    if args.http:
        run_http(token, allowed_repo, args.port)
    else:
        run_stdio(token, allowed_repo)


if __name__ == "__main__":
    main()
