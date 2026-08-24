#!/usr/bin/env python3
"""Claude Code PostToolUse hook for context suppression.

Intercepts Bash tool results from verification commands (scan-secrets,
gitleaks, pre-commit, go test, pytest, npm test, make test) and replaces
verbose success output with compact one-line summaries. Failures pass
through unchanged so the agent can act on them.

Principles: success is silent, failure is loud — and a summary is only ever
built from positive evidence the tool printed, never inferred from silence.

Protocol: reads JSON from stdin (``tool_response`` preferred, ``tool_result``
fallback). Writes ``hookSpecificOutput.updatedToolOutput`` (and ``tool_result``)
when suppression applies. Exit code 0 always (never blocks).
"""

from __future__ import annotations

import json
import os
import re
import sys
from datetime import UTC, datetime

import hook_io

FINDINGS_PATH = "/sandbox/workspace/.security/findings.jsonl"
MAX_INPUT_BYTES = 10 * 1024 * 1024  # 10 MB

# --- Command pattern matchers ---
#
# Anchored at the start of a command segment (after any VAR=... prefix and an
# optional runner such as ``uvx``/``npx``/``uv run``): a command that merely
# mentions a tool (``grep -n scan-secrets hooks.py``) must not have its
# output replaced by that tool's summary.
#
# Only tools whose successful run prints positive evidence are listed.
# Silence is never condensed into "passed": a linter or ``go vet`` that prints
# nothing because its interpreter is missing looks identical to a clean run,
# and Claude Code's Bash result carries no exit code to tell them apart.

# Wrappers that run the real command: ``sudo go test``, ``timeout 60 go test``,
# ``env FOO=1 go test``, ``mise exec -- go test``, ``uvx pytest``. Repeatable,
# since wrappers stack (``sudo timeout 60 go test``).
_CMD_PREFIX = (
    r"^(?:(?:uvx|npx|bunx|time|sudo|nice|stdbuf|command|timeout\s+[\d.]+[smhd]?"
    r"|env(?:\s+[A-Za-z_][A-Za-z0-9_]*=\S*)*|uv\s+run|poetry\s+run|pipenv\s+run"
    r"|mise\s+exec(?:\s+--)?|rye\s+run|pdm\s+run|hatch\s+run)\s+)*(?:\S*/)?"
)

_SCAN_SECRETS_RE = re.compile(_CMD_PREFIX + r"scan-secrets\b")
_GITLEAKS_RE = re.compile(_CMD_PREFIX + r"gitleaks\s+detect\b")
_PRECOMMIT_RE = re.compile(_CMD_PREFIX + r"pre-commit\s+run\b")
_GO_TEST_RE = re.compile(_CMD_PREFIX + r"go\s+test\b")
_MAKE_TEST_RE = re.compile(_CMD_PREFIX + r"make\s+(?:test|check)\b")
_NPM_TEST_RE = re.compile(_CMD_PREFIX + r"(?:npm|pnpm|yarn|bun)\s+(?:run\s+)?test\b")
_PYTEST_RE = re.compile(_CMD_PREFIX + r"(?:pytest\b|python3?(?:\.\d+)?\s+-m\s+pytest\b)")

# pre-commit output patterns
_PRECOMMIT_HOOK_LINE_RE = re.compile(
    r"^(.+?)\.{3,}\s*(Passed|Failed|Skipped|Fixed)\s*$", re.MULTILINE
)
_PRECOMMIT_FIXING_RE = re.compile(r"^(?:Fixing|Fixed)\s+(.+?)\.?$", re.MULTILINE)
_PRECOMMIT_FILE_CHANGED_RE = re.compile(
    r"^(?:reformatted|would reformat|fixed)\s+(.+?)$",
    re.MULTILINE | re.IGNORECASE,
)

# go test output patterns
_GO_TEST_OK_RE = re.compile(r"^ok\s+\S+\s+([\d.]+)s", re.MULTILINE)
_GO_TEST_FAIL_RE = re.compile(r"^FAIL\s+", re.MULTILINE)

# pytest output patterns
_PYTEST_SUMMARY_RE = re.compile(
    r"((\d+)\s+passed(?:,\s*\d+\s+(?:skipped|deselected|xfailed|xpassed|warnings?))*)"
    r"\s+in\s+([\d.]+)s",
    re.MULTILINE,
)

# Output that reports a failure is never condensed, whatever the command
# summarizer would make of the rest of it: a line-leading FAIL/ERROR/panic
# marker or a "<n> failed" style count.
_FAILURE_LINE_RE = re.compile(
    r"^(?:FAIL\b|FAILED\b|ERROR\b|[Ee]rror:|--- FAIL|panic:|Traceback|fatal:)", re.MULTILINE
)
_FAILURE_COUNT_RE = re.compile(
    r"\b[1-9]\d*\s+(?:failed|failing|failures?|errors?)\b", re.IGNORECASE
)

# Command shapes that are not summarized: a pipeline (``| tail`` can cut the
# FAIL line the summarizer keys on), ``||``, command substitution, and any
# compound whose segments are not exactly one verification command plus
# benign setup (cd/export/source...). ``pytest; go test`` ran two suites and
# one summary cannot speak for both; ``go test; echo $?`` hides the status.
_PIPE_OR_SUBSHELL_RE = re.compile(r"\||`|\$\(")
_SEGMENT_SPLIT_RE = re.compile(r"\r?\n|&&|;")
_ENV_PREFIX_RE = re.compile(r"^(?:[A-Za-z_][A-Za-z0-9_]*=\S*\s+)+")
_BENIGN_SEGMENT_RE = re.compile(
    r"^(?:cd|pushd|popd|export|unset|set|source|\.|true|ulimit|umask)(?:\s|$)|^#"
)
_QUOTED_RE = re.compile(r"'[^']*'|\"(?:[^\"\\]|\\.)*\"")
_CONTINUATION_RE = re.compile(r"\\\r?\n")

_PYTEST_FAIL_RE = re.compile(r"\b(\d+)\s+(?:failed|errors?)\b", re.IGNORECASE)


def log_suppression(command: str, summary: str) -> None:
    trace_id = os.environ.get("FULLSEND_TRACE_ID", "")
    finding = {
        "trace_id": trace_id,
        "timestamp": datetime.now(UTC).isoformat(),
        "phase": "hook_posttool",
        "scanner": "context_suppress_posttool",
        "name": "context_suppressed",
        "severity": "info",
        "detail": f"Suppressed output for: {command[:80]}",
        "summary": summary,
        "action": "suppress",
    }
    try:
        os.makedirs(os.path.dirname(FINDINGS_PATH), exist_ok=True)
        with open(FINDINGS_PATH, "a") as f:
            f.write(json.dumps(finding) + "\n")
    except OSError:
        pass


def suppress_scan_secrets(output: str) -> str | None:
    lower = output.lower()
    if "no leaks" in lower or "no secrets" in lower or "0 findings" in lower:
        return "scan-secrets: passed (no findings)"
    return None


def suppress_gitleaks(output: str) -> str | None:
    lower = output.lower()
    if "no leaks" in lower:
        return "gitleaks: passed (no leaks detected)"
    return None


def suppress_precommit(output: str) -> str | None:
    hook_results = _PRECOMMIT_HOOK_LINE_RE.findall(output)
    if not hook_results:
        # No per-hook lines: either nothing ran (an interpreter missing from
        # PATH is silent) or the output is not pre-commit's. Never "passed".
        return None

    statuses = [status for _, status in hook_results]
    failed = [name.strip() for name, status in hook_results if status == "Failed"]
    passed_or_skipped = all(s in ("Passed", "Skipped") for s in statuses)

    if passed_or_skipped:
        return f"pre-commit: all {len(hook_results)} hooks passed"

    fixing_files = _PRECOMMIT_FIXING_RE.findall(output)
    reformatted = _PRECOMMIT_FILE_CHANGED_RE.findall(output)
    auto_fixed_files = sorted(set(fixing_files + reformatted))

    if auto_fixed_files and not failed:
        file_list = ", ".join(auto_fixed_files[:10])
        suffix = f" (+{len(auto_fixed_files) - 10} more)" if len(auto_fixed_files) > 10 else ""
        return (
            f"pre-commit: auto-fixed [{file_list}{suffix}]"
            " \u2014 re-stage modified files before commit"
        )

    # Mixed auto-fix + errors, or pure errors: pass through full output
    return None


def suppress_go_test(output: str) -> str | None:
    if _GO_TEST_FAIL_RE.search(output):
        return None

    ok_matches = _GO_TEST_OK_RE.findall(output)
    if not ok_matches:
        return None

    total_time = sum(float(t) for t in ok_matches)
    pkg_count = len(ok_matches)
    return f"tests: {pkg_count} packages passed ({total_time:.1f}s)"


def suppress_pytest(output: str) -> str | None:
    if _PYTEST_FAIL_RE.search(output):
        return None

    match = _PYTEST_SUMMARY_RE.search(output)
    if match:
        # Echo the whole count list ("2 passed, 1 xfailed"), not only "passed".
        return f"tests: {match.group(1)} ({match.group(3)}s)"

    return None


_NPM_FAIL_RE = re.compile(r"\d+\s+failing\b", re.IGNORECASE)


def suppress_npm_test(output: str) -> str | None:
    lower = output.lower()
    if _NPM_FAIL_RE.search(lower) or "fail" in lower:
        return None
    if "passing" in lower or "tests passed" in lower:
        return "tests: passed"
    return None


_MAKE_TEST_OK_RE = re.compile(r"\bok\b", re.IGNORECASE)
_MAKE_TEST_PASS_RE = re.compile(r"\bpass(?:ed)?\b", re.IGNORECASE)


def suppress_make_test(output: str) -> str | None:
    lower = output.lower()
    if "fail" in lower or "error" in lower:
        return None
    if _MAKE_TEST_OK_RE.search(lower) or _MAKE_TEST_PASS_RE.search(lower):
        return "tests: passed"
    return None


def reports_failure(output: str) -> bool:
    return bool(_FAILURE_LINE_RE.search(output) or _FAILURE_COUNT_RE.search(output))


def _summarizer_for(segment: str):
    """Return the summarizer for one command segment, "benign" for setup
    commands, or None for anything else."""
    seg = _ENV_PREFIX_RE.sub("", segment.strip())
    if not seg or _BENIGN_SEGMENT_RE.match(seg):
        return "benign"
    for pattern, fn in _SUMMARIZERS:
        if pattern.search(seg):
            return fn
    return None


_SUMMARIZERS = [
    (_SCAN_SECRETS_RE, suppress_scan_secrets),
    (_GITLEAKS_RE, suppress_gitleaks),
    (_PRECOMMIT_RE, suppress_precommit),
    (_GO_TEST_RE, suppress_go_test),
    (_PYTEST_RE, suppress_pytest),
    (_NPM_TEST_RE, suppress_npm_test),
    (_MAKE_TEST_RE, suppress_make_test),
]


def select_summarizer(command: str):
    """Pick the single verification command in ``command``, or None."""
    command = _CONTINUATION_RE.sub(" ", command)
    # ``-run 'TestA|TestB'`` is a regex, not a pipeline: judge shape with the
    # quoted regions blanked out.
    unquoted = _QUOTED_RE.sub("''", command)
    if _PIPE_OR_SUBSHELL_RE.search(unquoted):
        return None
    chosen = []
    for segment in _SEGMENT_SPLIT_RE.split(unquoted):
        fn = _summarizer_for(segment)
        if fn is None:
            return None
        if fn != "benign":
            chosen.append(fn)
    return chosen[0] if len(chosen) == 1 else None


def try_suppress(command: str, output: str) -> str | None:
    fn = select_summarizer(command)
    if fn is None or reports_failure(output):
        return None
    return fn(output)


def main() -> None:
    try:
        raw = sys.stdin.read(MAX_INPUT_BYTES + 1)
        if len(raw) > MAX_INPUT_BYTES:
            raw = raw[:MAX_INPUT_BYTES]
        if not raw.strip():
            sys.exit(0)
        hook_input = json.loads(raw)
    except (json.JSONDecodeError, Exception):
        sys.exit(0)

    tool_name = hook_input.get("tool_name", "")
    if tool_name != "Bash":
        sys.exit(0)

    tool_input = hook_input.get("tool_input", {})
    if isinstance(tool_input, str):
        try:
            tool_input = json.loads(tool_input)
        except (json.JSONDecodeError, Exception):
            sys.exit(0)

    command = tool_input.get("command", "")
    if not command:
        sys.exit(0)

    original = hook_io.payload(hook_input)
    text = hook_io.scan_text(original)

    # Failures pass through: v1 adapters prefix ``Exit code``. Under Claude Code
    # a failed tool call fires PostToolUseFailure and never reaches here, and
    # ``interrupted`` marks a cancelled tool rather than a non-zero exit.
    if hook_io.looks_failed(original, text):
        sys.exit(0)

    summary = try_suppress(command, text)
    if summary is None:
        sys.exit(0)

    log_suppression(command, summary)
    if not hook_io.has_text_slot(original):
        sys.exit(0)
    hook_io.emit_updated(hook_io.apply_text(original, summary))
    sys.exit(0)


if __name__ == "__main__":
    main()
