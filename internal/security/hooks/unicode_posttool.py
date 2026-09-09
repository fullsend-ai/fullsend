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

Protocol: reads JSON from stdin (``tool_response`` preferred, ``tool_result``
fallback). Writes ``hookSpecificOutput.updatedToolOutput`` (and ``tool_result``)
when findings are detected. Always exits 0.
"""

from __future__ import annotations

import json
import os
import re
import sys
import unicodedata
from datetime import UTC, datetime

import hook_io

FINDINGS_PATH = "/sandbox/workspace/.security/findings.jsonl"
MAX_DECODED_LOG = 200
# Bound the strip fixpoint. Each pass strictly shortens on a match; this
# caps pathological adjacent reconstructed sequences (see #445).
MAX_SANITIZE_PASSES = 64

# Fail-closed backstop for _sanitize_fixpoint. A single non-overlapping
# `pattern.sub` pass on an adjacent-ESC run (e.g. ESC*65 + "["*130, where
# "[" is itself a valid ECMA-48 CSI final byte) removes only one
# reconstructed CSI per pass, so a large enough run outruns
# MAX_SANITIZE_PASSES. Both the ANSI and OSC patterns require a leading
# ESC (0x1B); stripping every ESC and C1 control byte (0x80-0x9F)
# unconditionally therefore guarantees neither can remain, regardless of
# how the surrounding bytes are shaped.
_ESC_C1_RE = re.compile("[\x1b\x80-\x9f]")
_ESC_C1_STRIP_RE = re.compile("[\x1b\x80-\x9f]+")

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


# Variation selectors to strip: any run of two or more, or a selector that
# does not follow a non-ASCII character (nothing for it to select).
_VS_STRIP_RE = re.compile("(?:[\ufe00-\ufe0f]{2,}|(?<![^\x00-\x7f])[\ufe00-\ufe0f])")
_SUPP_VS_STRIP_RE = re.compile(
    "(?:[\U000e0100-\U000e01ef]{2,}|(?<![^\x00-\x7f])[\U000e0100-\U000e01ef])"
)


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


def _sanitize_pass(text: str) -> tuple[str, list[dict]]:
    """Apply every control/invisible category once. Does not NFKC."""
    findings: list[dict] = []
    result = text

    for name, severity, pattern in _CHECKS:
        if name == "variation_selector":
            # Smuggling needs runs of selectors; one selector after a
            # non-ASCII character is ordinary text (emoji presentation "⚠️",
            # CJK/Mongolian variants) that an Edit must still match on disk.
            pattern = _VS_STRIP_RE
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

    # Supplementary variation selectors (VS17-VS256, U+E0100-U+E01EF): same
    # rule as the BMP ones — one selector after a non-ASCII base character is
    # an ideographic variation sequence (Japanese IVS), a run or an orphan is
    # smuggling.
    supp_matches = _SUPP_VS_STRIP_RE.findall(result)
    if supp_matches:
        findings.append(
            {
                "name": "variation_selector",
                "severity": "medium",
                "detail": (
                    f"{sum(len(m) for m in supp_matches)} supplementary variation selector "
                    "character(s) removed"
                ),
            }
        )
        result = _SUPP_VS_STRIP_RE.sub("", result)

    return result, findings


def _sanitize_fixpoint(text: str) -> tuple[str, list[dict]]:
    """Repeat _sanitize_pass until unchanged.

    A single ``pattern.sub`` is non-overlapping, so ESC ESC [[[[ becomes
    ESC [[ after one pass — itself a CSI sequence a second pass must remove.
    """
    findings: list[dict] = []
    result = text
    for _ in range(MAX_SANITIZE_PASSES):
        nxt, extra = _sanitize_pass(result)
        findings.extend(extra)
        if nxt == result:
            return result, findings
        result = nxt

    # Exhausted the pass budget without reaching a fixpoint. A pathological
    # run of adjacent ESC bytes (optionally reconstructed from fullwidth
    # brackets by NFKC) can make each pass remove only one CSI, outrunning
    # any fixed cap (#445). Returning the residual text here would fail
    # open — it can still contain a live CSI/OSC sequence. Fail closed
    # instead: strip every remaining ESC (0x1B) and C1 control byte
    # (0x80-0x9F) outright. ansi_escape and osc_escape both require a
    # leading ESC byte, so this guarantees neither survives.
    if _ESC_C1_RE.search(result):
        stripped = _ESC_C1_STRIP_RE.sub("", result)
        removed = len(result) - len(stripped)
        findings.append(
            {
                "name": "ansi_escape",
                "severity": "high",
                "detail": (
                    f"{removed} escape/control byte(s) force-stripped after exceeding "
                    f"{MAX_SANITIZE_PASSES} sanitize passes (fail-closed)"
                ),
            }
        )
        result = stripped

    return result, findings


def scan_text(text: str) -> tuple[str, list[dict]]:
    result, findings = _sanitize_fixpoint(text)

    # Compatibility characters (fullwidth, ligatures, vulgar fractions) are
    # reported but kept: NFKC-rewriting a Read result hands the agent file
    # content that is not on disk (CJK punctuation, "ﬁ" → "fi"), and every
    # Edit it then composes misses. Detection that depends on the normalized
    # form (canary, secret patterns) runs on a normalized *copy* in the chain
    # driver. The one rewrite kept is reconstructed control/invisible payload
    # below: the fullwidth form was a delivery vehicle, not content.
    nfkc = unicodedata.normalize("NFKC", result)
    if nfkc != result:
        diff_count = sum(1 for a, b in zip(result, nfkc, strict=False) if a != b)
        diff_count += abs(len(result) - len(nfkc))
        findings.append(
            {
                "name": "fullwidth",
                "severity": "medium",
                "detail": (
                    f"{max(diff_count, 1)} compatibility (fullwidth/NFKC) character(s) "
                    "detected; text kept, normalized copy used for detection"
                ),
            }
        )

        # NFKC can reconstruct control sequences from fullwidth characters
        # (ESC + fullwidth "[" → a valid CSI). Re-check every category to a
        # fixpoint so the last scan cannot leave a reconstructed payload in
        # the emitted field.
        nfkc_stripped, nfkc_findings = _sanitize_fixpoint(nfkc)
        if nfkc_findings:
            for f in nfkc_findings:
                f["detail"] += " (reassembled by NFKC; field normalized)"
            findings.extend(nfkc_findings)
            result = nfkc_stripped

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

    original = hook_io.payload(hook_input)
    findings: list[dict] = []

    def _sanitize_field(text: str) -> str:
        if not text:
            return text
        try:
            sanitized, field_findings = scan_text(text)
        except Exception as e:
            log_finding(
                "scan_error",
                "high",
                f"Unicode scan failed (passing original): {type(e).__name__}",
                "warn",
            )
            return text
        findings.extend(field_findings)
        return sanitized

    # Identifier fields (paths, URLs, commands) are scanned by other hooks but
    # never rewritten here: NFKC would hand Claude a path that does not exist.
    updated = hook_io.transform_strings(
        original, _sanitize_field, skip_keys=hook_io.IDENTIFIER_KEYS
    )
    if not findings:
        sys.exit(0)

    for f in findings:
        action = "critical_sanitize" if f["severity"] == "critical" else "sanitize"
        log_finding(f["name"], f["severity"], f["detail"], action)

    if updated == original:
        # Detection-only findings (compatibility characters kept): report,
        # but never emit a no-op rewrite that could clobber another hook's.
        json.dump(
            {
                "metadata": {
                    "unicode_findings": len(findings),
                    "categories": [f["name"] for f in findings],
                }
            },
            sys.stdout,
        )
        sys.exit(0)

    # PostToolUse hooks always exit 0 — they sanitize, never block.
    # Critical findings (tag chars) are stripped and logged to findings.jsonl
    # for the post-script to escalate.
    hook_io.emit_updated(
        updated,
        metadata={
            "unicode_findings": len(findings),
            "categories": [f["name"] for f in findings],
        },
    )
    sys.exit(0)


if __name__ == "__main__":
    main()
