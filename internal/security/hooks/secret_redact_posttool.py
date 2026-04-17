#!/usr/bin/env python3
"""Claude Code PostToolUse hook for secret redaction.

Intercepts tool results (Bash, WebFetch, Read) and redacts secrets
before they enter the LLM context window. This prevents the agent from
seeing or leaking credentials in subsequent output.

Protocol: reads JSON from stdin, writes JSON to stdout with modified
tool_result if secrets found. Exit code 0 always (never blocks).
"""

from __future__ import annotations

import json
import os
import re
import sys
from datetime import UTC, datetime

FINDINGS_PATH = "/tmp/workspace/.security/findings.jsonl"

# --- Known secret prefix patterns ---

_PREFIX_PATTERNS: list[tuple[str, re.Pattern]] = [
    ("openai_key", re.compile(r"sk-(?:proj-)?[A-Za-z0-9_-]{20,}")),
    ("github_pat", re.compile(r"(?:ghp|github_pat|gho|ghu|ghs|ghr)_[A-Za-z0-9_]{16,}")),
    ("slack_token", re.compile(r"xox[baprs]-[A-Za-z0-9\-]{10,}")),
    ("google_api_key", re.compile(r"AIza[A-Za-z0-9_-]{35}")),
    ("anthropic_key", re.compile(r"sk-ant-[A-Za-z0-9_-]{20,}")),
    ("aws_access_key", re.compile(r"AKIA[A-Z0-9]{16}")),
    (
        "aws_secret_key",
        re.compile(r"(?:aws_secret_access_key|AWS_SECRET_ACCESS_KEY)\s*[=:]\s*[A-Za-z0-9/+=]{40}"),
    ),
    ("stripe_key", re.compile(r"(?:sk|pk|rk)_(?:live|test)_[A-Za-z0-9]{10,}")),
    ("sendgrid_key", re.compile(r"SG\.[A-Za-z0-9_-]{22}\.[A-Za-z0-9_-]{43}")),
    ("hf_token", re.compile(r"hf_[A-Za-z0-9]{20,}")),
    ("npm_token", re.compile(r"npm_[A-Za-z0-9]{36}")),
    ("pypi_token", re.compile(r"pypi-[A-Za-z0-9_-]{20,}")),
    ("digitalocean_token", re.compile(r"dop_v1_[a-f0-9]{64}")),
    ("perplexity_key", re.compile(r"pplx-[a-f0-9]{48}")),
    ("databricks_token", re.compile(r"dapi[a-f0-9]{32}")),
    ("telegram_bot", re.compile(r"\d{8,10}:[A-Za-z0-9_-]{35}")),
    (
        "auth_header",
        re.compile(
            r"(?:Authorization|authorization)\s*:\s*(?:Bearer|Basic|Token)\s+[A-Za-z0-9_.+/=-]{20,}"
        ),
    ),
]

# --- Structural patterns ---

_STRUCTURAL_PATTERNS: list[tuple[str, re.Pattern]] = [
    (
        "env_secret",
        re.compile(
            r"(?:^|\s)(?:export\s+)?(?:"
            r"[A-Z_]*(?:SECRET|TOKEN|KEY|PASSWORD|PASSWD|CREDENTIAL|API_KEY|APIKEY|AUTH)"
            r"[A-Z_]*)"
            r"\s*=\s*['\"]?([A-Za-z0-9_.+/=@:%-]{8,})['\"]?",
            re.MULTILINE,
        ),
    ),
    (
        "json_secret",
        re.compile(
            r'"(?:[^"]*(?:secret|token|key|password|credential|apikey|api_key|auth)[^"]*)"'
            r'\s*:\s*"([A-Za-z0-9_.+/=@:%-]{8,})"',
            re.IGNORECASE,
        ),
    ),
    (
        "private_key",
        re.compile(
            r"-----BEGIN (?:RSA |EC |DSA |OPENSSH |PGP )?PRIVATE KEY-----"
            r"[\s\S]*?"
            r"-----END (?:RSA |EC |DSA |OPENSSH |PGP )?PRIVATE KEY-----",
        ),
    ),
    (
        "db_password",
        re.compile(
            r"(?:postgres|mysql|mongodb|redis)(?:ql)?://[^:]+:([^@\s]{8,})@",
            re.IGNORECASE,
        ),
    ),
]


def log_finding(name: str, detail: str):
    trace_id = os.environ.get("FULLSEND_TRACE_ID", "")
    finding = {
        "trace_id": trace_id,
        "timestamp": datetime.now(UTC).isoformat(),
        "phase": "hook_posttool",
        "scanner": "secret_redact_posttool",
        "name": name,
        "severity": "high",
        "detail": detail,
        "action": "redact",
    }
    try:
        with open(FINDINGS_PATH, "a") as f:
            f.write(json.dumps(finding) + "\n")
    except OSError:
        pass


def mask_token(token: str) -> str:
    if len(token) < 10:
        return "***"
    return f"{token[:4]}..."


def redact_text(text: str) -> tuple[str, list[dict]]:
    findings: list[dict] = []
    result = text

    for name, pattern in _PREFIX_PATTERNS:
        for match in pattern.finditer(result):
            token = match.group(0)
            masked = mask_token(token)
            if masked != token:
                findings.append({"pattern": name, "masked": masked})
                result = result.replace(token, masked)

    for name, pattern in _STRUCTURAL_PATTERNS:
        if name == "private_key":
            for match in pattern.finditer(result):
                block = match.group(0)
                findings.append({"pattern": name, "masked": "[REDACTED PRIVATE KEY]"})
                result = result.replace(block, "[REDACTED PRIVATE KEY]")
        else:
            for match in pattern.finditer(result):
                if match.lastindex and match.lastindex >= 1:
                    token = match.group(1)
                    masked = mask_token(token)
                    if masked != token:
                        findings.append({"pattern": name, "masked": masked})
                        result = result.replace(token, masked)

    return result, findings


MAX_INPUT_BYTES = 10 * 1024 * 1024  # 10 MB


def main():
    try:
        raw = sys.stdin.read(MAX_INPUT_BYTES + 1)
        if len(raw) > MAX_INPUT_BYTES:
            # Oversized input — truncate and scan what fits rather than
            # skipping entirely (post-tool, never blocks).
            raw = raw[:MAX_INPUT_BYTES]
        if not raw.strip():
            sys.exit(0)
        hook_input = json.loads(raw)
    except (json.JSONDecodeError, Exception):
        sys.exit(0)

    tool_result = hook_input.get("tool_result", "")
    if not tool_result or not isinstance(tool_result, str):
        sys.exit(0)

    try:
        redacted, findings = redact_text(tool_result)
    except Exception as e:
        # Redaction error — log and pass through original (post-tool, never blocks).
        log_finding("redaction_error", f"Redaction failed (passing original): {e}")
        sys.exit(0)

    if findings:
        for f in findings:
            log_finding(f["pattern"], f"Redacted {f['pattern']}: {f['masked']}")

        json.dump(
            {
                "tool_result": redacted,
                "metadata": {
                    "secrets_redacted": len(findings),
                    "patterns": [f["pattern"] for f in findings],
                },
            },
            sys.stdout,
        )

    sys.exit(0)


if __name__ == "__main__":
    main()
