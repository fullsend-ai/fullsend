#!/usr/bin/env python3
"""Claude Code PostToolUse hook for unicode security scanning.

Intercepts tool results (Read, Bash, WebFetch) and scans for non-rendering
Unicode characters that can encode hidden instructions: steganographic
payloads (tag characters), invisible text (zero-width), Trojan Source
attacks (bidi overrides), and ANSI escape injection.

All findings are sanitized (invisible characters stripped) and the cleaned
text is returned. PostToolUse hooks cannot block — they sanitize only.
Critical findings (tag characters) are logged to findings.jsonl for the
post-script to act on.

Protocol: reads JSON from stdin, writes JSON to stdout with modified
tool_result if findings detected. Always exits 0.
"""

from __future__ import annotations

import json
import os
import re
import sys
import unicodedata
from datetime import UTC, datetime

FINDINGS_PATH = "/sandbox/workspace/.security/findings.jsonl"
MAX_DECODED_LOG = 200

# --- Unicode categories to detect ---
# Aligned with Go UnicodeNormalizer (internal/security/unicode.go).

_CHECKS: list[tuple[str, str, re.Pattern]] = [
    (
        "tag_char",
        "critical",
        re.compile("[\U000e0000-\U000e007f]+"),
    ),
    (
        "zero_width",
        "high",
        re.compile(
            "[\u00ad\u034f\u061c\u0600-\u0605\u070f\u0890-\u0891\u08e2\u180e"
            "\u200b-\u200f\u2028\u2029\u2060-\u2064\u206a-\u206f\ufeff\ufff9-\ufffb]+"
        ),
    ),
    (
        "bidi_override",
        "high",
        re.compile("[\u202a-\u202e\u2066-\u2069]+"),
    ),
    (
        "variation_selector",
        "medium",
        re.compile("[\ufe00-\ufe0f]+"),
    ),
    # CSI: ECMA-48 compliant ranges (broader than Go's [0-9;]*[a-zA-Z]).
    (
        "ansi_escape",
        "medium",
        re.compile(r"\x1b\[[\x30-\x3f]*[\x20-\x2f]*[\x40-\x7e]"),
    ),
    # ST-terminated: OSC (ESC ]), DCS (ESC P), APC (ESC _), PM (ESC ^).
    # Uses negated class [^\x1b\x07]* instead of .*? to avoid O(n^2)
    # backtracking on dense unterminated sequences.
    (
        "osc_escape",
        "medium",
        re.compile(r"\x1b[\]P_^][^\x1b\x07]*(?:\x1b\\|\x07)"),
    ),
    (
        "null_byte",
        "high",
        re.compile("\x00+"),
    ),
]


def log_finding(name: str, severity: str, detail: str, action: str) -> None:
    trace_id = os.environ.get("FULLSEND_TRACE_ID", "")
    finding = {
        "trace_id": trace_id,
        "timestamp": datetime.now(UTC).isoformat(),
        "phase": "hook_posttool",
        "scanner": "unicode_posttool",
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


def decode_tag_chars(text: str) -> str:
    """Decode tag characters (U+E0000-U+E007F) to reveal hidden ASCII."""
    decoded = "".join(chr(ord(c) - 0xE0000) for c in text if 0xE0000 <= ord(c) <= 0xE007F)
    if len(decoded) > MAX_DECODED_LOG:
        return decoded[:MAX_DECODED_LOG] + "..."
    return decoded


def scan_text(text: str) -> tuple[str, list[dict]]:
    findings: list[dict] = []
    result = text

    for name, severity, pattern in _CHECKS:
        matches = pattern.findall(result)
        if not matches:
            continue

        total_chars = sum(len(m) for m in matches)
        detail = f"{total_chars} {name.replace('_', ' ')} character(s) removed"

        if name == "tag_char":
            decoded = decode_tag_chars(result)
            if decoded.strip():
                # Decoded text logged to findings.jsonl only — never to stdout
                # where it would enter the LLM context as a prompt injection vector.
                detail += f" (decoded hidden text: {decoded.strip()})"

        findings.append(
            {
                "name": name,
                "severity": severity,
                "detail": detail,
            }
        )

        result = pattern.sub("", result)

    # Supplementary variation selectors (VS17-VS256, U+E0100-U+E01EF).
    supp_vs = [c for c in result if 0xE0100 <= ord(c) <= 0xE01EF]
    if supp_vs:
        findings.append(
            {
                "name": "variation_selector",
                "severity": "medium",
                "detail": (f"{len(supp_vs)} supplementary variation selector character(s) removed"),
            }
        )
        result = "".join(c for c in result if not (0xE0100 <= ord(c) <= 0xE01EF))

    # NFKC normalization (fullwidth -> ASCII, compatibility decomposition).
    nfkc = unicodedata.normalize("NFKC", result)
    if nfkc != result:
        orig_chars = list(result)
        nfkc_chars = list(nfkc)
        min_len = min(len(orig_chars), len(nfkc_chars))
        diff_count = sum(1 for i in range(min_len) if orig_chars[i] != nfkc_chars[i])
        diff_count += abs(len(orig_chars) - len(nfkc_chars))
        diff_count = max(diff_count, 1)
        findings.append(
            {
                "name": "fullwidth",
                "severity": "high",
                "detail": f"NFKC normalization applied ({diff_count} characters affected)",
            }
        )
        result = nfkc

        # Second pass: NFKC can reconstruct escape sequences from fullwidth
        # characters (e.g. fullwidth [ + ESC → valid CSI). Re-check ANSI/OSC.
        for name, severity, pattern in _CHECKS:
            if name not in ("ansi_escape", "osc_escape"):
                continue
            matches = pattern.findall(result)
            if not matches:
                continue
            total_chars = sum(len(m) for m in matches)
            findings.append(
                {
                    "name": name,
                    "severity": severity,
                    "detail": (
                        f"{total_chars} {name.replace('_', ' ')} character(s) removed (post-NFKC)"
                    ),
                }
            )
            result = pattern.sub("", result)

    return result, findings


# sys.stdin.read(n) in text mode reads characters, not bytes.
MAX_INPUT_CHARS = 10 * 1024 * 1024


def main() -> None:
    try:
        raw = sys.stdin.read(MAX_INPUT_CHARS + 1)
        if len(raw) > MAX_INPUT_CHARS:
            log_finding(
                "input_truncated",
                "medium",
                f"Input truncated from {len(raw)} to {MAX_INPUT_CHARS} characters",
                "warn",
            )
            raw = raw[:MAX_INPUT_CHARS]
        if not raw.strip():
            sys.exit(0)
        hook_input = json.loads(raw)
    except json.JSONDecodeError:
        log_finding("parse_error", "medium", "Hook input is not valid JSON", "warn")
        sys.exit(0)
    except Exception as e:
        log_finding("parse_error", "high", f"Hook input parsing failed: {type(e).__name__}", "warn")
        sys.exit(0)

    if not isinstance(hook_input, dict):
        sys.exit(0)

    tool_result = hook_input.get("tool_result", "")
    if not tool_result or not isinstance(tool_result, str):
        sys.exit(0)

    try:
        sanitized, findings = scan_text(tool_result)
    except Exception as e:
        log_finding(
            "scan_error",
            "high",
            f"Unicode scan failed (passing original): {type(e).__name__}",
            "warn",
        )
        sys.exit(0)

    if not findings:
        sys.exit(0)

    for f in findings:
        action = "sanitize"
        if f["severity"] == "critical":
            action = "critical_sanitize"
        log_finding(f["name"], f["severity"], f["detail"], action)

    # PostToolUse hooks always exit 0 — they sanitize, never block.
    # Critical findings (tag chars) are stripped from tool_result and logged
    # to findings.jsonl for the post-script to escalate.
    json.dump(
        {
            "tool_result": sanitized,
            "metadata": {
                "unicode_findings": len(findings),
                "categories": [f["name"] for f in findings],
            },
        },
        sys.stdout,
    )

    sys.exit(0)


if __name__ == "__main__":
    main()
