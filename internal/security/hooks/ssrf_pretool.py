#!/usr/bin/env python3
"""Claude Code PreToolUse hook for SSRF protection.

Intercepts Bash and WebFetch tool calls to validate URLs against RFC 1918
private networks, cloud metadata endpoints, and dangerous schemes before
the agent can make outbound requests.

Protocol: reads JSON from stdin, writes JSON to stdout.
Exit codes: 0 = allow, 1 = block (with reason on stdout).
"""

from __future__ import annotations

import ipaddress
import json
import os
import re
import socket
import sys
from datetime import UTC, datetime

# --- Blocklists ---

BLOCKED_HOSTNAMES: set[str] = {
    "metadata.google.internal",
    "metadata.goog",
    "169.254.169.254",
    "100.100.100.200",
    "fd00:ec2::254",
}

BLOCKED_NETWORKS: list[ipaddress.IPv4Network | ipaddress.IPv6Network] = [
    ipaddress.IPv4Network("10.0.0.0/8"),
    ipaddress.IPv4Network("172.16.0.0/12"),
    ipaddress.IPv4Network("192.168.0.0/16"),
    ipaddress.IPv4Network("127.0.0.0/8"),
    ipaddress.IPv6Network("::1/128"),
    ipaddress.IPv4Network("169.254.0.0/16"),
    ipaddress.IPv6Network("fe80::/10"),
    ipaddress.IPv4Network("100.64.0.0/10"),
    ipaddress.IPv4Network("0.0.0.0/8"),
    ipaddress.IPv6Network("::/128"),
    ipaddress.IPv6Network("fc00::/7"),
]

BLOCKED_SCHEMES: set[str] = {"file", "ftp", "gopher", "data", "dict", "ldap", "tftp"}
ALLOWED_SCHEMES: set[str] = {"http", "https"}

URL_PATTERN = re.compile(
    r"""(?:https?|file|ftp|gopher|data|dict|ldap|tftp)://[^\s"'`|;<>()]+""",
    re.IGNORECASE,
)

FINDINGS_PATH = "/tmp/workspace/.security/findings.jsonl"


def log_finding(scanner: str, name: str, severity: str, detail: str, action: str):
    """Append a finding to the JSONL audit log."""
    trace_id = os.environ.get("FULLSEND_TRACE_ID", "")
    finding = {
        "trace_id": trace_id,
        "timestamp": datetime.now(UTC).isoformat(),
        "phase": "hook_pretool",
        "scanner": scanner,
        "name": name,
        "severity": severity,
        "detail": detail,
        "action": action,
    }
    try:
        with open(FINDINGS_PATH, "a") as f:
            f.write(json.dumps(finding) + "\n")
    except OSError:
        pass


def check_ip(ip_str: str) -> str | None:
    try:
        ip = ipaddress.ip_address(ip_str)
    except ValueError:
        return None
    for network in BLOCKED_NETWORKS:
        if ip in network:
            return f"IP {ip} is in blocked network {network}"
    if ip.is_private:
        return f"IP {ip} is a private address"
    return None


def validate_url(url: str) -> str | None:
    try:
        from urllib.parse import urlparse

        parsed = urlparse(url)
    except Exception:
        return "Malformed URL"

    scheme = (parsed.scheme or "").lower()
    if scheme in BLOCKED_SCHEMES:
        return f"Blocked scheme: {scheme}"
    if scheme not in ALLOWED_SCHEMES:
        return f"Disallowed scheme: {scheme}"

    hostname = (parsed.hostname or "").lower().rstrip(".")
    if not hostname:
        return "No hostname in URL"
    if hostname in BLOCKED_HOSTNAMES:
        return f"Blocked hostname: {hostname}"

    ip_reason = check_ip(hostname)
    if ip_reason:
        return ip_reason

    # DNS rebinding defense: resolve hostname and check resolved IPs
    try:
        addrinfos = socket.getaddrinfo(hostname, None, proto=socket.IPPROTO_TCP)
        for _family, _, _, _, sockaddr in addrinfos:
            resolved_ip = str(sockaddr[0])
            ip_reason = check_ip(resolved_ip)
            if ip_reason:
                return f"DNS rebinding: {hostname} resolved to blocked {resolved_ip} ({ip_reason})"
    except socket.gaierror:
        # DNS resolution failed — fail closed
        return f"DNS resolution failed for {hostname} (fail-closed)"

    return None


def process_tool_call(tool_input: dict) -> str | None:
    tool_name = tool_input.get("tool_name", "")
    tool_params = tool_input.get("tool_input", {})

    urls: list[str] = []
    if tool_name == "Bash":
        command = tool_params.get("command", "")
        urls = URL_PATTERN.findall(command)
    elif tool_name == "WebFetch":
        url = tool_params.get("url", "")
        if url:
            urls = [url]

    for url in urls:
        reason = validate_url(url)
        if reason:
            return f"SSRF blocked: {url} - {reason}"
    return None


MAX_INPUT_BYTES = 10 * 1024 * 1024  # 10 MB


def main():
    try:
        raw = sys.stdin.read(MAX_INPUT_BYTES + 1)
        if len(raw) > MAX_INPUT_BYTES:
            # Oversized input — fail closed.
            json.dump({"decision": "block", "reason": "Hook input exceeds 10 MB limit"}, sys.stdout)
            sys.exit(1)
        if not raw.strip():
            sys.exit(0)
        tool_input = json.loads(raw)
    except json.JSONDecodeError:
        # Unparseable input — fail closed (pre-tool hook must block).
        json.dump(
            {"decision": "block", "reason": "Unparseable hook input (fail-closed)"}, sys.stdout
        )
        sys.exit(1)
    except Exception as e:
        json.dump({"decision": "block", "reason": f"Hook error (fail-closed): {e}"}, sys.stdout)
        sys.exit(1)

    reason = process_tool_call(tool_input)

    if reason:
        log_finding("ssrf_pretool", "ssrf_blocked", "critical", reason, "block")
        json.dump({"decision": "block", "reason": reason}, sys.stdout)
        sys.exit(1)

    sys.exit(0)


if __name__ == "__main__":
    main()
