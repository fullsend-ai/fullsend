#!/usr/bin/env python3
"""Single PostToolUse driver: unicode → canary → suppress → redact.

Claude Code runs matching hooks in parallel and does not pipe stdout, so
sanitizers cannot be ordered by settings.json matcher position, and two
hooks that both emit ``updatedToolOutput`` race. This driver loads each
enabled sibling script and applies them in-process.

Unicode normalization runs first because every later content decision is
made on its output: an attacker who splits a canary token or a secret with
zero-width or fullwidth characters must not be able to slip it past
detection and then have this chain reassemble the clean token for Claude.

A stage is enabled when its script file is present next to this driver
(HookFiles omits disabled sanitizers). Tests disable a stage by omitting
its sibling file, not via environment variables.

Claude Code fires PostToolUse only for tool calls that *succeed*; failed
calls fire PostToolUseFailure, which carries the error text but cannot
replace it. The driver is wired to both: on a failure it runs canary
detection only (block + halt), since nothing can be rewritten there.
"""

from __future__ import annotations

import importlib.util
import json
import os
import sys
import types
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

import hook_io

HOOKS_DIR = Path(__file__).resolve().parent
MAX_INPUT_CHARS = hook_io.MAX_INPUT_CHARS
FINDINGS_PATH = "/sandbox/workspace/.security/findings.jsonl"

# Emitted when the canary block path itself fails. Hard-coded so that
# producing it cannot raise.
_ERR_CANARY_BLOCK = (
    '{"decision":"block","continue":false,'
    '"reason":"CANARY_HOOK_ERROR: canary leak detected; output withheld"}'
)

_STAGE_FILES = {
    "suppress": "context_suppress_posttool.py",
    "unicode": "unicode_posttool.py",
    "redact": "secret_redact_posttool.py",
    "canary": "canary_posttool.py",
}

_STAGE_CACHE: dict[str, types.ModuleType | None] = {}


def stage_enabled(token: str) -> bool:
    return (HOOKS_DIR / _STAGE_FILES[token]).is_file()


def _load_stage(token: str) -> types.ModuleType | None:
    if token in _STAGE_CACHE:
        return _STAGE_CACHE[token]
    mod = _import_stage(token)
    _STAGE_CACHE[token] = mod
    return mod


def _import_stage(token: str) -> types.ModuleType | None:
    filename = _STAGE_FILES[token]
    path = HOOKS_DIR / filename
    if not path.is_file():
        return None
    spec = importlib.util.spec_from_file_location(f"posttool_{token}", path)
    if spec is None or spec.loader is None:
        return None
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def _command(hook_input: dict[str, Any]) -> str:
    if hook_input.get("tool_name") != "Bash":
        return ""
    tool_input = hook_input.get("tool_input", {})
    if isinstance(tool_input, str):
        try:
            tool_input = json.loads(tool_input)
        except (json.JSONDecodeError, TypeError):
            return ""
    if not isinstance(tool_input, dict):
        return ""
    command = tool_input.get("command", "")
    return command if isinstance(command, str) else ""


def log_finding(name: str, severity: str, detail: str, action: str) -> None:
    finding = {
        "trace_id": os.environ.get("FULLSEND_TRACE_ID", ""),
        "timestamp": datetime.now(UTC).isoformat(),
        "phase": "hook_posttool",
        "scanner": "posttool_chain",
        "name": name,
        "severity": severity,
        "detail": detail,
        "action": action,
    }
    try:
        os.makedirs(os.path.dirname(FINDINGS_PATH), exist_ok=True)
        with open(FINDINGS_PATH, "a") as f:
            f.write(json.dumps(finding) + "\n")
    except OSError:
        pass


def _stage_error(metadata: dict[str, Any], token: str) -> None:
    """Record a stage failure.

    ``metadata`` on stdout reaches adapters and the debug log only, so the
    durable signal for the post-script is the findings log.
    """
    metadata[f"{token}_error"] = True
    log_finding(
        f"{token}_stage_error",
        "high",
        f"PostToolUse {token} stage failed; output passed through unsanitized",
        "warn",
    )


def _rewrite_note(metadata: dict[str, Any], command: str) -> str | None:
    """Tell the agent what was changed in the output it is about to read.

    Without this an agent that reads ``requ...`` in a file treats it as the
    file's content; with it, it knows the mask is the hook's and the value is
    still on disk.
    """
    notes: list[str] = []
    redacted = metadata.get("secrets_redacted")
    if redacted:
        notes.append(
            f"fullsend: {redacted} credential-like value(s) in this tool output were "
            "masked as `xxxx...`. The masks are not in the underlying file or command "
            "output; never copy a mask into an edit, a file or a commit."
        )
    if metadata.get("context_suppressed"):
        shown = command.strip().replace("\n", " ")
        if len(shown) > 80:
            shown = shown[:77] + "..."
        notes.append(
            f"fullsend: the output of `{shown}` reported success and was condensed to a "
            "one-line summary. Failing output is never condensed."
        )
    removed = [c for c in metadata.get("categories", []) if c != "fullwidth"]
    if removed:
        notes.append(
            f"fullsend: {len(removed)} hidden or control character sequence(s) "
            f"({', '.join(sorted(set(removed)))}) were removed from this tool output."
        )
    return "\n".join(notes) or None


def _canary_armed() -> bool:
    return stage_enabled("canary") and bool(os.environ.get("FULLSEND_CANARY_TOKEN", "").strip())


def _bail(reason: str) -> None:
    """Exit for input the chain cannot sanitize.

    Sanitizing stages fail open, but the canary must not: this driver is the
    only PostToolUse entry point Claude Code schedules, so input it cannot read
    would otherwise skip canary detection entirely.
    """
    if _canary_armed():
        log_finding("canary_unscannable", "critical", reason, "block")
        sys.stdout.write(json.dumps({"decision": "block", "continue": False, "reason": reason}))
        sys.exit(1)
    sys.exit(0)


def _handle_failure(hook_input: dict[str, Any]) -> None:
    """PostToolUseFailure: canary detection on the error text, nothing else.

    The event supports no output rewrite, so sanitizers are skipped; a canary
    hit still halts the session (``continue: false``). Fails closed like the
    PostToolUse path.
    """
    if not _canary_armed():
        sys.exit(0)
    # The documented shape carries the failure as a string ``error``, but the
    # key has varied across doc versions (``tool_error``), so do not trust one
    # key: scan every string in the payload. Nothing here is rewritable, so
    # over-scanning costs nothing and shape drift cannot open a gap.
    scannable: Any = hook_input
    canary_token = os.environ.get("FULLSEND_CANARY_TOKEN", "").strip()
    tool_name = hook_input.get("tool_name", "unknown")
    try:
        hit = hook_io.contains_canary(scannable, canary_token) or hook_io.contains_canary(
            hook_io.nfkc(scannable), canary_token
        )
        reason = f"CANARY_LEAKED: canary token found in failed {tool_name} call output"
    except Exception:  # noqa: BLE001
        hit = True
        reason = f"CANARY_SCAN_ERROR: canary scan failed on failed {tool_name} call; blocking"
    if not hit:
        sys.exit(0)
    log_finding("canary_leak", "critical", reason, "block")
    try:
        hook_io.emit_block(reason, None, stop=True)
    except Exception:  # noqa: BLE001
        sys.stdout.write(_ERR_CANARY_BLOCK)
    sys.exit(1)


def main() -> None:
    try:
        raw = sys.stdin.read(MAX_INPUT_CHARS + 1)
        if len(raw) > MAX_INPUT_CHARS:
            _bail("CANARY_HOOK_ERROR: input exceeds the 10 MB limit; cannot scan for canary")
        if not raw.strip():
            sys.exit(0)
        hook_input = json.loads(raw)
    except SystemExit:
        raise
    except json.JSONDecodeError:
        _bail("CANARY_HOOK_ERROR: malformed JSON input; cannot scan for canary")
    except Exception:  # noqa: BLE001
        _bail("CANARY_HOOK_ERROR: unexpected error reading input; cannot scan for canary")

    if not isinstance(hook_input, dict):
        _bail("CANARY_HOOK_ERROR: unexpected input shape; cannot scan for canary")

    if hook_input.get("hook_event_name") == "PostToolUseFailure":
        _handle_failure(hook_input)

    original = hook_io.payload(hook_input)
    updated = original
    metadata: dict[str, Any] = {}

    # 1. Unicode normalization. Must precede canary detection and secret
    #    redaction: obfuscation characters would otherwise hide a token from
    #    the scanners and be stripped afterwards, handing Claude the clean
    #    value. Identifier fields (paths, URLs, commands) are left alone —
    #    rewriting them would tell Claude it touched a file that does not exist.
    unicode_findings: list[dict] = []
    unicode_field_errors: list[str] = []
    if stage_enabled("unicode"):
        try:
            unicode_mod = _load_stage("unicode")
            if unicode_mod is not None:
                # Guarded per field, like the standalone script: one pathological
                # field must not abort the walk and leave every other field
                # unsanitized.
                def _sanitize(text: str) -> str:
                    if not text:
                        return text
                    try:
                        cleaned, findings = unicode_mod.scan_text(text)
                    except Exception as exc:  # noqa: BLE001
                        unicode_field_errors.append(type(exc).__name__)
                        unicode_mod.log_finding(
                            "scan_error",
                            "high",
                            f"Unicode scan failed (passing original): {type(exc).__name__}",
                            "warn",
                        )
                        return text
                    unicode_findings.extend(findings)
                    return cleaned

                updated = hook_io.transform_strings(
                    updated, _sanitize, skip_keys=hook_io.IDENTIFIER_KEYS
                )
                if unicode_findings:
                    metadata["unicode_findings"] = len(unicode_findings)
                    metadata["categories"] = [f["name"] for f in unicode_findings]
                    for f in unicode_findings:
                        action = "critical_sanitize" if f["severity"] == "critical" else "sanitize"
                        unicode_mod.log_finding(f["name"], f["severity"], f["detail"], action)
        except Exception:
            _stage_error(metadata, "unicode")
    if unicode_field_errors:
        metadata["unicode_error"] = True

    # 2. Canary detection, on the normalized text as well as the raw text.
    #    Fails closed: a scan that raises is treated as a hit.
    canary_token = os.environ.get("FULLSEND_CANARY_TOKEN", "").strip()
    canary_hit = False
    canary_scan_failed = False
    if stage_enabled("canary") and canary_token:
        try:
            canary_hit = (
                hook_io.contains_canary(updated, canary_token)
                or hook_io.contains_canary(original, canary_token)
                or hook_io.contains_canary(hook_io.nfkc(updated), canary_token)
            )
        except Exception:
            canary_hit = True
            canary_scan_failed = True
            _stage_error(metadata, "canary")

    if not canary_hit and stage_enabled("suppress"):
        try:
            suppress = _load_stage("suppress")
            command = _command(hook_input)
            text = hook_io.scan_text(updated)
            if suppress is not None and command and not hook_io.looks_failed(updated, text):
                summary = suppress.try_suppress(command, text)
                if summary is not None:
                    suppress.log_suppression(command, summary)
                    if hook_io.has_text_slot(updated):
                        updated = hook_io.apply_text(updated, summary)
                        metadata["context_suppressed"] = True
                    else:
                        metadata["shape_unpatched"] = True
        except Exception:
            _stage_error(metadata, "suppress")

    redact_findings: list[dict] = []
    redact_field_errors: list[str] = []
    nfkc_rewrites: list[int] = []
    if stage_enabled("redact"):
        try:
            redact_mod = _load_stage("redact")
            if redact_mod is not None:

                def _redact(text: str) -> str:
                    if not text:
                        return text
                    try:
                        cleaned, findings = redact_mod.redact_text(text)
                        normalized = hook_io.nfkc(text)
                        if normalized != text:
                            # Fullwidth/compatibility obfuscation: the unicode
                            # stage keeps such text, so scan a normalized copy
                            # and, only when that finds more, emit the
                            # normalized+redacted field instead.
                            cleaned_n, findings_n = redact_mod.redact_text(normalized)
                            seen = {(f["pattern"], f["masked"]) for f in findings}
                            if len(findings_n) > len(findings) or any(
                                (f["pattern"], f["masked"]) not in seen for f in findings_n
                            ):
                                cleaned, findings = cleaned_n, findings_n
                                nfkc_rewrites.append(1)
                    except Exception as exc:  # noqa: BLE001
                        redact_field_errors.append(type(exc).__name__)
                        redact_mod.log_finding(
                            "redaction_error",
                            f"Redaction failed (passing original): {type(exc).__name__}",
                        )
                        return text
                    redact_findings.extend(findings)
                    return cleaned

                updated = hook_io.transform_strings(updated, _redact)
                if nfkc_rewrites:
                    metadata["nfkc_redacted_fields"] = len(nfkc_rewrites)
                if redact_findings:
                    metadata["secrets_redacted"] = len(redact_findings)
                    metadata["patterns"] = [f["pattern"] for f in redact_findings]
                    for f in redact_findings:
                        redact_mod.log_finding(
                            f["pattern"], f"Redacted {f['pattern']}: {f['masked']}"
                        )
        except Exception:
            _stage_error(metadata, "redact")
    if redact_field_errors:
        metadata["redact_error"] = True

    # 3. Canary block. Fails closed at every step: exit 1 is unconditional,
    #    and output is withheld entirely if it could not be proven redacted.
    if canary_hit:
        tool_name = hook_input.get("tool_name", "unknown")
        if canary_scan_failed:
            reason = f"CANARY_SCAN_ERROR: canary scan failed on {tool_name} result; blocking"
        else:
            reason = f"CANARY_LEAKED: canary token found in {tool_name} result"

        blocked: Any | None
        try:
            blocked = hook_io.redact_canary(updated, canary_token)
            if hook_io.contains_canary(hook_io.nfkc(blocked), canary_token):
                # Token hidden behind compatibility characters: the unicode
                # stage keeps such text, so redact the normalized form.
                blocked = hook_io.redact_canary(hook_io.nfkc(blocked), canary_token)
            # Never emit output that still carries the token, whatever the
            # redactor did: withholding it costs the agent context, leaking it
            # defeats the hook.
            if hook_io.contains_canary(blocked, canary_token) or hook_io.contains_canary(
                hook_io.nfkc(blocked), canary_token
            ):
                blocked = None
                _stage_error(metadata, "canary_redact")
        except Exception:
            blocked = None
            _stage_error(metadata, "canary_redact")

        try:
            canary_mod = _load_stage("canary")
            if canary_mod is not None:
                canary_mod.log_finding("canary_leak", "critical", reason, "block")
        except Exception:
            _stage_error(metadata, "canary_log")

        try:
            hook_io.emit_block(reason, blocked, stop=True)
        except Exception:
            sys.stdout.write(_ERR_CANARY_BLOCK)
        sys.exit(1)

    if updated == original:
        # Never emit a no-op rewrite: Claude Code lets one hook's
        # updatedToolOutput replace another's, so a no-op would clobber a
        # legitimate rewrite from a repo-local hook. Stage errors are already
        # in the findings log; adapters still get the metadata.
        if metadata:
            json.dump({"metadata": metadata}, sys.stdout)
        sys.exit(0)

    hook_io.emit_updated(
        updated,
        metadata=metadata or None,
        additional_context=_rewrite_note(metadata, _command(hook_input)),
    )
    sys.exit(0)


if __name__ == "__main__":
    main()
