#!/usr/bin/env python3
"""Shared PostToolUse stdin/stdout protocol (contract v2).

Claude Code sends the tool output as ``tool_response`` (string or structured
object). Adapters and existing tests may still send ``tool_result``. Sanitizers
replace output via ``hookSpecificOutput.updatedToolOutput``.

``updatedToolOutput`` must match the original value's shape: a string stays a
string; a Bash object keeps ``stdout``/``stderr``/… keys. A bare string
replacement is ignored for built-in Claude Code tools.

Emissions also carry ``tool_result`` (and ``metadata``) so sequential adapters
can keep reading the v1 field.
"""

from __future__ import annotations

import json
import re
import sys
import unicodedata
from collections.abc import Callable, Iterable
from typing import Any

MAX_INPUT_CHARS = 10 * 1024 * 1024

# Text-bearing keys on structured tool output. stdout is listed first so
# apply_text writes a replacement there and blanks the remaining slots
# (stderr must be cleared on suppress, not left as a second copy).
_TEXT_KEYS = ("stdout", "stderr", "content", "text", "output")

# Keys whose values are identifiers Claude re-uses verbatim (paths, URLs,
# commands, exact-match edit strings). Unicode normalization must not rewrite
# them — NFKC/NFC would hand Claude a path that does not exist on disk. Secret
# redaction still walks them: it only replaces matched secret patterns and
# never reshapes the rest of the string.
IDENTIFIER_KEYS = frozenset(
    {
        "filePath",
        "file_path",
        "path",
        "filename",
        "fileName",
        "url",
        "uri",
        "command",
        "oldString",
        "newString",
    }
)


def payload(hook_input: dict[str, Any]) -> Any:
    """Return the tool output: ``tool_response`` preferred, else ``tool_result``."""
    if "tool_response" in hook_input:
        return hook_input["tool_response"]
    return hook_input.get("tool_result")


def scan_text(value: Any) -> str:
    """Flatten every string field in a tool output value for scanning.

    Claude Code Bash ``tool_response`` is ``{stdout, stderr, interrupted, isImage}``
    with stdout always a string (possibly empty). Detection must not stop at
    the first key — a leak only in stderr would otherwise be invisible.

    Fields are joined with a newline so a needle cannot match across a field
    boundary: such a match is unredactable, because the redactors rewrite each
    string field independently.
    """
    if value is None:
        return ""
    if isinstance(value, str):
        return value
    if isinstance(value, dict):
        return _join(scan_text(v) for v in value.values())
    if isinstance(value, list):
        return _join(scan_text(item) for item in value)
    if isinstance(value, (bool, int, float)):
        return ""
    return json.dumps(value)


def _join(parts: Iterable[str]) -> str:
    return "\n".join(part for part in parts if part)


def v1_text(value: Any) -> str:
    """Render a tool output value for the v1 ``tool_result`` field.

    A string passes through; a structured shape is serialized rather than
    flattened, so an adapter writing this back does not hand the agent a
    concatenation of unrelated fields.
    """
    if isinstance(value, str):
        return value
    return json.dumps(value)


def has_text_slot(value: Any) -> bool:
    """True when apply_text can write back without collapsing the shape."""
    if value is None or isinstance(value, str):
        return True
    if isinstance(value, dict):
        return any(isinstance(value.get(key), str) for key in _TEXT_KEYS)
    if isinstance(value, list) and len(value) == 1:
        item = value[0]
        return isinstance(item, dict) and isinstance(item.get("text"), str)
    return False


def apply_text(original: Any, new_text: str) -> Any:
    """Write ``new_text`` back into the original value's text slot(s).

    Structured values without a recognized text key are returned unchanged —
    Claude Code ignores a bare-string ``updatedToolOutput`` for built-in tools.
    When several text keys are present, the first (stdout) gets ``new_text``
    and the rest are blanked so verbose stderr cannot survive a suppress.
    """
    if original is None or isinstance(original, str):
        return new_text
    if isinstance(original, dict):
        out = dict(original)
        wrote = False
        for key in _TEXT_KEYS:
            if isinstance(original.get(key), str):
                out[key] = new_text if not wrote else ""
                wrote = True
        return out if wrote else original
    if isinstance(original, list) and len(original) == 1:
        item = original[0]
        if isinstance(item, dict) and isinstance(item.get("text"), str):
            block = dict(item)
            block["text"] = new_text
            return [block]
    return original


def looks_failed(value: Any, text: str) -> bool:
    """True when output should not be context-suppressed.

    v1 adapters prefix failures with ``Exit code``. Under Claude Code a
    non-zero-exit Bash call does not reach PostToolUse at all (it fires
    PostToolUseFailure), and ``interrupted`` marks a cancelled tool.
    """
    if text.startswith("Exit code"):
        return True
    return isinstance(value, dict) and value.get("interrupted") is True


def transform_strings(
    value: Any,
    fn: Callable[[str], str],
    *,
    skip_keys: frozenset[str] = frozenset(),
) -> Any:
    """Apply ``fn`` to every string in a nested JSON-like value.

    Values under a key in ``skip_keys`` are left untouched (see
    ``IDENTIFIER_KEYS``).
    """
    if isinstance(value, str):
        return fn(value)
    if isinstance(value, dict):
        return {
            k: (v if k in skip_keys else transform_strings(v, fn, skip_keys=skip_keys))
            for k, v in value.items()
        }
    if isinstance(value, list):
        return [transform_strings(v, fn, skip_keys=skip_keys) for v in value]
    return value


def _detection_form(text: str) -> str:
    # NFKD first so combining marks become separate code points, drop them and
    # every variation selector, then NFKC for the compatibility folding.
    decomposed = unicodedata.normalize("NFKD", text)
    stripped = "".join(
        c
        for c in decomposed
        if unicodedata.category(c) != "Mn"
        and not (0xFE00 <= ord(c) <= 0xFE0F or 0xE0100 <= ord(c) <= 0xE01EF)
    )
    return unicodedata.normalize("NFKC", stripped)


def nfkc(value: Any) -> Any:
    """Return ``value`` with every string in detection form: NFKC-normalized
    with combining marks and variation selectors removed.

    Sanitizers keep the original text; scanners that must see through
    fullwidth, combining-mark or selector obfuscation run on this copy.
    """
    return transform_strings(value, _detection_form)


def emit_updated(
    updated: Any,
    *,
    metadata: dict[str, Any] | None = None,
    additional_context: str | None = None,
) -> None:
    payload_out: dict[str, Any] = {
        "tool_result": v1_text(updated),
        "hookSpecificOutput": {
            "hookEventName": "PostToolUse",
            "updatedToolOutput": updated,
        },
    }
    if additional_context:
        # Claude Code inserts this next to the tool result, so the agent
        # knows the output it sees was rewritten and why.
        payload_out["hookSpecificOutput"]["additionalContext"] = additional_context
    if metadata:
        payload_out["metadata"] = metadata
    _write(payload_out)


def emit_block(reason: str, updated: Any | None = None, *, stop: bool = False) -> None:
    payload_out: dict[str, Any] = {"decision": "block", "reason": reason}
    if stop:
        # PostToolUse ``decision: block`` only appends the reason; ``continue``
        # is the documented field that actually halts the session.
        payload_out["continue"] = False
    if updated is not None:
        payload_out["tool_result"] = v1_text(updated)
        payload_out["hookSpecificOutput"] = {
            "hookEventName": "PostToolUse",
            "updatedToolOutput": updated,
        }
    _write(payload_out)


def _write(payload_out: dict[str, Any]) -> None:
    """Serialize first, then write — a serialization error must not leave
    half an object on stdout for the caller's fallback to append to."""
    sys.stdout.write(json.dumps(payload_out))


def canary_pattern(canary: str) -> re.Pattern[str] | None:
    """Case-insensitive matcher for the canary token.

    Detection and redaction must share one mechanism. Walking a ``str.lower()``
    copy with indices taken from the original string desynchronizes as soon as
    a character case-folds to more code points than it started with (``İ`` →
    two), which can leave a detected token unredacted in the emitted output.
    """
    needle = canary.strip()
    if not needle:
        return None
    return re.compile(re.escape(needle), re.IGNORECASE)


def contains_canary(value: Any, canary: str) -> bool:
    """True when the canary appears anywhere in ``value``."""
    pattern = canary_pattern(canary)
    if pattern is None:
        return False
    return pattern.search(scan_text(value)) is not None


def redact_canary(value: Any, canary: str) -> Any:
    """Replace case-insensitive canary matches in every string field."""
    pattern = canary_pattern(canary)
    if pattern is None:
        return value
    return transform_strings(value, lambda text: pattern.sub("[CANARY_REDACTED]", text))
